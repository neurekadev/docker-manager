package migrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/humanize"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/streammux"
)

// Job error classes of migrations.
const (
	ClassChecksumMismatch = "checksum_mismatch"
	ClassTransferFailed   = "transfer_failed"
	ClassDeployFailed     = "deploy_failed"
	ClassBlocked          = "migration_blocked"
	ClassStackMoved       = "stack_moved"
	ClassSourceStopFailed = "source_stop_failed"
)

// classed is a step failure with its own class and recovery guidance.
type classed struct {
	class, recovery string
	err             error
}

func (c *classed) Error() string      { return c.err.Error() }
func (c *classed) Unwrap() error      { return c.err }
func (c *classed) ErrorClass() string { return c.class }
func (c *classed) Recovery() string   { return c.recovery }

const (
	recoverySourceRestarted = "The source stack was put back and its services that ran before were started again (unless the job reports that this failed). "
	recoveryPartial         = "The destination may hold partial data of this migration; the next migration of this stack to it removes it first."
)

// stackOutput is the stack.migrate job's journaled output.
type stackOutput struct {
	Parts         []domain.MigrationPart `json:"parts"`
	SourceRunning []string               `json:"sourceRunning,omitempty"`
	Committed     bool                   `json:"committed,omitempty"`
	DeployJobID   string                 `json:"deployJobId,omitempty"`
}

func readOutput(sc *jobexec.StepContext) stackOutput {
	var o stackOutput
	if b := sc.Output(); len(b) > 0 {
		_ = json.Unmarshal(b, &o)
	}
	return o
}

func writeOutput(ctx context.Context, sc *jobexec.StepContext, fn func(o *stackOutput)) error {
	o := readOutput(sc)
	fn(&o)
	if o.Parts == nil {
		o.Parts = []domain.MigrationPart{}
	}
	return sc.SetOutput(ctx, o)
}

// startArgs are the start_source compensation's arguments.
type startArgs struct {
	MigrationID string                `json:"migrationId"`
	StackID     string                `json:"stackId"`
	Source      string                `json:"source"`
	Stack       protocol.ProjectRef   `json:"stack"`
	Services    []string              `json:"services"`
	Placement   domain.StackPlacement `json:"placement"`
}

func (s *Service) stackExecutor() jobexec.Executor {
	return jobexec.Executor{Kind: jobspec.StackMigrate, Steps: map[string]jobexec.StepFunc{
		"prepare":            s.stackPrepare,
		"stop_source":        s.stackStop,
		"transfer":           s.stackTransfer,
		"deploy_destination": s.stackDeploy,
		"finalize":           s.stackFinalize,
	}, Compensations: map[string]jobexec.CompensationFunc{jobspec.CompStartSource: s.startSource}}
}

func (s *Service) volumeExecutor() jobexec.Executor {
	return jobexec.Executor{Kind: jobspec.VolumeMigrate, Steps: map[string]jobexec.StepFunc{
		"prepare":  s.volumePrepare,
		"transfer": s.volumeTransfer,
		"finalize": s.volumeFinalize,
	}}
}

func stackInput(sc *jobexec.StepContext) (stackJobInput, error) {
	var in stackJobInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return in, fmt.Errorf("malformed stack.migrate input: %w", err)
	}
	if in.StackID == "" || in.Target == "" || !protocol.ValidDirName(in.TargetDir) {
		return in, errors.New("incomplete stack.migrate input")
	}
	return in, nil
}

func (in stackJobInput) sourceRef() protocol.ProjectRef {
	return protocol.ProjectRef{Root: in.Source.Root, RootPath: in.Source.RootPath, Dir: in.Source.Dir, ProjectName: in.Source.Project,
		ConfigFiles: in.ConfigFiles, EnvFiles: in.EnvFiles}
}

