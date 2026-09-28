package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/agents"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protection"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Operations (POST /stacks/{id}/operations) and their job kinds.
var operationKinds = map[string]domain.JobKind{
	"start":   jobspec.StackStart,
	"stop":    jobspec.StackStop,
	"restart": jobspec.StackRestart,
	"down":    jobspec.StackDown,
}

// protectedActions are the stack kinds refused on Docker Manager's own Compose
// project (#32): they would stop or delete Docker Manager. Deploys
// (redeploys, pulls, digest updates) are allowed: the agent hands its own
// container to a helper container (internal/agent/selfupdate).
var protectedActions = map[domain.JobKind]protection.Action{
	jobspec.StackStop: protection.Stop, jobspec.StackRestart: protection.Restart,
	jobspec.StackDown: protection.Down, jobspec.StackRemove: protection.Down,
}

// OperationKind returns the job kind of an operation ("start", "stop",
// "restart", "down").
func OperationKind(action string) (domain.JobKind, bool) {
	k, ok := operationKinds[action]
	return k, ok
}

func (s *Service) enqueue(ctx context.Context, p authz.Principal, st domain.Stack, kind domain.JobKind, r domain.StackJobRequest, in protocol.StackJobInput) (domain.Job, error) {
	for _, svc := range r.Services {
		if svc == "" || len(svc) > 128 || strings.ContainsAny(svc, " /\\") {
			return domain.Job{}, &domain.InputError{Field: "services", Message: "invalid service name"}
		}
	}
	if r.TimeoutSeconds < 0 || r.TimeoutSeconds > 3600 {
		return domain.Job{}, &domain.InputError{Field: "timeoutSeconds", Message: "must be between 0 and 3600"}
	}
	if action, ok := protectedActions[kind]; ok && s.opts.Protection != nil {
		p, err := s.opts.Protection.ProjectProtection(ctx, st.EnvironmentID, st.Name)
		if err != nil {
			return domain.Job{}, err
		}
		if err := protection.Check(p, action, false); err != nil {
			return domain.Job{}, &domain.DockerError{Code: domain.DockerProtected, Message: err.Error()}
		}
	}
	in.StackID, in.Stack, in.Services, in.TimeoutSeconds = st.ID, Ref(st), r.Services, r.TimeoutSeconds
	j, _, err := s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: kind, Principal: p, EnvironmentID: st.EnvironmentID,
		Targets: []domain.JobTarget{{Type: domain.TargetStack, ID: st.ID}}, Input: in, IdempotencyKey: r.IdempotencyKey})
	if err != nil {
		return domain.Job{}, err
	}
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		cur, err := store.GetStack(ctx, tx, st.ID)
		if err != nil {
			return err
		}
		cur.LastJobID, cur.LastJobKind, cur.UpdatedAt = j.ID, j.Kind, s.now()
		st = cur
		return store.UpdateStack(ctx, tx, &cur)
	})
	if err != nil {
		return j, err
	}
	s.publish(EventUpdated, st, map[string]string{"jobId": j.ID, "kind": string(j.Kind)})
	return j, nil
}

// Deploy enqueues a stack.deploy job. The agent deploys the definition on
// disk at the time the job runs and reports it; the finish hook records it
// as the applied revision.
func (s *Service) Deploy(ctx context.Context, p authz.Principal, st domain.Stack, r domain.StackJobRequest, o domain.StackDeployOptions) (domain.Job, error) {
	switch o.Pull {
	case "", "missing", "always":
	default:
		return domain.Job{}, &domain.InputError{Field: "pull", Message: "must be missing or always"}
	}
	if err := checkBuildTimeout(o.BuildTimeoutSeconds); err != nil {
		return domain.Job{}, err
	}
	regs, err := s.registryConnections(ctx, st)
	if err != nil {
		return domain.Job{}, err
	}
	return s.enqueue(ctx, p, st, jobspec.StackDeploy, r, protocol.StackJobInput{Pull: o.Pull, Build: o.Build,
		ForceRecreate: o.ForceRecreate, RemoveOrphans: o.RemoveOrphans, BuildTimeoutSeconds: o.BuildTimeoutSeconds,
		RegistryConnections: regs})
}

