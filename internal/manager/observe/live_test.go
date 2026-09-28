package observe

import (
	"slices"
	"sync"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

func names(ms []domain.LatestContainerMetrics) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Values.Name)
	}
	return out
}

func byName(ms []domain.LatestContainerMetrics, name string) domain.LatestContainerMetrics {
	for _, m := range ms {
		if m.Values.Name == name {
			return m
		}
	}
	return domain.LatestContainerMetrics{}
}

// TestLiveValuesServedWhileFresh: a metrics.live answer is kept in memory
// and announced on the bus without values; the latest reads prefer it
// over the stored sample while it is fresh (values it lacks stay stored),
// add containers only it knows, drop stored containers a complete answer
// no longer lists, and fall back to the stored samples once it is stale.
func TestLiveValuesServedWhileFresh(t *testing.T) {
	f := newFixture(t)
	ctx := testutil.Context(t)
	f.advance(2)
	if _, err := f.svc.Collect(ctx, env); err != nil {
		t.Fatal(err)
	}
	stored, ok, err := f.svc.Latest(ctx, env)
	if err != nil || !ok || stored.Host.CPUPercent == nil {
		t.Fatalf("stored %+v %v %v", stored, ok, err)
	}
	storedCPU := *stored.Host.CPUPercent
	sub := f.bus.Subscribe(16, func(e events.Event) bool { return e.Type == events.MetricsLive })
	defer sub.Close()

	cpu, mem, total, webCPU, apiMem := 42.0, int64(300), int64(1000), 7.0, int64(50)
	f.agent.live = &protocol.LiveMetricsOutput{At: f.clk.Now(),
		Host: protocol.LiveHostSample{CPUPercent: &cpu, CPUs: 2, MemoryUsedBytes: &mem, MemoryTotalBytes: &total},
		Containers: []protocol.LiveContainer{{Name: "web", ID: "c1", CPUPercent: &webCPU, MemoryBytes: &mem},
			{Name: "api", ID: "c2", MemoryBytes: &apiMem}}}
	f.clk.Advance(time.Second)
	if err := f.svc.CollectLive(ctx, env); err != nil {
		t.Fatal(err)
	}
	e := <-sub.C()
	if e.EnvironmentID != env || e.Attributes["host"] != "true" || len(e.Attributes) != 1 || !slices.Equal(e.Members, []string{"api", "web"}) {
		t.Fatalf("event %+v", e)
	}
	l, ok, err := f.svc.Latest(ctx, env)
	if err != nil || !ok || *l.Host.CPUPercent != 42 || *l.Host.MemoryUsedBytes != 300 || *l.Host.MemoryTotalBytes != 1000 || !l.At.Equal(f.clk.Now()) {
		t.Fatalf("latest with live values %+v", l)
	}
	cs, err := f.svc.LatestContainers(ctx, env, time.Minute)
	if err != nil || !slices.Equal(names(cs), []string{"api", "web"}) {
		t.Fatalf("containers %v %v", names(cs), err)
	}
	if web := byName(cs, "web"); *web.Values.CPUPercent != 7 || *web.Values.MemoryBytes != 300 || web.Values.MemoryLimitBytes != nil {
		t.Fatalf("web %+v", web.Values)
	}
	if api := byName(cs, "api"); api.Values.CPUPercent != nil || *api.Values.MemoryBytes != 50 {
		t.Fatalf("api %+v", api.Values)
	}

	// The first answer after a pause has no CPU: the stored CPU stays.
	f.agent.mu.Lock()
	f.agent.live.Host.CPUPercent = nil
	f.agent.live.Containers = []protocol.LiveContainer{{Name: "api", ID: "c2", MemoryBytes: &apiMem}}
	f.agent.mu.Unlock()
	if err := f.svc.CollectLive(ctx, env); err != nil {
		t.Fatal(err)
	}
	if l, _, _ := f.svc.Latest(ctx, env); *l.Host.CPUPercent != storedCPU || *l.Host.MemoryUsedBytes != 300 {
		t.Fatalf("latest without live CPU %+v", l.Host)
	}
	// A complete answer without web: web stopped since its stored sample.
	if cs, _ := f.svc.LatestContainers(ctx, env, time.Minute); !slices.Equal(names(cs), []string{"api"}) {
		t.Fatalf("complete answer %v", names(cs))
	}
	// An incomplete one keeps web's stored sample.
	f.agent.mu.Lock()
	f.agent.live.Flags = protocol.BatchContainersTruncated
	f.agent.mu.Unlock()
	if err := f.svc.CollectLive(ctx, env); err != nil {
		t.Fatal(err)
	}
	if cs, _ := f.svc.LatestContainers(ctx, env, time.Minute); !slices.Equal(names(cs), []string{"api", "web"}) || *byName(cs, "web").Values.CPUPercent == 7 {
		t.Fatalf("incomplete answer %v", names(cs))
	}

	// Stale live values are not served.
	f.clk.Advance(DefaultLiveFresh + time.Second)
	if l, _, _ := f.svc.Latest(ctx, env); *l.Host.CPUPercent != storedCPU || !l.At.Equal(stored.At) {
		t.Fatalf("latest with stale live values %+v", l)
	}
	if cs, _ := f.svc.LatestContainers(ctx, env, time.Minute); !slices.Equal(names(cs), []string{"web"}) {
		t.Fatalf("containers with stale live values %v", names(cs))
	}
}

