package alerts

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/movelock"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// failDisk raises a critical disk alert on env.
func (f *fixture) failDisk(env, name string) {
	f.t.Helper()
	d := disk(protocol.DiskFailing)
	d.Name, d.Passed = name, boolp(false)
	f.health.set(env, []protocol.SMARTDevice{d}, nil, nil)
	f.evaluate(env)
}

func channelsOf(msgs []sent) map[string]int {
	out := map[string]int{}
	for _, m := range msgs {
		out[m.channel]++
	}
	return out
}

func TestChannelSubscriptionsPickTheChannels(t *testing.T) {
	f := newFixture(t)
	all := f.channel("all", nil, true, nil, true)
	onlyEnv1 := f.channel("env-1 only", nil, false, []string{"env-1"}, true)
	onlyEnv2 := f.channel("env-2 only", nil, false, []string{"env-2"}, true)
	jobsOnly := f.channel("jobs only", []domain.NotificationEventKind{domain.NotifyJobFailed}, true, nil, true)
	quiet := f.channel("no resolved", nil, true, nil, false)
	f.failDisk("env-1", "/dev/sda")
	got := channelsOf(f.dispatch())
	if got[all.ID] != 1 || got[onlyEnv1.ID] != 1 || got[quiet.ID] != 1 || got[onlyEnv2.ID] != 0 || got[jobsOnly.ID] != 0 {
		t.Fatalf("firing: %+v", got)
	}
	f.health.set("env-1", []protocol.SMARTDevice{disk(protocol.DiskOK)}, nil, nil)
	f.evaluate("env-1")
	got = channelsOf(f.dispatch())
	if got[all.ID] != 1 || got[onlyEnv1.ID] != 1 || got[quiet.ID] != 0 || got[onlyEnv2.ID] != 0 || got[jobsOnly.ID] != 0 {
		t.Fatalf("resolved: %+v", got)
	}
	// A manager job (no environment) reaches every channel of the kind,
	// restricted ones too.
	j := backupJob(domain.JobFailed, domain.OriginScheduled)
	j.EnvironmentID = ""
	f.finish(j)
	got = channelsOf(f.dispatch())
	if got[jobsOnly.ID] != 1 || got[all.ID] != 1 || got[onlyEnv2.ID] != 1 {
		t.Fatalf("job: %+v", got)
	}
}

func TestFailedSendsBackOffAndGiveUp(t *testing.T) {
	f := newFixture(t)
	ch := f.channel("ops", nil, true, nil, true)
	f.sender.fail(domain.NotifyErrHTTP5xx)
	f.failDisk("env-1", "/dev/sda")
	f.clk.Advance(DeliveryDelay)
	var waits []time.Duration
	for range 9 {
		next, err := f.svc.Dispatch(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if next.IsZero() {
			t.Fatal("no retry scheduled")
		}
		waits = append(waits, next.Sub(f.clk.Now()))
		f.clk.Set(next)
	}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute,
		32 * time.Minute, time.Hour, time.Hour}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("waits %v, want %v", waits, want)
		}
	}
	ds := f.pending()
	if len(ds) != 1 || ds[0].ChannelID != ch.ID || ds[0].Attempts != 9 || ds[0].LastError != domain.NotifyErrHTTP5xx {
		t.Fatalf("%+v", ds)
	}
	// A day after it was written, the next failure gives up.
	f.clk.Set(ds[0].CreatedAt.Add(GiveUpAfter))
	if next, err := f.svc.Dispatch(f.ctx); err != nil || !next.IsZero() {
		t.Fatalf("%v %v", next, err)
	}
	if len(f.pending()) != 0 {
		t.Fatal("still pending")
	}
	a := f.one()
	if ds := f.deliveries(a.ID); len(ds) != 1 || ds[0].State != domain.DeliveryFailed {
		t.Fatalf("%+v", ds)
	}
	// A send that errors before reaching the service is retried too.
	f.sender.mu.Lock()
	f.sender.err = errors.New("database is locked")
	f.sender.mu.Unlock()
	f.health.set("env-1", []protocol.SMARTDevice{disk(protocol.DiskOK)}, nil, nil)
	f.evaluate("env-1")
	f.clk.Advance(DeliveryDelay)
	if next, err := f.svc.Dispatch(f.ctx); err != nil || next.IsZero() {
		t.Fatalf("%v %v", next, err)
	}
	if ds := f.pending(); len(ds) != 1 || ds[0].LastError != errClassInternal {
		t.Fatalf("%+v", ds)
	}
}