func checkBuildTimeout(seconds int) error {
	if seconds < 0 || time.Duration(seconds)*time.Second > jobspec.MaxBuildTimeout {
		return &domain.InputError{Field: "buildTimeoutSeconds", Message: fmt.Sprintf("must be between 0 and %d", int(jobspec.MaxBuildTimeout/time.Second))}
	}
	return nil
}

// Build enqueues a stack.build job (#33): the agent rebuilds the images of
// the stack's build sections (or of the named services) from the
// definition on disk through the Engine's BuildKit, without deploying.
// Base images authenticate with the named registry connections, or else
// with the environment's host-wide connection per registry (#19); build
// argument values stay in the Compose files and never enter the job input
// or the audit trail. Builds per environment are capped by the job
// engine's build class.
func (s *Service) Build(ctx context.Context, p authz.Principal, st domain.Stack, r domain.StackJobRequest, o domain.StackBuildOptions) (domain.Job, error) {
	if err := checkBuildTimeout(o.TimeoutSeconds); err != nil {
		return domain.Job{}, err
	}
	var regs []string
	if s.opts.Registries != nil {
		if len(o.RegistryIDs) > 0 {
			if err := s.opts.Registries.Usable(ctx, o.RegistryIDs); err != nil {
				return domain.Job{}, err
			}
			regs = slices.Clone(o.RegistryIDs)
			slices.Sort(regs)
			regs = slices.Compact(regs)
		} else {
			var err error
			if regs, err = s.buildCredentials(ctx, st); err != nil {
				return domain.Job{}, err
			}
		}
	} else if len(o.RegistryIDs) > 0 {
		return domain.Job{}, &domain.InputError{Field: "registryIds", Message: "registry connections are not available"}
	}
	r.TimeoutSeconds = 0 // stop grace periods do not apply to builds
	return s.enqueue(ctx, p, st, jobspec.StackBuild, r, protocol.StackJobInput{NoCache: o.NoCache, PullBase: o.Pull,
		BuildTimeoutSeconds: o.TimeoutSeconds, RegistryConnections: regs})
}

// buildCredentials offers the environment's host-wide registry connection
// per registry to base-image pulls (base-image references are only known
// inside BuildKit; narrower connections must be named).
func (s *Service) buildCredentials(ctx context.Context, st domain.Stack) ([]string, error) {
	ids, ambiguous, err := s.opts.Registries.BuildCredentials(ctx, st.EnvironmentID)
	if err != nil {
		return nil, err
	}
	if len(ambiguous) > 0 {
		s.log.Info("registry hosts with tied connections are not offered to the stack's builds; name them explicitly",
			"stack_id", st.ID, "hosts", ambiguous)
	}
	return ids, nil
}

// Operate enqueues start, stop, restart or down.
func (s *Service) Operate(ctx context.Context, p authz.Principal, st domain.Stack, action string, r domain.StackJobRequest) (domain.Job, error) {
	kind, ok := operationKinds[action]
	if !ok {
		return domain.Job{}, &domain.InputError{Field: "action", Message: "must be start, stop, restart or down"}
	}
	if kind == jobspec.StackDown && len(r.Services) > 0 {
		return domain.Job{}, &domain.InputError{Field: "services", Message: "down applies to the whole stack"}
	}
	return s.enqueue(ctx, p, st, kind, r, protocol.StackJobInput{})
}

