package alerts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// A message says what happened when it was written: a later change of the
// alert (another failed run of the same job) does not rewrite a message
// still waiting, nor one retried later.
func TestQueuedMessagesSayWhatHappenedThen(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	f.sender.fail(domain.NotifyErrTimeout)
	first := backupJob(domain.JobFailed, domain.OriginScheduled)
	f.finish(first)
	f.dispatch() // fails, waits for a retry
	second := backupJob(domain.JobPartial, domain.OriginScheduled)
	second.Targets = first.Targets
	f.finish(second)
	if a := f.one(); a.ResourceID != second.ID || a.Title != "Backup verification of silo_data partly failed" {
		t.Fatalf("%+v", a)
	}
	f.sender.succeed()
	f.clk.Advance(RetryMin)
	if _, err := f.svc.Dispatch(f.ctx); err != nil {
		t.Fatal(err)
	}
	got := f.sender.take()
	if len(got) != 1 || got[0].msg.URL != "https://docker.example.com/jobs/"+first.ID ||
		got[0].msg.Title != "Backup verification of silo_data failed" {
		t.Fatalf("%+v", got)
	}
}

// The dispatcher reads a bounded batch of a channel; what is left goes
// out right after.
func TestLargeBurstsGoOutInBatches(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	var devs []protocol.SMARTDevice
	for i := range DispatchBatch + 50 {
		d := disk(protocol.DiskFailing)
		d.Name, d.Passed = fmt.Sprintf("/dev/disk%03d", i), boolp(false)
		devs = append(devs, d)
	}
	f.health.set("env-1", devs, nil, nil)
	f.evaluate("env-1")
	f.clk.Advance(DeliveryDelay)
	next, err := f.svc.Dispatch(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := f.sender.take()
	if len(got) != 1 || got[0].msg.Title != fmt.Sprintf("%d alerts", DispatchBatch) {
		t.Fatalf("%+v", got)
	}
	if !next.Equal(f.svc.now()) {
		t.Fatalf("the rest waits: next %v, now %v", next, f.svc.now())
	}
	if next, err = f.svc.Dispatch(f.ctx); err != nil || !next.IsZero() {
		t.Fatalf("%v %v", next, err)
	}
	if got := f.sender.take(); len(got) != 1 || got[0].msg.Title != "50 alerts" {
		t.Fatalf("%+v", got)
	}
}

// An improvement never looks like a new problem: fewer missing disks, a
// pool less broken, an attribute failing in the past rather than now, a
// job partly failing after it failed.
func TestImprovementsNeitherReopenNorSend(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	quiet := func(what string) {
		t.Helper()
		if got := f.dispatch(); len(got) != 0 {
			t.Fatalf("%s was sent: %+v", what, got)
		}
		for _, a := range f.firing() {
			if !a.Dismissed() {
				t.Fatalf("%s re-opened %s", what, a.Title)
			}
		}
	}
	dismissAll := func() {
		t.Helper()
		f.dispatch()
		var ids []string
		for _, a := range f.firing() {
			ids = append(ids, a.ID)
		}
		if _, err := f.svc.DismissMany(f.ctx, ids, "u-x", false); err != nil {
			t.Fatal(err)
		}
	}
	arr := protocol.MDArray{Name: "md0", Level: "raid6", State: protocol.RAIDDegraded, Devices: 4, Active: 2}
	f.health.set("env-1", nil, []protocol.MDArray{arr}, nil)
	f.evaluate("env-1")
	dismissAll()
	arr.Active = 3 // one disk back
	f.health.set("env-1", nil, []protocol.MDArray{arr}, nil)
	f.evaluate("env-1")
	quiet("a disk coming back")
	arr.Active = 2 // lost again: worse
	f.health.set("env-1", nil, []protocol.MDArray{arr}, nil)
	f.evaluate("env-1")
	if a := f.one(); a.Dismissed() {
		t.Fatal("another missing disk did not re-open the alert")
	}
	if got := f.dispatch(); len(got) != 1 {
		t.Fatalf("%+v", got)
	}

	g := newFixture(t)
	f = g
	f.channel("ops", nil, true, nil, true)
	f.health.set("env-1", nil, nil, []protocol.ZFSPool{{Name: "tank", Health: "FAULTED", State: protocol.RAIDFailed}})
	f.evaluate("env-1")
	failing := disk(protocol.DiskFailing)
	failing.FailingAttributes = []protocol.SMARTAttribute{{ID: 5, Name: "Reallocated_Sector_Ct", WhenFailed: "now"}}
	f.health.set("env-1", []protocol.SMARTDevice{failing}, nil, []protocol.ZFSPool{{Name: "tank", Health: "FAULTED", State: protocol.RAIDFailed}})
	f.evaluate("env-1")
	f.finish(backupJob(domain.JobFailed, domain.OriginScheduled))
	dismissAll()
	past := disk(protocol.DiskWarning)
	past.FailingAttributes = []protocol.SMARTAttribute{{ID: 5, Name: "Reallocated_Sector_Ct", WhenFailed: "past"}}
	f.health.set("env-1", []protocol.SMARTDevice{past}, nil, []protocol.ZFSPool{{Name: "tank", Health: "DEGRADED", State: protocol.RAIDDegraded}})
	f.evaluate("env-1")
	f.finish(backupJob(domain.JobPartial, domain.OriginScheduled))
	quiet("a smaller problem")
	if as := f.firing(); len(as) != 3 {
		t.Fatalf("%+v", as)
	}
}

// Alerts changed in a job's transaction are announced as the database has
// them: a transaction that rolled back announces nothing, even when its
// job is reported changed or the held alert is flushed later.
func TestRolledBackJobAlertsAreNeverAnnounced(t *testing.T) {
	f := newFixture(t)
	sub := f.bus.Subscribe(8, func(e events.Event) bool { return e.Type == events.AlertUpdated })
	defer sub.Close()
	j := backupJob(domain.JobFailed, domain.OriginScheduled)
	boom := errors.New("a later finish hook failed")
	err := f.db.RunInTx(f.ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		for _, h := range f.hooks.finish[j.Kind] {
			if err := h(ctx, tx, j); err != nil {
				return err
			}
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if len(f.firing()) != 0 {
		t.Fatal("the rolled back alert is stored")
	}
	f.svc.jobsChanged([]string{j.ID})
	f.svc.announceReady(f.ctx)
	j2 := backupJob(domain.JobFailed, domain.OriginScheduled)
	j2.ID = ids.New()
	_ = f.db.RunInTx(f.ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		for _, h := range f.hooks.finish[j2.Kind] {
			if err := h(ctx, tx, j2); err != nil {
				return err
			}
		}
		return boom
	})
	f.clk.Advance(2 * ReconcileInterval)
	f.svc.flushHeld(f.ctx)
	select {
	case e := <-sub.C():
		t.Fatalf("a rolled back alert was announced: %+v", e)
	default:
	}
	// A committed one is announced with the stored revision.
	f.finish(backupJob(domain.JobFailed, domain.OriginScheduled))
	select {
	case e := <-sub.C():
		if a := f.one(); e.ResourceID != a.ID || e.Revision != a.Revision {
			t.Fatalf("%+v", e)
		}
	default:
		t.Fatal("the committed alert was not announced")
	}
}

// An alert whose read-back fails (a busy database; here a cancelled
// context) stays ready and is announced on the next try, never lost.
func TestAFailedReadIsAnnouncedLater(t *testing.T) {
	f := newFixture(t)
	sub := f.bus.Subscribe(8, func(e events.Event) bool { return e.Type == events.AlertUpdated })
	defer sub.Close()
	j := backupJob(domain.JobFailed, domain.OriginScheduled)
	if err := f.db.RunInTx(f.ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		for _, h := range f.hooks.finish[j.Kind] {
			if err := h(ctx, tx, j); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, fn := range f.hooks.change {
		fn([]string{j.ID})
	}
	gone, cancel := context.WithCancel(f.ctx)
	cancel()
	f.svc.announceReady(gone)
	select {
	case e := <-sub.C():
		t.Fatalf("announced without reading it: %+v", e)
	default:
	}
	f.svc.announceReady(f.ctx)
	select {
	case e := <-sub.C():
		if a := f.one(); e.ResourceID != a.ID {
			t.Fatalf("%+v", e)
		}
	default:
		t.Fatal("the alert was lost after a failed read")
	}
}

// Every time an alert gets worse its escalation counts up (a browser keys
// its local dismissal by it); quiet changes leave it.
func TestEscalationCountsWhatGotWorse(t *testing.T) {
	f := newFixture(t)
	warn := disk(protocol.DiskWarning)
	warn.Reallocated = i64(8)
	f.health.set("env-1", []protocol.SMARTDevice{warn}, nil, nil)
	f.evaluate("env-1")
	if a := f.one(); a.Escalation != 0 {
		t.Fatalf("%+v", a)
	}
	warn.Reallocated = i64(9)
	f.health.set("env-1", []protocol.SMARTDevice{warn}, nil, nil)
	f.evaluate("env-1")
	if a := f.one(); a.Escalation != 0 {
		t.Fatalf("a quiet change escalated: %+v", a)
	}
	warn.Pending = i64(1) // same severity, a new problem
	f.health.set("env-1", []protocol.SMARTDevice{warn}, nil, nil)
	f.evaluate("env-1")
	if a := f.one(); a.Escalation != 1 || a.Severity != domain.AlertWarning {
		t.Fatalf("%+v", a)
	}
	stored, err := store.GetAlert(f.ctx, f.db, f.one().ID)
	if err != nil || stored.Escalation != 1 || !strings.Contains(stored.Fingerprint, "pending") {
		t.Fatalf("%+v %v", stored, err)
	}
}
