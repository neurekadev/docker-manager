package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/authztest"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/authz/policy"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/metrics"
	"github.com/neurekadev/docker-manager/internal/manager/observe"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// healthAgents answers host.health for env-1 (#143).
type healthAgents struct {
	mu        sync.Mutex
	clk       clock.Clock
	offline   bool
	refreshes []string
}

func (a *healthAgents) RequestEnvironment(_ context.Context, env, name string, input any, _ time.Duration) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if env != "env-1" || a.offline || name != protocol.ReqHostHealth {
		return nil, errors.New("offline")
	}
	a.refreshes = append(a.refreshes, input.(protocol.HostHealthInput).Refresh)
	now := a.clk.Now().UTC()
	passed, temp, pending := true, 41, int64(8)
	progress, finish := 17.3, int64(4686)
	return json.Marshal(protocol.HostHealthOutput{SampledAt: now,
		SMART: protocol.SMARTReport{Status: protocol.SMARTOK, CheckedAt: &now, ScannedAt: &now, Devices: []protocol.SMARTDevice{
			{Name: "/dev/sda", Type: "sat", Protocol: protocol.DiskATA, Model: "TOSHIBA MG08ACA16TE", Serial: "X0A0A0A0FVGG", SMARTSupported: true,
				Passed: &passed, TemperatureC: &temp, Pending: &pending, State: protocol.DiskWarning, ReadAt: &now},
		}},
		RAID: protocol.RAIDReport{ReadAt: now,
			MD: []protocol.MDArray{{Name: "md0", Level: "raid1", State: protocol.RAIDRebuilding, Devices: 2, Active: 1, Action: protocol.MDRecovery,
				Progress: &progress, FinishSeconds: &finish,
				Members: []protocol.MDMember{{Name: "sdc1", Slot: 2, State: protocol.MemberActive}, {Name: "sda1", Slot: 0, State: protocol.MemberActive}}}},
			ZFS: []protocol.ZFSPool{{Name: "tank", Health: "DEGRADED", State: protocol.RAIDDegraded}}},
	})
}

func (a *healthAgents) Online(env string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return env == "env-1" && !a.offline
}

type healthAPIFixture struct {
	clk    *clock.Fake
	agents *healthAgents
	svc    *fakeAgents
	h      http.Handler
}