// Delete enqueues stack.remove: the stack is taken down and forgotten when
// the job succeeds. The project directory is kept, and so are its volumes
// unless o.Volumes asks to remove the ones the stack owns (the agent
// decides which are; a migrated source's volumes of the same project are
// held, #35).
func (s *Service) Delete(ctx context.Context, p authz.Principal, st domain.Stack, r domain.StackJobRequest, o domain.StackRemoveOptions) (domain.Job, error) {
	r.Services = nil
	in := protocol.StackJobInput{RemoveOrphans: true}
	if o.Volumes {
		// Older agents ignore the field (lenient input): refuse rather
		// than keep the volumes silently.
		fh, ok := s.opts.Agents.(interface {
			EnvironmentHasFeature(environmentID, feature string) bool
		})
		if !ok || !fh.EnvironmentHasFeature(st.EnvironmentID, protocol.FeatureStackRemoveVolumes) {
			return domain.Job{}, &domain.StackError{Code: domain.StackErrEnvironmentUnsupported,
				Message: "the environment's agent is offline or cannot remove a stack's volumes yet; upgrade it, or delete the stack without its volumes"}
		}
		in.RemoveVolumes = true
		if s.volumeHolds != nil {
			keep, err := s.volumeHolds(ctx, st.EnvironmentID, st.Name)
			if err != nil {
				return domain.Job{}, err
			}
			in.KeepVolumes = keep
		}
	}
	return s.enqueue(ctx, p, st, jobspec.StackRemove, r, in)
}

// registryConnections selects the registry connection of every image the
// deploy may pull (#19): the images of the definition on disk when the
// agent can validate it now, otherwise those of the last known definition.
// Build services are skipped; when the stack has any, the environment's
// host-wide connection per registry is added for their base images (as
// for stack.build; BuildKit asks per registry host). Ambiguous or revoked
// selections fail the request; nothing falls back to anonymous.
func (s *Service) registryConnections(ctx context.Context, st domain.Stack) ([]string, error) {
	if s.opts.Registries == nil {
		return nil, nil
	}
	var images []string
	builds := false
	var v protocol.ComposeValidateOutput
	if err := s.call(ctx, st.EnvironmentID, protocol.ReqComposeValidate, protocol.ComposeValidateInput{Stack: Ref(st)}, &v); err == nil && v.Valid {
		for _, sv := range v.Services {
			if !sv.Build {
				images = append(images, sv.Image)
			}
			builds = builds || sv.Build
		}
	} else {
		for _, sv := range st.Services {
			if !sv.Build {
				images = append(images, sv.Image)
			}
			builds = builds || sv.Build
		}
	}
	var ids []string
	if builds {
		b, err := s.buildCredentials(ctx, st)
		if err != nil {
			return nil, err
		}
		ids = append(ids, b...)
	}
	for _, img := range images {
		sel, err := s.opts.Registries.Select(ctx, domain.RegistrySelectRequest{Reference: img, EnvironmentID: st.EnvironmentID, StackID: st.ID})
		var fe *domain.FieldError
		switch {
		case errors.As(err, &fe):
			continue // not a valid reference: the deploy's validation reports it
		case err != nil:
			return nil, err
		}
		if sel.Selected != nil && !slices.Contains(ids, sel.Selected.ID) {
			ids = append(ids, sel.Selected.ID)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// Restore writes a revision's bytes back to disk (compare-and-set on the
// definition currently on disk, recorded first if it was new), records the
// result as a "restore" revision and offers a deploy. The environment must
// be online: an offline environment's revisions are read-only.
func (s *Service) Restore(ctx context.Context, p authz.Principal, st domain.Stack, revisionID string) (domain.StackRestore, error) {
	src, err := s.Revision(ctx, st.ID, revisionID)
	if err != nil {
		return domain.StackRestore{}, err
	}
	if src.ContentOmitted {
		return domain.StackRestore{}, &domain.StackError{Code: domain.StackErrContentUnavailable,
			Message: "this revision was recorded by hash only (its definition was larger than a deploy result carries); it cannot be restored"}
	}
	if !s.Online(ctx, st.EnvironmentID) {
		return domain.StackRestore{}, agentError(jobs.ErrAgentOffline)
	}
	var cur protocol.ComposeReadOutput
	if err := s.call(ctx, st.EnvironmentID, protocol.ReqComposeRead, protocol.ComposeReadInput{Stack: Ref(st)}, &cur); err != nil {
		return domain.StackRestore{}, err
	}
	if cur.Missing && len(cur.Snapshot.Files) == 0 {
		return domain.StackRestore{}, &domain.StackError{Code: domain.StackErrRootUnavailable, Message: "the project directory does not exist on the host"}
	}
	files := make([]protocol.SourceFile, 0, len(src.Files))
	for _, f := range src.Files {
		files = append(files, protocol.SourceFile{Path: f.Path, Content: f.Content})
	}
	var remove []string
	for _, f := range cur.Snapshot.Files {
		if !slices.ContainsFunc(src.Files, func(r domain.StackFile) bool { return r.Path == f.Path }) {
			remove = append(remove, f.Path)
		}
	}
	// Record what was on disk before overwriting it (an unrecorded edit is
	// never lost from history).
	var fresh domain.Stack
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if fresh, err = store.GetStack(ctx, tx, st.ID); err != nil {
			return err
		}
		if _, err := s.observe(ctx, tx, &fresh, cur.Snapshot, domain.RevisionExternal, authz.Service()); err != nil {
			return err
		}
		return store.UpdateStack(ctx, tx, &fresh)
	})
	if err != nil {
		return domain.StackRestore{}, err
	}
	var w protocol.ComposeWriteOutput
	if err := s.call(ctx, st.EnvironmentID, protocol.ReqComposeWrite, protocol.ComposeWriteInput{Stack: Ref(st), Mode: protocol.WriteReplace,
		Files: files, ExpectHash: cur.Snapshot.Hash, Remove: remove}, &w); err != nil {
		return domain.StackRestore{}, err
	}
	var res domain.StackRestore
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if fresh, err = store.GetStack(ctx, tx, st.ID); err != nil {
			return err
		}
		rev, err := s.recordRevision(ctx, tx, &fresh, w.Snapshot, domain.RevisionRestore, p, "", src.ID)
		if err != nil {
			return err
		}
		now := s.now()
		fresh.Observed, fresh.ObservedAt, fresh.UpdatedAt = rev.Ref(), &now, now
		res = domain.StackRestore{Stack: fresh, Revision: rev, DeployOffered: fresh.UndeployedChanges()}
		return store.UpdateStack(ctx, tx, &fresh)
	})
	if err != nil {
		return domain.StackRestore{}, err
	}
	s.log.Info("stack revision restored to disk", "stack_id", st.ID, "revision", src.Seq, "hash", w.Snapshot.Hash)
	s.publish(EventRevision, res.Stack, map[string]string{"source": string(domain.RevisionRestore), "seq": fmt.Sprint(res.Revision.Seq)})
	return res, nil
}