// TestLiveRoundsFollowDemand: live requests go out only while there is
// demand (a browser live stream), never twice at once for one environment
// and never to agents that do not serve metrics.live; without demand the
// kept values are forgotten.
func TestLiveRoundsFollowDemand(t *testing.T) {
	f := newFixture(t)
	ctx := testutil.Context(t)
	demand := false
	f.svc.opts.LiveDemand = func() bool { return demand }
	mem := int64(1)
	f.agent.live = &protocol.LiveMetricsOutput{At: f.clk.Now(), Host: protocol.LiveHostSample{MemoryUsedBytes: &mem}}
	sem := make(chan struct{}, DefaultConcurrency)
	var wg sync.WaitGroup
	round := func() {
		f.svc.liveRound(ctx, sem, &wg)
		wg.Wait()
	}
	calls := func() int {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		return f.agent.liveCalls
	}
	kept := func() int {
		f.svc.mu.Lock()
		defer f.svc.mu.Unlock()
		return len(f.svc.live)
	}
	round()
	if calls() != 0 {
		t.Fatal("asked without demand")
	}
	demand = true
	round()
	if calls() != 1 || kept() != 1 {
		t.Fatalf("calls %d kept %d", calls(), kept())
	}
	demand = false
	round()
	if calls() != 1 || kept() != 0 {
		t.Fatalf("without demand: calls %d kept %d", calls(), kept())
	}

	// One request at a time per environment.
	demand = true
	f.agent.mu.Lock()
	f.agent.release, f.agent.started = make(chan struct{}), make(chan struct{}, 10)
	f.agent.mu.Unlock()
	f.svc.liveRound(ctx, sem, &wg)
	<-f.agent.started
	f.svc.liveRound(ctx, sem, &wg)
	select {
	case <-f.agent.started:
		t.Fatal("a second live request started while the first was running")
	default:
	}
	close(f.agent.release)
	wg.Wait()
	f.agent.mu.Lock()
	f.agent.release, f.agent.started = nil, nil
	f.agent.mu.Unlock()

	// An agent without metrics.live is never asked; one answering
	// unsupported_request is skipped for a while.
	f.agent.mu.Lock()
	f.agent.noLive = true
	f.agent.mu.Unlock()
	before := calls()
	round()
	if calls() != before {
		t.Fatal("asked an agent that does not serve metrics.live")
	}
	f.agent.mu.Lock()
	f.agent.noLive, f.agent.live = false, nil
	f.agent.mu.Unlock()
	round()
	round()
	if calls() != before+1 {
		t.Fatalf("unsupported agent asked %d times", calls()-before)
	}
}