// principalOf is the job's initiator, used to re-check the destination's
// capabilities (the engine re-checks the source's at dispatch).
func principalOf(j domain.Job) authz.Principal {
	switch j.Origin {
	case domain.OriginAPIToken:
		return authz.Principal{Kind: authz.KindAPIToken, UserID: j.InitiatorUserID, TokenID: j.InitiatorTokenID}
	case domain.OriginManual:
		return authz.Principal{Kind: authz.KindUser, UserID: j.InitiatorUserID}
	}
	return authz.Service()
}

// DestinationStackResource is a stack as it would be located on env.
func DestinationStackResource(stackID, env string) authz.Resource {
	return authz.Resource{Type: catalog.TypeStack, ID: stackID, EnvironmentID: env, Parents: []authz.ResourceRef{}}
}

// Check is a capability on a resource.
type Check struct {
	Capability string
	Resource   authz.Resource
}

// DestinationStackCapabilities are what a stack migration needs on the
// destination besides stack.migrate on the stack: creating a stack there
// and deploying the stack once it is there.
func DestinationStackCapabilities(stackID, env string) []Check {
	return []Check{
		{Capability: "stack.create", Resource: authz.InEnvironment(catalog.TypeStack, env)},
		{Capability: "stack.deploy", Resource: DestinationStackResource(stackID, env)},
	}
}

// DestinationVolumeCapabilities are what a volume migration needs on the
// destination besides volume.migrate on the volume.
func DestinationVolumeCapabilities(env string) []Check {
	return []Check{{Capability: "volume.create", Resource: authz.InEnvironment(catalog.TypeVolume, env)}}
}

func (s *Service) authorizeDestination(ctx context.Context, jobID string, checks []Check) error {
	j, err := s.opts.Jobs.Get(ctx, jobID)
	if err != nil {
		return err
	}
	p := principalOf(j)
	if p.IsService() {
		return nil
	}
	c := authz.For(ctx, s.opts.Authorizer, p)
	for _, ch := range checks {
		if d := c.Can(ch.Capability, ch.Resource); !d.Allowed {
			return &classed{class: domain.ErrorAuthorizationRevoked, err: fmt.Errorf("the initiator no longer holds %s on the destination", ch.Capability),
				recovery: "Nothing was changed. Ask an administrator for the grant on the destination environment, then start the migration again."}
		}
	}
	return nil
}

func (s *Service) updateRecord(ctx context.Context, id string, fn func(m *domain.Migration)) error {
	return s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		m, err := store.GetMigration(ctx, tx, id)
		if err != nil {
			return err
		}
		fn(&m)
		m.UpdatedAt = s.now()
		return store.UpdateMigration(ctx, tx, &m)
	})
}

// cleanLeftovers removes the destination data of earlier unsuccessful
// migrations (same stack or same volume name) before a new one.
func (s *Service) cleanLeftovers(ctx context.Context, env, project string, leftovers []domain.Migration) error {
	for _, l := range leftovers {
		var out protocol.MigrationCleanupOutput
		if err := s.call(ctx, env, protocol.ReqMigrationCleanup, protocol.MigrationCleanupInput{MigrationID: l.ID, Project: project,
			Volumes: l.VolumeTargets()}, &out, s.opts.StopTimeout); err != nil {
			return &classed{class: ClassTransferFailed, err: fmt.Errorf("remove the partial data of migration %s: %w", l.ID, err),
				recovery: "Nothing was changed. Check the destination environment's agent, then start the migration again."}
		}
		if err := s.updateRecord(ctx, l.ID, func(m *domain.Migration) { m.TargetPartial = false }); err != nil {
			return err
		}
		s.log.Info("removed the partial data of an earlier migration", "migration_id", l.ID, "removed", len(out.Removed))
	}
	return nil
}

