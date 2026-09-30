package backups

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/lifecycle"
	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/restic"
)

// backup.run (#10):
//
//	prepare          validate, open (or initialize) the environment's
//	                 repository — finishing a key rotation on it — and
//	                 resolve every item's scope; items that cannot be
//	                 backed up fail individually, items removed since the
//	                 run was planned are skipped (ClassItemGone).
//	stop_containers  with shutdown on: record every affected service's
//	                 state and register the restart compensation (the
//	                 agent-side recovery record, journaled before anything
//	                 stops), then stop the running services of each stack in
//	                 reverse dependency order. A failed stop aborts the job
//	                 and the compensation restarts what was running.
//	snapshot         one restic snapshot per item (not idempotent); an item
//	                 removed before its turn is skipped, not failed. A
//	                 cancellation stops restic mid-item; the items backed
//	                 up so far keep their snapshots.
//	start_containers restart only the previously running services, in
//	                 dependency order; a dependency conflict is reported,
//	                 never forced.
//	record           write the host manifest into the repository.
//
// The compensation also runs after a failure, a cancellation or an agent
// restart, so containers are never stranded.

// shutdownRecord is the compensation's argument: what was running.
type shutdownRecord struct {
	Projects []projectState `json:"projects"`
	// Containers are standalone containers (IDs) stopped by a restore.
	Containers []string `json:"containers,omitempty"`
}

type projectState struct {
	Project    string   `json:"project"`
	WasRunning []string `json:"wasRunning"`
}

func (s *Service) input(sc *jobexec.StepContext) (protocol.BackupRunInput, error) {
	var in protocol.BackupRunInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return in, err
	}
	return in, in.Validate()
}

func (s *Service) output(sc *jobexec.StepContext) protocol.BackupRunOutput {
	var out protocol.BackupRunOutput
	_ = json.Unmarshal(sc.Output(), &out)
	return out
}

// openForJob opens the job's location with the command's credential.
func (s *Service) openForJob(ctx context.Context, sc *jobexec.StepContext, ref protocol.BackupRepositoryRef, init bool) (backup.Opened, error) {
	cred := sc.Secrets.RepositoryCredentialFor(ref.RepositoryID)
	loc, err := s.location(ref, cred)
	if err != nil {
		return backup.Opened{}, classed(err)
	}
	o, err := backup.OpenLocation(ctx, s.opts.Restic, loc, cred.Password, cred.PreviousPassword, init)
	if err != nil {
		return o, classed(err)
	}
	if o.CompressionIgnored {
		s.log.Info("the backup location has restic repository format 1, which cannot compress; writing without the compression setting",
			"repository_id", ref.RepositoryID, "scope", ref.Scope)
	}
	return o, nil
}

// classed turns handler errors into job error classes.
func classed(err error) error {
	var r *backup.Refusal
	if errors.As(err, &r) {
		return err
	}
	if restic.CodeOf(err) != "" {
		return err
	}
	return &backup.Refusal{Class: errorClass(err), Message: err.Error(), Guidance: "Check the repository settings and this agent's configuration."}
}

func (s *Service) stepPrepare(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := s.input(sc)
	if err != nil {
		return backup.Refuse(domain.ErrorRejected, "invalid backup input: "+err.Error(), "Run the backup again from the policy.")
	}
	sc.Progress(ctx, 2, "opening the backup repository")
	o, err := s.openForJob(ctx, sc, in.Repository, true)
	if err != nil {
		return err
	}
	out := protocol.BackupRunOutput{ResticRepositoryID: o.ResticRepositoryID, KeyGeneration: in.Repository.KeyGeneration}
	if o.Migrated || o.PreviousRemoved {
		out.Warnings = append(out.Warnings, "the repository was moved to the current Recovery Key")
	}
	usable, skipped := 0, 0
	for _, it := range in.Items {
		p := s.plan(ctx, it, &in.Repository, false)
		m := memberOf(in, it)
		switch {
		case isGone(p.err):
			skipped++
			skip(ctx, sc, &m, p.err)
		case p.err != nil:
			m.State, m.ErrorClass = backup.StateFailed, errorClass(p.err)
			sc.Item(ctx, it.Key(), domain.ItemFailed, p.err.Error())
		default:
			usable++
			m.Paths = snapPaths(p.paths)
			m.Volumes = p.volumes
		}
		out.Members = append(out.Members, m)
	}
	if err := sc.SetOutput(ctx, out); err != nil {
		return err
	}
	// Nothing left to back up because everything was removed is not a
	// failure: the job goes on and records the skipped members.
	if usable == 0 && skipped < len(in.Items) {
		return backup.Refuse("empty_scope", "no item of this backup can be backed up", "Check the items' errors and the policy's selection.")
	}
	return nil
}

