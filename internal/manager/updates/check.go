package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/imageref"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/manager/regclient"
	"github.com/neurekadev/docker-manager/internal/manager/registries"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/manager/updates/eligible"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// CheckInput is the input of update.check (a manager job).
type CheckInput struct {
	PolicyID string `json:"policyId"`
}

// CheckOutput is the result output of update.check.
type CheckOutput struct {
	Checked     int `json:"checked"`
	Available   int `json:"available"`
	UpToDate    int `json:"upToDate"`
	Quarantined int `json:"quarantined"`
	Ineligible  int `json:"ineligible"`
	Failed      int `json:"failed"`
	// SourceHashBefore/After: the stack's definition read before and after
	// the check ("" when the agent was offline).
	SourceHashBefore string `json:"sourceHashBefore,omitempty"`
	SourceHashAfter  string `json:"sourceHashAfter,omitempty"`
}

// classed is a classified manager job failure.
type classed struct {
	class, recovery string
	err             error
}

func (e *classed) Error() string      { return e.err.Error() }
func (e *classed) Unwrap() error      { return e.err }
func (e *classed) ErrorClass() string { return e.class }
func (e *classed) Recovery() string   { return e.recovery }

var _ jobexec.ClassedError = (*classed)(nil)

// Check error classes.
const (
	classPolicyNotFound   = "policy_not_found"
	classTargetNotFound   = "target_not_found"
	classSourceChanged    = protocol.UpdateClassSourceChanged
	classEnvironmentOffln = "environment_offline"
)

// checkStep is the update.check executor: it refreshes the policy's
// candidates from the registries (no pull, nothing on the host changes).
func (s *Service) checkStep(ctx context.Context, sc *jobexec.StepContext) error {
	var in CheckInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return fmt.Errorf("malformed update check input: %w", err)
	}
	p, err := store.GetUpdatePolicy(ctx, s.db, in.PolicyID)
	if errors.Is(err, domain.ErrUpdatePolicyNotFound) {
		return &classed{class: classPolicyNotFound, recovery: "The update policy was deleted.", err: err}
	}
	if err != nil {
		return err
	}
	if p.Inactive {
		return &classed{class: domain.UpdateErrTargetIneligible, recovery: "Remove the target exclusion and check again.",
			err: errors.New("the target is excluded from automatic updates")}
	}
	out, cands, err := s.Check(ctx, p, sc.JobID)
	for _, c := range cands {
		status := domain.ItemSucceeded
		switch c.Status {
		case domain.CandidateIneligible:
			status = domain.ItemSkipped
		case domain.CandidateCheckFailed:
			status = domain.ItemFailed
		}
		msg := string(c.Status)
		if c.Reason != "" {
			msg += ": " + c.Reason
		}
		if c.ErrorClass != "" {
			msg += ": " + c.ErrorClass
		}
		sc.Item(ctx, c.Service, status, msg)
	}
	if serr := sc.SetOutput(ctx, out); serr != nil && err == nil {
		err = serr
	}
	return err
}

