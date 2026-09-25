package api

import (
	"context"
	"net/http"
	"path"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/manager/backups"
)

// Backups (#10): the snapshot index, contents, downloads and
// verification. Snapshot contents can hold secrets (compose.yaml, .env,
// the manager database): browsing needs backup.contents.read plus the
// capability that reads the same data live (stack.definition.read for
// stack snapshots, volume.files.read for volume snapshots); manager-state
// snapshots are owner-only.

func backupResource(sn domain.BackupSnapshot) authz.Resource {
	return authz.Resource{Type: catalog.TypeBackup, ID: sn.ID,
		Parents: []authz.ResourceRef{{Type: catalog.TypeBackupRepository, ID: sn.RepositoryID}}}
}

// Backup is one snapshot DockYard knows.
//
// Shaping (#17): backup.read shows it in full; any other capability on it
// only id, time, repository and state.
type Backup struct {
	ID            string     `json:"id"`
	SnapshotTime  time.Time  `json:"snapshotTime"`
	RepositoryID  string     `json:"repositoryId"`
	State         string     `json:"state" enum:"complete,partial" doc:"partial: some files could not be read."`
	View          string     `json:"view" enum:"minimal,full"`
	Actions       []string   `json:"actions"`
	Kind          string     `json:"kind,omitempty" enum:"manager_state,stack,volume"`
	Item          string     `json:"item,omitempty"`
	Scope         string     `json:"scope,omitempty" doc:"manager or env:<environmentId>: which restic repository below the destination holds it."`
	EnvironmentID string     `json:"environmentId,omitempty"`
	StackID       string     `json:"stackId,omitempty"`
	StackName     string     `json:"stackName,omitempty"`
	Volume        string     `json:"volume,omitempty"`
	SnapshotID    string     `json:"snapshotId,omitempty" doc:"restic snapshot ID."`
	Paths         []string   `json:"paths,omitempty"`
	Volumes       []string   `json:"volumes,omitempty"`
	Consistency   string     `json:"consistency,omitempty" enum:"live,shutdown,snapshot" doc:"live: taken while containers ran (crash-consistent); shutdown: with the affected containers stopped; snapshot: consistent database snapshot."`
	ErrorClass    string     `json:"errorClass,omitempty"`
	Bytes         int64      `json:"bytes,omitempty"`
	SetID         string     `json:"setId,omitempty"`
	PolicyID      string     `json:"policyId,omitempty"`
	JobID         string     `json:"jobId,omitempty"`
	VerifiedAt    *time.Time `json:"verifiedAt,omitempty"`
	ForgottenAt   *time.Time `json:"forgottenAt,omitempty" doc:"Removed by retention (listed only with includeForgotten)."`
}

func newBackup(sn domain.BackupSnapshot, v authz.View) Backup {
	out := Backup{ID: sn.ID, SnapshotTime: sn.SnapshotTime, RepositoryID: sn.RepositoryID, State: sn.State, View: v.Level.String(),
		Actions: Actions(v)}
	if !v.Full() {
		return out
	}
	out.Kind, out.Item, out.Scope, out.EnvironmentID = sn.Kind, sn.Item, sn.Scope, sn.EnvironmentID
	out.StackID, out.StackName, out.Volume, out.SnapshotID = sn.StackID, sn.StackName, sn.Volume, sn.ResticSnapshotID
	out.Paths, out.Volumes, out.Consistency, out.ErrorClass = sn.Paths, sn.Volumes, sn.Consistency, sn.ErrorClass
	out.Bytes, out.SetID, out.PolicyID, out.JobID = sn.BytesTotal, sn.SetID, sn.PolicyID, sn.JobID
	out.VerifiedAt, out.ForgottenAt = sn.VerifiedAt, sn.ForgottenAt
	return out
}

type listBackupsInput struct {
	PageParams
	RepositoryID     string `query:"repositoryId" maxLength:"64"`
	PolicyID         string `query:"policyId" maxLength:"64"`
	SetID            string `query:"setId" maxLength:"64"`
	EnvironmentID    string `query:"environmentId" maxLength:"64"`
	StackID          string `query:"stackId" maxLength:"64"`
	Kind             string `query:"kind" enum:"manager_state,stack,volume"`
	IncludeForgotten bool   `query:"includeForgotten"`
}

