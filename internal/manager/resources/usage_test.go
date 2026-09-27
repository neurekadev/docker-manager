package resources

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/secrets"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store/storetest"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// usageAgent answers volume.usage, counting the requests; with gate set a
// request waits for it (started is signaled first).
type usageAgent struct {
	mu      sync.Mutex
	calls   int
	err     error
	gate    chan struct{}
	started chan struct{}
}

func (a *usageAgent) RequestEnvironment(ctx context.Context, _, name string, _ any, _ time.Duration) (json.RawMessage, error) {
	if name != protocol.ReqVolumeUsage {
		return nil, errors.New("unexpected request " + name)
	}
	a.mu.Lock()
	a.calls++
	err, gate, started := a.err, a.gate, a.started
	a.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(protocol.VolumeUsageOutput{Volumes: []protocol.VolumeUsage{{Name: "data", Size: 2048, RefCount: 1},
		{Name: "nfs", Size: -1, RefCount: -1}}})
}

func (a *usageAgent) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// servingAgent adds the hub's advertisement check.
type servingAgent struct {
	*usageAgent
	serves, online bool
}

func (a servingAgent) Online(string) bool { return a.online }
func (a servingAgent) EnvironmentServes(_, name string) bool {
	return a.serves && name == protocol.ReqVolumeUsage
}

func usageService(t *testing.T, agents Requester) (*Service, *clock.Fake) {
	t.Helper()
	key, _ := secrets.GenerateKey(nil)
	clk := testutil.FakeClock()
	svc, err := New(Options{DB: storetest.Migrated(t), Keyring: secrets.NewKeyring(key), Agents: agents,
		Jobs: &fakeJobs{jobs: map[string]domain.Job{}, subs: map[string][]chan struct{}{}}, Clock: clk, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc, clk
}

// TestVolumeUsageIsSharedAndCachedBriefly: concurrent callers share one
// agent request, later callers reuse it until the TTL passed, and a
// failure is not cached.
func TestVolumeUsageIsSharedAndCachedBriefly(t *testing.T) {
	agent := &usageAgent{gate: make(chan struct{}), started: make(chan struct{}, 4)}
	svc, clk := usageService(t, agent)
	ctx := testutil.Context(t)

	type result struct {
		r   domain.VolumeUsageReport
		err error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			r, err := svc.VolumeUsage(ctx, "env-1")
			results <- result{r, err}
		}()
	}
	<-agent.started
	close(agent.gate)
	for range 2 {
		res := <-results
		if res.err != nil || res.r.Unsupported || res.r.Sizes["data"] != 2048 || res.r.Sizes["nfs"] != -1 || !res.r.ComputedAt.Equal(clk.Now()) {
			t.Fatalf("%+v %v", res.r, res.err)
		}
	}
	if n := agent.count(); n != 1 {
		t.Fatalf("%d requests for concurrent callers, want 1", n)
	}

	clk.Advance(VolumeUsageTTL - time.Second)
	if _, err := svc.VolumeUsage(ctx, "env-1"); err != nil || agent.count() != 1 {
		t.Fatalf("within the TTL: %v, %d requests", err, agent.count())
	}
	clk.Advance(time.Second)
	agent.mu.Lock()
	agent.started, agent.err = nil, jobs.ErrAgentOffline
	agent.mu.Unlock()
	var de *domain.DockerError
	if _, err := svc.VolumeUsage(ctx, "env-1"); !errors.As(err, &de) || de.Code != domain.DockerEnvironmentOffline || agent.count() != 2 {
		t.Fatalf("expired: %v, %d requests", err, agent.count())
	}
	agent.mu.Lock()
	agent.err = nil
	agent.mu.Unlock()
	if r, err := svc.VolumeUsage(ctx, "env-1"); err != nil || r.Sizes["data"] != 2048 || agent.count() != 3 {
		t.Fatalf("after a failure: %+v %v, %d requests", r, err, agent.count())
	}
	// Environments are cached separately.
	if _, err := svc.VolumeUsage(ctx, "env-2"); err != nil || agent.count() != 4 {
		t.Fatalf("other environment: %v, %d requests", err, agent.count())
	}
}

// TestVolumeUsageOfOlderAgentsIsUnsupported: an agent that does not
// advertise volume.usage is never asked (an N-1 agent would close the
// session); one that answers unsupported_request is reported the same way.
func TestVolumeUsageOfOlderAgentsIsUnsupported(t *testing.T) {
	ctx := testutil.Context(t)
	agent := &usageAgent{}
	svc, _ := usageService(t, servingAgent{usageAgent: agent, serves: false, online: true})
	if r, err := svc.VolumeUsage(ctx, "env-1"); err != nil || !r.Unsupported || agent.count() != 0 {
		t.Fatalf("not advertised: %+v %v, %d requests", r, err, agent.count())
	}
	svc, _ = usageService(t, servingAgent{usageAgent: agent, serves: false, online: false})
	var de *domain.DockerError
	if _, err := svc.VolumeUsage(ctx, "env-1"); !errors.As(err, &de) || de.Code != domain.DockerEnvironmentOffline || agent.count() != 0 {
		t.Fatalf("offline: %v, %d requests", err, agent.count())
	}
	agent.err = codedErr{protocol.CodeUnsupportedRequest, "volume.usage is not served"}
	svc, _ = usageService(t, agent)
	if r, err := svc.VolumeUsage(ctx, "env-1"); err != nil || !r.Unsupported {
		t.Fatalf("unsupported_request: %+v %v", r, err)
	}
}