// Check refreshes a policy's candidates: eligibility, applied digest,
// the registry's host-platform digest and the resulting status. It never
// pulls. For stacks it reads the definition's hash before and after and
// fails with source_changed when it changed meanwhile (the check itself
// never writes it). jobID attributes registry credential use (#19).
func (s *Service) Check(ctx context.Context, p domain.UpdatePolicy, jobID string) (CheckOutput, []domain.UpdateCandidate, error) {
	existing, err := store.UpdateCandidates(ctx, s.db, p.ID)
	if err != nil {
		return CheckOutput{}, nil, err
	}
	byService := map[string]domain.UpdateCandidate{}
	for _, c := range existing {
		byService[c.Service] = c
	}
	quarantine, err := s.quarantined(ctx, p.ID)
	if err != nil {
		return CheckOutput{}, nil, err
	}
	var out CheckOutput
	var cands []domain.UpdateCandidate
	switch p.TargetType {
	case domain.UpdateTargetStack:
		cands, out, err = s.checkStack(ctx, p, jobID, byService, quarantine)
	case domain.UpdateTargetContainer:
		cands, err = s.checkContainer(ctx, p, jobID, byService, quarantine)
	default:
		err = fmt.Errorf("updates: unknown target type %q", p.TargetType)
	}
	if err != nil && len(cands) == 0 {
		return out, nil, err
	}
	var keep []string
	for i := range cands {
		c := &cands[i]
		c.UpdatedAt = s.now()
		if c.ID == "" {
			c.ID = ids.New()
		}
		if uerr := store.UpsertUpdateCandidate(ctx, s.db, c); uerr != nil {
			return out, cands, uerr
		}
		keep = append(keep, c.Service)
		out.Checked++
		switch c.Status {
		case domain.CandidateAvailable:
			out.Available++
		case domain.CandidateUpToDate:
			out.UpToDate++
		case domain.CandidateQuarantined:
			out.Quarantined++
		case domain.CandidateIneligible:
			out.Ineligible++
		case domain.CandidateCheckFailed:
			out.Failed++
		}
	}
	if derr := store.DeleteUpdateCandidatesExcept(ctx, s.db, p.ID, keep); derr != nil && err == nil {
		err = derr
	}
	return out, cands, err
}

// quarantined maps service -> quarantined digests.
func (s *Service) quarantined(ctx context.Context, policyID string) (map[string][]string, error) {
	qs, err := store.UpdateQuarantine(ctx, s.db, policyID)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, q := range qs {
		out[q.Service] = append(out[q.Service], q.Digest)
	}
	return out, nil
}

// sourceHash reads the hash of a stack's definition on disk ("" while the
// environment is offline or the read fails).
func (s *Service) sourceHash(ctx context.Context, st domain.Stack) string {
	env, err := s.opts.Environments.GetEnvironment(ctx, st.EnvironmentID)
	if err != nil || !env.Online {
		return ""
	}
	raw, err := s.opts.Agents.RequestEnvironment(ctx, st.EnvironmentID, protocol.ReqComposeRead,
		protocol.ComposeReadInput{Stack: projectRef(st)}, s.opts.RequestTimeout)
	if err != nil {
		s.log.Warn("update check: could not read the stack's definition", "stack_id", st.ID, "error", err)
		return ""
	}
	var out protocol.ComposeReadOutput
	if json.Unmarshal(raw, &out) != nil {
		return ""
	}
	return out.Snapshot.Hash
}

// reset clears the fields a check recomputes.
func reset(c domain.UpdateCandidate, policyID, service string) domain.UpdateCandidate {
	c.PolicyID, c.Service = policyID, service
	c.Eligible, c.Reason, c.ReasonMessage, c.NonVersionTag = false, "", "", false
	c.ErrorClass, c.ErrorMessage, c.RetryAfterSeconds = "", "", 0
	c.CandidateDigest, c.CandidateIndexDigest, c.CandidatePublishedAt = "", "", nil
	return c
}

func ineligible(c *domain.UpdateCandidate, reason, message string) {
	c.Eligible, c.Status, c.Reason, c.ReasonMessage = false, domain.CandidateIneligible, reason, message
}

func applyEligibility(c *domain.UpdateCandidate, r eligible.Result) bool {
	if r.Ref.Repository != "" {
		c.Registry, c.Repository, c.Tag = r.Ref.Host, r.Ref.Repository, r.Ref.Tag
	}
	if !r.Eligible {
		ineligible(c, r.Reason, r.Message)
		return false
	}
	c.Eligible, c.NonVersionTag, c.ReasonMessage = true, r.NonVersionTag, r.Message
	return true
}