type backupListOutput struct{ Body Page[Backup] }

func (h *backupsAPI) listBackups(ctx context.Context, in *listBackupsInput) (*backupListOutput, error) {
	svc, c, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	fp := QueryFingerprint("backups", in.RepositoryID, in.PolicyID, in.SetID, in.EnvironmentID, in.StackID, in.Kind,
		strconv.FormatBool(in.IncludeForgotten))
	var after agentCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fp, &after); err != nil {
			return nil, err
		}
	}
	items, next, err := ScanPage(ctx, Scan[domain.BackupSnapshot]{
		Limit: in.PageLimit(), After: after.ID,
		Fetch: func(ctx context.Context, afterID string, n int) ([]domain.BackupSnapshot, error) {
			return svc.ListSnapshots(ctx, domain.BackupSnapshotFilter{AfterID: afterID, Limit: n, RepositoryID: in.RepositoryID,
				PolicyID: in.PolicyID, SetID: in.SetID, EnvironmentID: in.EnvironmentID, StackID: in.StackID, Kind: in.Kind,
				IncludeForgotten: in.IncludeForgotten})
		},
		Position: func(sn domain.BackupSnapshot) string { return sn.ID },
		Visible:  func(sn domain.BackupSnapshot) bool { return authz.ViewOf(c, backupResource(sn)).Visible() },
	})
	if err != nil {
		return nil, Internal(err)
	}
	out := make([]Backup, 0, len(items))
	for _, sn := range items {
		out = append(out, newBackup(sn, authz.ViewOf(c, backupResource(sn))))
	}
	cursor, err := nextCursor(fp, next)
	if err != nil {
		return nil, err
	}
	return &backupListOutput{Body: NewPage(out, cursor, nil)}, nil
}

type backupIDInput struct {
	BackupID string `path:"backupId" maxLength:"64" doc:"Backup ID."`
}

// BackupDetail is a backup with its set.
type BackupDetail struct {
	Backup
	Set *BackupSetSummary `json:"set,omitempty" doc:"The backup set (run) it belongs to: every member with its own state and snapshot time."`
}

type backupOutput struct{ Body BackupDetail }

func (h *backupsAPI) visibleBackup(ctx context.Context, id string) (BackupService, authz.Checker, authz.Principal, domain.BackupSnapshot, authz.View, error) {
	svc, c, p, err := h.checker(ctx)
	if err != nil {
		return nil, nil, p, domain.BackupSnapshot{}, authz.View{}, err
	}
	sn, err := svc.GetSnapshot(ctx, id)
	if err != nil {
		return nil, nil, p, sn, authz.View{}, backupError(err)
	}
	v := authz.ViewOf(c, backupResource(sn))
	if !v.Visible() {
		return nil, nil, p, sn, v, NotFound("backup not found")
	}
	return svc, c, p, sn, v, nil
}

func (h *backupsAPI) getBackup(ctx context.Context, in *backupIDInput) (*backupOutput, error) {
	svc, _, _, sn, v, err := h.visibleBackup(ctx, in.BackupID)
	if err != nil {
		return nil, err
	}
	out := BackupDetail{Backup: newBackup(sn, v)}
	if v.Full() && sn.SetID != "" {
		if set, err := svc.GetSet(ctx, sn.SetID); err == nil {
			s := newBackupSet(set)
			out.Set = &s
		}
	}
	return &backupOutput{Body: out}, nil
}

// requireContents authorizes reading a snapshot's contents (#10, #17).
func (h *backupsAPI) requireContents(c authz.Checker, sn domain.BackupSnapshot, v authz.View, cp Capability) error {
	if !v.Has(string(cp)) {
		return Forbidden("not permitted: " + string(cp))
	}
	capability, res, ownerOnly := backups.ContentsCapabilities(sn)
	if ownerOnly {
		if !c.Can("system.restore", authz.Instance()).Allowed {
			return Forbidden("manager-state backups hold DockYard's own database: only the instance owner may open them")
		}
		return nil
	}
	if !c.Can(capability, res).Allowed {
		return Forbidden("not permitted: " + capability + " (the backup holds the same data)")
	}
	return nil
}

