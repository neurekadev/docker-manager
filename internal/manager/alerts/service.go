// Package alerts raises, keeps and delivers the manager's alerts (#159):
// a failing or unreadable disk, a degraded or failed RAID array or ZFS
// pool, an environment offline past its grace period, a failed scheduled
// (or API token) job and available image updates.
//
// Every alert is a row of the alerts table, unique per dedupe key while it
// fires. Raising or resolving one and the outbox rows of its messages
// (alert_deliveries) are written in one transaction; the dispatcher sends
// the outbox through notify.Service.Send (at least once: a message whose
// send succeeded but was not recorded is sent again). Alerts are evaluated
// from the bus (host health reports, connection changes), from job finish
// hooks (failed jobs, update checks) and by a reconcile loop that repairs
// what a dropped bus event missed. docs/internal/architecture/alerts.md.
package alerts

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/movelock"
	"github.com/neurekadev/docker-manager/internal/manager/notify"
	"github.com/neurekadev/docker-manager/internal/manager/observe"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Defaults and limits.
const (
	// DefaultOfflineGrace is how long an active environment may be
	// offline before it raises an alert (DOCKER_MANAGER_ALERT_OFFLINE_GRACE).
	DefaultOfflineGrace = 5 * time.Minute
	// ReconcileInterval is how often every alert is evaluated again (it
	// repairs what a dropped bus event missed).
	ReconcileInterval = time.Minute
	// DiskRemovedAfter resolves the alert of a disk or array that is no
	// longer reported (silently: it was removed).
	DiskRemovedAfter = 24 * time.Hour
	// JobExpiry resolves a failed job's alert without a new run
	// (silently).
	JobExpiry = 7 * 24 * time.Hour
	// AlertRetention keeps resolved alerts; DeliveryRetention finished
	// deliveries.
	AlertRetention    = 90 * 24 * time.Hour
	DeliveryRetention = 7 * 24 * time.Hour
	// DeliveryDelay is how long a new message waits before it is sent, so
	// a burst for one channel goes out as one digest.
	DeliveryDelay = 10 * time.Second
	// RetryMin and RetryMax bound the backoff of a failed send; GiveUpAfter
	// ends the retries of a message.
	RetryMin    = 30 * time.Second
	RetryMax    = time.Hour
	GiveUpAfter = 24 * time.Hour
	// DispatchParallel is how many channels are sent to at once.
	DispatchParallel = 4
	// DigestMaxLines bounds the lines of a digest message.
	DigestMaxLines = 20
)

// Sender delivers a message through a channel (notify.Service).
type Sender interface {
	Send(ctx context.Context, channelID string, msg domain.NotificationMessage) (notify.Result, error)
}

// HealthSource returns an environment's last disk health report
// (observe.Service).
type HealthSource interface {
	HostHealth(environmentID string) (observe.HostHealth, bool)
}

// JobHooks installs the finish hooks and the change listener (jobs.Engine).
type JobHooks interface {
	OnFinish(kind domain.JobKind, h jobs.FinishHook)
	OnChange(fn func(jobIDs []string))
}

// Options configures the service.
type Options struct {
	DB     *bun.DB
	Bus    *events.Bus
	Clock  clock.Clock
	Logger *slog.Logger
	// Sender sends messages; nil keeps them pending.
	Sender Sender
	// Health reads the disk health reports; nil raises no disk or RAID
	// alerts.
	Health HealthSource
	// MoveLock pauses evaluation and delivery while the manager moves.
	MoveLock *movelock.Lock
	// PublicURL is the manager's origin; messages link to its pages.
	PublicURL string
	// OfflineGrace (default DefaultOfflineGrace).
	OfflineGrace time.Duration
}

// Service owns the alerts.
type Service struct {
	opts Options
	db   *bun.DB
	clk  clock.Clock
	log  *slog.Logger
	// startedAt is when this manager started: an environment's offline
	// grace runs from its last connection change or from here, whichever
	// is later (every environment is marked offline at start).
	startedAt time.Time

	wake     chan struct{}
	dispatch chan struct{}

	mu sync.Mutex
	// pending are alerts changed inside a job's finishing transaction,
	// announced once the job's change is committed (OnChange).
	pending map[string][]domain.Alert
}

// New creates the service.
func New(opts Options) (*Service, error) {
	if opts.DB == nil {
		return nil, errors.New("alerts: DB is required")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.OfflineGrace <= 0 {
		opts.OfflineGrace = DefaultOfflineGrace
	}
	return &Service{opts: opts, db: opts.DB, clk: opts.Clock, log: opts.Logger, startedAt: opts.Clock.Now().UTC(),
		wake: make(chan struct{}, 1), dispatch: make(chan struct{}, 1), pending: map[string][]domain.Alert{}}, nil
}

// RegisterJobHooks installs the job_failed hook on every kind, the
// updates hook on update checks and the change listener that announces
// alerts changed by them. Call before the engine runs.
func (s *Service) RegisterJobHooks(h JobHooks, kinds []domain.JobKind, updateCheck domain.JobKind) {
	for _, k := range kinds {
		h.OnFinish(k, s.onJobFinished)
	}
	h.OnFinish(updateCheck, s.onUpdateCheckFinished)
	h.OnChange(s.jobsChanged)
}

// SetHealth installs the host health reports (the observation service is
// built after the job engine recovered; call before Run).
func (s *Service) SetHealth(h HealthSource) { s.opts.Health = h }

func (s *Service) now() time.Time { return s.clk.Now().UTC().Truncate(time.Microsecond) }

// locked reports whether the manager moves (nothing is evaluated or sent).
func (s *Service) locked() bool { return s.opts.MoveLock.ReadOnly() }

// Wake runs the reconcile loop soon.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) wakeDispatch() {
	select {
	case s.dispatch <- struct{}{}:
	default:
	}
}