func (s *Service) stackPrepare(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := stackInput(sc)
	if err != nil {
		return err
	}
	st, err := s.opts.Stacks.Get(ctx, in.StackID)
	if err != nil {
		return err
	}
	if st.EnvironmentID == in.Target || st.Dir != in.Source.Dir || st.Name != in.Source.Project {
		return &classed{class: ClassStackMoved, err: errors.New("the stack changed since the migration was requested"),
			recovery: "Nothing was changed. Preview the migration again."}
	}
	if err := s.authorizeDestination(ctx, sc.JobID, DestinationStackCapabilities(st.ID, in.Target)); err != nil {
		return err
	}
	m := domain.Migration{ID: sc.JobID, Kind: domain.MigrationKindStack, StackID: st.ID, SourceEnvironmentID: st.EnvironmentID,
		TargetEnvironmentID: in.Target, Source: in.Source, TargetDir: in.TargetDir, Volumes: in.Volumes, Images: in.Images,
		State: domain.MigrationRunning, CreatedAt: s.now(), UpdatedAt: s.now()}
	if err := s.ensureRecord(ctx, s.db, &m); err != nil {
		return err
	}
	sc.Progress(ctx, 2, "checking the destination")
	// The preview first (the destination's leftovers of earlier attempts of
	// this stack are not conflicts); only then are they removed.
	g, err := s.previewStack(ctx, st, StackRequest{TargetEnvironmentID: in.Target, Selection: in.Selection, TimeoutSeconds: in.TimeoutSeconds}, false)
	if err != nil {
		return err
	}
	if !g.plan.Allowed() {
		b := g.plan.Blockers[0]
		return &classed{class: ClassBlocked, err: fmt.Errorf("%s: %s", b.Code, b.Message),
			recovery: "Nothing was changed. Preview the migration again and resolve its blockers."}
	}
	for _, v := range in.Volumes {
		if !slices.ContainsFunc(g.plan.Volumes, func(p VolumePlan) bool { return p.Source == v.Source && p.Action == VolumeCopy }) {
			return &classed{class: ClassBlocked, err: fmt.Errorf("volume %s can no longer be copied", v.Source),
				recovery: "Nothing was changed. Preview the migration again."}
		}
	}
	leftovers, err := s.leftovers(ctx, in.Target, func(l domain.Migration) bool { return l.StackID == st.ID && l.ID != sc.JobID })
	if err != nil {
		return err
	}
	if err := s.cleanLeftovers(ctx, in.Target, st.Name, leftovers); err != nil {
		return err
	}
	return writeOutput(ctx, sc, func(*stackOutput) {})
}

func (s *Service) stackStop(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := stackInput(sc)
	if err != nil {
		return err
	}
	st, err := s.opts.Stacks.Get(ctx, in.StackID)
	if err != nil {
		return err
	}
	out := readOutput(sc)
	running := out.SourceRunning
	if running == nil {
		if running, err = s.runningServices(ctx, st.EnvironmentID, in.Source.Project); err != nil {
			return s.agentFailure("source", err, false)
		}
		if err := writeOutput(ctx, sc, func(o *stackOutput) { o.SourceRunning = running }); err != nil {
			return err
		}
		if err := s.updateRecord(ctx, sc.JobID, func(m *domain.Migration) { m.SourceRunning = running }); err != nil {
			return err
		}
	}
	// Registered before anything stops: whatever ends this job before its
	// cut-over completes puts the source back.
	if err := sc.AddCompensation(ctx, jobspec.CompStartSource, startArgs{MigrationID: sc.JobID, StackID: st.ID,
		Source: st.EnvironmentID, Stack: in.sourceRef(), Services: running, Placement: domain.PlacementOf(st)}); err != nil {
		return err
	}
	sc.Progress(ctx, 5, fmt.Sprintf("stopping %d services on the source", len(running)))
	var res protocol.MigrationLifecycleOutput
	if err := s.call(ctx, st.EnvironmentID, protocol.ReqMigrationStop, protocol.MigrationStopInput{MigrationID: sc.JobID,
		Stack: in.sourceRef(), TimeoutSeconds: in.TimeoutSeconds}, &res, s.opts.StopTimeout); err != nil {
		return &classed{class: ClassSourceStopFailed, err: fmt.Errorf("stop the source: %w", err),
			recovery: recoverySourceRestarted + "Check the source's containers, then run the migration again."}
	}
	return nil
}

