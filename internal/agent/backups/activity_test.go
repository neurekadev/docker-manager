package backups

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/restic"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

type recReporter struct {
	mu  sync.Mutex
	got []protocol.ProgressPayload
}

func (r *recReporter) Progress(_ context.Context, _ *jobexec.State, p protocol.ProgressPayload) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, p)
}

func (e *env) runReported(t *testing.T, in protocol.BackupRunInput) (protocol.BackupRunOutput, []protocol.ProgressPayload) {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	rep := &recReporter{}
	st := &jobexec.State{JobID: "job-1", Attempt: 1, Kind: jobspec.BackupRun, Input: raw, Secrets: e.credential("DYRK-TEST")}
	res, err := jobexec.Run(testutil.Context(t), e.executor(jobspec.BackupRun), st, jobexec.Options{Journal: &memJournal{}, Reporter: rep})
	if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("outcome %+v, %v", res, err)
	}
	return outputOf(t, res), rep.got
}

// TestBackupRunReportsActivityWhenAsked: with activity requested the
// executor reports the file restic reads, relative to the volume; job
// progress never carries a path; without it no activity is sent. The
// location's size is measured after the run.
func TestBackupRunReportsActivityWhenAsked(t *testing.T) {
	e := newEnv(t)
	in := e.runInput(false, protocol.BackupItem{Kind: backup.MemberVolume, Volume: "uploads"})
	in.Activity = true
	out, got := e.runReported(t, in)

	var files []string
	for _, p := range got {
		if strings.Contains(p.Message, e.dir) || strings.Contains(p.Message, "_data") {
			t.Errorf("job progress carries a path: %q", p.Message)
		}
		if a := p.Activity; a != nil {
			if p.Message != "" || p.Item != nil {
				t.Errorf("activity mixed with progress: %+v", p)
			}
			if err := a.Validate(); err != nil {
				t.Errorf("invalid activity %+v: %v", a, err)
			}
			if a.Item != backup.VolumeItem("uploads") || a.ItemCount != 1 {
				t.Errorf("activity = %+v", a)
			}
			if a.CurrentFile != "" {
				files = append(files, a.CurrentFile)
			}
		}
	}
	if len(files) == 0 {
		t.Fatalf("no current file reported: %+v", got)
	}
	for _, f := range files {
		if !strings.HasPrefix(f, "uploads/") || strings.Contains(f, e.dir) {
			t.Errorf("current file %q is not relative to the volume", f)
		}
	}
	if out.Stats == nil || out.Stats.SizeBytes <= 0 || out.Stats.UncompressedBytes != 2*out.Stats.SizeBytes ||
		out.Stats.CompressionRatio != 2 || out.Stats.Snapshots == 0 {
		t.Errorf("stats = %+v", out.Stats)
	}

	in.Activity, in.SetID = false, "set-2"
	_, got = e.runReported(t, in)
	for _, p := range got {
		if p.Activity != nil {
			t.Fatalf("activity sent without being asked: %+v", p)
		}
	}
}

// TestBackupRunSucceedsWhenStatsFail: measuring is best effort.
func TestBackupRunSucceedsWhenStatsFail(t *testing.T) {
	e := newEnv(t)
	e.store.Fail("stats", "", &restic.Error{Op: "stats", Code: restic.CodeLocked})
	out, _ := e.runReported(t, e.runInput(false, protocol.BackupItem{Kind: backup.MemberVolume, Volume: "uploads"}))
	if out.Stats != nil {
		t.Errorf("stats = %+v, want none", out.Stats)
	}
}

func TestRelativeFile(t *testing.T) {
	p := itemPlan{dir: "/stacks/app", volumePaths: map[string]string{"db": "/var/lib/docker/volumes/db/_data"}}
	for f, want := range map[string]string{
		"/var/lib/docker/volumes/db/_data/pg/base/1": "db/pg/base/1",
		"/stacks/app/compose.yaml":                   "compose.yaml",
		"/srv/elsewhere/secret.txt":                  "secret.txt",
	} {
		if got := relativeFile(p, f); got != want {
			t.Errorf("relativeFile(%q) = %q, want %q", f, got, want)
		}
	}
}
