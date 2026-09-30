package updates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/imageref"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// ActionQuarantine is the audit action of a quarantined candidate digest.
const ActionQuarantine = "update.quarantine"

// plan is what a run would apply.
type plan struct {
	policy  domain.UpdatePolicy
	stack   *domain.Stack
	details *protocol.ContainerDetails
	spec    *protocol.ContainerSpec
	items   []domain.UpdateCandidate
	skipped []domain.UpdateCandidate
	drift   bool
	// sourceHash is the applied revision's hash (stacks).
	sourceHash string
}

// runnable reports whether a candidate is applied by a run: an eligible
// update whose last check succeeded (never after a failed check or a
// failed pull: no repeated pulls on 401/403/429) and not quarantined.
func runnable(c domain.UpdateCandidate) bool {
	return c.Eligible && c.Status == domain.CandidateAvailable && c.CandidateDigest != ""
}

// planRun collects the candidates a run applies (all runnable ones, or the
// selected services).
func (s *Service) planRun(ctx context.Context, p domain.UpdatePolicy, selection []string) (*plan, error) {
	cands, err := store.UpdateCandidates(ctx, s.db, p.ID)
	if err != nil {
		return nil, err
	}
	pl := &plan{policy: p}
	for _, c := range cands {
		switch {
		case len(selection) > 0 && !slices.Contains(selection, c.ID) && !slices.Contains(selection, c.Service):
			continue
		case p.TargetType == domain.UpdateTargetStack && !selected(p, c.Service):
			c.Status, c.Reason = domain.CandidateIneligible, domain.UpdateReasonExcluded
			pl.skipped = append(pl.skipped, c)
		case runnable(c):
			pl.items = append(pl.items, c)
		default:
			pl.skipped = append(pl.skipped, c)
		}
	}
	for _, sel := range selection {
		if !slices.ContainsFunc(cands, func(c domain.UpdateCandidate) bool { return c.ID == sel || c.Service == sel }) {
			return nil, fieldErr("candidates", "unknown candidate %q", sel)
		}
	}
	switch p.TargetType {
	case domain.UpdateTargetStack:
		st, err := s.opts.Stacks.Get(ctx, p.TargetID)
		if err != nil {
			return nil, err
		}
		pl.stack = &st
		if st.Applied != nil {
			pl.sourceHash = st.Applied.Hash
		}
		pl.drift = st.Applied == nil || st.UndeployedChanges()
	case domain.UpdateTargetContainer:
		if len(pl.items) == 0 {
			return pl, nil
		}
		if s.opts.Resources == nil {
			return nil, errors.New("updates: the Docker resource service is not available")
		}
		d, err := s.opts.Resources.InspectContainer(ctx, p.EnvironmentID, p.TargetID)
		if err != nil {
			return nil, err
		}
		if reason, msg := s.containerIneligible(ctx, p.EnvironmentID, d); reason != "" {
			return nil, &domain.UpdateError{Code: domain.UpdateErrTargetIneligible, Message: msg}
		}
		_, spec, err := s.opts.Resources.ManagedSpec(ctx, p.EnvironmentID, d.Labels)
		if err != nil {
			return nil, err
		}
		pl.details, pl.spec = &d, spec
	}
	return pl, nil
}