// part is one unit of transfer.
type part struct {
	name    string
	send    protocol.MigrationSendInput
	receive protocol.MigrationReceiveInput
}

func stackParts(id string, in stackJobInput) []part {
	ref := in.sourceRef()
	out := []part{{name: protocol.PartProject,
		send:    protocol.MigrationSendInput{MigrationID: id, Part: protocol.PartProject, Stack: &ref},
		receive: protocol.MigrationReceiveInput{MigrationID: id, Part: protocol.PartProject}}}
	for _, v := range in.Volumes {
		out = append(out, part{name: "volume:" + v.Target,
			send: protocol.MigrationSendInput{MigrationID: id, Part: protocol.PartVolume, Volume: v.Source},
			receive: protocol.MigrationReceiveInput{MigrationID: id, Part: protocol.PartVolume,
				Volume: &protocol.MigrationVolumeSpec{Name: v.Target, Labels: in.VolumeLabels[v.Target]}}})
	}
	if len(in.Images) > 0 {
		out = append(out, part{name: protocol.PartImage,
			send:    protocol.MigrationSendInput{MigrationID: id, Part: protocol.PartImage, Images: in.Images},
			receive: protocol.MigrationReceiveInput{MigrationID: id, Part: protocol.PartImage, Images: in.Images}})
	}
	return out
}

func (s *Service) stackTransfer(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := stackInput(sc)
	if err != nil {
		return err
	}
	st, err := s.opts.Stacks.Get(ctx, in.StackID)
	if err != nil {
		return err
	}
	source := st.EnvironmentID
	if err := s.updateRecord(ctx, sc.JobID, func(m *domain.Migration) { m.TargetPartial = true }); err != nil {
		return err
	}
	if err := s.transferParts(ctx, sc, source, in.Target, stackParts(sc.JobID, in)); err != nil {
		return err
	}
	if readOutput(sc).Committed {
		return nil
	}
	sc.Progress(ctx, 85, "committing the project directory on the destination")
	var co protocol.MigrationCommitOutput
	if err := s.call(ctx, in.Target, protocol.ReqMigrationCommit, protocol.MigrationCommitInput{MigrationID: sc.JobID, Dir: in.TargetDir},
		&co, s.opts.RequestTimeout); err != nil {
		return s.agentFailure("destination", err, true)
	}
	return writeOutput(ctx, sc, func(o *stackOutput) { o.Committed = true })
}

// transferParts relays every part not transferred yet (a resumed or
// retried step skips the verified ones) and records their checksums.
func (s *Service) transferParts(ctx context.Context, sc *jobexec.StepContext, source, target string, parts []part) error {
	for i, p := range parts {
		if slices.ContainsFunc(readOutput(sc).Parts, func(d domain.MigrationPart) bool { return d.Name == p.name }) {
			continue
		}
		// Parts are the transfer's units: a cancellation stops before the
		// next one (the compensation puts the source back).
		if sc.CancelRequested() {
			return fmt.Errorf("before %s: %w", p.name, jobexec.ErrStepCancelled)
		}
		pct := 10 + 70*i/len(parts)
		sc.Progress(ctx, pct, "copying "+p.name)
		res, err := s.transferPart(ctx, sc, source, target, p)
		if err != nil {
			return err
		}
		mp := domain.MigrationPart{Name: p.name, Bytes: res.Relayed.Bytes, SHA256: res.Relayed.SHA256, Chunks: res.Relayed.Chunks}
		if err := writeOutput(ctx, sc, func(o *stackOutput) { o.Parts = append(o.Parts, mp) }); err != nil {
			return err
		}
		if err := s.updateRecord(ctx, sc.JobID, func(m *domain.Migration) {
			m.Parts = append(m.Parts, mp)
			m.Bytes += mp.Bytes
			for i := range m.Volumes {
				if "volume:"+m.Volumes[i].Target == p.name {
					m.Volumes[i].Copied, m.Volumes[i].Bytes, m.Volumes[i].SHA256 = true, mp.Bytes, mp.SHA256
				}
			}
		}); err != nil {
			return err
		}
		msg := fmt.Sprintf("%s, sha256 %s (verified by source, manager and destination)", humanize.Bytes(mp.Bytes), mp.SHA256)
		if n := res.Source.SkippedCount; n > 0 {
			msg += fmt.Sprintf("; %d entries skipped (sockets or device nodes)", n)
		}
		sc.Item(ctx, p.name, domain.ItemSucceeded, msg)
	}
	return nil
}