func TestAChannelKeepsItsOrderWhileRetrying(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	f.sender.fail(domain.NotifyErrTimeout)
	f.failDisk("env-1", "/dev/sda")
	f.clk.Advance(DeliveryDelay)
	if _, err := f.svc.Dispatch(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.sender.take()
	// A second alert is due before the first one's retry: it waits.
	f.failDisk("env-2", "/dev/sdb")
	f.clk.Advance(DeliveryDelay)
	if _, err := f.svc.Dispatch(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.sender.take(); len(got) != 0 {
		t.Fatalf("sent out of order: %+v", got)
	}
	// At the retry both go out together, the older first.
	f.sender.succeed()
	f.clk.Advance(RetryMin)
	if _, err := f.svc.Dispatch(f.ctx); err != nil {
		t.Fatal(err)
	}
	got := f.sender.take()
	if len(got) != 1 || got[0].msg.Title != "[Docker Manager] 2 alerts" {
		t.Fatalf("%+v", got)
	}
	lines := strings.Split(got[0].msg.Body, "\n")
	if len(lines) != 2 || lines[0] != "• Critical: Disk /dev/sda on homelab is failing" || lines[1] != "• Critical: Disk /dev/sdb on office is failing" {
		t.Fatalf("%q", got[0].msg.Body)
	}
	if len(f.pending()) != 0 {
		t.Fatal("still pending")
	}
}

func TestOnlyDueMessagesGoOut(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	f.failDisk("env-1", "/dev/sda")
	f.clk.Advance(DeliveryDelay / 2)
	f.failDisk("env-2", "/dev/sdb")
	// The first message is due; the second one's delay has not passed.
	f.clk.Advance(DeliveryDelay / 2)
	if _, err := f.svc.Dispatch(f.ctx); err != nil {
		t.Fatal(err)
	}
	got := f.sender.take()
	if len(got) != 1 || got[0].msg.Title != "[Docker Manager] Disk /dev/sda on homelab is failing" {
		t.Fatalf("%+v", got)
	}
	if len(f.pending()) != 1 {
		t.Fatalf("pending %d, want the second message", len(f.pending()))
	}
	f.clk.Advance(DeliveryDelay / 2)
	if _, err := f.svc.Dispatch(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.sender.take(); len(got) != 1 || got[0].msg.Title != "[Docker Manager] Disk /dev/sdb on office is failing" {
		t.Fatalf("%+v", got)
	}
}

func TestDeliveryPausesWhileTheManagerMoves(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	f.failDisk("env-1", "/dev/sda")
	f.lock.Set(movelock.ReadOnly)
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("sent while moving: %+v", got)
	}
	if len(f.pending()) != 1 {
		t.Fatal("the message was not kept")
	}
	f.lock.Set(movelock.Open)
	if got := f.dispatch(); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestMessagesOfGoneOrDisabledChannelsAreDropped(t *testing.T) {
	f := newFixture(t)
	off := f.channel("off", nil, true, nil, true)
	gone := f.channel("gone", nil, true, nil, true)
	narrowed := f.channel("narrowed", nil, true, nil, true)
	f.failDisk("env-1", "/dev/sda")
	a := f.one()
	// After the alert fired: one channel is turned off, one deleted, one
	// no longer subscribed to disk health.
	c, _ := store.GetNotificationChannel(f.ctx, f.db, off.ID)
	c.Enabled, c.Revision = false, c.Revision+1
	if err := store.UpdateNotificationChannel(f.ctx, f.db, &c, "", c.Revision-1); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteNotificationChannel(f.ctx, f.db, gone.ID, gone.Revision); err != nil {
		t.Fatal(err)
	}
	n, _ := store.GetNotificationChannel(f.ctx, f.db, narrowed.ID)
	n.EventKinds, n.Revision = []domain.NotificationEventKind{domain.NotifyRAID}, n.Revision+1
	if err := store.UpdateNotificationChannel(f.ctx, f.db, &n, "", n.Revision-1); err != nil {
		t.Fatal(err)
	}
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	ds := f.deliveries(a.ID)
	if len(ds) != 3 {
		t.Fatalf("%+v", ds)
	}
	for _, d := range ds {
		if d.State != domain.DeliveryDropped {
			t.Fatalf("%+v", d)
		}
	}
	// A channel deleted while the send runs is dropped too.
	live := f.channel("live", nil, true, nil, true)
	f.sender.mu.Lock()
	f.sender.err = domain.ErrNotificationChannelNotFound
	f.sender.mu.Unlock()
	f.failDisk("env-2", "/dev/sdb")
	f.dispatch()
	for _, d := range f.pending() {
		if d.ChannelID == live.ID {
			t.Fatalf("%+v", d)
		}
	}
}

func TestDigestIsBoundedAndPurgeKeepsRecentHistory(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	var devs []protocol.SMARTDevice
	for i := range DigestMaxLines + 5 {
		d := disk(protocol.DiskFailing)
		d.Name, d.Passed = "/dev/sd"+string(rune('a'+i)), boolp(false)
		devs = append(devs, d)
	}
	f.health.set("env-1", devs, nil, nil)
	f.evaluate("env-1")
	got := f.dispatch()
	if len(got) != 1 || got[0].msg.Title != "[Docker Manager] 25 alerts" {
		t.Fatalf("%+v", got)
	}
	lines := strings.Split(got[0].msg.Body, "\n")
	if len(lines) != DigestMaxLines+1 || lines[DigestMaxLines] != "…and 5 more." {
		t.Fatalf("%d lines, last %q", len(lines), lines[len(lines)-1])
	}
	// Resolve them, then let time pass: finished deliveries go after 7
	// days, resolved alerts after 90.
	f.health.set("env-1", nil, nil, nil)
	f.clk.Advance(DiskRemovedAfter)
	f.evaluate("env-1")
	f.clk.Advance(DeliveryRetention)
	if err := f.svc.Purge(f.ctx); err != nil {
		t.Fatal(err)
	}
	res, _ := f.svc.List(f.ctx, domain.AlertFilter{State: domain.AlertListResolved}, "", 0)
	if len(res) != len(devs) || len(f.deliveries(res[0].ID)) != 0 {
		t.Fatalf("%d resolved, %d deliveries", len(res), len(f.deliveries(res[0].ID)))
	}
	f.clk.Advance(AlertRetention)
	if err := f.svc.Purge(f.ctx); err != nil {
		t.Fatal(err)
	}
	if res, _ := f.svc.List(f.ctx, domain.AlertFilter{}, "", 0); len(res) != 0 {
		t.Fatalf("%d alerts kept", len(res))
	}
}

// Messages never carry serial numbers, job error texts or anything a
// channel's address holds; neither do the stored alerts or the logs.
func TestMessagesCarryNoSecretsSerialsOrJobErrors(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	d := disk(protocol.DiskWarning)
	d.Serial, d.Reallocated = "SERIAL-CANARY-4711", i64(3)
	f.health.set("env-1", []protocol.SMARTDevice{d}, nil, nil)
	f.evaluate("env-1")
	j := backupJob(domain.JobFailed, domain.OriginScheduled)
	j.ErrorMessage = "restic: wrong password JOB-ERROR-CANARY"
	j.Recovery = "Check the repository password RECOVERY-CANARY"
	f.finish(j)
	msgs := f.dispatch()
	if len(msgs) != 1 {
		t.Fatalf("%+v", msgs)
	}
	stored, err := f.svc.List(f.ctx, domain.AlertFilter{}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(struct {
		Msgs   []sent
		Alerts []domain.Alert
		Detail []string
	}{msgs, stored, []string{Detail(stored[0]), Detail(stored[1])}})
	all := string(blob) + f.logs.String() + msgs[0].msg.Title + msgs[0].msg.Body
	for _, canary := range []string{"SERIAL-CANARY-4711", "JOB-ERROR-CANARY", "RECOVERY-CANARY", "sealed-ops"} {
		if strings.Contains(all, canary) {
			t.Errorf("%s leaked", canary)
		}
	}
}