// RecordObserved reads the stack's definition from disk and records it as a
// revision when it changed (source: file_manager for #15 saves, external for
// #23's watcher and reconciliation). It returns the new revision, or nil
// when nothing changed.
func (s *Service) RecordObserved(ctx context.Context, stackID string, source domain.RevisionSource, author authz.Principal) (*domain.StackRevision, error) {
	if source != domain.RevisionFileManager && source != domain.RevisionExternal && source != domain.RevisionEditor {
		return nil, fmt.Errorf("stacks: %s is not an observation source", source)
	}
	st, err := store.GetStack(ctx, s.db, stackID)
	if err != nil {
		return nil, err
	}
	var cur protocol.ComposeReadOutput
	if err := s.call(ctx, st.EnvironmentID, protocol.ReqComposeRead, protocol.ComposeReadInput{Stack: Ref(st)}, &cur); err != nil {
		return nil, err
	}
	return s.recordRead(ctx, st.ID, cur, source, author)
}

func (s *Service) recordRead(ctx context.Context, stackID string, cur protocol.ComposeReadOutput, source domain.RevisionSource, author authz.Principal) (*domain.StackRevision, error) {
	if cur.Missing && len(cur.Snapshot.Files) == 0 {
		return nil, nil // nothing on disk to record
	}
	var rev *domain.StackRevision
	var st domain.Stack
	err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if st, err = store.GetStack(ctx, tx, stackID); err != nil {
			return err
		}
		if rev, err = s.observe(ctx, tx, &st, cur.Snapshot, source, author); err != nil {
			return err
		}
		return store.UpdateStack(ctx, tx, &st)
	})
	if err != nil || rev == nil {
		return rev, err
	}
	s.publish(EventRevision, st, map[string]string{"source": string(source), "seq": fmt.Sprint(rev.Seq)})
	return rev, nil
}

