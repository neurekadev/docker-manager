package observe

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/metrics"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// healthAgent answers host.health for env-1 from its report (the agent's
// clock is the manager's unless sampledAt is set; fail answers with an
// error).
type healthAgent struct {
	mu          sync.Mutex
	clk         clock.Clock
	report      protocol.HostHealthOutput
	sampledAt   time.Time
	fail        error
	offline     bool
	serves      bool
	unsupported bool
	refreshes   []string
	calls       chan string
}

func (a *healthAgent) EnvironmentServes(envID, name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return envID == env && (name != protocol.ReqHostHealth || a.serves)
}

func (a *healthAgent) Online(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return id == env && !a.offline
}

func (a *healthAgent) RequestEnvironment(_ context.Context, envID, name string, input any, _ time.Duration) (json.RawMessage, error) {
	a.mu.Lock()
	if a.offline || envID != env {
		a.mu.Unlock()
		return nil, errOffline
	}
	if name != protocol.ReqHostHealth || a.unsupported {
		a.mu.Unlock()
		return nil, unsupported{}
	}
	in := input.(protocol.HostHealthInput)
	a.refreshes = append(a.refreshes, in.Refresh)
	if a.fail != nil {
		a.mu.Unlock()
		return nil, a.fail
	}
	out := a.report
	now := a.clk.Now().UTC()
	if !a.sampledAt.IsZero() {
		now = a.sampledAt
	}
	out.SampledAt, out.RAID.ReadAt = now, now
	calls := a.calls
	a.mu.Unlock()
	if calls != nil {
		calls <- in.Refresh
	}
	return json.Marshal(out)
}

func (a *healthAgent) set(fn func(r *protocol.HostHealthOutput)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fn(&a.report)
}

func (a *healthAgent) refreshCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.refreshes)
}

func healthReport(clk clock.Clock) protocol.HostHealthOutput {
	at := clk.Now().UTC()
	passed, temp := true, 36
	return protocol.HostHealthOutput{
		SMART: protocol.SMARTReport{Status: protocol.SMARTOK, ScannedAt: &at, CheckedAt: &at, Devices: []protocol.SMARTDevice{
			{Name: "/dev/sda", Type: "sat", Protocol: protocol.DiskATA, Model: "WDC WD40EFZX", Serial: "WD-1", SMARTSupported: true,
				Passed: &passed, TemperatureC: &temp, State: protocol.DiskOK, ReadAt: &at},
		}},
		RAID: protocol.RAIDReport{MD: []protocol.MDArray{{Name: "md0", Level: "raid1", State: protocol.RAIDHealthy, Devices: 2, Active: 2,
			Members: []protocol.MDMember{{Name: "sda1", Slot: 0, State: protocol.MemberActive}, {Name: "sdb1", Slot: 1, State: protocol.MemberActive}}}},
			ZFS: []protocol.ZFSPool{}},
	}
}

type healthFixture struct {
	clk   *clock.Fake
	agent *healthAgent
	store *metrics.Store
	bus   *events.Bus
	svc   *Service
	path  string
}

func newHealthFixture(t *testing.T) *healthFixture {
	t.Helper()
	clk := testutil.FakeClock()
	f := &healthFixture{clk: clk, agent: &healthAgent{clk: clk, serves: true}, bus: events.New(clk), path: filepath.Join(t.TempDir(), "metrics.db")}
	f.agent.report = healthReport(clk)
	f.open(t)
	return f
}