// transferPart relays one part, retrying it from the start after a lost
// session (once the agent is back) or a checksum mismatch.
func (s *Service) transferPart(ctx context.Context, sc *jobexec.StepContext, source, target string, p part) (RelayResult, error) {
	for attempt := 1; ; attempt++ {
		res, err := s.relayPart(ctx, sc, source, target, p)
		if err == nil {
			return res, nil
		}
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		lost := errors.Is(err, jobs.ErrAgentOffline) || errors.Is(err, streammux.ErrSessionClosed)
		var pe *PartError
		mismatch := errors.As(err, &pe) && pe.Code == protocol.CodeDigestMismatch
		switch {
		case lost && attempt < s.opts.PartAttempts:
			s.log.Warn("migration transfer lost an agent; waiting for it", "migration_id", sc.JobID, "part", p.name, "attempt", attempt)
			sc.Progress(ctx, -1, "an agent disconnected during "+p.name+"; waiting for it to reconnect")
			if !s.waitOnline(ctx, source, target) {
				return res, s.interrupted(source, target, p.name, err)
			}
			continue
		case lost:
			return res, s.interrupted(source, target, p.name, err)
		case mismatch && attempt < s.opts.PartAttempts:
			s.log.Warn("migration part failed verification; retrying", "migration_id", sc.JobID, "part", p.name, "attempt", attempt)
			continue
		case mismatch:
			return res, &classed{class: ClassChecksumMismatch, err: fmt.Errorf("%s: %w", p.name, err),
				recovery: recoverySourceRestarted + "The data failed verification repeatedly; check both hosts' disks and memory, then run the migration again. " + recoveryPartial}
		}
		return res, &classed{class: ClassTransferFailed, err: fmt.Errorf("%s: %w", p.name, err),
			recovery: recoverySourceRestarted + "Fix the cause reported by the agent, then run the migration again. " + recoveryPartial}
	}
}

func (s *Service) interrupted(source, target, name string, cause error) error {
	side := "the source or destination"
	switch {
	case !s.opts.Agents.Online(source):
		side = "the source"
	case !s.opts.Agents.Online(target):
		side = "the destination"
	}
	return &classed{class: domain.ErrorAgentOffline,
		err: fmt.Errorf("%s environment's agent disconnected during %s and did not return within %s: %w (%w)", side, name, s.opts.ReconnectWait,
			jobexec.ErrStepInterrupted, cause),
		recovery: "The migration stopped mid-transfer. " + recoverySourceRestarted +
			"If the source's agent is offline, start the stack once it reconnects. " + recoveryPartial}
}

// waitOnline waits (bounded by ReconnectWait) until every environment's
// agent is online.
func (s *Service) waitOnline(ctx context.Context, envs ...string) bool {
	deadline := s.clk.Now().Add(s.opts.ReconnectWait)
	t := s.clk.NewTicker(time.Second)
	defer t.Stop()
	for {
		ok := true
		for _, e := range envs {
			ok = ok && s.opts.Agents.Online(e)
		}
		if ok {
			return true
		}
		if !s.clk.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-t.C():
		}
	}
}

// relayPart opens the part's stream pair and relays it.
func (s *Service) relayPart(ctx context.Context, sc *jobexec.StepContext, source, target string, p part) (RelayResult, error) {
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dst, err := s.opts.Agents.OpenStream(pctx, target, protocol.StreamMigrationReceive, p.receive, streammux.OpenOptions{JobID: sc.JobID})
	if err != nil {
		return RelayResult{}, err
	}
	src, err := s.opts.Agents.OpenStream(pctx, source, protocol.StreamMigrationSend, p.send, streammux.OpenOptions{JobID: sc.JobID})
	if err != nil {
		dst.Abort(protocol.CloseReasonCancelled, protocol.CodeCancelled, "the source is unavailable")
		return RelayResult{}, err
	}
	return Relay(pctx, src, dst, RelayOptions{Limiter: s.limiter, BufferSize: s.opts.RelayBuffer})
}