// request builds the update.run job request of a plan.
func (s *Service) request(ctx context.Context, pl *plan) (jobs.Request, error) {
	p := pl.policy
	in := protocol.UpdateRunInput{PolicyID: p.ID, WaitTimeoutSeconds: p.WaitTimeoutSeconds}
	var targets []domain.JobTarget
	stackID := ""
	if pl.stack != nil {
		ref := projectRef(*pl.stack)
		in.StackID, in.Stack, in.ExpectSourceHash, stackID = pl.stack.ID, &ref, pl.sourceHash, pl.stack.ID
		targets = []domain.JobTarget{{Type: domain.TargetStack, ID: pl.stack.ID}}
	} else {
		// The container's ownership labels (current or legacy keys), sent
		// under the keys its agent writes.
		own := map[string]string{}
		for _, k := range protocol.OwnershipLabels {
			if v := protocol.LabelValue(pl.details.Labels, k); v != "" {
				own[k] = v
			}
		}
		if fh, ok := s.opts.Agents.(featureHub); ok && !fh.EnvironmentHasFeature(p.EnvironmentID, protocol.FeatureLabels) {
			own = protocol.LegacyLabels(own)
		}
		in.Container = &protocol.UpdateContainer{Name: pl.details.Name, ID: pl.details.ID, Spec: *pl.spec, Ownership: own}
		targets = []domain.JobTarget{{Type: domain.TargetContainer, ID: pl.details.Name}}
	}
	for _, c := range pl.items {
		u := protocol.UpdateService{Service: c.Service, Reference: c.Reference, Digest: c.CandidateDigest, IndexDigest: c.CandidateIndexDigest,
			Platform: c.Platform}
		if in.Container != nil {
			u.Reference = pl.spec.Image // the saved reference, unchanged
		}
		sel, err := s.opts.Registries.Select(ctx, domain.RegistrySelectRequest{Reference: u.Reference, EnvironmentID: p.EnvironmentID, StackID: stackID})
		if err != nil {
			return jobs.Request{}, err
		}
		if sel.Selected != nil {
			u.RegistryConnection = sel.Selected.ID
			if !slices.Contains(in.RegistryConnections, sel.Selected.ID) {
				in.RegistryConnections = append(in.RegistryConnections, sel.Selected.ID)
			}
		}
		in.Services = append(in.Services, u)
	}
	slices.Sort(in.RegistryConnections)
	if err := in.Validate(); err != nil {
		return jobs.Request{}, err
	}
	return jobs.Request{Kind: jobspec.UpdateRun, PolicyID: p.ID, EnvironmentID: p.EnvironmentID, Targets: targets, Input: in}, nil
}

// Preview shows what a run would do now; nothing changes.
func (s *Service) Preview(ctx context.Context, policyID string, selection []string) (domain.UpdatePreview, error) {
	p, err := store.GetUpdatePolicy(ctx, s.db, policyID)
	if err != nil {
		return domain.UpdatePreview{}, err
	}
	pl, err := s.planRun(ctx, p, selection)
	if err != nil {
		return domain.UpdatePreview{}, err
	}
	pv := domain.UpdatePreview{Policy: p, Skipped: pl.skipped, SourceHash: pl.sourceHash, SourceDrift: pl.drift && len(pl.items) > 0,
		InWindow: InWindow(p, s.clk.Now())}
	running := map[string]bool{}
	if pl.stack != nil {
		pv.Dependencies = pl.stack.Services
		for _, st := range pl.stack.EngineServices {
			running[st.Service] = st.Running > 0
		}
	} else if pl.details != nil {
		running[pl.details.Name] = pl.details.Running
	}
	var applied []string
	for _, c := range pl.items {
		it := domain.UpdatePreviewItem{Candidate: c, Running: running[c.Service]}
		switch {
		case !it.Running:
			it.Downtime = "Stopped now: kept stopped and not recreated (a standalone container is recreated but not started); no downtime."
		case pl.stack != nil:
			it.Downtime = "Stopped, recreated from the same Compose definition and started again after its dependencies meet their conditions; " +
				"unavailable until it is running and healthy."
			applied = append(applied, c.Service)
		default:
			it.Downtime = "Stopped, recreated from its saved specification and started again; unavailable until it is running and healthy."
		}
		pv.Items = append(pv.Items, it)
	}
	if pl.stack != nil {
		pv.Restarted = restartSet(pl.stack.Services, applied, running)
	}
	pv.SharedTag = s.sharedTag(ctx, p, pl)
	pv.Fingerprint = fingerprint(pl)
	return pv, nil
}

