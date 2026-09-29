// Package scheduler is Docker Manager's shared cron scheduler (#13). It owns the
// durable schedule state of every scheduled policy (backups, repository
// verification, update checks and runs, prune, later kinds) and decides
// WHEN a run is due; the job engine (#26) executes, locks and recovers the
// jobs it enqueues.
//
//   - Policy workstreams register a PolicySource per kind (Register). The
//     scheduler synchronizes schedules from it, revalidates a policy when a
//     run is due and again at job dispatch, and asks it for the run's job
//     requests.
//   - Runs are enqueued as the manager service identity (authz.Service(),
//     origin scheduled), never as the policy's creator: they continue when
//     that account is disabled or deleted.
//   - Every processed instant is a durable run row (UNIQUE per schedule and
//     instant; idempotency key <kind>:<policy>:<instant>), written before
//     the jobs are enqueued with that key, so a restart never enqueues a
//     run twice.
//   - Missed runs after downtime: at most one catch-up run per schedule for
//     kinds that catch up (backups, verification, update checks), none for
//     prune and update runs; either way the missed instants are recorded.
//   - Overlap: a policy with a non-terminal job does not start another run
//     (recorded as skipped).
//
// Expressions are parsed and evaluated by internal/cron (DST rules there).
// See docs/internal/architecture/scheduler.md.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/cron"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/movelock"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// Defaults.
const (
	// DefaultGrace: a run processed at most this long after its instant
	// is on time; older instants count as missed.
	DefaultGrace = 5 * time.Minute
	// DefaultMaxSleep bounds one wait of the runner, so wall-clock jumps
	// (NTP steps, suspend/resume) and policy edits without Notify are
	// noticed within a minute.
	DefaultMaxSleep = time.Minute
	// DefaultHistory is the number of runs kept per schedule.
	DefaultHistory = 100
	// maxMissedScan bounds how many missed instants are counted.
	maxMissedScan = 10000
	// maxPolicyIDLen bounds policy IDs (they are part of job idempotency keys).
	maxPolicyIDLen = 64
)

// Options configures the scheduler.
type Options struct {
	DB     *bun.DB
	Clock  clock.Clock
	Logger *slog.Logger
	// Jobs is the job engine runs are enqueued in.
	Jobs *jobs.Engine
	// Audit records runs that did not enqueue a job (missed, skipped,
	// rejected, failed); the engine audits jobs itself. Optional.
	Audit audit.TxRecorder
	// MoveLock is the manager-move lock: while it is read-only (the manager
	// moves to a new server) Tick fires nothing (docs/internal/architecture/manager-move.md).
	MoveLock *movelock.Lock
	// Grace, MaxSleep and History override the defaults (tests).
	Grace    time.Duration
	MaxSleep time.Duration
	History  int
}

// Service is the scheduler. Create it with New, register policy sources,
// then Run it.
type Service struct {
	opts Options
	db   *bun.DB
	log  *slog.Logger
	eng  *jobs.Engine

	mu      sync.RWMutex
	kinds   map[string]Kind
	order   []string
	sources map[string]PolicySource

	tickMu sync.Mutex
	wake   chan struct{}
}