// agentFailure classifies a failed request to an agent.
func (s *Service) agentFailure(side string, err error, partial bool) error {
	rec := recoverySourceRestarted
	if partial {
		rec += recoveryPartial
	}
	if errors.Is(err, jobs.ErrAgentOffline) || errors.Is(err, protocol.ErrRequestTimeout) {
		return &classed{class: domain.ErrorAgentOffline, err: fmt.Errorf("the %s agent: %w (%w)", side, jobexec.ErrStepInterrupted, err),
			recovery: "The " + side + " environment's agent is unavailable. " + rec}
	}
	return &classed{class: ClassTransferFailed, err: fmt.Errorf("the %s agent: %w", side, err), recovery: rec}
}

func (s *Service) stackDeploy(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := stackInput(sc)
	if err != nil {
		return err
	}
	out := readOutput(sc)
	jobID := out.DeployJobID
	if jobID == "" {
		st, err := s.opts.Stacks.Get(ctx, in.StackID)
		if err != nil {
			return err
		}
		if st.EnvironmentID != in.Target {
			// Cut-over: the stack record moves to the destination (its ID,
			// revisions and stack-scoped rules stay).
			p := domain.PlacementOf(st)
			p.EnvironmentID, p.Root, p.RootPath, p.Dir = in.Target, protocol.RootStacks, "", in.TargetDir
			p.EngineState, p.EngineServices, p.EngineObservedAt, p.PreviousState = domain.EngineStateUnknown, nil, nil, nil
			err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
				if st, err = s.opts.Stacks.Place(ctx, tx, in.StackID, p); err != nil {
					return err
				}
				m, err := store.GetMigration(ctx, tx, sc.JobID)
				if err != nil {
					return err
				}
				m.CutOver, m.UpdatedAt = true, s.now()
				return store.UpdateMigration(ctx, tx, &m)
			})
			if errors.Is(err, domain.ErrStackNameTaken) {
				return &classed{class: ClassBlocked, err: errors.New("the destination got a stack with the same name meanwhile"),
					recovery: recoverySourceRestarted + recoveryPartial}
			}
			if err != nil {
				return err
			}
			s.opts.Stacks.Published(st, map[string]string{"migrationId": sc.JobID, "change": "migrated"})
		}
		j, err := s.opts.Jobs.Get(ctx, sc.JobID)
		if err != nil {
			return err
		}
		sc.Progress(ctx, 88, "deploying on the destination")
		dj, err := s.opts.Stacks.Deploy(ctx, principalOf(j), st, domain.StackJobRequest{IdempotencyKey: "migration-" + sc.JobID,
			TimeoutSeconds: in.TimeoutSeconds}, domain.StackDeployOptions{Pull: "missing"})
		if err != nil {
			return &classed{class: ClassDeployFailed, err: fmt.Errorf("start the destination deploy: %w", err),
				recovery: recoverySourceRestarted + recoveryPartial}
		}
		jobID = dj.ID
		if err := writeOutput(ctx, sc, func(o *stackOutput) { o.DeployJobID = jobID }); err != nil {
			return err
		}
		if err := s.updateRecord(ctx, sc.JobID, func(m *domain.Migration) { m.DeployJobID = jobID }); err != nil {
			return err
		}
	}
	dj, err := s.waitJob(ctx, jobID)
	if err != nil {
		return err
	}
	if dj.State != domain.JobSucceeded {
		msg := string(dj.State)
		if dj.ErrorClass != "" {
			msg += " (" + dj.ErrorClass + ")"
		}
		return &classed{class: ClassDeployFailed, err: fmt.Errorf("the destination deploy (job %s) ended %s", jobID, msg),
			recovery: recoverySourceRestarted + "See the deploy job for the cause. " + recoveryPartial}
	}
	return nil
}

