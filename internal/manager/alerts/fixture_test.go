package alerts

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/movelock"
	"github.com/neurekadev/docker-manager/internal/manager/notify"
	"github.com/neurekadev/docker-manager/internal/manager/observe"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/manager/store/storetest"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// fakeHealth serves host health reports per environment.
type fakeHealth struct {
	mu      sync.Mutex
	reports map[string]observe.HostHealth
}

func (h *fakeHealth) HostHealth(env string) (observe.HostHealth, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.reports[env]
	return r, ok
}

func (h *fakeHealth) set(env string, smart []protocol.SMARTDevice, md []protocol.MDArray, zfs []protocol.ZFSPool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reports[env] = observe.HostHealth{HostHealthOutput: protocol.HostHealthOutput{
		SMART: protocol.SMARTReport{Status: protocol.SMARTOK, Devices: smart},
		RAID:  protocol.RAIDReport{MD: md, ZFS: zfs},
	}}
}

// sent is one message the fake sender received.
type sent struct {
	channel string
	msg     domain.NotificationMessage
}

// fakeSender records messages and answers with result (or err).
type fakeSender struct {
	mu     sync.Mutex
	msgs   []sent
	result notify.Result
	err    error
}

func (s *fakeSender) Send(_ context.Context, channelID string, msg domain.NotificationMessage) (notify.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, sent{channel: channelID, msg: msg})
	if s.err != nil {
		return notify.Result{}, s.err
	}
	return s.result, nil
}

func (s *fakeSender) fail(class string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result = notify.Result{OK: false, ErrorClass: class}
}

func (s *fakeSender) succeed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result = notify.Result{OK: true}
}

// take returns and forgets the messages received so far.
func (s *fakeSender) take() []sent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.msgs
	s.msgs = nil
	return out
}

// fakeHooks records the job engine registrations.
type fakeHooks struct {
	finish map[domain.JobKind][]jobs.FinishHook
	change []func([]string)
}

func (h *fakeHooks) OnFinish(kind domain.JobKind, fn jobs.FinishHook) {
	h.finish[kind] = append(h.finish[kind], fn)
}

func (h *fakeHooks) OnChange(fn func([]string)) { h.change = append(h.change, fn) }

type fixture struct {
	t      *testing.T
	ctx    context.Context
	db     *bun.DB
	clk    *clock.Fake
	bus    *events.Bus
	health *fakeHealth
	sender *fakeSender
	lock   *movelock.Lock
	hooks  *fakeHooks
	svc    *Service
	logs   *testutil.LogBuffer
}