// BackupNode is an entry of a snapshot.
type BackupNode struct {
	Name  string    `json:"name"`
	Path  string    `json:"path"`
	Type  string    `json:"type" enum:"file,dir,symlink,dev,chardev,fifo,socket,irregular"`
	Size  int64     `json:"size"`
	Mode  uint32    `json:"mode" doc:"Permission bits."`
	UID   int       `json:"uid"`
	GID   int       `json:"gid"`
	MTime time.Time `json:"mtime,omitzero"`
}

// BackupContents is a snapshot listing.
type BackupContents struct {
	Path      string       `json:"path"`
	Entries   []BackupNode `json:"entries"`
	Truncated bool         `json:"truncated"`
}

type backupContentsInput struct {
	BackupID  string `path:"backupId" maxLength:"64" doc:"Backup ID."`
	Path      string `query:"path" maxLength:"4096" doc:"Absolute directory inside the snapshot; empty lists everything (bounded)."`
	Recursive bool   `query:"recursive"`
	Limit     int    `query:"limit" minimum:"1" maximum:"10000" default:"1000"`
}

type backupContentsOutput struct{ Body BackupContents }

func (h *backupsAPI) listContents(ctx context.Context, in *backupContentsInput) (*backupContentsOutput, error) {
	svc, c, _, sn, v, err := h.visibleBackup(ctx, in.BackupID)
	if err != nil {
		return nil, err
	}
	if err := h.requireContents(c, sn, v, CapBackupContentsRead); err != nil {
		return nil, err
	}
	l, err := svc.Contents(ctx, sn, in.Path, in.Recursive, in.Limit)
	if err != nil {
		return nil, backupError(err)
	}
	out := BackupContents{Path: in.Path, Entries: []BackupNode{}, Truncated: l.Truncated}
	for _, n := range l.Nodes {
		out.Entries = append(out.Entries, BackupNode{Name: n.Name, Path: n.Path, Type: n.Type, Size: n.Size, Mode: n.Mode & 0o7777,
			UID: n.UID, GID: n.GID, MTime: n.MTime})
	}
	return &backupContentsOutput{Body: out}, nil
}

type downloadBackupInput struct {
	BackupID string `path:"backupId" maxLength:"64" doc:"Backup ID."`
	Path     string `query:"path" required:"true" maxLength:"4096" doc:"Absolute path of one regular file inside the snapshot."`
}

func (h *backupsAPI) downloadContent(ctx context.Context, in *downloadBackupInput) (*huma.StreamResponse, error) {
	svc, c, _, sn, v, err := h.visibleBackup(ctx, in.BackupID)
	if err != nil {
		return nil, err
	}
	if err := h.requireContents(c, sn, v, CapBackupContentsDownload); err != nil {
		return nil, err
	}
	node, err := svc.FileInfo(ctx, sn, in.Path)
	if err != nil {
		return nil, backupError(err)
	}
	audit.SetDetail(ctx, "backupId", sn.ID)
	audit.SetDetail(ctx, "path", node.Path)
	audit.SetDetail(ctx, "bytes", node.Size)
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		hctx.SetHeader("Content-Type", "application/octet-stream")
		hctx.SetHeader("Content-Disposition", contentDisposition(path.Base(node.Path)))
		hctx.SetHeader("Content-Length", strconv.FormatInt(node.Size, 10))
		hctx.SetHeader("X-Content-Type-Options", "nosniff")
		hctx.SetStatus(http.StatusOK)
		if err := svc.Download(hctx.Context(), sn, node, hctx.BodyWriter()); err != nil {
			// Headers are sent: abort the connection so the client sees a
			// truncated download instead of a complete-looking file.
			panic(http.ErrAbortHandler)
		}
	}}, nil
}