// RecordFileSave is the file manager's hook (#15): after a save inside a
// stack's project directory, a change to a definition file records a
// revision (source file_manager) and marks undeployed changes. It never
// deploys.
func (s *Service) RecordFileSave(ctx context.Context, stackID, relPath string, author authz.Principal) (*domain.StackRevision, error) {
	root, err := s.Root(ctx, stackID)
	if err != nil {
		return nil, err
	}
	st, err := store.GetStack(ctx, s.db, stackID)
	if err != nil {
		return nil, err
	}
	if !IsDefinitionFile(st, root.DefinitionFiles, relPath) {
		return nil, nil
	}
	return s.RecordObserved(ctx, stackID, domain.RevisionFileManager, author)
}

// IsDefinitionFile reports whether a project-relative path is part of the
// stack's definition (a Compose, override or env file), so the file manager
// (#15) also requires stack.definition.read/write for it.
func IsDefinitionFile(st domain.Stack, known []string, relPath string) bool {
	return slices.Contains(known, relPath) || slices.Contains(definitionNames, relPath) ||
		slices.Contains(st.ConfigFiles, relPath) || slices.Contains(st.EnvFiles, relPath)
}

// Reconciler re-reads every stack of an environment after its agent
// reconnected (#3): definitions edited while it was away are recorded as
// external revisions and the Engine state is refreshed. It runs in the
// background on the session (never failing the session).
func (s *Service) Reconciler() agents.Reconciler {
	return func(ctx context.Context, sess *agents.Session) error {
		if !sess.Serves(protocol.ReqComposeRead) || !sess.Serves(protocol.ReqComposeServices) {
			return nil // an agent without the #7 requests
		}
		env := sess.EnvironmentID()
		go s.reconcile(ctx, env, func(ctx context.Context, name string, in, out any) error {
			raw, err := sess.Request(ctx, name, in, s.opts.RequestTimeout)
			if err != nil {
				return agentError(err)
			}
			return json.Unmarshal(raw, out)
		})
		return nil
	}
}

type requester func(ctx context.Context, name string, in, out any) error

func (s *Service) reconcile(ctx context.Context, env string, req requester) {
	list, err := store.ListStacks(ctx, s.db, domain.StackFilter{EnvironmentID: env})
	if err != nil {
		s.log.Error("stack reconciliation: list stacks", "environment_id", env, "error", err)
		return
	}
	for _, st := range list {
		if ctx.Err() != nil {
			return
		}
		var cur protocol.ComposeReadOutput
		if err := req(ctx, protocol.ReqComposeRead, protocol.ComposeReadInput{Stack: Ref(st)}, &cur); err != nil {
			s.log.Warn("stack reconciliation: read definition", "stack_id", st.ID, "error", err)
		} else if _, err := s.recordRead(ctx, st.ID, cur, domain.RevisionExternal, authz.Service()); err != nil {
			s.log.Error("stack reconciliation: record revision", "stack_id", st.ID, "error", err)
		}
		var live protocol.ComposeServicesOutput
		if err := req(ctx, protocol.ReqComposeServices, protocol.ComposeServicesInput{ProjectName: st.Name}, &live); err != nil {
			s.log.Warn("stack reconciliation: read Engine state", "stack_id", st.ID, "error", err)
			continue
		}
		if err := s.storeLive(ctx, st.ID, live.Containers); err != nil {
			s.log.Error("stack reconciliation: record Engine state", "stack_id", st.ID, "error", err)
		}
	}
}