// newFixture opens a migrated database with two active, online
// environments (env-1 "homelab", env-2 "office") and the service; its
// clock starts at testutil.Epoch.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := testutil.Context(t)
	db := storetest.Migrated(t)
	clk := testutil.FakeClock()
	logger, logs := testutil.CaptureLogger()
	f := &fixture{t: t, ctx: ctx, db: db, clk: clk, bus: events.New(clk), health: &fakeHealth{reports: map[string]observe.HostHealth{}},
		sender: &fakeSender{result: notify.Result{OK: true}}, lock: movelock.New(),
		hooks: &fakeHooks{finish: map[domain.JobKind][]jobs.FinishHook{}}, logs: logs}
	now := clk.Now().UTC()
	for id, name := range map[string]string{"env-1": "homelab", "env-2": "office"} {
		e := domain.Environment{ID: id, Name: name, EngineID: "E-" + id, InstallID: "i-" + id, AgentID: "agent-" + id,
			Status: domain.EnvironmentActive, Online: true, ConnectionChangedAt: &now, Revision: 1, CreatedAt: now, UpdatedAt: now}
		if err := store.InsertEnvironment(ctx, db, &e); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := New(Options{DB: db, Bus: f.bus, Clock: clk, Logger: logger, Sender: f.sender, Health: f.health, MoveLock: f.lock,
		PublicURL: "https://docker.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	svc.RegisterJobHooks(f.hooks, []domain.JobKind{"backup.run", "stack.deploy", "update.check"}, "update.check")
	f.svc = svc
	return f
}

// channel stores a notification channel (its address is never opened:
// the sender is fake).
func (f *fixture) channel(name string, kinds []domain.NotificationEventKind, all bool, envs []string, resolved bool) domain.NotificationChannel {
	f.t.Helper()
	now := f.clk.Now().UTC()
	if kinds == nil {
		kinds = domain.NotificationEventKinds()
	}
	c := domain.NotificationChannel{ID: ids.New(), Name: name, Service: "generic", Enabled: true, EventKinds: kinds, SendResolved: resolved,
		AllEnvironments: all, EnvironmentIDs: envs, AddressFingerprint: "fp_test", AddressVersion: 1, AddressUpdatedAt: now, Revision: 1,
		CreatedAt: now, UpdatedAt: now}
	if err := store.InsertNotificationChannel(f.ctx, f.db, &c, "sealed-"+name); err != nil {
		f.t.Fatal(err)
	}
	return c
}

// setEnvironment changes an environment's connection state and status.
func (f *fixture) setEnvironment(id string, online bool, changedAt time.Time, status domain.EnvironmentStatus) {
	f.t.Helper()
	e, err := store.GetEnvironment(f.ctx, f.db, id)
	if err != nil {
		f.t.Fatal(err)
	}
	e.Online, e.ConnectionChangedAt, e.Status = online, &changedAt, status
	e.Revision++
	if err := store.UpdateEnvironment(f.ctx, f.db, &e, e.Revision-1); err != nil {
		f.t.Fatal(err)
	}
}

// firing returns the firing alerts (oldest first).
func (f *fixture) firing() []domain.Alert {
	f.t.Helper()
	as, err := store.FiringAlerts(f.ctx, f.db, "", "")
	if err != nil {
		f.t.Fatal(err)
	}
	return as
}

// one returns the only firing alert.
func (f *fixture) one() domain.Alert {
	f.t.Helper()
	as := f.firing()
	if len(as) != 1 {
		f.t.Fatalf("want one firing alert, have %d: %+v", len(as), as)
	}
	return as[0]
}

// deliveries returns every delivery of an alert.
func (f *fixture) deliveries(alertID string) []domain.AlertDelivery {
	f.t.Helper()
	ds, err := store.AlertDeliveries(f.ctx, f.db, alertID)
	if err != nil {
		f.t.Fatal(err)
	}
	return ds
}

// pending returns every pending delivery.
func (f *fixture) pending() []domain.AlertDelivery {
	f.t.Helper()
	ds, err := store.PendingAlertDeliveries(f.ctx, f.db)
	if err != nil {
		f.t.Fatal(err)
	}
	return ds
}

// dispatch waits out the delivery delay and sends what is due.
func (f *fixture) dispatch() []sent {
	f.t.Helper()
	f.clk.Advance(DeliveryDelay)
	if _, err := f.svc.Dispatch(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	return f.sender.take()
}

// health evaluates env-1's report.
func (f *fixture) evaluate(env string) {
	f.t.Helper()
	if err := f.svc.EvaluateHealth(f.ctx, env); err != nil {
		f.t.Fatal(err)
	}
}

// finish runs the finish hooks of j's kind in a transaction, like the job
// engine, then reports the change.
func (f *fixture) finish(j domain.Job) {
	f.t.Helper()
	if j.ID == "" {
		j.ID = ids.New()
	}
	if err := f.db.RunInTx(f.ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		for _, h := range f.hooks.finish[j.Kind] {
			if err := h(ctx, tx, j); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
	for _, fn := range f.hooks.change {
		fn([]string{j.ID})
	}
}

func i64(v int64) *int64 { return &v }

func intp(v int) *int { return &v }

func boolp(v bool) *bool { return &v }
