package alerts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// channelWith stores a channel for every environment with these
// subscriptions.
func (f *fixture) channelWith(name string, subs domain.NotificationSubscriptions) domain.NotificationChannel {
	f.t.Helper()
	now := f.clk.Now().UTC()
	c := domain.NotificationChannel{ID: ids.New(), Name: name, Service: "generic", Enabled: true, Subscriptions: subs,
		AllEnvironments: true, AddressFingerprint: "fp_test", AddressVersion: 1, AddressUpdatedAt: now, Revision: 1,
		CreatedAt: now, UpdatedAt: now}
	if err := store.InsertNotificationChannel(f.ctx, f.db, &c, "sealed-"+name); err != nil {
		f.t.Fatal(err)
	}
	return c
}

// notifications returns the stored notifications, newest first.
func (f *fixture) notifications() []domain.Notification {
	f.t.Helper()
	ns, err := store.ListNotifications(f.ctx, f.db, domain.NotificationFilter{}, "", 0)
	if err != nil {
		f.t.Fatal(err)
	}
	return ns
}

func output(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// run is a finished job of kind on env-1 that started three minutes
// before it ended.
func (f *fixture) run(kind domain.JobKind, state domain.JobState, origin domain.JobOrigin) domain.Job {
	end := f.clk.Now().UTC()
	start := end.Add(-3*time.Minute - 12*time.Second)
	return domain.Job{ID: ids.New(), Kind: kind, Origin: origin, EnvironmentID: "env-1", State: state, StartedAt: &start,
		FinishedAt: &end}
}

func TestAPruneReportsWhatItReclaimedPerKindOfObject(t *testing.T) {
	f := newFixture(t)
	all := f.channel("all", nil, true, nil, true)
	failures := f.channelWith("failures", domain.NotificationSubscriptions{domain.NotifyPrune: {domain.OutcomeFailure}})
	sub := f.bus.Subscribe(8, func(e events.Event) bool { return e.Type == events.NotificationCreated })
	defer sub.Close()
	j := f.run("prune.run", domain.JobSucceeded, domain.OriginManual)
	j.ResultOutput = output(t, protocol.PruneRunOutput{Removed: 5, Skipped: 1, BytesReclaimed: 3 << 30, Items: []protocol.PruneRunItem{
		{Category: protocol.PruneStoppedContainers, Status: protocol.PruneItemRemoved, Bytes: 100 << 20},
		{Category: protocol.PruneStoppedContainers, Status: protocol.PruneItemRemoved, Bytes: 28 << 20},
		{Category: protocol.PruneUnusedImages, Status: protocol.PruneItemRemoved, Bytes: 2 << 30},
		{Category: protocol.PruneDanglingImages, Status: protocol.PruneItemSkipped, Bytes: 1 << 30},
		{Category: protocol.PruneUnusedNetworks, Status: protocol.PruneItemRemoved},
		{Category: protocol.PruneBuildCache, Status: protocol.PruneItemRemoved, Bytes: (1 << 30) - (128 << 20)},
	}})
	f.finish(j)
	ns := f.notifications()
	if len(ns) != 1 {
		t.Fatalf("%+v", ns)
	}
	n := ns[0]
	if n.Kind != domain.NotifyPrune || n.Outcome != domain.OutcomeSuccess || n.Title != "Prune on homelab reclaimed 3 GiB" ||
		n.JobID != j.ID || n.Facts["containersRemoved"] != "2" || n.Facts["containersBytes"] != "134217728" ||
		n.Facts["imagesRemoved"] != "1" || n.Facts["networksRemoved"] != "1" || n.Facts["buildCacheBytes"] != "939524096" {
		t.Fatalf("%+v", n)
	}
	// Announced once the job's transaction committed.
	select {
	case e := <-sub.C():
		if e.ResourceID != n.ID || e.Notification == nil {
			t.Fatalf("%+v", e)
		}
	default:
		t.Fatal("the notification was not announced")
	}
	// Only the channel that wants a prune's success gets it, with the
	// breakdown as fields.
	got := f.dispatch()
	if len(got) != 1 || got[0].channel != all.ID {
		t.Fatalf("%+v (failures channel %s)", got, failures.ID)
	}
	m := got[0].msg
	if m.Title != "[Docker Manager] Prune on homelab reclaimed 3 GiB" || m.Tone != domain.ToneSuccess ||
		m.Body != "Removed 5 objects and reclaimed 3 GiB." || m.URL != "https://docker.example.com/jobs/"+j.ID {
		t.Fatalf("%+v", m)
	}
	for name, want := range map[string]string{"Environment": "homelab", "Reclaimed": "3 GiB", "Containers": "2 containers · 128 MiB",
		"Images": "1 image · 2 GiB", "Networks": "1 network", "Build cache": "1 entry · 896 MiB", "Skipped": "1",
		"Duration": "3 min 12 s", "Started by": "A user"} {
		if v, _ := field(m.Fields, name); v != want {
			t.Errorf("%s: %q, want %q", name, v, want)
		}
	}
	if _, ok := field(m.Fields, "Volumes"); ok {
		t.Error("a kind of object nothing was removed of is listed")
	}
	// A prune that removed nothing says so.
	empty := f.run("prune.run", domain.JobSucceeded, domain.OriginScheduled)
	empty.ResultOutput = output(t, protocol.PruneRunOutput{})
	f.finish(empty)
	if n := f.notifications()[0]; n.Title != "Prune on homelab found nothing to remove" || NotificationDetail(n) != "Nothing was unused." {
		t.Fatalf("%+v", n)
	}
}

func TestAFailedBackupIsExplainedAndSentOnce(t *testing.T) {
	f := newFixture(t)
	all := f.channel("all", nil, true, nil, true)
	failures := f.channelWith("failures", domain.NotificationSubscriptions{domain.NotifyBackup: {domain.OutcomeFailure}})
	j := f.run("backup.run", domain.JobFailed, domain.OriginScheduled)
	j.PolicyID = "pol-1"
	j.ErrorClass = "storage_unreachable"
	j.ErrorMessage = "dial tcp s3.internal: JOB-ERROR-CANARY"
	j.Recovery = "RECOVERY-CANARY"
	j.Input = output(t, map[string]any{"policyName": "Nightly", "repository": map[string]any{"repositoryId": "repo-1",
		"destination": "s3:https://s3.internal/DESTINATION-CANARY"}})
	j.ResultOutput = output(t, protocol.BackupRunOutput{Members: []backup.Member{
		{Item: "stack/s1", Kind: "stack", StackName: "app", State: backup.StateFailed, ErrorClass: "storage_unreachable", Bytes: -1},
		{Item: "volume/db", Kind: "volume", Volume: "db", State: backup.StateFailed},
	}, Warnings: []string{"WARNING-CANARY"}})
	f.finish(j)
	// A scheduled backup that failed is an alert too (the Alerts page)…
	if a := f.one(); a.Kind != domain.NotifyJobFailed || a.JobKind != "backup.run" {
		t.Fatalf("%+v", a)
	}
	// …but its message is the notification's: one per channel.
	got := f.dispatch()
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	for _, s := range got {
		m := s.msg
		if m.Title != "[Docker Manager] Backup Nightly on homelab failed" || m.Tone != domain.ToneCritical ||
			m.Body != "The backup storage could not be reached (DNS, network or TLS). Check that the host can reach the storage "+
				"endpoint. Not backed up: app, db." {
			t.Fatalf("%s: %+v", s.channel, m)
		}
		if v, _ := field(m.Fields, "What to do"); v != "Check that the host can reach the storage endpoint." {
			t.Fatalf("%q", v)
		}
		if v, _ := field(m.Fields, "Items"); v != "0 of 2 backed up · 2 failed" {
			t.Fatalf("%q", v)
		}
		blob := string(output(t, m))
		for _, canary := range []string{"JOB-ERROR-CANARY", "RECOVERY-CANARY", "DESTINATION-CANARY", "WARNING-CANARY"} {
			if strings.Contains(blob, canary) {
				t.Fatalf("%s leaked: %s", canary, blob)
			}
		}
	}
	blob := string(output(t, f.notifications()))
	for _, canary := range []string{"JOB-ERROR-CANARY", "RECOVERY-CANARY", "DESTINATION-CANARY", "WARNING-CANARY"} {
		if strings.Contains(blob, canary) {
			t.Fatalf("%s stored: %s", canary, blob)
		}
	}
	// The next run succeeds with unreadable files: the alert resolves
	// (without a message of its own) and the warning goes to the channel
	// that wants it.
	ok := f.run("backup.run", domain.JobSucceeded, domain.OriginScheduled)
	ok.PolicyID = "pol-1"
	ok.ResultOutput = output(t, protocol.BackupRunOutput{Members: []backup.Member{
		{Item: "stack/s1", Kind: "stack", StackName: "app", State: backup.StateComplete, Bytes: 3 << 30},
		{Item: "volume/db", Kind: "volume", Volume: "db", State: backup.StatePartial, ErrorClass: "files_unreadable", Bytes: 1 << 30},
	}})
	f.finish(ok)
	if len(f.firing()) != 0 {
		t.Fatal("still firing")
	}
	got = f.dispatch()
	if len(got) != 1 || got[0].channel != all.ID || got[0].msg.Tone != domain.ToneWarning ||
		got[0].msg.Title != "[Docker Manager] Backup on homelab finished with warnings" ||
		!strings.Contains(got[0].msg.Body, "2 of 2 items backed up (4 GiB read) in 3 min 12 s. Some files could not be read in db") {
		t.Fatalf("%+v (failures %s)", got, failures.ID)
	}
	// A cancelled run is not reported.
	f.finish(f.run("backup.run", domain.JobCancelled, domain.OriginManual))
	if ns := f.notifications(); len(ns) != 2 {
		t.Fatalf("%+v", ns)
	}
}

func TestAnUpdateRunListsWhatItUpdated(t *testing.T) {
	f := newFixture(t)
	f.channel("all", nil, true, nil, true)
	j := f.run("update.run", domain.JobSucceeded, domain.OriginScheduled)
	j.Input = output(t, map[string]any{"policyId": "pol-1", "container": map[string]any{"name": "web",
		"spec": map[string]any{"env": []string{"PASSWORD=ENV-CANARY"}}}})
	j.ResultOutput = output(t, protocol.UpdateRunOutput{Services: []protocol.UpdateServiceResult{
		{Service: "web", Reference: "nginx:1.27", FromDigest: "sha256:aaaaaaaaaaaaaaaa", ToDigest: "sha256:bbbbbbbbbbbbbbbb",
			Outcome: protocol.UpdateUpdated},
		{Service: "cache", Reference: "redis:7", Outcome: protocol.UpdateUnchanged},
	}})
	f.finish(j)
	n := f.notifications()[0]
	if n.Kind != domain.NotifyUpdates || n.Title != "Updated web on homelab: 1 service" ||
		n.Facts["updatedServices"] != "web (nginx:1.27): aaaaaaaaaaaa → bbbbbbbbbbbb" || n.Facts["unchangedServices"] != "cache" {
		t.Fatalf("%+v", n)
	}
	if strings.Contains(string(output(t, n)), "ENV-CANARY") {
		t.Fatal("the container's environment was stored")
	}
	got := f.dispatch()
	if len(got) != 1 || got[0].msg.Body != "Recreated with the new image: 1 service." {
		t.Fatalf("%+v", got)
	}
}

func TestChannelsGetOnlyTheOutcomesTheyChose(t *testing.T) {
	f := newFixture(t)
	critical := f.channelWith("critical", domain.NotificationSubscriptions{domain.NotifyDiskHealth: {domain.OutcomeCritical}})
	everything := f.channel("all", nil, true, nil, true)
	// A warning reaches only the channel that wants warnings.
	d := disk(protocol.DiskWarning)
	d.Reallocated = i64(3)
	f.health.set("env-1", []protocol.SMARTDevice{d}, nil, nil)
	f.evaluate("env-1")
	got := channelsOf(f.dispatch())
	if got[everything.ID] != 1 || got[critical.ID] != 0 {
		t.Fatalf("warning: %+v", got)
	}
	// It gets critical: now both.
	f.failDisk("env-1", "/dev/sda")
	got = channelsOf(f.dispatch())
	if got[everything.ID] != 1 || got[critical.ID] != 1 {
		t.Fatalf("critical: %+v", got)
	}
}

func TestAResolutionGoesOnlyToChannelsThatWereTold(t *testing.T) {
	f := newFixture(t)
	told := f.channel("told", nil, true, nil, true)
	f.failDisk("env-1", "/dev/sda")
	if got := f.dispatch(); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	// A channel added since never heard of the problem: no "Resolved".
	late := f.channel("late", nil, true, nil, true)
	f.health.set("env-1", []protocol.SMARTDevice{disk(protocol.DiskOK)}, nil, nil)
	f.evaluate("env-1")
	got := channelsOf(f.dispatch())
	if got[told.ID] != 1 || got[late.ID] != 0 {
		t.Fatalf("%+v", got)
	}
}
