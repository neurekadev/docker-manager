package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/backup"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/backups"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Live activity of running backups (#10): what each unfinished backup job
// is doing now. Jobs are filtered by job.read; the file restic reads is a
// path of the backed-up data and needs the scope's files-read capability
// (stack.files.read, volume.files.read; the manager state: the owner).
// Clients poll it while a backup runs; nothing of it is stored.

// BackupActivity is an unfinished backup job.
type BackupActivity struct {
	JobID         string `json:"jobId"`
	Kind          string `json:"kind" enum:"backup.run,manager.backup"`
	State         string `json:"state" enum:"queued,blocked,dispatched,running,cancelling"`
	SetID         string `json:"setId"`
	PolicyID      string `json:"policyId,omitempty"`
	EnvironmentID string `json:"environmentId,omitempty"`
	// Percent and Message are the job's progress (no file paths).
	Percent   int    `json:"percent" doc:"Job progress 0-100, -1 unknown."`
	Message   string `json:"message,omitempty"`
	ItemCount int    `json:"itemCount" doc:"Stacks, volumes or manager state this job backs up."`
	// Current is the item being backed up (absent before the first report).
	Current *BackupActivityItem `json:"current,omitempty"`
}

// BackupActivityItem is what a backup job reads now.
type BackupActivityItem struct {
	Item             string    `json:"item" example:"volume/media"`
	Kind             string    `json:"kind" enum:"stack,volume,manager_state"`
	StackID          string    `json:"stackId,omitempty"`
	StackName        string    `json:"stackName,omitempty"`
	Volume           string    `json:"volume,omitempty"`
	Index            int       `json:"index" doc:"0-based position among the job's items."`
	Percent          int       `json:"percent" doc:"Progress of this item, 0-100."`
	FilesDone        int64     `json:"filesDone"`
	FilesTotal       int64     `json:"filesTotal"`
	BytesDone        int64     `json:"bytesDone"`
	BytesTotal       int64     `json:"bytesTotal"`
	SecondsRemaining int64     `json:"secondsRemaining,omitempty"`
	CurrentFile      string    `json:"currentFile,omitempty" doc:"The file being read, relative to the item (<volume>/<path> inside a volume). Only for holders of the scope's files-read capability."`
	ReportedAt       time.Time `json:"reportedAt"`
}

// BackupActivityList is every unfinished backup job the caller may see.
type BackupActivityList struct {
	Jobs []BackupActivity `json:"jobs"`
}

type backupActivityOutput struct{ Body BackupActivityList }

func (h *backupsAPI) listActivity(ctx context.Context, _ *struct{}) (*backupActivityOutput, error) {
	svc, c, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	acts, err := svc.Activity(ctx)
	if err != nil {
		return nil, Internal(err)
	}
	out := BackupActivityList{Jobs: []BackupActivity{}}
	for _, a := range acts {
		j := a.Job
		if !c.Can(string(CapJobRead), authz.JobResource(j)).Allowed {
			continue
		}
		items := len(a.Items)
		if items == 0 {
			items = 1 // the manager state
		}
		ba := BackupActivity{JobID: j.ID, Kind: string(j.Kind), State: string(j.State), SetID: a.SetID, PolicyID: a.PolicyID,
			EnvironmentID: j.EnvironmentID, Percent: j.Progress.Percent, Message: j.Progress.Message, ItemCount: items}
		if r := a.Report; r != nil {
			ba.Current = activityItem(c, a, *r)
		}
		out.Jobs = append(out.Jobs, ba)
	}
	return &backupActivityOutput{Body: out}, nil
}

// activityItem shapes a report, keeping the file only for callers who may
// read the item's files.
func activityItem(c authz.Checker, a backups.BackupActivity, r protocol.ActivityPayload) *BackupActivityItem {
	it := &BackupActivityItem{Item: r.Item, Kind: backup.MemberManagerState, Index: r.ItemIndex, Percent: r.Percent,
		FilesDone: r.FilesDone, FilesTotal: r.FilesTotal, BytesDone: r.BytesDone, BytesTotal: r.BytesTotal,
		SecondsRemaining: r.SecondsRemaining, ReportedAt: a.ReportedAt}
	env := a.Job.EnvironmentID
	readable := false
	switch {
	case len(a.Items) == 0:
		if r.Item != backup.ItemManagerState {
			return nil
		}
		readable = c.Can("system.restore", authz.Instance()).Allowed
	default:
		found := false
		for _, bi := range a.Items {
			if bi.Key() != r.Item {
				continue
			}
			found = true
			it.Kind, it.StackID, it.StackName, it.Volume = bi.Kind, bi.StackID, bi.StackName, bi.Volume
			switch bi.Kind {
			case backup.MemberStack:
				readable = c.Can("stack.files.read", authz.Resource{Type: catalog.TypeStack, ID: bi.StackID, EnvironmentID: env}).Allowed
			case backup.MemberVolume:
				readable = c.Can("volume.files.read", authz.Resource{Type: catalog.TypeVolume, ID: bi.Volume, EnvironmentID: env}).Allowed
			}
		}
		if !found {
			return nil // a report for an item the job does not back up
		}
	}
	if readable {
		it.CurrentFile = r.CurrentFile
	}
	return it
}

func registerBackupActivity(a huma.API, h *backupsAPI) {
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-backup-activity", Method: http.MethodGet, Path: BasePath + "/backup-activity",
			Summary: "List running backups",
			Description: "Every unfinished backup job the caller may read (job.read), with what it backs up now: the item, its " +
				"progress, file and byte counts, restic's estimate and the file being read. The file is a path of the backed-up " +
				"data: it is returned only with stack.files.read / volume.files.read on the item (the manager state: the owner). " +
				"Live data kept in memory only; poll it while a backup runs.",
			Tags: []string{tagBackups},
		},
		Capability: CapJobRead, Scope: ScopeResource,
	}, h.listActivity)
}