// skip marks a member whose item was removed before its turn (err is the
// plan's ClassItemGone error): skipped, never failed, and said so in the
// job's items and progress.
func skip(ctx context.Context, sc *jobexec.StepContext, m *backup.Member, err error) {
	m.State, m.ErrorClass = backup.StateSkipped, backup.ClassItemGone
	m.Paths, m.Volumes = nil, nil
	sc.Item(ctx, m.Item, domain.ItemSkipped, err.Error())
	sc.Progress(ctx, -1, "skipped: "+err.Error())
}

// memberDone reports whether a member needs no snapshot (any more).
func memberDone(m backup.Member) bool {
	return m.State == backup.StateFailed || m.State == backup.StateSkipped || m.SnapshotID != ""
}

func memberOf(in protocol.BackupRunInput, it protocol.BackupItem) backup.Member {
	env, _ := backup.ScopeEnvironment(in.Repository.Scope)
	m := backup.Member{Item: it.Key(), Kind: it.Kind, Scope: in.Repository.Scope, RepositoryID: in.Repository.RepositoryID,
		EnvironmentID: env, StackID: it.StackID, StackName: it.StackName, Volume: it.Volume, State: backup.StatePending,
		Consistency: backup.ConsistencyLive}
	if it.Kind == backup.MemberStack {
		m.RequiredCapabilities = []string{"compose", "files"}
	} else {
		m.RequiredCapabilities = []string{"files"}
	}
	return m
}

func (s *Service) stepStopContainers(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := s.input(sc)
	if err != nil {
		return err
	}
	if !in.Shutdown {
		return nil
	}
	eng, err := s.engine()
	if err != nil {
		return classed(err)
	}
	out := s.output(sc)
	failed := map[string]bool{}
	for _, m := range out.Members {
		if m.State == backup.StateFailed || m.State == backup.StateSkipped {
			failed[m.Item] = true
		}
	}
	rep := &protocol.ShutdownReport{}
	var rec shutdownRecord
	type stop struct {
		graph   *lifecycle.Graph
		project string
		running []string
	}
	var stops []stop
	// live are stack members backed up while running (Docker Manager's own
	// project): their snapshots stay marked live.
	live := map[string]bool{}
	for _, it := range in.Items {
		if it.Kind != backup.MemberStack || failed[it.Key()] {
			continue
		}
		p := s.plan(ctx, it, &in.Repository, true)
		if p.err != nil {
			continue
		}
		containers, err := lifecycle.ProjectContainers(ctx, eng, p.project)
		if err != nil {
			return err
		}
		g, err := lifecycle.GraphFromContainers(containers)
		if err != nil {
			return err
		}
		protected := map[string]bool{}
		for _, a := range p.affected {
			if a.Protected != "" {
				protected[a.Service] = true
			}
		}
		if len(protected) > 0 {
			// Docker Manager's own project is never stopped (#32).
			rep.Conflicts = append(rep.Conflicts, fmt.Sprintf("stack %s contains Docker Manager's own containers; it is backed up live", p.project))
			live[it.Key()] = true
			continue
		}
		running := map[string]bool{}
		for _, c := range containers {
			svc := c.Labels[lifecycle.ComposeServiceLabel]
			if c.State == "running" || c.State == "restarting" || c.State == "paused" {
				running[svc] = true
			}
		}
		var was []string
		for _, svc := range g.Services() {
			rep.PreState = append(rep.PreState, protocol.ShutdownServiceState{Project: p.project, Service: svc, Running: running[svc]})
			if running[svc] {
				was = append(was, svc)
			}
		}
		rep.Conflicts = append(rep.Conflicts, p.conflicts...)
		if len(was) == 0 {
			continue
		}
		rec.Projects = append(rec.Projects, projectState{Project: p.project, WasRunning: was})
		stops = append(stops, stop{graph: g, project: p.project, running: was})
	}
	out.Shutdown = rep
	if err := sc.SetOutput(ctx, out); err != nil {
		return err
	}
	if len(stops) == 0 {
		return nil
	}
	// The recovery record is journaled before anything stops.
	if err := sc.AddCompensation(ctx, jobspec.CompStartContainers, rec); err != nil {
		return err
	}
	for _, st := range stops {
		sc.Progress(ctx, 10, "stopping "+st.project)
		r, err := lifecycle.Stop(ctx, st.graph, s.projectRuntime(eng, st.project), st.running, s.lifecycleOpts(ctx, sc))
		for _, svc := range r.Stopped {
			rep.Stopped = append(rep.Stopped, st.project+"/"+svc)
		}
		if err != nil {
			out.Shutdown = rep
			_ = sc.SetOutput(ctx, out)
			return backup.Refuse("shutdown_failed", fmt.Sprintf("stopping %s failed: %v", st.project, err),
				"Nothing was backed up. The containers that were running are started again; check the stack and run the backup again.")
		}
	}
	for i := range out.Members {
		if out.Members[i].Kind == backup.MemberStack && out.Members[i].State != backup.StateFailed &&
			out.Members[i].State != backup.StateSkipped && !live[out.Members[i].Item] {
			out.Members[i].Consistency = backup.ConsistencyShutdown
		}
	}
	out.Shutdown = rep
	return sc.SetOutput(ctx, out)
}