func newHealthAPIFixture(t *testing.T, requests string) *healthAPIFixture {
	t.Helper()
	ctx := testutil.Context(t)
	clk := testutil.FakeClock()
	st, err := metrics.Open(ctx, metrics.Options{Path: filepath.Join(t.TempDir(), "metrics.db"), Clock: clk, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	agents := &healthAgents{clk: clk}
	obs := observe.New(observe.Options{Store: st, Agents: agents, Bus: events.New(clk), Clock: clk, Logger: testutil.Logger(t)})
	svc := newFakeAgents()
	a := svc.agents["ag-1"]
	a.Capabilities = strings.Replace(a.Capabilities, `"requests":[]`, `"requests":`+requests, 1)
	at := testutil.Epoch
	a.CapabilitiesAt = &at
	svc.agents["ag-1"] = a
	pol := authztest.New().Owner("olga").
		Member("sam", "system").Group("system", "allow environment.system.read @env:env-1").
		Member("mia", "metrics").Group("metrics", "allow environment.metrics.read @env:env-1").
		Member("rita", "restricted")
	pol.Locate(func(ref authz.ResourceRef) policy.Location {
		if ref.Type == catalog.TypeAgent {
			if a, ok := svc.agents[ref.ID]; ok {
				return policy.Location{Found: true, EnvironmentID: a.EnvironmentID}
			}
		}
		return policy.Location{}
	})
	mux := http.NewServeMux()
	New(mux, Deps{Agents: svc, Authorizer: pol, Clock: clk, Idempotency: &memIdempotency{}, Observe: obs})
	return &healthAPIFixture{clk: clk, agents: agents, svc: svc, h: authztest.Authenticate(withTestContext(t, mux, ""))}
}

func (f *healthAPIFixture) check(t *testing.T, user, scope string) authztest.Response {
	t.Helper()
	return authztest.Do(t, f.h, user, authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-1/disk-health/checks",
		Body: map[string]string{"scope": scope}})
}

func (f *healthAPIFixture) system(t *testing.T, user string) EnvironmentSystem {
	t.Helper()
	r := authztest.Do(t, f.h, user, authztest.Call{Method: http.MethodGet, Path: "/api/v1/environments/env-1/system"})
	if r.Status != http.StatusOK {
		t.Fatalf("system: %d %s", r.Status, r.Body)
	}
	var out EnvironmentSystem
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDiskHealthCheckRoute(t *testing.T) {
	f := newHealthAPIFixture(t, `["host.health"]`)

	// Before any report the sections say so.
	sys := f.system(t, "sam")
	if sys.DiskHealth == nil || sys.DiskHealth.Status != DiskHealthUnknown || sys.RAID == nil || sys.RAID.Status != DiskHealthUnknown {
		t.Fatalf("before a report: %+v %+v", sys.DiskHealth, sys.RAID)
	}

	r := f.check(t, "sam", "smart")
	if r.Status != http.StatusOK {
		t.Fatalf("check: %d %s", r.Status, r.Body)
	}
	var out DiskHealthCheck
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Scope != "smart" || out.DiskHealth.Status != "ok" || len(out.DiskHealth.Devices) != 1 || out.DiskHealth.Devices[0].State != "warning" ||
		*out.DiskHealth.Devices[0].PendingSectors != 8 || out.DiskHealth.Devices[0].Serial != "X0A0A0A0FVGG" {
		t.Fatalf("check answer %+v", out)
	}
	if len(out.RAID.Arrays) != 2 || out.RAID.Arrays[0].Kind != "md" || out.RAID.Arrays[0].State != "rebuilding" ||
		*out.RAID.Arrays[0].Progress != 17.3 || out.RAID.Arrays[1].Kind != "zfs" || out.RAID.Arrays[1].Health != "DEGRADED" {
		t.Fatalf("raid %+v", out.RAID)
	}

	// Too soon: 429 with Retry-After; RAID has its own limit.
	r = f.check(t, "sam", "smart")
	if r.Status != http.StatusTooManyRequests || r.Header.Get("Retry-After") != "30" {
		t.Fatalf("second check: %d %q %s", r.Status, r.Header.Get("Retry-After"), r.Body)
	}
	if r := f.check(t, "sam", "raid"); r.Status != http.StatusOK {
		t.Fatalf("raid check: %d %s", r.Status, r.Body)
	}
	f.agents.mu.Lock()
	refreshes := strings.Join(f.agents.refreshes, ",")
	f.agents.mu.Unlock()
	if refreshes != "smart,raid" {
		t.Fatalf("agent asked with %q", refreshes)
	}

	// The system information carries the stored report.
	sys = f.system(t, "sam")
	if sys.DiskHealth.Status != "ok" || len(sys.DiskHealth.Devices) != 1 || sys.DiskHealth.CheckedAt == nil || sys.RAID.Status != "ok" ||
		len(sys.RAID.Arrays) != 2 || len(sys.RAID.Arrays[0].Members) != 2 {
		t.Fatalf("system %+v %+v", sys.DiskHealth, sys.RAID)
	}

	// Permissions: a read capability of the system information checks;
	// metrics alone may not; a caller without access gets 404.
	if r := f.check(t, "mia", "raid"); r.Status != http.StatusForbidden {
		t.Errorf("mia: %d", r.Status)
	}
	if r := f.check(t, "rita", "raid"); r.Status != http.StatusNotFound {
		t.Errorf("rita: %d", r.Status)
	}
	if r := f.check(t, "sam", "selftest"); r.Status != http.StatusUnprocessableEntity {
		t.Errorf("unknown scope: %d %s", r.Status, r.Body)
	}

	// Offline.
	f.clk.Advance(time.Minute)
	f.agents.mu.Lock()
	f.agents.offline = true
	f.agents.mu.Unlock()
	r = f.check(t, "sam", "raid")
	if r.Status != http.StatusServiceUnavailable || !strings.Contains(string(r.Body), CodeEnvironmentOffline) {
		t.Fatalf("offline: %d %s", r.Status, r.Body)
	}
}

func TestDiskHealthOfAnOutdatedAgent(t *testing.T) {
	f := newHealthAPIFixture(t, `[]`)
	sys := f.system(t, "sam")
	if sys.DiskHealth.Status != DiskHealthAgentOutdated || sys.RAID.Status != DiskHealthAgentOutdated || len(sys.DiskHealth.Devices) != 0 {
		t.Fatalf("%+v %+v", sys.DiskHealth, sys.RAID)
	}
}

func TestHealthCheckErrors(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{&observe.HealthRateLimitError{RetryAfter: 1500 * time.Millisecond}, http.StatusTooManyRequests, CodeRateLimited},
		{observe.ErrHealthOffline, http.StatusServiceUnavailable, CodeEnvironmentOffline},
		{observe.ErrHealthUnsupported, http.StatusNotImplemented, CodeAgentUnsupported},
		{observe.ErrHealthTimeout, http.StatusGatewayTimeout, CodeTimeout},
		{observe.ErrHealthScope, http.StatusUnprocessableEntity, CodeValidationFailed},
	} {
		var e *Error
		if !errors.As(healthCheckErr(c.err, "NAS"), &e) || e.GetStatus() != c.status || e.Code != c.code {
			t.Errorf("%v: %+v", c.err, e)
		}
	}
	var e *Error
	if errors.As(healthCheckErr(&observe.HealthRateLimitError{RetryAfter: 1500 * time.Millisecond}, "NAS"), &e) &&
		e.GetHeaders().Get("Retry-After") != "2" {
		t.Errorf("Retry-After rounds up: %q", e.GetHeaders().Get("Retry-After"))
	}
}