// publish announces changed alerts (after their transaction committed)
// and wakes the dispatcher when messages may be waiting.
func (s *Service) publish(changed []domain.Alert) {
	if len(changed) == 0 {
		return
	}
	for i := range changed {
		a := changed[i]
		s.opts.Bus.Publish(events.Event{Type: events.AlertUpdated, ResourceType: events.ResourceAlert, ResourceID: a.ID,
			EnvironmentID: a.EnvironmentID, Revision: a.Revision, Alert: &a,
			Attributes: map[string]string{"kind": string(a.Kind), "state": string(a.State)}})
	}
	s.wakeDispatch()
}

// jobsChanged announces the alerts a finishing job changed, now that the
// job's transaction committed.
func (s *Service) jobsChanged(jobIDs []string) {
	var out []domain.Alert
	s.mu.Lock()
	for _, id := range jobIDs {
		out = append(out, s.pending[id]...)
		delete(s.pending, id)
	}
	s.mu.Unlock()
	s.publish(out)
}

// hold keeps alerts changed in a job's transaction until it commits.
func (s *Service) hold(jobID string, changed []domain.Alert) {
	if len(changed) == 0 {
		return
	}
	s.mu.Lock()
	s.pending[jobID] = append(s.pending[jobID], changed...)
	s.mu.Unlock()
}

// flushHeld announces alerts held longer than a reconcile round (a job
// change that was never reported: the event only asks views to refetch).
func (s *Service) flushHeld() {
	s.mu.Lock()
	var out []domain.Alert
	for id, as := range s.pending {
		out = append(out, as...)
		delete(s.pending, id)
	}
	s.mu.Unlock()
	s.publish(out)
}

// inTx runs fn in a transaction and announces the alerts it changed after
// the commit.
func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context, tx bun.Tx) ([]domain.Alert, error)) error {
	var changed []domain.Alert
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var err error
		changed, err = fn(ctx, tx)
		return err
	})
	if err != nil {
		return err
	}
	s.publish(changed)
	return nil
}

// Run evaluates alerts (bus events, a reconcile ticker) and sends their
// messages until ctx ends.
func (s *Service) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { s.runReconcile(ctx) })
	wg.Go(func() { s.runDispatch(ctx) })
	wg.Wait()
}

// trigger selects the bus events that change alerts: connection changes,
// archives and new host health reports.
func trigger(e events.Event) bool {
	switch e.Type {
	case events.EnvironmentOnline, events.EnvironmentOffline, events.EnvironmentArchived, events.EnvironmentReattached,
		events.EnvironmentCreated:
		return true
	case events.InventoryUpdated:
		return e.Attributes["health"] == "true"
	}
	return false
}

func (s *Service) runReconcile(ctx context.Context) {
	sub := s.opts.Bus.Subscribe(256, trigger)
	defer sub.Close()
	ticker := s.clk.NewTicker(ReconcileInterval)
	defer ticker.Stop()
	next := s.reconcile(ctx)
	for {
		var timer clock.Timer
		var due <-chan time.Time
		if !next.IsZero() {
			timer = s.clk.NewTimer(max(next.Sub(s.clk.Now()), 0))
			due = timer.C()
		}
		full := true
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case e := <-sub.C():
			if e.Type == events.InventoryUpdated && e.EnvironmentID != "" && !s.locked() {
				// A new host health report: that environment only.
				if err := s.EvaluateHealth(ctx, e.EnvironmentID); err != nil && ctx.Err() == nil {
					s.log.Warn("could not evaluate disk alerts", "environment_id", e.EnvironmentID, "error", err)
				}
				full = false
			}
		case <-ticker.C():
		case <-s.wake:
		case <-due:
		}
		if timer != nil {
			timer.Stop()
		}
		if full {
			next = s.reconcile(ctx)
		}
	}
}

// reconcile evaluates every alert once and returns when the next
// environment's offline grace ends (zero: none pending).
func (s *Service) reconcile(ctx context.Context) time.Time {
	if s.locked() || ctx.Err() != nil {
		return time.Time{}
	}
	s.flushHeld()
	next, err := s.EvaluateOffline(ctx)
	if err != nil && ctx.Err() == nil {
		s.log.Warn("could not evaluate offline alerts", "error", err)
	}
	if err := s.evaluateAllHealth(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("could not evaluate disk alerts", "error", err)
	}
	if err := s.ReconcileUpdates(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("could not evaluate update alerts", "error", err)
	}
	if err := s.ExpireJobs(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("could not expire job alerts", "error", err)
	}
	if err := s.Purge(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("could not purge old alerts", "error", err)
	}
	return next
}

// Purge deletes resolved alerts after AlertRetention and finished
// deliveries after DeliveryRetention.
func (s *Service) Purge(ctx context.Context) error {
	now := s.now()
	if _, err := store.PurgeResolvedAlerts(ctx, s.db, now.Add(-AlertRetention)); err != nil {
		return err
	}
	_, err := store.PurgeAlertDeliveries(ctx, s.db, now.Add(-DeliveryRetention))
	return err
}