// restartSet returns the running dependents that declare restart: true on
// an updated service (transitively), which the run restarts too.
func restartSet(defs []domain.StackServiceDef, updated []string, running map[string]bool) []string {
	seen := map[string]bool{}
	var walk func(n string)
	walk = func(n string) {
		for _, d := range defs {
			for _, dep := range d.DependsOn {
				if dep.Service == n && dep.Restart && !seen[d.Name] {
					seen[d.Name] = true
					walk(d.Name)
				}
			}
		}
	}
	for _, u := range updated {
		walk(u)
	}
	var out []string
	for n := range seen {
		if !slices.Contains(updated, n) && running[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// sharedTag lists the other consumers of the run's tags on the
// environment: other stacks' services, the stack's services that are not
// updated, and standalone containers. Pulling a tag moves it for all of
// them; they pick up the new image at their next recreate (accepted v1
// behavior, #20).
func (s *Service) sharedTag(ctx context.Context, p domain.UpdatePolicy, pl *plan) []domain.UpdateSharedConsumer {
	out := []domain.UpdateSharedConsumer{}
	if len(pl.items) == 0 {
		return out
	}
	updated := map[string]bool{}
	for _, c := range pl.items {
		updated[c.Service] = true
	}
	matches := func(ref string) string {
		for _, c := range pl.items {
			if imageref.SameTag(ref, c.Reference) {
				return c.Reference
			}
		}
		return ""
	}
	sts, err := s.opts.Stacks.List(ctx, domain.StackFilter{EnvironmentID: p.EnvironmentID})
	if err != nil {
		s.log.Warn("update preview: could not list stacks", "environment_id", p.EnvironmentID, "error", err)
	}
	projects := map[string]bool{}
	for _, st := range sts {
		projects[st.Name] = true
		for _, img := range st.Images {
			if pl.stack != nil && st.ID == pl.stack.ID && updated[img.Service] {
				continue
			}
			if ref := matches(img.Image); ref != "" {
				out = append(out, domain.UpdateSharedConsumer{Reference: ref, StackID: st.ID, StackName: st.Name, Service: img.Service})
			}
		}
	}
	if s.opts.Resources != nil {
		cs, err := s.opts.Resources.ListContainers(ctx, p.EnvironmentID)
		if err != nil {
			s.log.Info("update preview: containers not listed (environment offline?)", "environment_id", p.EnvironmentID, "error", err)
		}
		for _, c := range cs {
			if c.Stack != nil && projects[c.Stack.Project] {
				continue // listed above through its stack
			}
			if pl.details != nil && c.ID == pl.details.ID {
				continue
			}
			if ref := matches(c.Image); ref != "" {
				cons := domain.UpdateSharedConsumer{Reference: ref, Container: c.Name}
				if c.Stack != nil {
					cons.StackName, cons.Service = c.Stack.Project, c.Stack.Service
				}
				out = append(out, cons)
			}
		}
	}
	slices.SortFunc(out, func(a, b domain.UpdateSharedConsumer) int {
		return strings.Compare(a.StackName+"/"+a.Service+"/"+a.Container, b.StackName+"/"+b.Service+"/"+b.Container)
	})
	return out
}

// fingerprint identifies a plan: target, applied source, candidates and
// their digests.
func fingerprint(pl *plan) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%s\n", pl.policy.ID, pl.policy.TargetID, pl.sourceHash)
	for _, c := range pl.items {
		fmt.Fprintf(h, "%s %s %s %s\n", c.Service, c.Reference, c.CandidateDigest, c.AppliedDigest)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// RunRequest is a manual run.
type RunRequest struct {
	Principal authz.Principal
	PolicyID  string
	// Candidates selects candidates by ID or service (empty: all).
	Candidates []string
	// Fingerprint, when set, must equal the current preview's: a change
	// since the preview refuses the run (update_preview_stale).
	Fingerprint    string
	IdempotencyKey string
}

// Run enqueues a manual update.run (the policy's schedule and window do
// not apply; the source must be the applied revision).
func (s *Service) Run(ctx context.Context, r RunRequest) (domain.Job, error) {
	p, err := store.GetUpdatePolicy(ctx, s.db, r.PolicyID)
	if err != nil {
		return domain.Job{}, err
	}
	pl, err := s.planRun(ctx, p, r.Candidates)
	if err != nil {
		return domain.Job{}, err
	}
	if r.Fingerprint != "" && r.Fingerprint != fingerprint(pl) {
		return domain.Job{}, &domain.UpdateError{Code: domain.UpdateErrPreviewStale,
			Message: "the candidates, digests or the stack's definition changed since the preview; preview again"}
	}
	if len(pl.items) == 0 {
		return domain.Job{}, &domain.UpdateError{Code: domain.UpdateErrNoCandidates,
			Message: "nothing to update: no checked candidate with a new digest (run a check first; quarantined and failed candidates are not applied)"}
	}
	if pl.drift {
		return domain.Job{}, &domain.UpdateError{Code: domain.UpdateErrSourceDrift,
			Message: "the stack's definition on disk differs from the applied revision (undeployed changes): deploy the stack first; an update never deploys an edit"}
	}
	req, err := s.request(ctx, pl)
	if err != nil {
		return domain.Job{}, err
	}
	req.Principal, req.IdempotencyKey = r.Principal, r.IdempotencyKey
	j, _, err := s.opts.Jobs.Enqueue(ctx, req)
	return j, err
}

// StartCheck enqueues a manual update.check.
func (s *Service) StartCheck(ctx context.Context, principal authz.Principal, policyID, key string) (domain.Job, error) {
	p, err := store.GetUpdatePolicy(ctx, s.db, policyID)
	if err != nil {
		return domain.Job{}, err
	}
	req := checkRequest(p)
	req.Principal, req.IdempotencyKey = principal, key
	j, _, err := s.opts.Jobs.Enqueue(ctx, req)
	return j, err
}

func checkRequest(p domain.UpdatePolicy) jobs.Request {
	t := domain.JobTarget{Type: domain.TargetStack, ID: p.TargetID}
	if p.TargetType == domain.UpdateTargetContainer {
		t.Type = domain.TargetContainer
	}
	return jobs.Request{Kind: jobspec.UpdateCheck, PolicyID: p.ID, EnvironmentID: p.EnvironmentID, Targets: []domain.JobTarget{t},
		Input: CheckInput{PolicyID: p.ID}}
}

// Finish hooks.

// pullFailure are job error classes after which a candidate needs a new
// successful check before another run (no repeated pulls on 401/403/429).
func requiresCheck(class string) bool {
	return class != ""
}

// onRunFinished records a run's outcome in the job's finishing
// transaction: applied digests and history on success; on failure the
// candidates attributed to the new images are quarantined (and audited),
// the others need a new check before the next run.
func (s *Service) onRunFinished(ctx context.Context, db bun.IDB, j domain.Job) error {
	var in protocol.UpdateRunInput
	if err := json.Unmarshal(j.Input, &in); err != nil {
		s.log.Warn("update run with a malformed input", "job_id", j.ID)
		return nil
	}
	p, err := store.GetUpdatePolicy(ctx, db, in.PolicyID)
	if errors.Is(err, domain.ErrUpdatePolicyNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var out protocol.UpdateRunOutput
	if len(j.ResultOutput) > 0 {
		if err := json.Unmarshal(j.ResultOutput, &out); err != nil {
			s.log.Warn("update run reported a malformed output", "job_id", j.ID)
		}
	}
	cands, err := store.UpdateCandidates(ctx, db, p.ID)
	if err != nil {
		return err
	}
	byService := map[string]*domain.UpdateCandidate{}
	for i := range cands {
		byService[cands[i].Service] = &cands[i]
	}
	now := s.now()
	succeeded := j.State == domain.JobSucceeded
	var images []domain.StackImage
	for _, u := range in.Services {
		c := byService[u.Service]
		r := out.Result(u.Service)
		h := domain.UpdateHistoryEntry{ID: ids.New(), PolicyID: p.ID, EnvironmentID: p.EnvironmentID, TargetType: p.TargetType,
			TargetID: p.TargetID, Service: u.Service, Reference: u.Reference, RegistryConnectionID: u.RegistryConnection, JobID: j.ID,
			SourceHashBefore: out.SourceHashBefore, SourceHashAfter: out.SourceHashAfter, At: now, ToDigest: u.Digest}
		if r != nil {
			h.FromDigest, h.FromImageID, h.ToImageID = r.FromDigest, r.FromImageID, r.ToImageID
			if r.ToDigest != "" {
				h.ToDigest = r.ToDigest
			}
		}
		switch {
		case succeeded && r != nil && r.Outcome == protocol.UpdateUpdated:
			h.Outcome = domain.UpdateOutcomeUpdated
			if c != nil {
				c.PreviousDigest, c.AppliedDigest, c.AppliedImageID = c.AppliedDigest, h.ToDigest, r.ToImageID
				c.Status, c.CandidateDigest, c.CandidateIndexDigest, c.CandidatePublishedAt = domain.CandidateUpToDate, "", "", nil
			}
			images = append(images, domain.StackImage{Service: u.Service, Image: u.Reference, ImageID: r.ToImageID, Digest: h.ToDigest,
				Platform: u.Platform})
		case succeeded && r != nil && r.Outcome == protocol.UpdateUnchanged:
			h.Outcome = domain.UpdateOutcomeUnchanged
			if c != nil {
				c.Status, c.CandidateDigest, c.CandidateIndexDigest, c.CandidatePublishedAt = domain.CandidateUpToDate, "", "", nil
			}
		case succeeded && r != nil && r.Outcome == protocol.UpdateKeptStopped:
			h.Outcome = domain.UpdateOutcomeKeptStopped
		case succeeded:
			continue // nothing reported for it
		default:
			h.Outcome, h.ErrorClass = domain.UpdateOutcomeFailed, j.ErrorClass
			attempted := r != nil && (r.Outcome == protocol.UpdateFailed || r.Outcome == protocol.UpdateUpdated)
			if out.Quarantine && attempted {
				if err := s.quarantine(ctx, db, p, u, j, c); err != nil {
					return err
				}
			} else if c != nil && c.Status == domain.CandidateAvailable && requiresCheck(j.ErrorClass) {
				c.Status, c.ErrorClass = domain.CandidateRunFailed, j.ErrorClass
				c.ErrorMessage = "The last update run failed (" + j.ErrorClass + "); a successful check is needed before the next run."
			}
		}
		if err := store.InsertUpdateHistory(ctx, db, h); err != nil {
			return err
		}
		if c != nil {
			c.UpdatedAt = now
			if err := store.UpsertUpdateCandidate(ctx, db, c); err != nil {
				return err
			}
		}
	}
	if p.TargetType == domain.UpdateTargetStack && in.StackID != "" && (len(images) > 0 || out.After != nil) {
		var after []domain.StackServiceState
		for _, st := range out.After {
			after = append(after, domain.StackServiceState{Service: st.Service, Containers: st.Containers, Running: st.Running, ImageIDs: st.ImageIDs})
		}
		if err := s.opts.Stacks.RecordUpdatedImages(ctx, db, in.StackID, images, after); err != nil {
			return err
		}
	}
	return nil
}

// quarantine records a failed candidate digest (never retried
// automatically) and audits it.
func (s *Service) quarantine(ctx context.Context, db bun.IDB, p domain.UpdatePolicy, u protocol.UpdateService, j domain.Job, c *domain.UpdateCandidate) error {
	q := domain.UpdateQuarantine{PolicyID: p.ID, Service: u.Service, Digest: u.Digest, JobID: j.ID, ErrorClass: j.ErrorClass, CreatedAt: s.now()}
	if err := store.QuarantineUpdateDigest(ctx, db, q); err != nil {
		return err
	}
	if c != nil {
		c.Status, c.ErrorClass = domain.CandidateQuarantined, j.ErrorClass
		c.ErrorMessage = "The update to this digest failed (" + j.ErrorClass + ") and it is quarantined: it is not applied automatically. " +
			"There is no automatic rollback; to go back, pin the previous digest (" + c.AppliedDigest + ") in your own definition and deploy it."
	}
	if s.opts.Audit == nil {
		return nil
	}
	return s.opts.Audit.RecordTx(ctx, db, domain.AuditEvent{Category: domain.AuditOperations, Action: ActionQuarantine,
		Actor: audit.ServiceActor(), EnvironmentID: p.EnvironmentID, JobID: j.ID, Outcome: domain.AuditFailure, ErrorClass: j.ErrorClass,
		Targets: []domain.AuditTarget{{Type: catalog.TypeUpdatePolicy, ID: p.ID, EnvironmentID: p.EnvironmentID}},
		Details: map[string]any{"service": u.Service, "digest": u.Digest, "reference": u.Reference}})
}

// onDeployFinished refreshes the digest baseline after a stack deploy:
// the applied images changed, so the stack's candidates need a new check
// (a deploy is a new source revision; an update never writes one).
func (s *Service) onDeployFinished(ctx context.Context, db bun.IDB, j domain.Job) error {
	if j.State != domain.JobSucceeded {
		return nil
	}
	for _, t := range j.Targets {
		if t.Type != domain.TargetStack {
			continue
		}
		ps, err := store.UpdatePoliciesForTarget(ctx, db, j.EnvironmentID, domain.UpdateTargetStack, t.ID)
		if err != nil {
			return err
		}
		for _, p := range ps {
			cands, err := store.UpdateCandidates(ctx, db, p.ID)
			if err != nil {
				return err
			}
			for _, c := range cands {
				if c.Status == domain.CandidateIneligible {
					continue
				}
				c.Status, c.CandidateDigest, c.CandidateIndexDigest, c.UpdatedAt = domain.CandidateUnchecked, "", "", s.now()
				c.CandidatePublishedAt = nil
				c.ErrorClass, c.ErrorMessage = "", ""
				if err := store.UpsertUpdateCandidate(ctx, db, &c); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
