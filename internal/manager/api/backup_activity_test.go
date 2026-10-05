package api

import (
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/authztest"
	"github.com/neurekadev/docker-manager/internal/manager/backups"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func activityChecker(t *testing.T, p *authztest.Policy, user string) authz.Checker {
	t.Helper()
	c, err := p.Compile(testutil.Context(t), authz.Principal{Kind: authz.KindUser, UserID: user})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestActivityFileNeedsFilesRead: the file restic reads is a path of the
// backed-up data (#10): it reaches only holders of the item's files-read
// capability; everyone else still sees the counts.
func TestActivityFileNeedsFilesRead(t *testing.T) {
	at := time.Date(2026, 9, 27, 3, 0, 0, 0, time.UTC)
	run := backups.BackupActivity{
		Job: domain.Job{ID: "j1", Kind: jobspec.BackupRun, EnvironmentID: "e1"},
		Items: []protocol.BackupItem{{Kind: backup.MemberStack, StackID: "st-1", StackName: "shop"},
			{Kind: backup.MemberVolume, Volume: "media"}},
		ReportedAt: at,
	}
	vol := protocol.ActivityPayload{Item: backup.VolumeItem("media"), ItemIndex: 1, ItemCount: 2, Percent: 40, FilesDone: 4,
		FilesTotal: 10, CurrentFile: "media/2026/a.jpg"}
	stk := protocol.ActivityPayload{Item: backup.StackItem("st-1"), ItemCount: 2, Percent: 10, CurrentFile: "compose.yaml"}

	reader := activityChecker(t, authztest.Only("u1", "allow volume.files.read @volume:e1/media"), "u1")
	other := activityChecker(t, authztest.Only("u2", "allow volume.files.read @volume:e1/other", "allow stack.definition.read @all"), "u2")

	if got := activityItem(reader, run, vol); got == nil || got.CurrentFile != vol.CurrentFile || got.Volume != "media" ||
		got.Kind != backup.MemberVolume || got.FilesDone != 4 || !got.ReportedAt.Equal(at) {
		t.Errorf("reader sees %+v", got)
	}
	if got := activityItem(other, run, vol); got == nil || got.CurrentFile != "" || got.Percent != 40 {
		t.Errorf("another volume's reader sees %+v", got)
	}
	// stack.definition.read is not the stack's files-read capability.
	if got := activityItem(other, run, stk); got == nil || got.CurrentFile != "" || got.StackName != "shop" {
		t.Errorf("stack without stack.files.read: %+v", got)
	}
	// An item the job does not back up is dropped.
	bogus := protocol.ActivityPayload{Item: backup.VolumeItem("elsewhere"), ItemCount: 2, CurrentFile: "x"}
	if got := activityItem(reader, run, bogus); got != nil {
		t.Errorf("foreign item reported: %+v", got)
	}

	mgr := backups.BackupActivity{Job: domain.Job{ID: "j2", Kind: jobspec.ManagerBackup}, ReportedAt: at}
	state := protocol.ActivityPayload{Item: backup.ItemManagerState, ItemCount: 1, CurrentFile: "docker-manager.db"}
	owner := activityChecker(t, authztest.New().Owner("o1"), "o1")
	if got := activityItem(owner, mgr, state); got == nil || got.CurrentFile != "docker-manager.db" || got.Kind != backup.MemberManagerState {
		t.Errorf("owner sees %+v", got)
	}
	if got := activityItem(reader, mgr, state); got == nil || got.CurrentFile != "" {
		t.Errorf("non-owner sees the manager state's files: %+v", got)
	}
}

// TestActivityCancellable: a running backup is offered for cancelling only
// to job.cancel holders and only until a cancellation was requested; jobs
// the caller may not read are left out.
func TestActivityCancellable(t *testing.T) {
	run := backups.BackupActivity{Job: domain.Job{ID: "j1", Kind: jobspec.BackupRun, EnvironmentID: "e1", State: domain.JobRunning},
		Items: []protocol.BackupItem{{Kind: backup.MemberVolume, Volume: "media"}}}
	reader := activityChecker(t, authztest.Only("u1", "allow job.read @env:e1"), "u1")
	canceller := activityChecker(t, authztest.Only("u2", "allow job.read @env:e1", "allow job.cancel @env:e1"), "u2")
	elsewhere := activityChecker(t, authztest.Only("u3", "allow job.read @env:e2", "allow job.cancel @env:e2"), "u3")

	if got, ok := shapeActivity(reader, run); !ok || got.Cancellable || got.JobID != "j1" || got.ItemCount != 1 {
		t.Errorf("reader sees %+v (%v)", got, ok)
	}
	if got, ok := shapeActivity(canceller, run); !ok || !got.Cancellable {
		t.Errorf("canceller sees %+v (%v)", got, ok)
	}
	if got, ok := shapeActivity(elsewhere, run); ok {
		t.Errorf("another environment's user sees %+v", got)
	}
	requested := run
	requested.Job.CancelRequested, requested.Job.State = true, domain.JobCancelling
	if got, ok := shapeActivity(canceller, requested); !ok || got.Cancellable || got.State != string(domain.JobCancelling) {
		t.Errorf("after the request: %+v (%v)", got, ok)
	}
}

// TestActivityCountsAndRetention: a backup names how many stacks and
// volumes it backs up; a retention job is listed too (one item, no counts).
func TestActivityCountsAndRetention(t *testing.T) {
	c := activityChecker(t, authztest.Only("u1", "allow job.read @env:e1"), "u1")
	run := backups.BackupActivity{Job: domain.Job{ID: "j1", Kind: jobspec.BackupRun, EnvironmentID: "e1", State: domain.JobRunning},
		Items: []protocol.BackupItem{{Kind: backup.MemberStack, StackID: "s1"}, {Kind: backup.MemberStack, StackID: "s2"},
			{Kind: backup.MemberVolume, Volume: "media"}}}
	if got, ok := shapeActivity(c, run); !ok || got.ItemCount != 3 || got.Stacks != 2 || got.Volumes != 1 {
		t.Errorf("backup = %+v (%v)", got, ok)
	}
	ret := backups.BackupActivity{Job: domain.Job{ID: "j2", Kind: jobspec.BackupRetention, EnvironmentID: "e1", State: domain.JobRunning,
		Progress: domain.JobProgress{Percent: 40, Message: "freeing the space of the removed backups"}}, PolicyID: "p1"}
	if got, ok := shapeActivity(c, ret); !ok || got.Kind != string(jobspec.BackupRetention) || got.ItemCount != 1 ||
		got.Stacks != 0 || got.Message == "" || got.PolicyID != "p1" {
		t.Errorf("retention = %+v (%v)", got, ok)
	}
}

// TestBackupStorageSums: locations add up; the ratio and the compressed
// share are recomputed from the totals; unmeasured locations are left out.
func TestBackupStorageSums(t *testing.T) {
	t1, t2 := time.Date(2026, 9, 27, 1, 0, 0, 0, time.UTC), time.Date(2026, 9, 27, 2, 0, 0, 0, time.UTC)
	got := newBackupStorage([]domain.BackupLocation{
		{Scope: "manager", SizeBytes: 100, UncompressedBytes: 300, CompressionRatio: 3, CompressionProgress: 100, StatsSnapshots: 4, StatsAt: &t2},
		{Scope: "env:e1", SizeBytes: 300, UncompressedBytes: 500, CompressionRatio: 1.67, CompressionProgress: 50, StatsSnapshots: 6, StatsAt: &t1},
		{Scope: "env:e2", SizeBytes: 999},
	})
	if got == nil || got.SizeBytes != 400 || got.UncompressedBytes != 800 || got.CompressionRatio != 2 || got.Snapshots != 10 ||
		got.CompressionProgress != (100*300+50*500)/800.0 || !got.MeasuredAt.Equal(t1) || len(got.Locations) != 2 ||
		got.Locations[1].EnvironmentID != "e1" {
		t.Errorf("storage = %+v", got)
	}
	if newBackupStorage([]domain.BackupLocation{{Scope: "manager", SizeBytes: 5}}) != nil {
		t.Error("storage without a measurement")
	}
}

// TestStackSnapshotContentsNeedTheStacksFileAndDefinitionRead: a stack
// snapshot holds the project directory (Compose files and .env included)
// and the stack's volumes, so browsing it needs what the file manager
// needs on the stack, not the Compose definition alone (#279).
func TestStackSnapshotContentsNeedTheStacksFileAndDefinitionRead(t *testing.T) {
	sn := domain.BackupSnapshot{Kind: backup.MemberStack, StackID: "st-1", EnvironmentID: "e1"}
	view := authz.View{Level: authz.Full, Actions: []string{string(CapBackupContentsRead)}}
	h := &backupsAPI{}
	for _, c := range []struct {
		name  string
		rules []string
		ok    bool
	}{
		{"definition only", []string{"allow stack.definition.read @stack:st-1"}, false},
		{"files only", []string{"allow stack.files.read @stack:st-1"}, false},
		{"files and definition", []string{"allow stack.files.read @stack:st-1", "allow stack.definition.read @env:e1"}, true},
	} {
		err := h.requireContents(activityChecker(t, authztest.Only("u1", c.rules...), "u1"), sn, view, CapBackupContentsRead)
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	vol := domain.BackupSnapshot{Kind: backup.MemberVolume, Volume: "media", EnvironmentID: "e1"}
	if err := h.requireContents(activityChecker(t, authztest.Only("u2", "allow volume.files.read @volume:e1/media"), "u2"),
		vol, view, CapBackupContentsRead); err != nil {
		t.Errorf("volume snapshot with volume.files.read: %v", err)
	}
}