// waitJob waits until a job is terminal.
func (s *Service) waitJob(ctx context.Context, id string) (domain.Job, error) {
	ch, cancel := s.opts.Jobs.Subscribe(id)
	defer cancel()
	for {
		j, err := s.opts.Jobs.Get(ctx, id)
		if err != nil {
			return j, err
		}
		if j.State.Terminal() {
			return j, nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return j, ctx.Err()
		}
	}
}

func (s *Service) stackFinalize(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := stackInput(sc)
	if err != nil {
		return err
	}
	var m domain.Migration
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if m, err = store.GetMigration(ctx, tx, sc.JobID); err != nil {
			return err
		}
		if m.State == domain.MigrationCompleted {
			return nil
		}
		now := s.now()
		m.State, m.CutOver, m.TargetPartial, m.UpdatedAt, m.FinishedAt = domain.MigrationCompleted, true, false, now, &now
		if err := store.UpdateMigration(ctx, tx, &m); err != nil {
			return err
		}
		s.mu.Lock()
		hooks := slices.Clone(s.moved)
		s.mu.Unlock()
		for _, h := range hooks {
			if err := h(ctx, tx, m.StackID, m.SourceEnvironmentID, m.TargetEnvironmentID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// The migration is complete: nothing is undone any more (the
	// compensation also checks the record).
	if err := sc.ReleaseCompensation(ctx, jobspec.CompStartSource); err != nil {
		return err
	}
	var co protocol.MigrationCleanupOutput
	if err := s.call(ctx, in.Target, protocol.ReqMigrationCleanup, protocol.MigrationCleanupInput{MigrationID: sc.JobID, Finished: true},
		&co, s.opts.RequestTimeout); err != nil {
		s.log.Warn("could not remove the migration's staging directory on the destination", "migration_id", sc.JobID, "error", err)
	}
	sc.Progress(ctx, 100, "migrated; the source stays stopped until its removal is confirmed")
	s.log.Info("stack migrated", "migration_id", sc.JobID, "stack_id", m.StackID, "from", m.SourceEnvironmentID, "to", m.TargetEnvironmentID,
		"bytes", m.Bytes)
	return nil
}

// startSource is the start_source compensation: put the stack record back
// on the source (when it had moved) and start the services that ran
// before. It does nothing once the migration completed.
func (s *Service) startSource(ctx context.Context, raw json.RawMessage) error {
	var a startArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("malformed compensation arguments: %w", err)
	}
	m, err := store.GetMigration(ctx, s.db, a.MigrationID)
	if err != nil && !errors.Is(err, domain.ErrMigrationNotFound) {
		return err
	}
	if m.State == domain.MigrationCompleted || m.State == domain.MigrationSourceRemoved {
		return nil
	}
	if m.CutOver {
		if m.DeployJobID != "" {
			if j, err := s.opts.Jobs.Get(ctx, m.DeployJobID); err == nil && !j.State.Terminal() {
				_, _ = s.opts.Jobs.Cancel(ctx, m.DeployJobID)
			}
		}
		var st domain.Stack
		err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
			if st, err = s.opts.Stacks.Place(ctx, tx, a.StackID, a.Placement); err != nil {
				return err
			}
			cur, err := store.GetMigration(ctx, tx, a.MigrationID)
			if err != nil {
				return err
			}
			cur.CutOver, cur.UpdatedAt = false, s.now()
			return store.UpdateMigration(ctx, tx, &cur)
		})
		if err != nil {
			return fmt.Errorf("put the stack back on its source: %w", err)
		}
		s.opts.Stacks.Published(st, map[string]string{"migrationId": a.MigrationID, "change": "migration_rolled_back"})
	}
	var res protocol.MigrationLifecycleOutput
	if err := s.call(ctx, a.Source, protocol.ReqMigrationStart, protocol.MigrationStartInput{MigrationID: a.MigrationID, Stack: a.Stack,
		Services: a.Services}, &res, s.opts.StopTimeout); err != nil {
		if errors.Is(err, jobs.ErrAgentOffline) {
			return errors.New("the source environment's agent is offline: start the stack once it reconnects")
		}
		return fmt.Errorf("start the source's services: %w", err)
	}
	s.log.Info("migration rolled back: source services started", "migration_id", a.MigrationID, "services", len(a.Services))
	return nil
}