func (s *Service) lifecycleOpts(ctx context.Context, sc *jobexec.StepContext) lifecycle.Options {
	return lifecycle.Options{Clock: s.opts.Clock, WaitTimeout: s.opts.WaitTimeout,
		Progress: func(service, message string) { sc.Progress(ctx, -1, service+": "+message) }}
}

func (s *Service) stepSnapshot(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := s.input(sc)
	if err != nil {
		return err
	}
	o, err := s.openForJob(ctx, sc, in.Repository, false)
	if err != nil {
		return err
	}
	out := s.output(sc)
	host := backup.ScopeDir(in.Repository.Scope)
	n := len(in.Items)
	// A cancellation stops restic mid-item (it writes no snapshot for it);
	// the items backed up so far keep theirs.
	rctx, stop := sc.WatchCancel(ctx, s.opts.Clock, jobexec.DefaultCancelPoll)
	defer stop()
	for i, it := range in.Items {
		if rctx.Err() != nil && ctx.Err() == nil {
			return stopped(ctx, sc, out)
		}
		idx := slices.IndexFunc(out.Members, func(m backup.Member) bool { return m.Item == it.Key() })
		if idx < 0 {
			out.Members = append(out.Members, memberOf(in, it))
			idx = len(out.Members) - 1
		}
		m := &out.Members[idx]
		if memberDone(*m) {
			continue
		}
		p := s.plan(ctx, it, &in.Repository, false)
		if isGone(p.err) {
			skip(ctx, sc, m, p.err)
			_ = sc.SetOutput(ctx, out)
			continue
		}
		if p.err != nil {
			m.State, m.ErrorClass = backup.StateFailed, errorClass(p.err)
			sc.Item(ctx, it.Key(), domain.ItemFailed, p.err.Error())
			_ = sc.SetOutput(ctx, out)
			continue
		}
		tags := []string{backup.TagDockerManager, backup.SetTag(in.SetID), backup.ItemTag(it.Key())}
		if in.PolicyID != "" {
			tags = append(tags, backup.PolicyTag(in.PolicyID))
		}
		base := 10 + 80*i/n
		var saved time.Time
		sum, err := o.Repo.Backup(rctx, restic.BackupRequest{Paths: p.paths, Excludes: p.excludes, Tags: tags, Host: host,
			Progress: func(pr restic.Progress) {
				// restic reports every second; the job record (visible to
				// job.read holders, no paths) at most every 5 s.
				if now := s.opts.Clock.Now(); pr.Percent >= 100 || saved.IsZero() || now.Sub(saved) >= progressInterval {
					saved = now
					sc.Progress(ctx, base+int(pr.Percent*0.8/float64(n)), progressMessage(it.Key(), pr))
				}
				if in.Activity {
					sc.Activity(ctx, activityOf(p, i, n, pr))
				}
			}})
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			if rctx.Err() != nil {
				return stopped(ctx, sc, out)
			}
			// Removed while restic read it: nothing was stored, and the
			// item no longer exists (only a Not Found counts).
			if again := s.plan(ctx, it, &in.Repository, false); isGone(again.err) {
				skip(ctx, sc, m, again.err)
				_ = sc.SetOutput(ctx, out)
				continue
			}
			m.State, m.ErrorClass = backup.StateFailed, restic.CodeOf(err)
			if m.ErrorClass == "" {
				m.ErrorClass = restic.CodeFailed
			}
			sc.Item(ctx, it.Key(), domain.ItemFailed, err.Error())
			_ = sc.SetOutput(ctx, out)
			continue
		}
		m.SnapshotID, m.Paths, m.Volumes, m.Bytes = sum.SnapshotID, snapPaths(p.paths), p.volumes, sum.TotalBytesProcessed
		m.VolumePaths = p.volumePaths
		if p.dir != "" {
			m.ProjectPath = snapPath(p.dir)
		}
		m.State = backup.StateComplete
		if sum.Incomplete {
			m.State, m.ErrorClass = backup.StatePartial, "files_unreadable"
			out.Warnings = append(out.Warnings, fmt.Sprintf("%s: some files could not be read (%s)", it.Key(), strings.Join(sum.Errors, "; ")))
		}
		if snaps, err := o.Repo.Snapshots(ctx, restic.SnapshotFilter{Tags: []string{backup.SetTag(in.SetID), backup.ItemTag(it.Key())}}); err == nil {
			for _, sn := range snaps {
				if sn.ID == sum.SnapshotID {
					m.SnapshotTime = sn.Time.UTC()
				}
			}
		}
		if m.SnapshotTime.IsZero() {
			m.SnapshotTime = s.opts.Clock.Now().UTC()
		}
		sc.Item(ctx, it.Key(), domain.ItemSucceeded, "snapshot "+sum.SnapshotID[:min(8, len(sum.SnapshotID))])
		if err := sc.SetOutput(ctx, out); err != nil {
			return err
		}
	}
	return nil
}