// storeLive records the observed Engine state of a stack.
func (s *Service) storeLive(ctx context.Context, stackID string, containers []protocol.StackContainer) error {
	var st domain.Stack
	changed := false
	err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if st, err = store.GetStack(ctx, tx, stackID); err != nil {
			return err
		}
		before, prev := st.EngineState, st.EngineServices
		s.setEngine(&st, statesOfContainers(containers))
		changed = before != st.EngineState || !sameStates(prev, st.EngineServices)
		return store.UpdateStack(ctx, tx, &st)
	})
	if errors.Is(err, domain.ErrStackNotFound) {
		return nil
	}
	if err == nil && changed {
		s.publish(EventUpdated, st, map[string]string{"change": "engine_state"})
	}
	return err
}

func sameStates(a, b []domain.StackServiceState) bool {
	return slices.EqualFunc(a, b, func(x, y domain.StackServiceState) bool {
		return x.Service == y.Service && x.Containers == y.Containers && x.Running == y.Running && slices.Equal(x.ImageIDs, y.ImageIDs)
	})
}

func statesOfContainers(containers []protocol.StackContainer) []domain.StackServiceState {
	by := map[string]*domain.StackServiceState{}
	var names []string
	for _, c := range containers {
		if c.OneOff {
			continue
		}
		st, ok := by[c.Service]
		if !ok {
			st = &domain.StackServiceState{Service: c.Service}
			by[c.Service] = st
			names = append(names, c.Service)
		}
		st.Containers++
		if c.State == "running" {
			st.Running++
		}
		if c.ImageID != "" && !slices.Contains(st.ImageIDs, c.ImageID) {
			st.ImageIDs = append(st.ImageIDs, c.ImageID)
		}
	}
	slices.Sort(names)
	out := make([]domain.StackServiceState, 0, len(names))
	for _, n := range names {
		out = append(out, *by[n])
	}
	return out
}

// Job finish hooks: the stack record follows its jobs' outcomes in the
// jobs' finishing transactions.
func (s *Service) registerHooks() {
	s.opts.Jobs.OnFinish(jobspec.StackDeploy, s.onDeployFinished)
	for _, k := range []domain.JobKind{jobspec.StackStart, jobspec.StackStop, jobspec.StackRestart, jobspec.StackDown} {
		s.opts.Jobs.OnFinish(k, s.onOperationFinished)
	}
	s.opts.Jobs.OnFinish(jobspec.StackRemove, s.onRemoveFinished)
	s.opts.Jobs.OnFinish(jobspec.StackImport, s.onImportFinished)
	s.opts.Jobs.OnFinish(jobspec.StackRename, s.onRenameFinished)
	s.opts.Jobs.OnFinish(jobspec.StackPull, s.onPullFinished)
	s.registerRetries()
}

func stackTarget(j domain.Job) string {
	for _, t := range j.Targets {
		if t.Type == domain.TargetStack {
			return t.ID
		}
	}
	return ""
}

// output decodes a stack job's result output; malformed output is logged
// and treated as absent.
func (s *Service) output(j domain.Job) (protocol.StackJobOutput, bool) {
	var out protocol.StackJobOutput
	if len(j.ResultOutput) == 0 {
		return out, false
	}
	if err := json.Unmarshal(j.ResultOutput, &out); err != nil {
		s.log.Warn("stack job reported a malformed result output", "job_id", j.ID, "kind", j.Kind)
		return protocol.StackJobOutput{}, false
	}
	if out.Sources != nil && protocol.SourceHash(out.Sources.Files) != out.Sources.Hash {
		s.log.Warn("stack job reported sources whose hash does not match", "job_id", j.ID)
		out.Sources = nil
	}
	return out, true
}

func initiator(j domain.Job) authz.Principal {
	p := authz.Principal{Kind: authz.KindUser, UserID: j.InitiatorUserID, TokenID: j.InitiatorTokenID}
	if j.InitiatorTokenID != "" {
		p.Kind = authz.KindAPIToken
	}
	if j.InitiatorUserID == "" {
		p = authz.Service()
	}
	return p
}