func (f *healthFixture) open(t *testing.T) {
	t.Helper()
	st, err := metrics.Open(testutil.Context(t), metrics.Options{Path: f.path, Clock: f.clk, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f.store = st
	f.svc = New(Options{Store: st, Agents: f.agent, Bus: f.bus, Clock: f.clk, Logger: testutil.Logger(t),
		Environments: func() []string { return []string{env} }})
}

// drain returns the health events published so far.
func drain(sub *events.Subscription) []events.Event {
	var out []events.Event
	for {
		select {
		case e := <-sub.C():
			out = append(out, e)
		default:
			return out
		}
	}
}

func TestRefreshHealthStoresAndAnnouncesChangesOnly(t *testing.T) {
	f := newHealthFixture(t)
	ctx := testutil.Context(t)
	sub := f.bus.Subscribe(16, func(e events.Event) bool { return e.Type == events.InventoryUpdated })
	defer sub.Close()

	h, err := f.svc.RefreshHealth(ctx, env, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.SMART.Devices) != 1 || !h.ReceivedAt.Equal(f.clk.Now()) {
		t.Fatalf("report %+v", h)
	}
	evs := drain(sub)
	if len(evs) != 1 || evs[0].EnvironmentID != env || evs[0].Attributes["health"] != "true" {
		t.Fatalf("events %+v", evs)
	}

	// The same content read a minute later: kept in memory, not stored
	// or announced again.
	f.clk.Advance(time.Minute)
	h, err = f.svc.RefreshHealth(ctx, env, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := f.svc.HostHealth(env); !got.RAID.ReadAt.Equal(f.clk.Now()) {
		t.Errorf("memory keeps the newest read: %v", got.RAID.ReadAt)
	}
	if evs := drain(sub); len(evs) != 0 {
		t.Fatalf("unchanged report announced: %+v", evs)
	}
	recs, err := f.store.HostHealths(ctx)
	if err != nil || len(recs) != 1 || !recs[0].ReceivedAt.Equal(f.clk.Now().Add(-time.Minute)) {
		t.Fatalf("stored %+v, %v", recs, err)
	}

	// A disk starts failing: stored and announced.
	f.agent.set(func(r *protocol.HostHealthOutput) {
		pending := int64(8)
		r.SMART.Devices[0].Pending = &pending
		r.SMART.Devices[0].State = protocol.DiskWarning
	})
	if _, err := f.svc.RefreshHealth(ctx, env, ""); err != nil {
		t.Fatal(err)
	}
	if evs := drain(sub); len(evs) != 1 {
		t.Fatalf("changed report not announced: %+v", evs)
	}

	// A restart restores the last stored report.
	f.open(t)
	if err := f.svc.Load(ctx); err != nil {
		t.Fatal(err)
	}
	got, ok := f.svc.HostHealth(env)
	if !ok || got.SMART.Devices[0].State != protocol.DiskWarning || got.SMART.Devices[0].Serial != "WD-1" {
		t.Fatalf("restored %+v, %v", got, ok)
	}
}

func TestRefreshHealthKeepsDevicesWhileARestartedAgentReads(t *testing.T) {
	f := newHealthFixture(t)
	ctx := testutil.Context(t)
	if _, err := f.svc.RefreshHealth(ctx, env, ""); err != nil {
		t.Fatal(err)
	}
	f.agent.set(func(r *protocol.HostHealthOutput) {
		r.SMART = protocol.SMARTReport{Status: protocol.SMARTOK, Checking: true, Devices: []protocol.SMARTDevice{}}
	})
	h, err := f.svc.RefreshHealth(ctx, env, "")
	if err != nil {
		t.Fatal(err)
	}
	if !h.SMART.Checking || len(h.SMART.Devices) != 1 || h.SMART.CheckedAt == nil {
		t.Fatalf("%+v", h.SMART)
	}
}

// TestRefreshHealthDropsAnOvertakenAnswer: an answer sampled before the
// kept report (a poll that arrives after a check's answer) does not
// replace it, unless the kept report is older than healthTimeout.
func TestRefreshHealthDropsAnOvertakenAnswer(t *testing.T) {
	f := newHealthFixture(t)
	ctx := testutil.Context(t)
	sub := f.bus.Subscribe(16, func(e events.Event) bool { return e.Type == events.InventoryUpdated })
	defer sub.Close()
	if _, err := f.svc.RefreshHealth(ctx, env, protocol.HealthRefreshSMART); err != nil {
		t.Fatal(err)
	}
	drain(sub)
	f.agent.set(func(r *protocol.HostHealthOutput) { r.SMART.Checking = true })
	f.agent.mu.Lock()
	f.agent.sampledAt = f.clk.Now().Add(-2 * time.Second).UTC()
	f.agent.mu.Unlock()
	h, err := f.svc.RefreshHealth(ctx, env, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := f.svc.HostHealth(env); h.SMART.Checking || got.SMART.Checking {
		t.Fatalf("an overtaken answer replaced the report: %+v", got.SMART)
	}
	if evs := drain(sub); len(evs) != 0 {
		t.Fatalf("an overtaken answer was announced: %+v", evs)
	}
	// Long after the kept report, an older agent clock is not a race.
	f.clk.Advance(healthTimeout)
	if h, err := f.svc.RefreshHealth(ctx, env, ""); err != nil || !h.SMART.Checking {
		t.Fatalf("%+v, %v", h.SMART, err)
	}
}

func TestCheckHealthRateLimitsAndMapsErrors(t *testing.T) {
	f := newHealthFixture(t)
	ctx := testutil.Context(t)
	if _, err := f.svc.CheckHealth(ctx, env, HealthScopeSMART); err != nil {
		t.Fatal(err)
	}
	var rl *HealthRateLimitError
	if _, err := f.svc.CheckHealth(ctx, env, HealthScopeSMART); !errors.As(err, &rl) || rl.RetryAfter != SMARTCheckSpacing {
		t.Fatalf("second check: %v", err)
	}
	// RAID has its own spacing.
	if _, err := f.svc.CheckHealth(ctx, env, HealthScopeRAID); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(RAIDCheckSpacing)
	if _, err := f.svc.CheckHealth(ctx, env, HealthScopeRAID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CheckHealth(ctx, env, HealthScopeSMART); !errors.As(err, &rl) || rl.RetryAfter != SMARTCheckSpacing-RAIDCheckSpacing {
		t.Fatalf("smart check within its spacing: %v", err)
	}
	f.clk.Advance(SMARTCheckSpacing)
	if _, err := f.svc.CheckHealth(ctx, env, HealthScopeSMART); err != nil {
		t.Fatal(err)
	}
	f.agent.mu.Lock()
	refreshes := append([]string(nil), f.agent.refreshes...)
	f.agent.mu.Unlock()
	if want := []string{"smart", "raid", "raid", "smart"}; len(refreshes) != len(want) {
		t.Fatalf("refreshes %q", refreshes)
	} else {
		for i := range want {
			if refreshes[i] != want[i] {
				t.Fatalf("refreshes %q, want %q", refreshes, want)
			}
		}
	}

	// A failed check does not use up the slot.
	f.clk.Advance(SMARTCheckSpacing)
	f.agent.mu.Lock()
	f.agent.fail = protocol.ErrRequestTimeout
	f.agent.mu.Unlock()
	if _, err := f.svc.CheckHealth(ctx, env, HealthScopeSMART); !errors.Is(err, ErrHealthTimeout) {
		t.Fatalf("timed out check: %v", err)
	}
	f.agent.mu.Lock()
	f.agent.fail = nil
	f.agent.mu.Unlock()
	if _, err := f.svc.CheckHealth(ctx, env, HealthScopeSMART); err != nil {
		t.Fatalf("retry after a failed check: %v", err)
	}
	// A caller that gave up keeps the slot: aborting and repeating never
	// gets around the spacing.
	f.clk.Advance(SMARTCheckSpacing)
	gone, cancel := context.WithCancel(ctx)
	cancel()
	f.agent.mu.Lock()
	f.agent.fail = context.Canceled
	f.agent.mu.Unlock()
	if _, err := f.svc.CheckHealth(gone, env, HealthScopeSMART); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled check: %v", err)
	}
	f.agent.mu.Lock()
	f.agent.fail = nil
	f.agent.mu.Unlock()
	if _, err := f.svc.CheckHealth(ctx, env, HealthScopeSMART); !errors.As(err, &rl) {
		t.Fatalf("check after a cancelled one: %v", err)
	}

	if _, err := f.svc.CheckHealth(ctx, env, "selftest"); !errors.Is(err, ErrHealthScope) {
		t.Errorf("unknown scope: %v", err)
	}
	f.agent.mu.Lock()
	f.agent.serves = false
	f.agent.mu.Unlock()
	if _, err := f.svc.CheckHealth(ctx, env, HealthScopeRAID); !errors.Is(err, ErrHealthUnsupported) {
		t.Errorf("an agent without host.health: %v", err)
	}
	f.agent.mu.Lock()
	f.agent.serves, f.agent.offline = true, true
	f.agent.mu.Unlock()
	if _, err := f.svc.CheckHealth(ctx, env, HealthScopeRAID); !errors.Is(err, ErrHealthOffline) {
		t.Errorf("offline: %v", err)
	}
	f.agent.mu.Lock()
	f.agent.offline, f.agent.unsupported = false, true
	f.agent.mu.Unlock()
	f.clk.Advance(time.Minute)
	if _, err := f.svc.RefreshHealth(ctx, env, ""); !errors.Is(err, ErrHealthUnsupported) {
		t.Errorf("unsupported_request: %v", err)
	}
	f.svc.mu.Lock()
	skip := f.svc.health.skip[env]
	f.svc.mu.Unlock()
	if !skip.Equal(f.clk.Now().Add(unsupportedBackoff)) {
		t.Errorf("backoff until %v", skip)
	}
}

func TestFollowHealthWhileTheAgentChecks(t *testing.T) {
	f := newHealthFixture(t)
	ctx := testutil.Context(t)
	f.agent.set(func(r *protocol.HostHealthOutput) { r.SMART.Checking = true })
	if _, err := f.svc.RefreshHealth(ctx, env, "smart"); err != nil {
		t.Fatal(err)
	}
	if got := <-f.svc.health.follow; got != env {
		t.Fatalf("follow %q", got)
	}
	// Stuck for longer than healthFollowUpMax: no more follow-ups.
	f.clk.Advance(healthFollowUpMax + time.Second)
	if _, err := f.svc.RefreshHealth(ctx, env, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-f.svc.health.follow:
		t.Fatalf("followed a stuck read: %q", got)
	default:
	}
	// "Check disks now" starts a new read: followed again.
	if _, err := f.svc.RefreshHealth(ctx, env, protocol.HealthRefreshSMART); err != nil {
		t.Fatal(err)
	}
	if got := <-f.svc.health.follow; got != env {
		t.Fatalf("follow after a new check %q", got)
	}
	// Done: the next check follows again.
	f.agent.set(func(r *protocol.HostHealthOutput) { r.SMART.Checking = false })
	if _, err := f.svc.RefreshHealth(ctx, env, ""); err != nil {
		t.Fatal(err)
	}
	f.svc.mu.Lock()
	_, following := f.svc.health.following[env]
	f.svc.mu.Unlock()
	if following {
		t.Fatal("still following after the read ended")
	}
}

// waitIdle waits until no host health request is in flight.
func waitIdle(ctx context.Context, t *testing.T, s *Service) {
	t.Helper()
	for ctx.Err() == nil {
		s.mu.Lock()
		n := len(s.health.inflight)
		s.mu.Unlock()
		if n == 0 {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("host health request still in flight")
}

func TestRunHealthPollsOnlineEnvironments(t *testing.T) {
	f := newHealthFixture(t)
	f.agent.calls = make(chan string, 8)
	ctx, cancel := context.WithCancel(testutil.Context(t))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); f.svc.runHealth(ctx) }()
	defer func() { cancel(); wg.Wait() }()

	// The periodic ticker exists; an environment comes online: asked 5 s
	// later.
	if err := f.clk.BlockUntilWaiters(ctx, 1); err != nil {
		t.Fatal(err)
	}
	f.bus.Publish(events.Event{Type: events.EnvironmentOnline, ResourceType: events.ResourceEnvironment, ResourceID: env, EnvironmentID: env})
	if err := f.clk.BlockUntilWaiters(ctx, 2); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(healthOnlineDelay)
	if got := <-f.agent.calls; got != "" {
		t.Fatalf("refresh %q", got)
	}
	waitIdle(ctx, t, f.svc)
	if _, ok := f.svc.HostHealth(env); !ok {
		t.Fatal("no report kept")
	}

	// The minute poll asks again.
	f.clk.Advance(DefaultHealthInterval - healthOnlineDelay)
	<-f.agent.calls
	waitIdle(ctx, t, f.svc)

	// An agent that stopped serving host.health is never asked (an older
	// agent would close the session on an unknown request name).
	f.agent.mu.Lock()
	f.agent.serves = false
	f.agent.mu.Unlock()
	f.clk.Advance(DefaultHealthInterval)
	waitIdle(ctx, t, f.svc)
	if n := f.agent.refreshCount(); n != 2 {
		t.Fatalf("%d requests, want 2", n)
	}
}