// stopped ends a snapshot step cancelled on request: the output keeps the
// members backed up so far, the others stay pending (the manager records
// them as cancelled) and the job ends cancelled.
func stopped(ctx context.Context, sc *jobexec.StepContext, out protocol.BackupRunOutput) error {
	if err := sc.SetOutput(ctx, out); err != nil {
		return err
	}
	return fmt.Errorf("the backup was stopped: %w", jobexec.ErrStepCancelled)
}

// progressInterval spaces the persisted progress of a snapshot.
const progressInterval = 5 * time.Second

// progressMessage is the job progress text of an item: counts, no paths.
func progressMessage(item string, pr restic.Progress) string {
	if pr.FilesTotal <= 0 {
		return "backing up " + item
	}
	return fmt.Sprintf("backing up %s (%d of %d files)", item, pr.FilesDone, pr.FilesTotal)
}

// activityOf is the live activity of item i of n (#10). The current file
// is given relative to the item: <volume>/<path> inside a volume, the
// project-relative path inside a stack directory, else its base name.
func activityOf(p itemPlan, i, n int, pr restic.Progress) protocol.ActivityPayload {
	a := protocol.ActivityPayload{Item: p.item.Key(), ItemIndex: i, ItemCount: n, Percent: min(100, max(0, int(pr.Percent))),
		FilesDone: max(0, pr.FilesDone), FilesTotal: max(0, pr.FilesTotal), BytesDone: max(0, pr.BytesDone),
		BytesTotal: max(0, pr.BytesTotal), SecondsRemaining: max(0, pr.SecondsRemaining)}
	if pr.CurrentFile != "" {
		a.CurrentFile = relativeFile(p, snapPath(pr.CurrentFile))
		if len(a.CurrentFile) > protocol.MaxActivityPath {
			a.CurrentFile = "…" + a.CurrentFile[len(a.CurrentFile)-protocol.MaxActivityPath+len("…"):]
		}
	}
	return a
}

func relativeFile(p itemPlan, f string) string {
	for _, name := range slices.Sorted(maps.Keys(p.volumePaths)) {
		if dir := p.volumePaths[name]; snapWithin(f, dir) {
			return path.Join(name, snapRel(f, dir))
		}
	}
	if p.dir != "" {
		if dir := snapPath(p.dir); snapWithin(f, dir) {
			return snapRel(f, dir)
		}
	}
	return path.Base(f)
}

func (s *Service) stepStartContainers(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := s.input(sc)
	if err != nil {
		return err
	}
	if !in.Shutdown {
		return nil
	}
	out := s.output(sc)
	if out.Shutdown == nil {
		return nil
	}
	rec := recordOf(out.Shutdown)
	restarted, err := s.resume(ctx, sc, rec)
	out.Shutdown.Restarted = append(out.Shutdown.Restarted, restarted...)
	if err != nil {
		out.Shutdown.RestartError = err.Error()
		_ = sc.SetOutput(ctx, out)
		return backup.Refuse("restart_failed", "restarting the stopped containers failed: "+err.Error(),
			"The backup itself completed. Start the affected stacks manually (only the services that were running before).")
	}
	if err := sc.SetOutput(ctx, out); err != nil {
		return err
	}
	return sc.ReleaseCompensation(ctx, jobspec.CompStartContainers)
}