// New creates the scheduler with the built-in kinds and installs its hooks
// on the job engine: run history follows every job's terminal state, and
// scheduled jobs are revalidated by their policy source at dispatch.
func New(opts Options) (*Service, error) {
	if opts.DB == nil || opts.Jobs == nil {
		return nil, errors.New("scheduler: DB and Jobs are required")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Grace <= 0 {
		opts.Grace = DefaultGrace
	}
	if opts.MaxSleep <= 0 {
		opts.MaxSleep = DefaultMaxSleep
	}
	if opts.History <= 0 {
		opts.History = DefaultHistory
	}
	s := &Service{opts: opts, db: opts.DB, log: opts.Logger, eng: opts.Jobs,
		kinds: map[string]Kind{}, sources: map[string]PolicySource{}, wake: make(chan struct{}, 1)}
	for _, k := range BuiltinKinds() {
		if err := s.RegisterKind(k); err != nil {
			return nil, err
		}
	}
	for _, kind := range jobspec.Kinds() {
		opts.Jobs.OnFinish(kind, func(ctx context.Context, db bun.IDB, j domain.Job) error {
			if j.Origin != domain.OriginScheduled {
				return nil
			}
			return store.SetScheduleRunJobState(ctx, db, j)
		})
	}
	opts.Jobs.SetScheduledCheck(s.checkAtDispatch)
	return s, nil
}

// RegisterKind adds a schedule kind (later workstreams; tests).
func (s *Service) RegisterKind(k Kind) error {
	if err := k.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.kinds[k.Key]; dup {
		return fmt.Errorf("scheduler: kind %s registered twice", k.Key)
	}
	k.JobKinds = slices.Clone(k.JobKinds)
	s.kinds[k.Key] = k
	s.order = append(s.order, k.Key)
	return nil
}

// Register installs the policy source of a kind. Each kind has one source.
func (s *Service) Register(kind string, src PolicySource) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.kinds[kind]; !ok {
		return fmt.Errorf("%w: %s", domain.ErrScheduleKindUnknown, kind)
	}
	if src == nil {
		return fmt.Errorf("scheduler: nil source for %s", kind)
	}
	if _, dup := s.sources[kind]; dup {
		return fmt.Errorf("scheduler: kind %s already has a source", kind)
	}
	s.sources[kind] = src
	s.Notify()
	return nil
}

// Kinds returns the registered kinds in registration order.
func (s *Service) Kinds() []Kind {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Kind, 0, len(s.order))
	for _, k := range s.order {
		out = append(out, s.kinds[k])
	}
	return out
}

// Kind returns a kind by key.
func (s *Service) Kind(key string) (Kind, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.kinds[key]
	return k, ok
}

func (s *Service) source(kind string) (Kind, PolicySource, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.kinds[kind]
	src := s.sources[kind]
	return k, src, ok && src != nil
}

// Notify asks the runner for a pass now (a policy was created, edited,
// enabled, disabled or deleted). Policy owners call it after committing.
func (s *Service) Notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) now() time.Time { return s.opts.Clock.Now().UTC().Truncate(time.Microsecond) }

// Run drives the scheduler until ctx ends: a pass (Tick), then a wait until
// the earliest next run (at most MaxSleep, or until Notify).
func (s *Service) Run(ctx context.Context) error {
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("scheduler pass failed", "error", err)
		}
		wait := s.opts.MaxSleep
		// Nothing fires while the manager moves: no need to wake for a due run.
		if !s.opts.MoveLock.ReadOnly() {
			if next, ok, err := s.nextDue(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("scheduler: could not read the next run", "error", err)
			} else if ok {
				if d := next.Sub(s.opts.Clock.Now()); d < wait {
					wait = d
				}
			}
		}
		if wait <= 0 {
			// Still due right after a pass: the pass failed for it; retry
			// shortly instead of spinning.
			wait = time.Second
		}
		t := s.opts.Clock.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C():
		case <-s.wake:
			t.Stop()
		}
	}
}

// nextDue returns the earliest next run of an enabled schedule whose kind
// has a source.
func (s *Service) nextDue(ctx context.Context) (time.Time, bool, error) {
	on := true
	all, err := store.Schedules(ctx, s.db, domain.ScheduleFilter{Enabled: &on})
	if err != nil {
		return time.Time{}, false, err
	}
	var best time.Time
	for _, sc := range all {
		if _, _, ok := s.source(sc.Kind); !ok || sc.NextRunAt == nil {
			continue
		}
		if best.IsZero() || sc.NextRunAt.Before(best) {
			best = *sc.NextRunAt
		}
	}
	return best, !best.IsZero(), nil
}

// Tick runs one pass at the clock's current time: synchronize schedules
// from their sources, record due and missed dueSet, then enqueue every
// pending run. Tests call it directly with a fake clock.
func (s *Service) Tick(ctx context.Context) error {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	if s.opts.MoveLock.ReadOnly() {
		return nil // the manager moves to a new server: the new one runs the schedules
	}
	now := s.now()
	var errs []error
	for _, k := range s.Kinds() {
		if _, src, ok := s.source(k.Key); ok {
			if err := s.sync(ctx, k, src, now); err != nil {
				errs = append(errs, err)
			}
		}
	}
	on := true
	all, err := store.Schedules(ctx, s.db, domain.ScheduleFilter{Enabled: &on})
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, sc := range all {
		k, _, ok := s.source(sc.Kind)
		if !ok || sc.InvalidReason != "" {
			continue
		}
		if err := s.advance(ctx, k, sc, now); err != nil {
			errs = append(errs, fmt.Errorf("schedule %s: %w", sc.ID, err))
		}
	}
	pending, err := store.PendingScheduleRuns(ctx, s.db)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, r := range pending {
		if err := s.process(ctx, r, now); err != nil {
			errs = append(errs, fmt.Errorf("run %s: %w", r.ID, err))
		}
	}
	return errors.Join(errs...)
}

