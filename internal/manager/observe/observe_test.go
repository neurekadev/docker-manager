package observe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/events"
	"github.com/neurekadev/dockyard/internal/manager/metrics"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

const env = "env-1"

// fakeAgent serves host.metrics from a buffer like the agent sampler, with
// its own (possibly skewed) clock.
type fakeAgent struct {
	mu       sync.Mutex
	epoch    string
	batches  []protocol.MetricBatch
	skew     time.Duration // agent clock minus manager clock
	clk      clock.Clock
	offline  bool
	pageSize int
	requests []protocol.HostMetricsInput
	release  chan struct{} // when set, requests wait for it
	started  chan struct{}
	inv      *protocol.EngineInventory
	invCalls int
}

var errOffline = errors.New("agent offline")

type unsupported struct{}

func (unsupported) Error() string     { return "unsupported" }
func (unsupported) AgentCode() string { return protocol.CodeUnsupportedRequest }

func (a *fakeAgent) RequestEnvironment(ctx context.Context, envID, name string, input any, _ time.Duration) (json.RawMessage, error) {
	a.mu.Lock()
	release, started := a.release, a.started
	a.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.offline || envID != env {
		return nil, errOffline
	}
	switch name {
	case protocol.ReqEngineInfo:
		a.invCalls++
		if a.inv == nil {
			return nil, unsupported{}
		}
		return json.Marshal(a.inv)
	case protocol.ReqHostMetrics:
	default:
		return nil, unsupported{}
	}
	in := input.(protocol.HostMetricsInput)
	a.requests = append(a.requests, in)
	after := in.AfterSeq
	if in.Epoch != a.epoch {
		after = 0
	}
	out := protocol.HostMetricsOutput{Epoch: a.epoch, Now: a.clk.Now().Add(a.skew), IntervalSeconds: 10, Batches: []protocol.MetricBatch{}}
	limit := a.pageSize
	if limit == 0 {
		limit = protocol.MaxMetricsBatchesPerResponse
	}
	for _, b := range a.batches {
		if b.Seq <= after {
			continue
		}
		if len(out.Batches) == limit {
			out.More = true
			break
		}
		out.Batches = append(out.Batches, b)
	}
	return json.Marshal(out)
}

func (a *fakeAgent) Online(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return id == env && !a.offline
}

// tick appends a batch sampled "now" on the agent clock.
func (a *fakeAgent) tick(cpu float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	seq := uint64(len(a.batches) + 1)
	if n := len(a.batches); n > 0 {
		seq = a.batches[n-1].Seq + 1
	}
	a.batches = append(a.batches, protocol.MetricBatch{Seq: seq, At: a.clk.Now().Add(a.skew),
		Host:       protocol.HostSample{CPUPercent: &cpu, CPUs: 2},
		Containers: []protocol.ContainerSample{{Name: "web", ID: "c1", CPUPercent: &cpu}}})
}

func (a *fakeAgent) restart(epoch string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.epoch, a.batches = epoch, nil
}

type fixture struct {
	clk   *clock.Fake
	agent *fakeAgent
	store *metrics.Store
	bus   *events.Bus
	svc   *Service
	path  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clk := testutil.FakeClock()
	clk.Set(clk.Now().Truncate(time.Minute))
	f := &fixture{clk: clk, agent: &fakeAgent{epoch: "epoch-a", clk: clk}, bus: events.New(clk), path: filepath.Join(t.TempDir(), "metrics.db")}
	f.open(t)
	return f
}