func recordOf(rep *protocol.ShutdownReport) shutdownRecord {
	byProject := map[string][]string{}
	var order []string
	for _, st := range rep.PreState {
		if _, ok := byProject[st.Project]; !ok {
			order = append(order, st.Project)
			byProject[st.Project] = nil
		}
		if st.Running {
			byProject[st.Project] = append(byProject[st.Project], st.Service)
		}
	}
	var rec shutdownRecord
	for _, p := range order {
		if len(byProject[p]) > 0 {
			rec.Projects = append(rec.Projects, projectState{Project: p, WasRunning: byProject[p]})
		}
	}
	return rec
}

// resume restarts exactly the services that were running, dependencies
// first (lifecycle.Resume refuses instead of starting a service that was
// stopped before).
func (s *Service) resume(ctx context.Context, sc *jobexec.StepContext, rec shutdownRecord) ([]string, error) {
	eng, err := s.engine()
	if err != nil {
		return nil, err
	}
	var started []string
	var errs []error
	for _, p := range rec.Projects {
		containers, err := lifecycle.ProjectContainers(ctx, eng, p.Project)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		g, err := lifecycle.GraphFromContainers(containers)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		o := lifecycle.Options{Clock: s.opts.Clock, WaitTimeout: s.opts.WaitTimeout}
		if sc != nil {
			o = s.lifecycleOpts(ctx, sc)
		}
		r, err := lifecycle.Resume(ctx, g, s.projectRuntime(eng, p.Project), p.WasRunning, o)
		for _, svc := range r.Started {
			started = append(started, p.Project+"/"+svc)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Project, err))
		}
	}
	for _, id := range rec.Containers {
		d, err := eng.InspectContainer(ctx, id)
		if engine.IsCode(err, engine.CodeNotFound) {
			continue
		}
		if err == nil && d.State.Running {
			continue
		}
		if err == nil {
			err = eng.StartContainer(ctx, id)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("container %s: %w", id, err))
			continue
		}
		started = append(started, id)
	}
	return started, errors.Join(errs...)
}

// compStartContainers is the compensation: restart what was running
// (idempotent: services already running are left alone by Resume).
func (s *Service) compStartContainers(ctx context.Context, args json.RawMessage) error {
	var rec shutdownRecord
	if err := json.Unmarshal(args, &rec); err != nil {
		return err
	}
	_, err := s.resume(ctx, nil, rec)
	return err
}

func (s *Service) stepRecord(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := s.input(sc)
	if err != nil {
		return err
	}
	out := s.output(sc)
	m := backup.Manifest{Kind: backup.ManifestHost, SetID: in.SetID, InstanceID: in.InstanceID, PolicyID: in.PolicyID,
		PolicyName: in.PolicyName, StartedAt: in.StartedAt, CreatedAt: s.opts.Clock.Now().UTC(),
		Repositories: []backup.RepositoryRef{{ID: in.Repository.RepositoryID, Destination: in.Repository.Destination,
			KeyFingerprint: in.Repository.KeyFingerprint, KeyGeneration: in.Repository.KeyGeneration}},
		Locations: []backup.LocationRef{{RepositoryID: in.Repository.RepositoryID, Scope: in.Repository.Scope,
			Repository: in.Repository.Destination.Repository(in.Repository.Scope), ResticRepositoryID: out.ResticRepositoryID,
			EnvironmentName: in.EnvironmentName, KeyFingerprint: in.Repository.KeyFingerprint}},
		Members: out.Members}
	if env, ok := backup.ScopeEnvironment(in.Repository.Scope); ok {
		m.Locations[0].EnvironmentID = env
		if e, err := s.engine(); err == nil {
			m.Locations[0].EngineID = e.Identity().EngineID
		}
	}
	for i := range m.Members {
		if m.Members[i].State == backup.StatePending {
			m.Members[i].State, m.Members[i].ErrorClass = backup.StateFailed, "not_run"
		}
	}
	sort.Slice(m.Members, func(i, j int) bool { return m.Members[i].Item < m.Members[j].Item })
	m.Completeness = backup.Completeness(m.Members)
	b, err := backup.EncodeManifest(m)
	if err != nil {
		return err
	}
	o, err := s.openForJob(ctx, sc, in.Repository, false)
	if err != nil {
		return err
	}
	sum, err := o.Repo.Backup(ctx, restic.BackupRequest{Stdin: bytes.NewReader(b), StdinFilename: backup.ManifestFile,
		Tags: []string{backup.TagManifest, backup.SetTag(in.SetID)}, Host: backup.ScopeDir(in.Repository.Scope)})
	if err != nil {
		return err
	}
	out.ManifestSnapshotID = sum.SnapshotID
	out.Members = m.Members
	out.Stats = protocol.StatsOf(backup.MeasureStats(ctx, o.Repo))
	return sc.SetOutput(ctx, out)
}