// fingerprint is what resets a schedule's cursor when it changes.
func fingerprint(cronExpr, tz string, enabled bool) string {
	return cronExpr + "\x00" + tz + "\x00" + strconv.FormatBool(enabled)
}

// sync makes the kind's schedules match its source.
func (s *Service) sync(ctx context.Context, k Kind, src PolicySource, now time.Time) error {
	list, err := src.Schedules(ctx)
	if err != nil {
		return fmt.Errorf("list %s schedules: %w", k.Key, err)
	}
	existing, err := store.Schedules(ctx, s.db, domain.ScheduleFilter{Kind: k.Key})
	if err != nil {
		return err
	}
	byPolicy := map[string]domain.Schedule{}
	for _, sc := range existing {
		byPolicy[sc.PolicyID] = sc
	}
	seen := map[string]bool{}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		for _, p := range list {
			if p.PolicyID == "" || len(p.PolicyID) > maxPolicyIDLen || seen[p.PolicyID] {
				s.log.Warn("scheduler: ignoring a schedule with an empty, long or duplicate policy ID", "kind", k.Key)
				continue
			}
			seen[p.PolicyID] = true
			sc, ok := byPolicy[p.PolicyID]
			if !ok {
				sc = domain.Schedule{ID: ids.New(), Kind: k.Key, PolicyID: p.PolicyID, CreatedAt: now}
			}
			changed := !ok || fingerprint(sc.Cron, sc.TimeZone, sc.Enabled) != fingerprint(p.Cron, p.TimeZone, p.Enabled)
			if !changed && sc.Name == p.Name && sc.EnvironmentID == p.EnvironmentID {
				continue
			}
			sc.Name, sc.EnvironmentID, sc.Cron, sc.TimeZone, sc.Enabled = p.Name, p.EnvironmentID, p.Cron, p.TimeZone, p.Enabled
			sc.UpdatedAt = now
			if changed {
				// New, edited, enabled or disabled: evaluate from now on; the
				// time before the change is never "missed".
				sc.Cursor = now
				sc.InvalidReason = ""
				sc.NextRunAt = nil
				if spec, loc, err := parseSpec(sc.Cron, sc.TimeZone); err != nil {
					sc.InvalidReason = err.Error()
				} else if sc.Enabled {
					if o, ok := spec.Next(now, loc); ok {
						at := o.At.UTC()
						sc.NextRunAt = &at
					}
				}
			}
			if !ok {
				if err := store.InsertSchedule(ctx, tx, &sc); err != nil {
					return err
				}
			} else if err := store.UpdateSchedule(ctx, tx, &sc); err != nil {
				return err
			}
		}
		for _, sc := range existing {
			if !seen[sc.PolicyID] {
				if err := store.DeleteSchedule(ctx, tx, sc.ID); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// advance records the instants of a schedule that are due at now: at most
// one run to enqueue (pending) and at most one row summarizing missed
// instants.
func (s *Service) advance(ctx context.Context, k Kind, sc domain.Schedule, now time.Time) error {
	clockBack := sc.Cursor.After(now) // the wall clock moved backwards
	if !clockBack && (sc.NextRunAt == nil || sc.NextRunAt.After(now)) {
		return nil
	}
	spec, loc, err := parseSpec(sc.Cron, sc.TimeZone)
	if err != nil {
		return nil // sync recorded InvalidReason
	}
	base := sc.Cursor
	if clockBack {
		// Evaluate from now: instants that were already processed are
		// refused by the run rows' uniqueness, and a cursor left far in the
		// future by a wrong clock does not stall the schedule.
		base = now
	}
	d := dueInstants(spec, loc, base, now)
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if d.count > 0 {
			onTime := now.Sub(d.latest) <= s.opts.Grace
			run := onTime || k.CatchUp == domain.CatchUpOnce
			// The missed instants: all but the run's, or all of them.
			missed := d
			if run {
				missed = d.withoutLatest()
			}
			if missed.count > 0 {
				if err := s.recordMissed(ctx, tx, k, sc, missed, onTime && run, now); err != nil {
					return err
				}
			}
			if run {
				r := domain.ScheduleRun{ID: ids.New(), ScheduleID: sc.ID, ScheduledFor: d.latest,
					IdempotencyKey: runKey(sc, d.latest), Outcome: domain.RunPending, CreatedAt: now, UpdatedAt: now}
				if !onTime {
					first := d.first
					r.CatchUp, r.MissedCount, r.MissedFrom = true, d.count, &first
					r.Reason = fmt.Sprintf("catch-up run: %s were due while the manager was not running (or its clock jumped forward); "+
						"%s schedules run once to catch up", countRuns(d.count, d.more), k.Label)
				}
				if err := store.InsertScheduleRun(ctx, tx, &r); err != nil && !errors.Is(err, store.ErrScheduleRunExists) {
					return err
				}
			}
			sc.Cursor = d.latest
		} else if sc.Cursor.After(now) {
			sc.Cursor = now
		}
		sc.NextRunAt = nil
		if o, ok := spec.Next(sc.Cursor, loc); ok {
			at := o.At.UTC()
			sc.NextRunAt = &at
		}
		sc.UpdatedAt = now
		if err := store.UpdateSchedule(ctx, tx, &sc); err != nil {
			return err
		}
		return store.PruneScheduleRuns(ctx, tx, sc.ID, s.opts.History)
	})
}

func countRuns(n int, more bool) string {
	switch {
	case more:
		return fmt.Sprintf("more than %d scheduled runs", n)
	case n == 1:
		return "1 scheduled run"
	}
	return fmt.Sprintf("%d scheduled runs", n)
}

// dueSet summarizes the due instants in (base, now]: the first, the
// latest, the one before the latest, and how many (more: at least count).
type dueSet struct {
	count        int
	more         bool
	first        time.Time
	latest, prev time.Time
}

// withoutLatest drops the latest instant.
func (d dueSet) withoutLatest() dueSet {
	if d.count <= 1 {
		return dueSet{}
	}
	return dueSet{count: d.count - 1, more: d.more, first: d.first, latest: d.prev}
}

// dueInstants finds the instants in (base, now]. It counts at most
// maxMissedScan of them; beyond that the latest two are located by
// searching growing windows before now, so a long downtime of a frequent
// schedule stays cheap and still resolves to the true latest instant.
func dueInstants(spec *cron.Schedule, loc *time.Location, base, now time.Time) dueSet {
	var d dueSet
	var prev, last time.Time
	for o, ok := spec.Next(base, loc); ok && !o.At.After(now); o, ok = spec.Next(o.At, loc) {
		if d.count == 0 {
			d.first = o.At.UTC()
		}
		if d.count == maxMissedScan {
			d.more = true
			prev, last = lastTwo(spec, loc, o.At, now)
			break
		}
		d.count++
		prev, last = last, o.At.UTC()
	}
	d.latest, d.prev = last, prev
	return d
}

// lastTwo returns the last two instants in [from, now] (from is one of
// them; prev may be zero only if from is the last).
func lastTwo(spec *cron.Schedule, loc *time.Location, from, now time.Time) (prev, last time.Time) {
	for _, w := range []time.Duration{time.Hour, 24 * time.Hour, 32 * 24 * time.Hour, 400 * 24 * time.Hour} {
		start := now.Add(-w)
		if !start.After(from) {
			break
		}
		prev, last = time.Time{}, time.Time{}
		n := 0
		for o, ok := spec.Next(start, loc); ok && !o.At.After(now); o, ok = spec.Next(o.At, loc) {
			prev, last = last, o.At.UTC()
			n++
		}
		if n >= 2 {
			return prev, last
		}
	}
	prev, last = time.Time{}, time.Time{}
	for o, ok := spec.Next(from.Add(-time.Nanosecond), loc); ok && !o.At.After(now); o, ok = spec.Next(o.At, loc) {
		prev, last = last, o.At.UTC()
	}
	return prev, last
}

func (s *Service) recordMissed(ctx context.Context, tx bun.Tx, k Kind, sc domain.Schedule, missed dueSet, currentOnTime bool, now time.Time) error {
	first, last := missed.first, missed.latest
	span := countRuns(missed.count, missed.more) + " between " + fmtInstant(first) + " and " + fmtInstant(last) + " were missed"
	if missed.count == 1 {
		span = "the run due at " + fmtInstant(first) + " was missed"
	}
	reason := span + " while the manager was not running (or its clock jumped forward); "
	switch {
	case currentOnTime:
		reason += "the current run started on time"
	case k.CatchUp == domain.CatchUpOnce:
		reason += "one catch-up run replaces them"
	default:
		reason += k.Label + " schedules do not catch up missed runs; the next run is at its scheduled time"
	}
	r := domain.ScheduleRun{ID: ids.New(), ScheduleID: sc.ID, ScheduledFor: last, IdempotencyKey: runKey(sc, last),
		Outcome: domain.RunMissed, MissedCount: missed.count, MissedFrom: &first, Reason: reason, ErrorClass: "missed",
		CreatedAt: now, UpdatedAt: now}
	if err := store.InsertScheduleRun(ctx, tx, &r); err != nil {
		if errors.Is(err, store.ErrScheduleRunExists) {
			return nil
		}
		return err
	}
	return s.audit(ctx, tx, k, sc, r)
}

func fmtInstant(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// runKey is the run's idempotency key.
func runKey(sc domain.Schedule, at time.Time) string {
	return sc.Kind + ":" + sc.PolicyID + ":" + fmtInstant(at)
}

// jobKey is the job-engine idempotency key of the run's i-th job.
func jobKey(runKey string, i int) string { return runKey + "#" + strconv.Itoa(i) }

// process enqueues a pending run (also after a crash between recording it
// and enqueueing, or between enqueueing and linking its jobs).
func (s *Service) process(ctx context.Context, r domain.ScheduleRun, now time.Time) error {
	sc, ok, err := store.GetSchedule(ctx, s.db, r.ScheduleID)
	if err != nil || !ok {
		return err
	}
	k, src, ok := s.source(sc.Kind)
	if !ok {
		return nil // the kind's source is not registered (yet): stays pending
	}
	due := Due{Kind: sc.Kind, PolicyID: sc.PolicyID, ScheduledFor: r.ScheduledFor, CatchUp: r.CatchUp, Key: r.IdempotencyKey}

	existing, err := s.existingJobs(ctx, r.IdempotencyKey)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		// Nothing enqueued yet: decide whether the run still happens.
		if now.Sub(r.ScheduledFor) > s.opts.Grace {
			later, err := store.LaterScheduleRunExists(ctx, s.db, sc.ID, r.ScheduledFor)
			if err != nil {
				return err
			}
			switch {
			case later:
				return s.finishRun(ctx, k, sc, r, domain.RunMissed, "missed",
					"the manager stopped before this run was enqueued; a later run replaced it")
			case k.CatchUp == domain.CatchUpSkip:
				return s.finishRun(ctx, k, sc, r, domain.RunMissed, "missed",
					"the manager stopped before this run was enqueued; "+k.Label+" schedules do not catch up missed runs")
			}
		}
		active, err := store.ActivePolicyJob(ctx, s.db, sc.PolicyID, k.JobKinds, nil)
		if err != nil {
			return err
		}
		if active != "" {
			return s.finishRun(ctx, k, sc, r, domain.RunSkipped, "previous_run_active",
				"skipped: the previous run is still active (job "+active+")")
		}
		if err := src.Validate(ctx, sc.PolicyID); err != nil {
			return s.sourceFailure(ctx, k, sc, r, "validate", err)
		}
	}
	reqs, err := src.Jobs(ctx, due)
	if err != nil {
		if len(existing) > 0 {
			return s.linkJobs(ctx, k, sc, r, existing, "")
		}
		return s.sourceFailure(ctx, k, sc, r, "build jobs", err)
	}
	if len(reqs) == 0 && len(existing) == 0 {
		return s.finishRun(ctx, k, sc, r, domain.RunSkipped, "nothing_to_run", "skipped: the policy selected nothing to run")
	}
	if len(reqs) > MaxJobsPerRun {
		return s.finishRun(ctx, k, sc, r, domain.RunFailed, domain.ErrorInternal,
			fmt.Sprintf("the policy produced %d jobs; at most %d are allowed per run", len(reqs), MaxJobsPerRun))
	}
	linked := existing
	failure := ""
	for i := range reqs {
		if i < len(linked) {
			continue
		}
		req := reqs[i]
		req.Principal = authz.Service()
		req.PolicyID = sc.PolicyID
		req.IdempotencyKey = jobKey(r.IdempotencyKey, i)
		j, _, err := s.eng.Enqueue(ctx, req)
		if errors.Is(err, domain.ErrJobIdempotencyConflict) {
			// Enqueued before a crash with an input that changed since:
			// keep the job that exists.
			found, ok, ferr := store.FindJobByIdempotencyKey(ctx, s.db, &domain.Job{IdempotencyKey: req.IdempotencyKey})
			if ferr != nil {
				return ferr
			}
			if ok {
				j, err = found, nil
			}
		}
		if err != nil {
			s.log.Error("scheduler: could not enqueue a scheduled job", "kind", sc.Kind, "policy_id", sc.PolicyID,
				"job_kind", req.Kind, "error", err)
			failure = enqueueClass(err)
			break
		}
		linked = append(linked, j)
	}
	if len(linked) == 0 {
		return s.finishRun(ctx, k, sc, r, domain.RunFailed, failure, "the job could not be enqueued ("+failure+")")
	}
	return s.linkJobs(ctx, k, sc, r, linked, failure)
}

// existingJobs finds jobs a previous attempt enqueued for the run.
func (s *Service) existingJobs(ctx context.Context, key string) ([]domain.Job, error) {
	var out []domain.Job
	for i := range MaxJobsPerRun {
		j, ok, err := store.FindJobByIdempotencyKey(ctx, s.db, &domain.Job{IdempotencyKey: jobKey(key, i)})
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		out = append(out, j)
	}
	return out, nil
}

func enqueueClass(err error) string {
	switch {
	case errors.Is(err, domain.ErrJobKindUnavailable):
		return "job_kind_unavailable"
	case errors.Is(err, domain.ErrJobInvalid), errors.Is(err, domain.ErrJobUnknownKind):
		return "invalid_job"
	}
	return domain.ErrorInternal
}

func (s *Service) sourceFailure(ctx context.Context, k Kind, sc domain.Schedule, r domain.ScheduleRun, what string, err error) error {
	if rej, ok := asRejection(err); ok {
		class := rej.Class
		if class == "" {
			class = "rejected"
		}
		return s.finishRun(ctx, k, sc, r, domain.RunRejected, class, "rejected: "+rej.Reason)
	}
	s.log.Error("scheduler: policy source failed", "kind", sc.Kind, "policy_id", sc.PolicyID, "step", what, "error", err)
	return s.finishRun(ctx, k, sc, r, domain.RunFailed, domain.ErrorInternal, "the policy could not be evaluated ("+what+" failed; see the manager log)")
}

func (s *Service) linkJobs(ctx context.Context, k Kind, sc domain.Schedule, r domain.ScheduleRun, linked []domain.Job, failure string) error {
	now := s.now()
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		for i, j := range linked {
			// Re-read: the job may have finished since it was enqueued (its
			// finish hook found no linked row then).
			cur, err := store.GetJob(ctx, tx, j.ID)
			if err == nil {
				j = cur
			} else if !errors.Is(err, domain.ErrJobNotFound) {
				return err
			}
			if err := store.AddScheduleRunJob(ctx, tx, r.ID, i, j); err != nil {
				return err
			}
		}
		reason := r.Reason
		if failure != "" {
			reason = "only " + strconv.Itoa(len(linked)) + " of the run's jobs could be enqueued (" + failure + ")"
		}
		if err := store.FinishScheduleRun(ctx, tx, r.ID, domain.RunEnqueued, reason, failure, now); err != nil {
			return err
		}
		if failure != "" {
			r.Outcome, r.Reason, r.ErrorClass = domain.RunFailed, reason, failure
			return s.audit(ctx, tx, k, sc, r)
		}
		return nil
	})
}