type verifyBackupInput struct {
	BackupID string `path:"backupId" maxLength:"64" doc:"Backup ID."`
	IdempotencyKeyParam
	Body *struct {
		ReadDataSubset string `json:"readDataSubset,omitempty" maxLength:"16" example:"10%" doc:"Also read part of the pack data (restic --read-data-subset)."`
	}
}

func (h *backupsAPI) verifyBackup(ctx context.Context, in *verifyBackupInput) (*JobAccepted, error) {
	svc, _, p, sn, v, err := h.visibleBackup(ctx, in.BackupID)
	if err != nil {
		return nil, err
	}
	if !v.Has(string(CapBackupVerify)) {
		return nil, Forbidden("not permitted: " + string(CapBackupVerify))
	}
	subset := ""
	if in.Body != nil {
		subset = in.Body.ReadDataSubset
	}
	j, err := svc.VerifySnapshot(ctx, sn, p, subset, in.IdempotencyKey)
	if err != nil {
		return nil, backupError(err)
	}
	return Accepted(j), nil
}

func registerBackups(a huma.API, deps Deps) {
	h := &backupsAPI{svc: deps.Backups, authz: authz.OrDenyAll(deps.Authorizer), deps: deps}
	registerBackupRepositories(a, h)
	registerBackupPolicies(a, h)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-backups", Method: http.MethodGet, Path: BasePath + "/backups",
			Summary: "List backups",
			Description: "The snapshot index (shared backup history of the instance, whoever configured the policy), filtered per item " +
				"(#17): backup.read shows a backup in full, any other capability on it only id, time, repository and state.",
			Tags: []string{tagBackups}, Errors: []int{http.StatusUnprocessableEntity},
		},
		Capability: CapBackupRead, Scope: ScopeResource,
	}, h.listBackups)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-backup", Method: http.MethodGet, Path: BasePath + "/backups/{backupId}",
			Summary: "Get a backup", Description: "With its backup set (every member's state and per-host snapshot time).",
			Tags: []string{tagBackups}, Errors: []int{http.StatusNotFound},
		},
		Capability: CapBackupRead, Scope: ScopeResource,
	}, h.getBackup)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-backup-contents", Method: http.MethodGet, Path: BasePath + "/backups/{backupId}/contents",
			Summary: "Browse a backup",
			Description: "Lists entries of the snapshot. Needs backup.contents.read and the capability that reads the same data live: " +
				"stack.definition.read for stack backups (they hold compose.yaml and .env), volume.files.read for volume backups; " +
				"manager-state backups are owner-only.",
			Tags: []string{tagBackups}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable},
		},
		Capability: CapBackupContentsRead, Scope: ScopeResource,
	}, h.listContents)
	dl := Operation{
		Operation: huma.Operation{
			OperationID: "download-backup-content", Method: http.MethodGet, Path: BasePath + "/backups/{backupId}/contents/download",
			Summary: "Download a file from a backup",
			Description: "One regular file (at most 2 GiB; 409 backup_not_a_file, 413 backup_file_too_large). Same authorization as " +
				"browsing, with backup.contents.download. Audited.",
			Tags: []string{tagBackups}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
				http.StatusRequestEntityTooLarge, http.StatusServiceUnavailable},
			Responses: map[string]*huma.Response{"200": {Description: "File bytes", Content: map[string]*huma.MediaType{
				"application/octet-stream": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}}},
		},
		Capability: CapBackupContentsDownload, Scope: ScopeResource, Audit: AuditAlways,
	}
	Register(a, dl, h.downloadContent)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-backup-verification", Method: http.MethodPost, Path: BasePath + "/backups/{backupId}/verifications",
			Summary: "Verify a backup's repository",
			Description: "Queues a check of the repository location holding the backup (restic check, optionally reading a subset " +
				"of the data). Damage fails the job with repository_damaged.",
			Tags: []string{tagBackups}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
		},
		Capability: CapBackupVerify, Scope: ScopeResource, Idempotency: IdempotencyJob,
	}, h.verifyBackup)
}