func (f *fixture) open(t *testing.T) {
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

// advance moves both clocks by 10 s and lets the agent sample.
func (f *fixture) advance(n int) {
	for range n {
		f.clk.Advance(10 * time.Second)
		f.agent.tick(float64(len(f.agent.batches)))
	}
}

func (f *fixture) rows(t *testing.T) int64 {
	t.Helper()
	st, err := f.store.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st.Rows["host_raw"]
}

func cpuSeries(t *testing.T, f *fixture, from, to time.Time) ([]*float64, int) {
	t.Helper()
	r, err := f.store.Query(context.Background(), domain.MetricQuery{EnvironmentID: env, Kind: domain.MetricHost, From: from, To: to,
		Step: 10 * time.Second, Keys: []string{"cpu.percent"}})
	if err != nil {
		t.Fatal(err)
	}
	return r.Series[0].Values, r.Flags
}

func render(vs []*float64) string {
	out := ""
	for i, v := range vs {
		if i > 0 {
			out += " "
		}
		if v == nil {
			out += "-"
		} else {
			out += fmt.Sprintf("%g", *v)
		}
	}
	return out
}

// TestReconnectDoesNotDuplicateSamples: the collector's cursor advances
// with the samples; a manager restart resumes from the stored cursor; a
// lost cursor re-reads the whole agent buffer without storing anything
// twice; an agent restart (new epoch) continues with new samples; an
// offline interval stays a gap.
func TestReconnectDoesNotDuplicateSamples(t *testing.T) {
	f := newFixture(t)
	ctx := testutil.Context(t)
	start := f.clk.Now()
	f.advance(6)
	res, err := f.svc.Collect(ctx, env)
	if err != nil || res.Batches != 6 || res.Epoch != "epoch-a" {
		t.Fatalf("%+v %v", res, err)
	}
	f.advance(2)
	if res, err := f.svc.Collect(ctx, env); err != nil || res.Batches != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if got := f.agent.requests[1]; got.Epoch != "epoch-a" || got.AfterSeq != 6 {
		t.Fatalf("second request %+v", got)
	}

	// Manager restart: the cursor comes from the metrics database.
	f.open(t)
	f.advance(1)
	if res, err := f.svc.Collect(ctx, env); err != nil || res.Batches != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if got := f.agent.requests[len(f.agent.requests)-1]; got.AfterSeq != 8 {
		t.Fatalf("after restart %+v", got)
	}

	// Lost cursor: the whole buffer comes again; nothing is duplicated.
	if _, err := f.store.DB().ExecContext(ctx, `DELETE FROM collector_state`); err != nil {
		t.Fatal(err)
	}
	f.open(t)
	res, err = f.svc.Collect(ctx, env)
	if err != nil || res.Batches != 9 || res.Inserted != 0 || f.rows(t) != 9 {
		t.Fatalf("redelivery %+v %v rows %d", res, err, f.rows(t))
	}

	// Offline for a minute: the agent keeps sampling but its process
	// restarts (new epoch, empty buffer), so that minute is lost: a gap.
	f.agent.mu.Lock()
	f.agent.offline = true
	f.agent.mu.Unlock()
	f.advance(6)
	if _, err := f.svc.Collect(ctx, env); !errors.Is(err, errOffline) {
		t.Fatal(err)
	}
	f.agent.restart("epoch-b")
	f.agent.mu.Lock()
	f.agent.offline = false
	f.agent.mu.Unlock()
	f.advance(2)
	res, err = f.svc.Collect(ctx, env)
	if err != nil || res.Epoch != "epoch-b" || res.Batches != 2 || res.Inserted != 2*2 {
		t.Fatalf("new epoch %+v %v", res, err)
	}
	vals, flags := cpuSeries(t, f, start.Add(10*time.Second), f.clk.Now().Add(10*time.Second))
	// The new agent process numbers its samples from 0 again (fake CPU
	// values follow the buffer length).
	if got := render(vals); got != "0 1 2 3 4 5 6 7 8 - - - - - - 0 1" || flags != 0 {
		t.Fatalf("series %s flags %d", got, flags)
	}
}

// TestClockSkewIsCorrectedAndFlagged: an agent clock an hour ahead is
// corrected by the measured offset (samples land at manager time, flagged);
// small offsets are ignored; a timestamp in the future is clamped.
func TestClockSkewIsCorrectedAndFlagged(t *testing.T) {
	f := newFixture(t)
	ctx := testutil.Context(t)
	f.agent.skew = time.Hour
	start := f.clk.Now()
	f.advance(3)
	res, err := f.svc.Collect(ctx, env)
	if err != nil || res.Skew != -time.Hour || f.svc.Skew(env) != -time.Hour {
		t.Fatalf("%+v %v", res, err)
	}
	vals, flags := cpuSeries(t, f, start.Add(10*time.Second), start.Add(40*time.Second))
	if render(vals) != "0 1 2" || flags&domain.SampleSkewCorrected == 0 {
		t.Fatalf("%s flags %d", render(vals), flags)
	}
	// The correction is sticky: the same samples re-delivered after a
	// restart land in the same slots.
	if c, ok, _ := f.store.Cursor(ctx, env); !ok || c.Skew != -time.Hour {
		t.Fatalf("stored cursor %+v", c)
	}

	// Within the tolerance nothing is shifted.
	g := newFixture(t)
	g.agent.skew = 1500 * time.Millisecond
	g.advance(2)
	if res, err := g.svc.Collect(ctx, env); err != nil || res.Skew != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	// A sample stamped in the manager's future is clamped and flagged.
	g.agent.mu.Lock()
	g.agent.batches = append(g.agent.batches, protocol.MetricBatch{Seq: 3, At: g.clk.Now().Add(time.Second), Host: protocol.HostSample{CPUPercent: new(float64)}})
	g.agent.mu.Unlock()
	if _, err := g.svc.Collect(ctx, env); err != nil {
		t.Fatal(err)
	}
	l, ok, err := g.store.Latest(ctx, env)
	if err != nil || !ok || l.Flags&domain.SampleClamped == 0 || l.At.After(g.clk.Now()) {
		t.Fatalf("%+v %v %v", l, ok, err)
	}
}

func TestCollectPagesAndRejectsInvalidOutput(t *testing.T) {
	f := newFixture(t)
	ctx := testutil.Context(t)
	f.agent.pageSize = 4
	f.advance(10)
	res, err := f.svc.Collect(ctx, env)
	if err != nil || res.Batches != 10 || len(f.agent.requests) != 3 {
		t.Fatalf("%+v %v %d requests", res, err, len(f.agent.requests))
	}
	// Out-of-range values never reach the store.
	f.agent.mu.Lock()
	bad := 250.0
	f.agent.batches = append(f.agent.batches, protocol.MetricBatch{Seq: 11, At: f.clk.Now(), Host: protocol.HostSample{CPUPercent: &bad}})
	f.agent.mu.Unlock()
	if _, err := f.svc.Collect(ctx, env); !errors.Is(err, protocol.ErrInvalidFrame) {
		t.Fatalf("invalid output accepted: %v", err)
	}
}

// TestCollectorLoopIsBounded: a slow agent is not fetched twice at once;
// the loop continues with other rounds.
func TestCollectorLoopIsBounded(t *testing.T) {
	f := newFixture(t)
	f.agent.release, f.agent.started = make(chan struct{}), make(chan struct{}, 10)
	ctx, cancel := context.WithCancel(testutil.Context(t))
	done := make(chan struct{})
	go func() { defer close(done); f.svc.runCollector(ctx) }()
	if err := f.clk.BlockUntilWaiters(ctx, 1); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(10 * time.Second)
	<-f.agent.started // the first fetch hangs
	for range 3 {
		if err := f.clk.BlockUntilWaiters(ctx, 1); err != nil {
			t.Fatal(err)
		}
		f.clk.Advance(10 * time.Second)
	}
	select {
	case <-f.agent.started:
		t.Fatal("a second fetch started while the first was running")
	default:
	}
	close(f.agent.release)
	cancel()
	<-done
}

func TestUnsupportedAgentIsSkipped(t *testing.T) {
	f := newFixture(t)
	ctx := testutil.Context(t)
	f.svc.opts.Agents = unsupportedAgents{}
	if _, err := f.svc.Collect(ctx, env); err == nil {
		t.Fatal("no error")
	}
	f.svc.mu.Lock()
	until := f.svc.skipUntil[env]
	f.svc.mu.Unlock()
	if !until.After(f.clk.Now()) {
		t.Fatalf("not skipped: %s", until)
	}
	if err := f.svc.Reconcile(ctx, env); err != nil {
		t.Fatalf("reconcile must never fail the session: %v", err)
	}
}

type unsupportedAgents struct{}

func (unsupportedAgents) RequestEnvironment(context.Context, string, string, any, time.Duration) (json.RawMessage, error) {
	return nil, unsupported{}
}
func (unsupportedAgents) Online(string) bool { return true }

// TestInventoryRefreshedOnChange: the reconciler stores the inventory;
// Docker events refresh it once per debounce window.
func TestInventoryRefreshedOnChange(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(testutil.Context(t))
	defer cancel()
	f.agent.inv = &protocol.EngineInventory{EngineID: "E", Hostname: "h", CPUs: 2, MemoryBytes: 1, CollectedAt: f.clk.Now(), Containers: 1}
	sub := f.bus.Subscribe(16, func(e events.Event) bool { return e.Type == events.InventoryUpdated })
	if err := f.svc.Reconcile(ctx, env); err != nil {
		t.Fatal(err)
	}
	if inv, ok := f.svc.Inventory(env); !ok || inv.Containers != 1 {
		t.Fatalf("%+v", inv)
	}
	<-sub.C()
	done := make(chan struct{})
	go func() { defer close(done); f.svc.runInventory(ctx) }()
	if err := f.clk.BlockUntilWaiters(ctx, 1); err != nil { // the periodic ticker
		t.Fatal(err)
	}
	f.agent.mu.Lock()
	f.agent.inv = &protocol.EngineInventory{EngineID: "E", Hostname: "h", CPUs: 2, MemoryBytes: 1, CollectedAt: f.clk.Now(), Containers: 3}
	f.agent.mu.Unlock()
	for range 5 { // a burst of Docker events
		f.bus.Publish(events.Event{Type: events.DockerEvent, ResourceType: "container", ResourceID: "web", EnvironmentID: env})
	}
	if err := f.clk.BlockUntilWaiters(ctx, 2); err != nil { // ticker + debounce timer
		t.Fatal(err)
	}
	f.clk.Advance(DefaultInventoryDebounce)
	<-sub.C()
	if inv, _ := f.svc.Inventory(env); inv.Containers != 3 {
		t.Fatalf("%+v", inv)
	}
	f.agent.mu.Lock()
	calls := f.agent.invCalls
	f.agent.mu.Unlock()
	if calls != 2 {
		t.Fatalf("%d engine.info calls for one burst", calls)
	}
	// A restarted manager serves the stored inventory before the agent is back.
	f.open(t)
	if err := f.svc.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if inv, ok := f.svc.Inventory(env); !ok || inv.Containers != 3 {
		t.Fatalf("%+v %v", inv, ok)
	}
	cancel()
	<-done
}

func TestJournalBoundsReplayAndResets(t *testing.T) {
	clk := testutil.FakeClock()
	j := NewJournal(JournalOptions{Clock: clk, Size: 5, MaxAge: time.Minute, ListenerQueue: 2})
	ev := func(n int) events.Event {
		return events.Event{Type: events.DockerEvent, EnvironmentID: env, ResourceID: fmt.Sprint(n), At: clk.Now()}
	}
	for n := range 8 {
		j.Append(ev(n))
	}
	sub := j.Subscribe(env, "")
	if sub.Cursor != j.Epoch()+".8" || len(sub.Replay) != 0 || sub.Reset != "" {
		t.Fatalf("%+v", sub)
	}
	sub.Listener.Close()
	// Size bound: seq 4..8 retained; a cursor at 3 still resumes (nothing
	// between it and the retained entries is missing), 2 does not.
	if s := j.Subscribe(env, j.Epoch()+".3"); s.Reset != "" || len(s.Replay) != 5 || s.Replay[0].Seq != 4 {
		t.Fatalf("%+v", s)
	}
	if s := j.Subscribe(env, j.Epoch()+".2"); s.Reset != ResetCursorExpired {
		t.Fatalf("%+v", s)
	}
	// Age bound: after two minutes old entries are trimmed on append.
	clk.Advance(2 * time.Minute)
	j.Append(ev(9))
	if s := j.Subscribe(env, j.Epoch()+".7"); s.Reset != ResetCursorExpired {
		t.Fatalf("aged %+v", s)
	}
	if s := j.Subscribe(env, j.Epoch()+".8"); s.Reset != "" || len(s.Replay) != 1 {
		t.Fatalf("%+v", s)
	}
	// A slow listener overflows: it is told to reset and continues.
	l := j.Subscribe(env, "").Listener
	for n := range 5 {
		j.Append(ev(10 + n))
	}
	cursor, lost := l.Overflowed()
	if !lost || cursor != j.Epoch()+".14" || len(l.C()) != 0 {
		t.Fatalf("%s %v", cursor, lost)
	}
	j.Append(ev(20))
	if e := <-l.C(); e.Seq != 15 {
		t.Fatalf("%+v", e)
	}
	if _, lost := l.Overflowed(); lost {
		t.Fatal("overflow not cleared")
	}
	// Bus loss resets every environment's stream.
	j.resetAll(ResetGap)
	if e := <-l.C(); e.Reset != ResetGap {
		t.Fatalf("%+v", e)
	}
}

func TestJournalFollowsTheBus(t *testing.T) {
	clk := testutil.FakeClock()
	bus := events.New(clk)
	j := NewJournal(JournalOptions{Bus: bus, Clock: clk})
	ctx, cancel := context.WithCancel(testutil.Context(t))
	done := make(chan struct{})
	l := j.Subscribe(env, "").Listener
	sub := bus.Subscribe(busBuffer, journaled)
	go func() { defer close(done); j.consume(ctx, sub) }()
	bus.Publish(events.Event{Type: events.FilesInvalidated, EnvironmentID: env}) // not journaled
	bus.Publish(events.Event{Type: events.EnvironmentOnline, ResourceType: events.ResourceEnvironment, ResourceID: env})
	if e := <-l.C(); e.Event.Type != events.EnvironmentOnline || e.Seq != 1 {
		t.Fatalf("%+v", e)
	}
	cancel()
	<-done
}