func (s *Service) finishRun(ctx context.Context, k Kind, sc domain.Schedule, r domain.ScheduleRun, outcome domain.ScheduleOutcome, class, reason string) error {
	now := s.now()
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := store.FinishScheduleRun(ctx, tx, r.ID, outcome, reason, class, now); err != nil {
			return err
		}
		r.Outcome, r.Reason, r.ErrorClass = outcome, reason, class
		return s.audit(ctx, tx, k, sc, r)
	})
}

// audit records a run that did not (fully) enqueue its jobs, as the
// manager service identity.
func (s *Service) audit(ctx context.Context, tx bun.IDB, k Kind, sc domain.Schedule, r domain.ScheduleRun) error {
	if s.opts.Audit == nil {
		return nil
	}
	return s.opts.Audit.RecordTx(ctx, tx, domain.AuditEvent{
		Category: domain.AuditOperations, Action: "schedule.run_" + string(r.Outcome), Actor: audit.ServiceActor(),
		EnvironmentID: sc.EnvironmentID, Outcome: domain.AuditFailure, ErrorClass: r.ErrorClass,
		Targets: []domain.AuditTarget{{Type: k.PolicyType, ID: sc.PolicyID, EnvironmentID: sc.EnvironmentID}},
		Details: map[string]any{"kind": sc.Kind, "scheduledFor": fmtInstant(r.ScheduledFor), "missedCount": r.MissedCount},
	})
}