func (s *Service) checkStack(ctx context.Context, p domain.UpdatePolicy, jobID string, prev map[string]domain.UpdateCandidate,
	quarantine map[string][]string) ([]domain.UpdateCandidate, CheckOutput, error) {
	var out CheckOutput
	st, err := s.opts.Stacks.Get(ctx, p.TargetID)
	if errors.Is(err, domain.ErrStackNotFound) {
		return nil, out, &classed{class: classTargetNotFound, recovery: "The policy's stack was removed; delete the policy.", err: err}
	}
	if err != nil {
		return nil, out, err
	}
	out.SourceHashBefore = s.sourceHash(ctx, st)
	images := map[string]domain.StackImage{}
	for _, i := range st.Images {
		images[i.Service] = i
	}
	services := slices.Clone(st.Services)
	slices.SortFunc(services, func(a, b domain.StackServiceDef) int { return strings.Compare(a.Name, b.Name) })
	excludedByLabel := map[string]bool{}
	if s.opts.Resources != nil {
		// Only the stack's containers: the checks of every policy start
		// together, and a full listing per check would hold the agent's
		// request slots on a loaded host (#307).
		containers, err := s.opts.Resources.ListProjectContainers(ctx, p.EnvironmentID, st.Name)
		if err != nil {
			return nil, out, err
		}
		for _, container := range containers {
			if container.Stack != nil && container.Stack.Project == st.Name && protocol.UpdateExcluded(container.Labels) {
				excludedByLabel[container.Stack.Service] = true
			}
		}
	}
	var cands []domain.UpdateCandidate
	for _, def := range services {
		c := reset(prev[def.Name], p.ID, def.Name)
		img, deployed := images[def.Name]
		c.Reference, c.Platform, c.AppliedDigest, c.AppliedImageID = def.Image, "", "", ""
		if deployed {
			c.Reference, c.Platform, c.AppliedDigest, c.AppliedImageID = img.Image, img.Platform, img.Digest, img.ImageID
		}
		switch {
		case excludedByLabel[def.Name]:
			ineligible(&c, domain.UpdateReasonExcluded, "A container of this service has docker-manager.update.exclude=true.")
		case !selected(p, def.Name):
			ineligible(&c, domain.UpdateReasonExcluded, "The service is not opted in by this policy (excluded or not listed).")
		case !applyEligibility(&c, eligible.Check(eligible.Subject{Reference: c.Reference, Build: def.Build, PullPolicy: def.PullPolicy})):
		case st.Applied == nil || !deployed || img.ImageID == "":
			ineligible(&c, domain.UpdateReasonNotDeployed, "Docker Manager has not deployed this service yet: deploy the stack to record the digest it runs.")
		case img.Digest == "":
			ineligible(&c, domain.UpdateReasonNoBaseline, "The image the service runs has no registry digest (it was built or loaded locally): "+
				"deploy the stack with pull: always to record the digest it runs.")
		default:
			if err := s.compare(ctx, p, &c, prev[def.Name], st.ID, []string{img.Digest}, jobID, quarantine[def.Name]); err != nil {
				return cands, out, err
			}
		}
		c.SourceHashBefore = out.SourceHashBefore
		cands = append(cands, c)
	}
	out.SourceHashAfter = s.sourceHash(ctx, st)
	for i := range cands {
		cands[i].SourceHashAfter = out.SourceHashAfter
	}
	if out.SourceHashBefore != "" && out.SourceHashAfter != "" && out.SourceHashBefore != out.SourceHashAfter {
		return cands, out, &classed{class: classSourceChanged,
			recovery: "The stack's definition changed on disk while it was checked (Docker Manager never writes it during a check). " +
				"Review the change, deploy the stack, then check again.",
			err: fmt.Errorf("the definition hash changed during the check (%s -> %s)", shortHash(out.SourceHashBefore), shortHash(out.SourceHashAfter))}
	}
	return cands, out, nil
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func (s *Service) checkContainer(ctx context.Context, p domain.UpdatePolicy, jobID string, prev map[string]domain.UpdateCandidate,
	quarantine map[string][]string) ([]domain.UpdateCandidate, error) {
	if s.opts.Resources == nil {
		return nil, errors.New("updates: the Docker resource service is not available")
	}
	c := reset(prev[p.TargetID], p.ID, p.TargetID)
	d, err := s.opts.Resources.InspectContainer(ctx, p.EnvironmentID, p.TargetID)
	var de *domain.DockerError
	switch {
	case errors.As(err, &de) && de.Code == domain.DockerNotFound:
		ineligible(&c, domain.UpdateReasonNotDeployed, "The container no longer exists on the environment.")
		return []domain.UpdateCandidate{c}, nil
	case errors.As(err, &de) && de.Code == domain.DockerEnvironmentOffline:
		return nil, &classed{class: classEnvironmentOffln, recovery: "The environment's agent is offline; the check runs again on schedule.", err: err}
	case err != nil:
		return nil, err
	}
	if reason, msg := s.containerIneligible(ctx, p.EnvironmentID, d); reason != "" {
		ineligible(&c, reason, msg)
		return []domain.UpdateCandidate{c}, nil
	}
	if protocol.UpdateExcluded(d.Labels) {
		ineligible(&c, domain.UpdateReasonExcluded, "The container has docker-manager.update.exclude=true.")
		return []domain.UpdateCandidate{c}, nil
	}
	_, spec, err := s.opts.Resources.ManagedSpec(ctx, p.EnvironmentID, d.Labels)
	if err != nil {
		return nil, err
	}
	c.Reference, c.AppliedImageID, c.AppliedDigest, c.Platform = spec.Image, d.ImageID, "", ""
	if !applyEligibility(&c, eligible.Check(eligible.Subject{Reference: spec.Image})) {
		return []domain.UpdateCandidate{c}, nil
	}
	img, err := s.opts.Resources.InspectImage(ctx, p.EnvironmentID, d.ImageID)
	if err != nil {
		return nil, err
	}
	if img.OS != "" && img.Architecture != "" {
		c.Platform = img.OS + "/" + img.Architecture
		if img.Variant != "" {
			c.Platform += "/" + img.Variant
		}
	}
	applied := imageref.DigestsFor(spec.Image, img.RepoDigests)
	if len(applied) == 0 {
		ineligible(&c, domain.UpdateReasonNoBaseline, "The image the container runs has no registry digest (built or loaded locally).")
		return []domain.UpdateCandidate{c}, nil
	}
	c.AppliedDigest = applied[0]
	if err := s.compare(ctx, p, &c, prev[p.TargetID], "", applied, jobID, quarantine[c.Service]); err != nil {
		return nil, err
	}
	return []domain.UpdateCandidate{c}, nil
}

// compare checks the registry and sets the candidate's status. The
// comparison is on the host-platform manifest: the tag's platform digest
// (or its index digest, which the Engine may have recorded) equal to an
// applied digest is up to date; so is an applied index digest whose
// platform manifest equals the new one (only the index changed). Registry
// failures are recorded on the candidate (check_failed with the class and
// retry guidance) and never trigger a pull. A new digest gets its image's
// creation time (known: the candidate before this check, whose time is
// reused for the same digest).
func (s *Service) compare(ctx context.Context, p domain.UpdatePolicy, c *domain.UpdateCandidate, known domain.UpdateCandidate, stackID string,
	applied []string, jobID string, quarantined []string) error {
	now := s.now()
	c.CheckedAt, c.CheckJobID = &now, jobID
	req := registries.CheckRequest{RegistrySelectRequest: domain.RegistrySelectRequest{Reference: c.Reference, EnvironmentID: p.EnvironmentID,
		StackID: stackID}, Platform: c.Platform, JobID: jobID}
	res, err := s.opts.Registries.Check(ctx, req)
	c.RegistryConnectionID = ""
	if res.Selection.Selected != nil {
		c.RegistryConnectionID = res.Selection.Selected.ID
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		class, msg, retry, ok := checkFailure(err)
		if !ok {
			return err
		}
		c.Status, c.ErrorClass, c.ErrorMessage, c.RetryAfterSeconds = domain.CandidateCheckFailed, class, msg, retry
		return nil
	}
	platform, index := res.Result.PlatformDigest, res.Result.Digest
	if platform == "" {
		platform = index
	}
	if index == platform {
		index = ""
	}
	if slices.Contains(applied, platform) || (index != "" && slices.Contains(applied, index)) || s.samePlatform(ctx, p, c, stackID, applied, platform, jobID) {
		c.Status = domain.CandidateUpToDate
		return nil
	}
	c.CandidateDigest, c.CandidateIndexDigest = platform, index
	c.CandidatePublishedAt = s.published(ctx, req, known, platform)
	c.Status = domain.CandidateAvailable
	if slices.Contains(quarantined, platform) {
		c.Status = domain.CandidateQuarantined
		c.ReasonMessage = "This digest failed an update before and is quarantined: it is not applied automatically. " +
			"A newer digest of the tag becomes a new candidate."
	}
	return nil
}

// published returns the creation time of the candidate image: the one
// already recorded for this digest, else read from the registry (image
// config "created"). Display only: nil when unavailable, never an error.
func (s *Service) published(ctx context.Context, req registries.CheckRequest, known domain.UpdateCandidate, digest string) *time.Time {
	if known.CandidateDigest == digest && known.CandidatePublishedAt != nil {
		at := known.CandidatePublishedAt.UTC()
		return &at
	}
	at, err := s.opts.Registries.Created(ctx, req, digest)
	if err != nil || at.IsZero() {
		if err != nil && !errors.Is(err, regclient.ErrNoCreated) && ctx.Err() == nil {
			s.log.Debug("update check: the candidate image's creation time is unavailable", "error_class", regclient.ClassOf(err))
		}
		return nil
	}
	at = at.UTC()
	return &at
}

// samePlatform resolves an applied index digest to its host-platform
// manifest: equal to the tag's current platform digest means only the
// index changed (no update).
func (s *Service) samePlatform(ctx context.Context, p domain.UpdatePolicy, c *domain.UpdateCandidate, stackID string, applied []string, platform, jobID string) bool {
	ref, err := imageref.Parse(c.Reference)
	if err != nil || c.Platform == "" {
		return false
	}
	for _, d := range applied {
		res, err := s.opts.Registries.Check(ctx, registries.CheckRequest{RegistrySelectRequest: domain.RegistrySelectRequest{
			Reference: ref.Name() + "@" + d, EnvironmentID: p.EnvironmentID, StackID: stackID}, Platform: c.Platform, JobID: jobID})
		if err == nil && res.Result.PlatformDigest == platform {
			return true
		}
	}
	return false
}

// checkFailure maps a registry check error to a candidate error (class,
// message, retry guidance); ok is false for errors that are not about the
// registry or its connection (they fail the job).
func checkFailure(err error) (class, message string, retryAfter int, ok bool) {
	var re *regclient.Error
	var amb *domain.AmbiguousRegistryError
	var fe *domain.FieldError
	switch {
	case errors.As(err, &re):
		msg := re.Message
		switch re.Class {
		case regclient.ClassUnauthorized:
			msg = "The registry refused the credentials (or requires them): check the registry connection for this image. " + msg
		case regclient.ClassRateLimited:
			msg = "The registry's rate limit was reached; the check is not retried until its guidance allows. " + msg
		}
		return re.Class, strings.TrimSpace(msg), int(re.RetryAfter.Seconds()), true
	case errors.As(err, &amb):
		return "ambiguous_registry_connection", "Several registry connections match this image equally: bind one to the stack " +
			"or environment, or give it a higher priority.", 0, true
	case errors.Is(err, domain.ErrRegistryConnectionRevoked):
		return "registry_connection_revoked", "The registry connection selected for this image is revoked; Docker Manager never falls " +
			"back to anonymous access.", 0, true
	case errors.As(err, &fe):
		return domain.UpdateReasonInvalidRef, fe.Message, 0, true
	}
	return "", "", 0, false
}