// onDeployFinished records the applied revision (the bytes the agent
// deployed, a new "deploy" revision at every deploy), the applied images and
// the observed state. A deploy that failed while applying keeps the last
// applied revision and records the failed one and the pre-deploy state for
// recovery; a deploy that failed before applying changes nothing.
func (s *Service) onDeployFinished(ctx context.Context, db bun.IDB, j domain.Job) error {
	st, err := store.GetStack(ctx, db, stackTarget(j))
	if errors.Is(err, domain.ErrStackNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	out, ok := s.output(j)
	now := s.now()
	var rev *domain.StackRevision
	if ok && out.Sources != nil {
		r, err := s.recordRevision(ctx, db, &st, *out.Sources, domain.RevisionDeploy, initiator(j), j.ID, "")
		if err != nil {
			return err
		}
		rev = &r
		st.Observed, st.ObservedAt = r.Ref(), &now
	}
	switch {
	case j.State == domain.JobSucceeded:
		st.Status = domain.StackDeployed
		st.Failed = nil
		// A deploy that started no container (the Engine already ran the
		// definition) keeps the last deploy time; older agents never
		// report it.
		if !ok || !out.Unchanged {
			st.AppliedAt = &now
		}
		st.Images = nil
		if rev != nil {
			st.Applied = rev.Ref()
		} else {
			// The deploy succeeded but its report was lost: what is applied
			// is unknown, so the stack shows undeployed changes until the
			// next deploy.
			s.log.Warn("stack deploy succeeded without reporting its sources; applied revision unknown", "stack_id", st.ID, "job_id", j.ID)
			st.Applied = nil
		}
		if ok {
			for _, i := range out.Images {
				st.Images = append(st.Images, domain.StackImage{Service: i.Service, Image: i.Image, ImageID: i.ImageID,
					Digest: i.Digest, Platform: i.Platform, Build: i.Build})
			}
			st.Services, st.Binds, st.PreviousState = servicesFrom(out.Services), bindsFrom(out.Binds), statesFrom(out.Before)
		}
	case rev != nil: // failed while applying
		st.Status = domain.StackFailed
		st.Failed = rev.Ref()
		st.PreviousState = statesFrom(out.Before)
	}
	if ok && out.After != nil {
		s.setEngine(&st, statesFrom(out.After))
	}
	st.UpdatedAt = now
	if err := store.UpdateStack(ctx, db, &st); err != nil {
		return err
	}
	s.publish(EventUpdated, st, map[string]string{"jobId": j.ID, "kind": string(j.Kind), "state": string(j.State)})
	return nil
}

// onOperationFinished updates the status after start/stop/restart/down.
func (s *Service) onOperationFinished(ctx context.Context, db bun.IDB, j domain.Job) error {
	st, err := store.GetStack(ctx, db, stackTarget(j))
	if errors.Is(err, domain.ErrStackNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var in protocol.StackJobInput
	_ = json.Unmarshal(j.Input, &in)
	if j.State == domain.JobSucceeded && len(in.Services) == 0 {
		switch j.Kind {
		case jobspec.StackStart, jobspec.StackRestart:
			st.Status = domain.StackDeployed
		case jobspec.StackStop:
			st.Status = domain.StackStopped
		case jobspec.StackDown:
			st.Status = domain.StackDown
		}
	}
	if out, ok := s.output(j); ok && out.After != nil {
		s.setEngine(&st, statesFrom(out.After))
	}
	st.UpdatedAt = s.now()
	if err := store.UpdateStack(ctx, db, &st); err != nil {
		return err
	}
	s.publish(EventUpdated, st, map[string]string{"jobId": j.ID, "kind": string(j.Kind), "state": string(j.State)})
	return nil
}

// onRemoveFinished forgets a stack whose removal succeeded: its record,
// revisions and the permission rules naming it or its services (in the
// job's transaction). Volumes and the project directory stay on the host.
func (s *Service) onRemoveFinished(ctx context.Context, db bun.IDB, j domain.Job) error {
	st, err := store.GetStack(ctx, db, stackTarget(j))
	if errors.Is(err, domain.ErrStackNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if j.State != domain.JobSucceeded {
		return s.onOperationFinished(ctx, db, j)
	}
	return s.forget(ctx, db, st, j, "stack removed")
}