// Volume migrations.

func volumeInput(sc *jobexec.StepContext) (volumeJobInput, error) {
	var in volumeJobInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return in, fmt.Errorf("malformed volume.migrate input: %w", err)
	}
	if !protocol.ValidDockerName(in.Volume) || !protocol.ValidDockerName(in.Name) || in.Target == "" {
		return in, errors.New("incomplete volume.migrate input")
	}
	return in, nil
}

func (s *Service) volumePrepare(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := volumeInput(sc)
	if err != nil {
		return err
	}
	j, err := s.opts.Jobs.Get(ctx, sc.JobID)
	if err != nil {
		return err
	}
	if err := s.authorizeDestination(ctx, sc.JobID, DestinationVolumeCapabilities(in.Target)); err != nil {
		return err
	}
	m := domain.Migration{ID: sc.JobID, Kind: domain.MigrationKindVolume, SourceEnvironmentID: j.EnvironmentID, TargetEnvironmentID: in.Target,
		Volumes: []domain.MigrationVolume{{Source: in.Volume, Target: in.Name}}, State: domain.MigrationRunning, CreatedAt: s.now(), UpdatedAt: s.now()}
	if err := s.ensureRecord(ctx, s.db, &m); err != nil {
		return err
	}
	g, err := s.previewVolume(ctx, j.EnvironmentID, in.Volume, VolumeRequest{TargetEnvironmentID: in.Target, TargetName: in.Name,
		AcknowledgeCrashConsistency: in.Ack}, false)
	if err != nil {
		return err
	}
	if !g.plan.Allowed() {
		b := g.plan.Blockers[0]
		return &classed{class: ClassBlocked, err: fmt.Errorf("%s: %s", b.Code, b.Message),
			recovery: "Nothing was changed. Preview the migration again and resolve its blockers."}
	}
	leftovers, err := s.leftovers(ctx, in.Target, func(l domain.Migration) bool {
		return l.ID != sc.JobID && l.Kind == domain.MigrationKindVolume && slices.Contains(l.VolumeTargets(), in.Name)
	})
	if err != nil {
		return err
	}
	if err := s.cleanLeftovers(ctx, in.Target, "", leftovers); err != nil {
		return err
	}
	return writeOutput(ctx, sc, func(*stackOutput) {})
}

func (s *Service) volumeTransfer(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := volumeInput(sc)
	if err != nil {
		return err
	}
	j, err := s.opts.Jobs.Get(ctx, sc.JobID)
	if err != nil {
		return err
	}
	if err := s.updateRecord(ctx, sc.JobID, func(m *domain.Migration) { m.TargetPartial = true }); err != nil {
		return err
	}
	p := part{name: "volume:" + in.Name,
		send: protocol.MigrationSendInput{MigrationID: sc.JobID, Part: protocol.PartVolume, Volume: in.Volume},
		receive: protocol.MigrationReceiveInput{MigrationID: sc.JobID, Part: protocol.PartVolume,
			Volume: &protocol.MigrationVolumeSpec{Name: in.Name, Labels: in.Labels}}}
	return s.transferParts(ctx, sc, j.EnvironmentID, in.Target, []part{p})
}

func (s *Service) volumeFinalize(ctx context.Context, sc *jobexec.StepContext) error {
	return s.updateRecord(ctx, sc.JobID, func(m *domain.Migration) {
		if m.State != domain.MigrationCompleted {
			now := s.now()
			m.State, m.TargetPartial, m.FinishedAt = domain.MigrationCompleted, false, &now
		}
	})
}
