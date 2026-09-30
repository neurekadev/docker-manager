package alerts

import (
	"errors"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

func (f *fixture) user(id, name string) {
	f.t.Helper()
	group, err := store.DefaultGroupID(f.ctx, f.db)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := store.CreateUser(f.ctx, f.db, domain.NewUser{ID: id, Username: id, DisplayName: name, GroupID: group,
		WebAuthnHandle: []byte("h-" + id), CreatedAt: f.clk.Now().UTC()}); err != nil {
		f.t.Fatal(err)
	}
}

func TestDismissIsInstanceWideAndReopensWhenWorse(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	f.user("u-alex", "Alex")
	warn := disk(protocol.DiskWarning)
	warn.Reallocated = i64(8)
	f.health.set("env-1", []protocol.SMARTDevice{warn}, nil, nil)
	f.evaluate("env-1")
	f.dispatch()
	a := f.one()
	sub := f.bus.Subscribe(8, func(e events.Event) bool { return e.Type == events.AlertUpdated })
	defer sub.Close()

	f.clk.Advance(time.Minute)
	d, err := f.svc.Dismiss(f.ctx, a.ID, "u-alex")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Dismissed() || d.DismissedBy != "u-alex" || d.DismissedByName != "Alex" || !d.DismissedAt.Equal(f.clk.Now().UTC()) ||
		d.Revision != a.Revision+1 || d.State != domain.AlertFiring {
		t.Fatalf("%+v", d)
	}
	if e := <-sub.C(); e.ResourceID != a.ID || e.Alert == nil || !e.Alert.Dismissed() {
		t.Fatalf("%+v", e)
	}
	// Listed as dismissed, not active.
	active, _ := f.svc.List(f.ctx, domain.AlertFilter{State: domain.AlertListActive}, "", 0)
	dismissed, _ := f.svc.List(f.ctx, domain.AlertFilter{State: domain.AlertListDismissed}, "", 0)
	if len(active) != 0 || len(dismissed) != 1 {
		t.Fatalf("active %d, dismissed %d", len(active), len(dismissed))
	}
	// Dismissing again changes nothing.
	again, err := f.svc.Dismiss(f.ctx, a.ID, "u-alex")
	if err != nil || again.Revision != d.Revision {
		t.Fatalf("%+v %v", again, err)
	}
	// More of the same keeps it dismissed and quiet.
	warn.Reallocated = i64(9)
	f.health.set("env-1", []protocol.SMARTDevice{warn}, nil, nil)
	f.evaluate("env-1")
	if b := f.one(); !b.Dismissed() {
		t.Fatalf("%+v", b)
	}
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	// Worse: re-opened for everyone and sent again.
	failing := warn
	failing.State, failing.Passed = protocol.DiskFailing, boolp(false)
	f.health.set("env-1", []protocol.SMARTDevice{failing}, nil, nil)
	f.evaluate("env-1")
	if b := f.one(); b.Dismissed() || b.DismissedBy != "" || b.Severity != domain.AlertCritical {
		t.Fatalf("%+v", b)
	}
	if got := f.dispatch(); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestResolvedAlertsCannotBeDismissed(t *testing.T) {
	f := newFixture(t)
	f.failDisk("env-1", "/dev/sda")
	a := f.one()
	f.health.set("env-1", []protocol.SMARTDevice{disk(protocol.DiskOK)}, nil, nil)
	f.evaluate("env-1")
	if _, err := f.svc.Dismiss(f.ctx, a.ID, "u-x"); !errors.Is(err, domain.ErrAlertNotFiring) {
		t.Fatalf("%v", err)
	}
	if _, err := f.svc.Dismiss(f.ctx, "nope", "u-x"); !errors.Is(err, domain.ErrAlertNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestDismissManySkipsResolvedAndUnknown(t *testing.T) {
	f := newFixture(t)
	f.failDisk("env-1", "/dev/sda")
	f.failDisk("env-2", "/dev/sdb")
	as := f.firing()
	// env-2's disk (/dev/sdb) is healthy again: its alert resolves.
	ok := disk(protocol.DiskOK)
	ok.Name = "/dev/sdb"
	f.health.set("env-2", []protocol.SMARTDevice{ok}, nil, nil)
	f.evaluate("env-2")
	out, err := f.svc.DismissMany(f.ctx, []string{as[0].ID, as[1].ID, "nope"}, "u-x", false)
	if err != nil || len(out) != 1 || out[0].ID != as[0].ID || !out[0].Dismissed() {
		t.Fatalf("%+v %v", out, err)
	}
}
