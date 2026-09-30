package alerts

import (
	"context"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/movelock"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func (f *fixture) offline() time.Time {
	f.t.Helper()
	next, err := f.svc.EvaluateOffline(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	return next
}

func TestOfflineAlertAfterTheGracePeriod(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	f.clk.Advance(time.Hour)
	went := f.clk.Now().UTC()
	f.setEnvironment("env-1", false, went, domain.EnvironmentActive)
	if next := f.offline(); !next.Equal(went.Add(DefaultOfflineGrace)) || len(f.firing()) != 0 {
		t.Fatalf("next %v, firing %+v", next, f.firing())
	}
	f.clk.Advance(DefaultOfflineGrace - time.Second)
	f.offline()
	if len(f.firing()) != 0 {
		t.Fatal("raised within the grace period")
	}
	f.clk.Advance(time.Second)
	f.offline()
	a := f.one()
	if a.Kind != domain.NotifyEnvironmentOffline || a.Severity != domain.AlertCritical || a.Title != "homelab is offline" ||
		a.Facts["since"] != went.Format(time.RFC3339) {
		t.Fatalf("%+v", a)
	}
	if got := f.dispatch(); len(got) != 1 || got[0].msg.Title != "[Docker Manager] homelab is offline" ||
		got[0].msg.URL != "https://docker.example.com/environments/env-1" {
		t.Fatalf("%+v", got)
	}
	// Evaluated again: nothing new.
	f.offline()
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	// Back online: resolved.
	f.setEnvironment("env-1", true, f.clk.Now().UTC(), domain.EnvironmentActive)
	f.offline()
	if len(f.firing()) != 0 {
		t.Fatal("still firing")
	}
	if got := f.dispatch(); len(got) != 1 || got[0].msg.Title != "[Docker Manager] Resolved: homelab is offline" {
		t.Fatalf("%+v", got)
	}
}

func TestShortOutageIsNeverSent(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	f.clk.Advance(time.Hour)
	f.setEnvironment("env-1", false, f.clk.Now().UTC(), domain.EnvironmentActive)
	f.clk.Advance(DefaultOfflineGrace)
	f.offline()
	a := f.one()
	// It reconnects before the message went out: neither the alert nor
	// its resolution is sent.
	f.setEnvironment("env-1", true, f.clk.Now().UTC(), domain.EnvironmentActive)
	f.offline()
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	for _, d := range f.deliveries(a.ID) {
		if d.State != domain.DeliveryDropped {
			t.Fatalf("%+v", d)
		}
	}
}

// At start every environment is marked offline (no session survives a
// restart): the grace runs from the manager's start, so a restart with
// long-offline environments raises nothing at once.
func TestNoAlertStormAtStartup(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	longAgo := f.clk.Now().Add(-30 * 24 * time.Hour).UTC()
	f.setEnvironment("env-1", false, longAgo, domain.EnvironmentActive)
	f.setEnvironment("env-2", false, f.clk.Now().UTC(), domain.EnvironmentActive)
	next := f.offline()
	if len(f.firing()) != 0 {
		t.Fatalf("alerts at startup: %+v", f.firing())
	}
	if want := f.svc.startedAt.Add(DefaultOfflineGrace); !next.Equal(want) {
		t.Fatalf("next %v, want %v", next, want)
	}
	f.clk.Advance(DefaultOfflineGrace)
	f.offline()
	if as := f.firing(); len(as) != 2 {
		t.Fatalf("%+v", as)
	}
	// They go out as one digest.
	if got := f.dispatch(); len(got) != 1 || got[0].msg.Title != "[Docker Manager] 2 alerts" || got[0].msg.URL != "https://docker.example.com/alerts" {
		t.Fatalf("%+v", got)
	}
}

func TestOfflineGraceIsConfigurable(t *testing.T) {
	f := newFixture(t)
	svc, err := New(Options{DB: f.db, Bus: f.bus, Clock: f.clk, MoveLock: f.lock, OfflineGrace: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	f.setEnvironment("env-1", false, f.clk.Now().UTC(), domain.EnvironmentActive)
	f.clk.Advance(29 * time.Minute)
	if _, err := svc.EvaluateOffline(f.ctx); err != nil || len(f.firing()) != 0 {
		t.Fatalf("%v %+v", err, f.firing())
	}
	f.clk.Advance(time.Minute)
	if _, err := svc.EvaluateOffline(f.ctx); err != nil || len(f.firing()) != 1 {
		t.Fatalf("%v %+v", err, f.firing())
	}
}

func TestNothingIsEvaluatedWhileTheManagerMoves(t *testing.T) {
	f := newFixture(t)
	f.clk.Advance(time.Hour)
	f.lock.Set(movelock.AgentsRefused)
	f.setEnvironment("env-1", false, f.clk.Now().UTC(), domain.EnvironmentActive)
	f.clk.Advance(time.Hour)
	f.offline()
	f.svc.reconcile(f.ctx)
	if len(f.firing()) != 0 {
		t.Fatal("an offline alert while the manager moves")
	}
	f.lock.Set(movelock.Open)
	f.offline()
	if len(f.firing()) != 1 {
		t.Fatal("no alert after the move ended")
	}
}

func TestArchivingEndsAlertsSilently(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	f.clk.Advance(time.Hour)
	f.setEnvironment("env-1", false, f.clk.Now().UTC(), domain.EnvironmentActive)
	f.clk.Advance(DefaultOfflineGrace)
	f.offline()
	f.dispatch()
	f.setEnvironment("env-1", false, f.clk.Now().UTC(), domain.EnvironmentArchived)
	f.offline()
	if len(f.firing()) != 0 {
		t.Fatal("still firing")
	}
	res, err := f.svc.List(f.ctx, domain.AlertFilter{State: domain.AlertListResolved}, "", 0)
	if err != nil || len(res) != 1 || res[0].Resolution != domain.AlertResolvedArchived {
		t.Fatalf("%+v %v", res, err)
	}
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("archiving was announced: %+v", got)
	}
}

// The reconcile loop repairs what the bus missed: an environment that
// went offline without an event still raises its alert on the next tick,
// and the alert is announced on the bus.
func TestReconcileLoopCatchesMissedEvents(t *testing.T) {
	f := newFixture(t)
	sub := f.bus.Subscribe(16, func(e events.Event) bool { return e.Type == events.AlertUpdated })
	defer sub.Close()
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.svc.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()
	// The reconcile and dispatch loops wait on their tickers.
	if err := f.clk.BlockUntilWaiters(f.ctx, 2); err != nil {
		t.Fatal(err)
	}
	f.setEnvironment("env-1", false, f.clk.Now().UTC(), domain.EnvironmentActive)
	f.clk.Advance(ReconcileInterval)
	f.clk.Advance(DefaultOfflineGrace)
	wait := testutil.ContextWithin(t, 10*time.Second)
	for {
		select {
		case e := <-sub.C():
			if e.Alert == nil || e.Alert.Kind != domain.NotifyEnvironmentOffline || e.ResourceID != e.Alert.ID {
				t.Fatalf("%+v", e)
			}
			return
		case <-wait.Done():
			t.Fatal("the reconcile loop raised no alert")
		}
	}
}