// checkAtDispatch is the engine's ScheduledCheck: the policy of a queued
// scheduled job is revalidated right before dispatch.
func (s *Service) checkAtDispatch(ctx context.Context, j domain.Job) (string, error) {
	sc, _, ok, err := store.ScheduleRunOfJob(ctx, s.db, j.ID)
	if err != nil {
		return "", err
	}
	if !ok {
		if j.PolicyID == "" {
			return "", nil
		}
		// The run was not linked yet (the pass enqueueing it is still
		// running) or the policy's schedule was removed.
		for _, k := range s.Kinds() {
			if slices.Contains(k.JobKinds, j.Kind) {
				if found, ok, err := store.ScheduleByPolicy(ctx, s.db, k.Key, j.PolicyID); err != nil {
					return "", err
				} else if ok {
					sc = found
					break
				}
			}
		}
		if sc.ID == "" {
			return "", nil
		}
	}
	_, src, ok := s.source(sc.Kind)
	if !ok {
		return "", nil
	}
	if err := src.Validate(ctx, sc.PolicyID); err != nil {
		if rej, ok := asRejection(err); ok {
			return rej.Reason, nil
		}
		return "", err
	}
	return "", nil
}

// ValidateSpec checks an expression and IANA zone; errors are *InvalidError
// (errors.Is domain.ErrScheduleInvalid) listing each problem. Policy owners
// call it when a policy is created or edited.
func ValidateSpec(cronExpr, tz string) error {
	_, _, err := parseSpec(cronExpr, tz)
	return err
}

// InvalidError lists what is wrong with an expression and/or time zone.
type InvalidError struct {
	Problems []*cron.ParseError
}

func (e *InvalidError) Error() string {
	msgs := make([]string, 0, len(e.Problems))
	for _, p := range e.Problems {
		msgs = append(msgs, p.Error())
	}
	sort.Strings(msgs)
	out := "invalid schedule: "
	for i, m := range msgs {
		if i > 0 {
			out += "; "
		}
		out += m
	}
	return out
}

// Is matches domain.ErrScheduleInvalid.
func (e *InvalidError) Is(target error) bool { return target == domain.ErrScheduleInvalid }

func parseSpec(cronExpr, tz string) (*cron.Schedule, *time.Location, error) {
	var problems []*cron.ParseError
	spec, err := cron.Parse(cronExpr)
	var pe *cron.ParseError
	if errors.As(err, &pe) {
		problems = append(problems, pe)
	}
	loc, err := cron.LoadLocation(tz)
	if errors.As(err, &pe) {
		problems = append(problems, pe)
	}
	if len(problems) > 0 {
		return nil, nil, &InvalidError{Problems: problems}
	}
	return spec, loc, nil
}
