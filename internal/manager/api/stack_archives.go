package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/humanize"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/migrations"
	"github.com/neurekadev/docker-manager/internal/manager/stackarchives"
)

// Stack archives (#313): export a stack with its volumes as an archive for
// download, upload such an archive and create a stack from it in an
// environment.

// Stack archive capabilities.
const (
	CapStackExport        Capability = "stack.export"
	CapStackFilesDownload Capability = "stack.files.download"
)

// Stack archive error codes.
const (
	CodeStackArchiveBlocked  = "stack_archive_blocked"
	CodeStackArchiveInvalid  = "invalid_stack_archive"
	CodeStackArchiveInUse    = "stack_archive_in_use"
	CodeTooManyStackArchives = "too_many_stack_archives"
	CodeManagerSpace         = "manager_space"
	CodeUploadIncomplete     = "upload_incomplete"
)

// StackArchiveService exports and imports stack archives
// (*stackarchives.Service).
type StackArchiveService interface {
	MaxSize() int64
	PreviewExport(ctx context.Context, st domain.Stack, r stackarchives.ExportRequest) (stackarchives.ExportPlan, error)
	StartExport(ctx context.Context, p authz.Principal, st domain.Stack, r stackarchives.ExportRequest) (domain.Job, error)
	Export(ctx context.Context, stackID, jobID string) (stackarchives.ExportFile, error)
	OpenExport(f stackarchives.ExportFile) (io.ReadSeekCloser, error)
	StoreUpload(ctx context.Context, p authz.Principal, size int64, body io.Reader) (stackarchives.Upload, error)
	Upload(p authz.Principal, id string) (stackarchives.Upload, error)
	DeleteUpload(p authz.Principal, id string) error
	PreviewImport(ctx context.Context, p authz.Principal, archiveID string, r stackarchives.ImportRequest) (stackarchives.ImportPlan, error)
	StartImport(ctx context.Context, p authz.Principal, archiveID string, r stackarchives.ImportRequest) (domain.Stack, domain.Job, error)
}

// StackArchiveVolume is a named volume of a stack and what an export does
// with it.
type StackArchiveVolume struct {
	Key          string `json:"key" example:"data" doc:"Compose volume key."`
	Name         string `json:"name" example:"web_data"`
	Included     bool   `json:"included" doc:"Its data goes into the archive."`
	Excluded     bool   `json:"excluded,omitempty" doc:"Left out by the request (excludeVolumes)."`
	NotPermitted bool   `json:"notPermitted,omitempty" doc:"The caller may not download the volume's files (volume.files.download): it must be left out."`
	Reason       string `json:"reason,omitempty" doc:"Why it cannot be included."`
	Bytes        int64  `json:"bytes"`
	Entries      int64  `json:"entries"`
	Truncated    bool   `json:"truncated,omitempty" doc:"The size is a lower bound (the scan hit its budget)."`
}

// StackArchiveExclusion is something an archive does not hold.
type StackArchiveExclusion struct {
	Kind   string `json:"kind" enum:"volume,anonymous_volume,bind"`
	Key    string `json:"key,omitempty"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// StackExportFile is an archive ready for download.
type StackExportFile struct {
	ExportID  string    `json:"exportId" doc:"The stack.export job's ID; download it from GET /stacks/{stackId}/exports/{exportId}."`
	FileName  string    `json:"fileName" example:"web-2026-10-10.tar.gz"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt" doc:"The archive is removed from the manager then."`
	Volumes   []string  `json:"volumes" doc:"Compose keys of the included volumes."`
}

// StackExportPreview is an export's check, computed before anything stops.
type StackExportPreview struct {
	StackID          string                  `json:"stackId" example:"0192f5e4-8b7a-7c3e-9d2f-1a2b3c4d5e6f"`
	Allowed          bool                    `json:"allowed" doc:"No blockers: the export can start."`
	Blockers         []MigrationFinding      `json:"blockers"`
	Warnings         []MigrationFinding      `json:"warnings"`
	Volumes          []StackArchiveVolume    `json:"volumes"`
	NotIncluded      []StackArchiveExclusion `json:"notIncluded" doc:"Anonymous volumes and bind mounts outside the project folder."`
	ProjectBytes     int64                   `json:"projectBytes"`
	VolumeBytes      int64                   `json:"volumeBytes"`
	TotalBytes       int64                   `json:"totalBytes"`
	Truncated        bool                    `json:"truncated,omitempty"`
	ManagerFreeBytes int64                   `json:"managerFreeBytes" doc:"Free space for archives on the manager (-1: unknown)."`
	MaxBytes         int64                   `json:"maxBytes" doc:"DOCKER_MANAGER_STACK_ARCHIVE_MAX_MB in bytes."`
	Running          []string                `json:"running" doc:"Services that run: they stop while the archive is written and start again."`
	DowntimeSeconds  int64                   `json:"downtimeSeconds" doc:"Estimated downtime (0: nothing runs)."`
	Latest           *StackExportFile        `json:"latest,omitempty" doc:"The newest archive of the stack still available."`
}

func newExportFile(f *stackarchives.ExportFile) *StackExportFile {
	if f == nil {
		return nil
	}
	return &StackExportFile{ExportID: f.JobID, FileName: f.FileName, Size: f.Size, SHA256: f.SHA256, CreatedAt: f.CreatedAt,
		ExpiresAt: f.ExpiresAt, Volumes: append([]string{}, f.Volumes...)}
}

func newExclusions(in []stackarchives.Exclusion) []StackArchiveExclusion {
	out := make([]StackArchiveExclusion, 0, len(in))
	for _, e := range in {
		out = append(out, StackArchiveExclusion(e))
	}
	return out
}

func newExportPreview(p stackarchives.ExportPlan) StackExportPreview {
	out := StackExportPreview{StackID: p.StackID, Allowed: p.Allowed(), Blockers: findings(p.Blockers), Warnings: findings(p.Warnings),
		Volumes: []StackArchiveVolume{}, NotIncluded: newExclusions(p.NotIncluded), ProjectBytes: p.ProjectBytes, VolumeBytes: p.VolumeBytes,
		TotalBytes: p.TotalBytes, Truncated: p.Truncated, ManagerFreeBytes: p.ManagerFree, MaxBytes: p.MaxBytes,
		Running: append([]string{}, p.Running...), DowntimeSeconds: p.DowntimeSeconds, Latest: newExportFile(p.Latest)}
	for _, v := range p.Volumes {
		out.Volumes = append(out.Volumes, StackArchiveVolume{Key: v.Key, Name: v.Name, Included: v.Included, Excluded: v.Excluded,
			NotPermitted: v.NotPermitted, Reason: v.Reason, Bytes: v.Bytes, Entries: v.Entries, Truncated: v.Truncated})
	}
	return out
}

// StackExportBody selects what an export includes.
type StackExportBody struct {
	ExcludeVolumes []string `json:"excludeVolumes,omitempty" maxItems:"64" example:"[\"cache\"]" doc:"Compose keys of named volumes whose data is left out. Default: every plain local named volume is included."`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty" minimum:"0" maximum:"3600" example:"30" doc:"Stop grace period of the stack's containers."`
}

type stackExportPreviewInput struct {
	StackID string `path:"stackId" maxLength:"64" doc:"Stack ID."`
	Body    StackExportBody
}

type stackExportInput struct {
	StackID string `path:"stackId" maxLength:"64" doc:"Stack ID."`
	IdempotencyKeyParam
	Body StackExportBody
}

type stackExportDownloadInput struct {
	StackID  string `path:"stackId" maxLength:"64" doc:"Stack ID."`
	ExportID string `path:"exportId" maxLength:"64" doc:"Export ID (the stack.export job's ID)."`
	Range    string `header:"Range" maxLength:"128"`
}

type stackExportPreviewOutput struct{ Body StackExportPreview }

// StackArchiveVolumeInfo is a volume an archive holds.
type StackArchiveVolumeInfo struct {
	Key     string `json:"key" example:"data"`
	Name    string `json:"name" example:"web_data" doc:"Its name at export."`
	Bytes   int64  `json:"bytes"`
	Entries int64  `json:"entries"`
}

// StackArchive is an uploaded archive.
type StackArchive struct {
	ID             string                   `json:"id" example:"0192f5e4-8b7a-7c3e-9d2f-1a2b3c4d5e6f"`
	Size           int64                    `json:"size" example:"52428800"`
	SHA256         string                   `json:"sha256"`
	CreatedAt      time.Time                `json:"createdAt"`
	ExpiresAt      time.Time                `json:"expiresAt" doc:"An unused upload is removed then."`
	ExportedAt     time.Time                `json:"exportedAt"`
	ManagerVersion string                   `json:"managerVersion,omitempty" doc:"The exporting manager's version."`
	Name           string                   `json:"name" example:"web" doc:"The stack's (Compose project) name at export."`
	DisplayName    string                   `json:"displayName,omitempty"`
	Description    string                   `json:"description,omitempty"`
	PinnedName     string                   `json:"pinnedName,omitempty" doc:"The project name a Compose file pins with a top-level name: (the stack must use it)."`
	ProjectBytes   int64                    `json:"projectBytes"`
	ProjectEntries int64                    `json:"projectEntries"`
	Volumes        []StackArchiveVolumeInfo `json:"volumes"`
	NotIncluded    []StackArchiveExclusion  `json:"notIncluded"`
	Services       []string                 `json:"services"`
}

func newStackArchive(u stackarchives.Upload) StackArchive {
	m := u.Manifest
	out := StackArchive{ID: u.ID, Size: u.Size, SHA256: u.SHA256, CreatedAt: u.CreatedAt, ExpiresAt: u.ExpiresAt, ExportedAt: m.ExportedAt,
		ManagerVersion: m.ManagerVersion, Name: m.Stack.Name, DisplayName: m.Stack.DisplayName, Description: m.Stack.Description,
		PinnedName: u.PinnedName, ProjectBytes: u.Project.Bytes, ProjectEntries: u.Project.Entries, Volumes: []StackArchiveVolumeInfo{},
		NotIncluded: newExclusions(m.NotIncluded), Services: []string{}}
	for _, v := range m.Volumes {
		st := u.Volumes[v.Key]
		out.Volumes = append(out.Volumes, StackArchiveVolumeInfo{Key: v.Key, Name: v.Name, Bytes: st.Bytes, Entries: st.Entries})
	}
	for _, sv := range m.Services {
		out.Services = append(out.Services, sv.Name)
	}
	return out
}

type stackArchiveUploadInput struct {
	ContentLength int64 `header:"Content-Length" minimum:"0" doc:"Required: the archive's size."`
}

type stackArchiveOutput struct{ Body StackArchive }

type stackArchivePath struct {
	ArchiveID string `path:"archiveId" maxLength:"64" doc:"Uploaded archive ID."`
}

// StackArchiveImportBody chooses where and how a stack is created from an
// archive.
type StackArchiveImportBody struct {
	EnvironmentID string  `json:"environmentId" minLength:"1" maxLength:"64" example:"0192f5e4-8b7a-7c3e-9d2f-1a2b3c4d5e70" doc:"The environment the stack is created in."`
	Name          string  `json:"name" minLength:"1" maxLength:"63" example:"web" doc:"The new stack's Compose project name (and folder). Volumes named after the project follow it."`
	DisplayName   *string `json:"displayName,omitempty" maxLength:"128" example:"Shop" doc:"Omitted: the archive's; empty: none."`
	Description   *string `json:"description,omitempty" maxLength:"1024" example:"Orders and payments" doc:"Omitted: the archive's; empty: none."`
	Deploy        bool    `json:"deploy,omitempty" doc:"Deploy the stack once its files and volumes are in place (also needs stack.deploy)."`
}

func (b StackArchiveImportBody) request() stackarchives.ImportRequest {
	return stackarchives.ImportRequest{EnvironmentID: b.EnvironmentID, Name: b.Name, DisplayName: b.DisplayName, Description: b.Description,
		Deploy: b.Deploy}
}

type stackArchiveImportPreviewInput struct {
	ArchiveID string `path:"archiveId" maxLength:"64" doc:"Uploaded archive ID."`
	Body      StackArchiveImportBody
}

type stackArchiveImportInput struct {
	ArchiveID string `path:"archiveId" maxLength:"64" doc:"Uploaded archive ID."`
	IdempotencyKeyParam
	Body StackArchiveImportBody
}

// StackArchiveImportVolume is a volume the new stack gets.
type StackArchiveImportVolume struct {
	Key    string `json:"key" example:"data"`
	Source string `json:"source" example:"web_data" doc:"Its name in the archive."`
	Name   string `json:"name" example:"shop_data" doc:"Its name for the new stack."`
	Bytes  int64  `json:"bytes"`
}

// StackArchiveImportPreview checks creating a stack from an archive before
// anything is written.
type StackArchiveImportPreview struct {
	ArchiveID        string                     `json:"archiveId" example:"0192f5e4-8b7a-7c3e-9d2f-1a2b3c4d5e6f"`
	EnvironmentID    string                     `json:"environmentId" example:"0192f5e4-8b7a-7c3e-9d2f-1a2b3c4d5e70"`
	Name             string                     `json:"name" example:"shop"`
	Allowed          bool                       `json:"allowed" doc:"No blockers: the stack can be created."`
	Blockers         []MigrationFinding         `json:"blockers"`
	Warnings         []MigrationFinding         `json:"warnings"`
	Volumes          []StackArchiveImportVolume `json:"volumes"`
	ProjectBytes     int64                      `json:"projectBytes"`
	VolumeBytes      int64                      `json:"volumeBytes"`
	StacksFreeBytes  int64                      `json:"stacksFreeBytes" doc:"Free bytes of the environment's stacks volume (-1: unknown)."`
	VolumesFreeBytes int64                      `json:"volumesFreeBytes" doc:"Free bytes of its Docker data root (-1: unknown)."`
}

type stackArchiveImportPreviewOutput struct{ Body StackArchiveImportPreview }

func newArchiveImportPreview(p stackarchives.ImportPlan) StackArchiveImportPreview {
	out := StackArchiveImportPreview{ArchiveID: p.ArchiveID, EnvironmentID: p.EnvironmentID, Name: p.Name, Allowed: p.Allowed(),
		Blockers: findings(p.Blockers), Warnings: findings(p.Warnings), Volumes: []StackArchiveImportVolume{}, ProjectBytes: p.ProjectBytes,
		VolumeBytes: p.VolumeBytes, StacksFreeBytes: p.StacksFree, VolumesFreeBytes: p.VolumesFree}
	for _, v := range p.Volumes {
		out.Volumes = append(out.Volumes, StackArchiveImportVolume{Key: v.Key, Source: v.Source, Name: v.Name, Bytes: v.Bytes})
	}
	return out
}

type stackArchivesAPI struct {
	svc    StackArchiveService
	stacks *stacksAPI
	authz  authz.Authorizer
	deps   Deps
}

func (h *stackArchivesAPI) available() error {
	if h.svc == nil {
		return Unavailable(CodeUnavailable, "the stack archive service is not available")
	}
	return nil
}

func archiveErr(err error) error {
	var be *stackarchives.BlockedError
	var ae *stackarchives.AgentError
	switch {
	case errors.As(err, &be):
		var details []ErrorDetail
		for _, b := range be.Blockers {
			details = append(details, ErrorDetail{Field: "check." + b.Code, Message: b.Message})
		}
		return NewError(http.StatusConflict, CodeStackArchiveBlocked, "the check has blockers; check again for details", details...)
	case errors.Is(err, stackarchives.ErrExportNotFound):
		return NotFound("archive not found (archives are kept for a day)")
	case errors.Is(err, stackarchives.ErrUploadNotFound):
		return NotFound("uploaded archive not found (uploads are kept for a day)")
	case errors.Is(err, stackarchives.ErrInvalid):
		return NewError(http.StatusUnprocessableEntity, CodeStackArchiveInvalid, err.Error())
	case errors.Is(err, stackarchives.ErrUploadTooLarge):
		return NewError(http.StatusRequestEntityTooLarge, CodePayloadTooLarge, err.Error())
	case errors.Is(err, stackarchives.ErrTooManyUploads):
		return Conflict(CodeTooManyStackArchives, fmt.Sprintf("you have %d uploaded archives in use or still arriving; wait for one to finish", stackarchives.MaxUploadsPerUser))
	case errors.Is(err, stackarchives.ErrNoSpace):
		return NewError(http.StatusInsufficientStorage, CodeManagerSpace, err.Error())
	case errors.Is(err, stackarchives.ErrUploadIncomplete):
		return NewError(http.StatusBadRequest, CodeUploadIncomplete, "the upload ended early; nothing was kept")
	case errors.Is(err, stackarchives.ErrUploadInUse):
		return Conflict(CodeStackArchiveInUse, "a stack is being created from this archive")
	case errors.As(err, &ae):
		switch {
		case ae.Offline:
			return NewError(http.StatusServiceUnavailable, CodeEnvironmentOffline, "the environment's agent is offline").WithRetryable(true)
		case ae.Timeout:
			return NewError(http.StatusGatewayTimeout, CodeTimeout, "the environment's agent did not answer in time")
		}
		return NewError(http.StatusBadGateway, CodeEngineError, ae.Error())
	}
	return stackErr(err)
}

// exportStack requires stack.export plus reading the stack's files and
// Compose definition (the archive holds them).
func (h *stackArchivesAPI) exportStack(ctx context.Context, id string) (authz.Checker, authz.Principal, domain.Stack, error) {
	c, p, st, v, err := h.stacks.requireStack(ctx, id, CapStackExport)
	if err != nil {
		return c, p, st, err
	}
	for _, cp := range []Capability{CapStackFilesDownload, CapStackDefinitionRead} {
		if !v.Has(string(cp)) {
			return c, p, st, Forbidden("not permitted: " + string(cp))
		}
	}
	return c, p, st, h.available()
}

// mayDownload reports whether c may download a volume's files.
func mayDownload(c authz.Checker, st domain.Stack) func(string) bool {
	return func(name string) bool {
		return c.Can("volume.files.download", authz.Resource{Type: catalog.TypeVolume, ID: name, EnvironmentID: st.EnvironmentID,
			Parents: []authz.ResourceRef{{Type: catalog.TypeStack, ID: st.ID}}}).Allowed
	}
}

func (h *stackArchivesAPI) previewExport(ctx context.Context, in *stackExportPreviewInput) (*stackExportPreviewOutput, error) {
	c, _, st, err := h.exportStack(ctx, in.StackID)
	if err != nil {
		return nil, err
	}
	may := mayDownload(c, st)
	plan, err := h.svc.PreviewExport(ctx, st, stackarchives.ExportRequest{ExcludeVolumes: in.Body.ExcludeVolumes, MayDownload: may})
	if err != nil {
		return nil, archiveErr(err)
	}
	// Only an archive the caller may download (its volumes) is offered.
	if plan.Latest != nil && slices.ContainsFunc(plan.Latest.VolumeNames, func(v string) bool { return !may(v) }) {
		plan.Latest = nil
	}
	return &stackExportPreviewOutput{Body: newExportPreview(plan)}, nil
}

func (h *stackArchivesAPI) startExport(ctx context.Context, in *stackExportInput) (*JobAccepted, error) {
	c, p, st, err := h.exportStack(ctx, in.StackID)
	if err != nil {
		return nil, err
	}
	audit.SetDetail(ctx, "excludedVolumeCount", len(in.Body.ExcludeVolumes))
	j, err := h.svc.StartExport(ctx, p, st, stackarchives.ExportRequest{ExcludeVolumes: in.Body.ExcludeVolumes,
		TimeoutSeconds: in.Body.TimeoutSeconds, IdempotencyKey: in.IdempotencyKey, MayDownload: mayDownload(c, st)})
	if err != nil {
		return nil, archiveErr(err)
	}
	audit.SetDetail(ctx, "exportId", j.ID)
	return Accepted(j), nil
}

func (h *stackArchivesAPI) download(ctx context.Context, in *stackExportDownloadInput) (*huma.StreamResponse, error) {
	c, _, st, err := h.exportStack(ctx, in.StackID)
	if err != nil {
		return nil, err
	}
	audit.SetDetail(ctx, "exportId", in.ExportID)
	f, err := h.svc.Export(ctx, st.ID, in.ExportID)
	if err != nil {
		return nil, archiveErr(err)
	}
	// The archive holds the data of its volumes: whoever downloads it (not
	// only who exported it) must be allowed to download their files.
	may := mayDownload(c, st)
	for _, v := range f.VolumeNames {
		if !may(v) {
			return nil, Forbidden("not permitted: volume.files.download on " + v)
		}
	}
	start, length, partial, err := byteRange(in.Range, f.Size)
	if err != nil {
		return nil, NewError(http.StatusRequestedRangeNotSatisfiable, CodeRangeNotSatisfiable, "the range is outside the archive").
			WithHeader("Content-Range", fmt.Sprintf("bytes */%d", f.Size))
	}
	file, err := h.svc.OpenExport(f)
	if err != nil {
		return nil, archiveErr(err)
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, Internal(err)
	}
	audit.SetDetail(ctx, "bytes", length)
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		defer func() { _ = file.Close() }()
		hctx.SetHeader("Content-Type", "application/gzip")
		hctx.SetHeader("Content-Disposition", contentDisposition(f.FileName))
		hctx.SetHeader("X-Content-Type-Options", "nosniff")
		hctx.SetHeader("Accept-Ranges", "bytes")
		hctx.SetHeader("ETag", ETag(f.SHA256))
		hctx.SetHeader("Content-Length", strconv.FormatInt(length, 10))
		status := http.StatusOK
		if partial {
			status = http.StatusPartialContent
			hctx.SetHeader("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, f.Size))
		}
		hctx.SetStatus(status)
		if _, err := io.CopyN(hctx.BodyWriter(), file, length); err != nil {
			// The client went away or the file broke: never a "complete"
			// truncated download.
			audit.SetErrorClass(hctx.Context(), "aborted")
			panic(http.ErrAbortHandler)
		}
	}}, nil
}

// requireCreateAnywhere requires stack.create in at least one environment
// (an upload is not tied to an environment yet).
func (h *stackArchivesAPI) requireCreateAnywhere(ctx context.Context) (authz.Principal, error) {
	c, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return p, err
	}
	if h.deps.Agents == nil {
		return p, Unavailable(CodeUnavailable, "the environment service is not available")
	}
	envs, err := h.deps.Agents.ListEnvironments(ctx, domain.EnvironmentFilter{Statuses: []domain.EnvironmentStatus{domain.EnvironmentActive}, Limit: 500})
	if err != nil {
		return p, Internal(err)
	}
	for _, e := range envs {
		if c.Can(string(CapStackCreate), authz.InEnvironment(catalog.TypeStack, e.ID)).Allowed {
			return p, h.available()
		}
	}
	return p, Forbidden("not permitted: " + string(CapStackCreate))
}

// archiveBodyKey carries the raw request body of the upload.
type archiveBodyKey struct{}

func archiveBody(ctx huma.Context, next func(huma.Context)) {
	next(huma.WithValue(ctx, archiveBodyKey{}, ctx.BodyReader()))
}

func (h *stackArchivesAPI) upload(ctx context.Context, in *stackArchiveUploadInput) (*stackArchiveOutput, error) {
	p, err := h.requireCreateAnywhere(ctx)
	if err != nil {
		return nil, err
	}
	if ct, _, _ := mime.ParseMediaType(ctxHeader(ctx, "Content-Type")); ct != "" && ct != "application/octet-stream" && ct != "application/gzip" &&
		ct != "application/x-gzip" && ct != "application/x-tar" {
		return nil, NewError(http.StatusUnsupportedMediaType, CodeUnsupportedMediaType, "send the archive as application/octet-stream or application/gzip")
	}
	if ctxHeader(ctx, "Content-Length") == "" || in.ContentLength <= 0 {
		return nil, NewError(http.StatusLengthRequired, CodeLengthRequired, "uploads need a Content-Length")
	}
	if maxSize := h.svc.MaxSize(); in.ContentLength > maxSize {
		return nil, NewError(http.StatusRequestEntityTooLarge, CodePayloadTooLarge, fmt.Sprintf("stack archives are limited to %s", humanize.Bytes(maxSize)))
	}
	body, _ := ctx.Value(archiveBodyKey{}).(io.Reader)
	if body == nil {
		return nil, Internal(errors.New("upload body reader missing"))
	}
	audit.SetDetail(ctx, "bytes", in.ContentLength)
	u, err := h.svc.StoreUpload(ctx, p, in.ContentLength, body)
	if err != nil {
		if errors.Is(err, stackarchives.ErrInvalid) {
			audit.SetErrorClass(ctx, CodeStackArchiveInvalid)
		}
		return nil, archiveErr(err)
	}
	audit.SetDetail(ctx, "archiveId", u.ID)
	audit.SetDetail(ctx, "volumeCount", len(u.Manifest.Volumes))
	return &stackArchiveOutput{Body: newStackArchive(u)}, nil
}

func (h *stackArchivesAPI) principal(ctx context.Context) (authz.Principal, error) {
	_, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return p, err
	}
	return p, h.available()
}

func (h *stackArchivesAPI) get(ctx context.Context, in *stackArchivePath) (*stackArchiveOutput, error) {
	p, err := h.principal(ctx)
	if err != nil {
		return nil, err
	}
	u, err := h.svc.Upload(p, in.ArchiveID)
	if err != nil {
		return nil, archiveErr(err)
	}
	return &stackArchiveOutput{Body: newStackArchive(u)}, nil
}

func (h *stackArchivesAPI) remove(ctx context.Context, in *stackArchivePath) (*struct{}, error) {
	p, err := h.principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.DeleteUpload(p, in.ArchiveID); err != nil {
		return nil, archiveErr(err)
	}
	return nil, nil
}

// importAccess requires what creating a stack from the archive needs in
// the environment: stack.create, volume.create when it has volumes and
// stack.deploy to deploy it.
func (h *stackArchivesAPI) importAccess(ctx context.Context, archiveID string, b StackArchiveImportBody) (authz.Principal, stackarchives.Upload, error) {
	p, err := h.stacks.requireInEnvironment(ctx, b.EnvironmentID, CapStackCreate)
	if err != nil {
		return p, stackarchives.Upload{}, err
	}
	if err := h.available(); err != nil {
		return p, stackarchives.Upload{}, err
	}
	u, err := h.svc.Upload(p, archiveID)
	if err != nil {
		return p, u, archiveErr(err)
	}
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return p, u, err
	}
	var checks []migrations.Check
	for _, ch := range stackarchives.ImportChecks(b.EnvironmentID, len(u.Manifest.Volumes) > 0, b.Deploy) {
		checks = append(checks, migrations.Check(ch))
	}
	return p, u, destination(c, b.EnvironmentID, checks)
}

func (h *stackArchivesAPI) previewImport(ctx context.Context, in *stackArchiveImportPreviewInput) (*stackArchiveImportPreviewOutput, error) {
	p, _, err := h.importAccess(ctx, in.ArchiveID, in.Body)
	if err != nil {
		return nil, err
	}
	plan, err := h.svc.PreviewImport(ctx, p, in.ArchiveID, in.Body.request())
	if err != nil {
		return nil, archiveErr(err)
	}
	return &stackArchiveImportPreviewOutput{Body: newArchiveImportPreview(plan)}, nil
}

func (h *stackArchivesAPI) startImport(ctx context.Context, in *stackArchiveImportInput) (*JobAccepted, error) {
	p, u, err := h.importAccess(ctx, in.ArchiveID, in.Body)
	if err != nil {
		return nil, err
	}
	audit.SetDetail(ctx, "archiveId", in.ArchiveID)
	audit.SetDetail(ctx, "project", in.Body.Name)
	audit.SetDetail(ctx, "volumeCount", len(u.Manifest.Volumes))
	st, j, err := h.svc.StartImport(ctx, p, in.ArchiveID, in.Body.request())
	if err != nil {
		return nil, archiveErr(err)
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeStack, ID: st.ID, EnvironmentID: st.EnvironmentID})
	return Accepted(j), nil
}

func registerStackArchives(a huma.API, deps Deps) {
	h := &stackArchivesAPI{svc: deps.StackArchives, authz: authz.OrDenyAll(deps.Authorizer), deps: deps,
		stacks: &stacksAPI{svc: deps.Stacks, authz: authz.OrDenyAll(deps.Authorizer), deps: deps}}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
		http.StatusUnprocessableEntity, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout}
	one := BasePath + "/stacks/{stackId}"
	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-export-preview", Method: http.MethodPost, Path: one + "/export-previews",
		Summary: "Check a stack export",
		Description: "Computes what exporting the stack as an archive does, before anything stops: the named volumes and whether " +
			"their data is included (plain local volumes; external volumes, volumes with driver options or other drivers, anonymous " +
			"volumes and bind mounts outside the project folder are not), the sizes against the archive limit " +
			"(DOCKER_MANAGER_STACK_ARCHIVE_MAX_MB) and the manager's free space, the services that stop and the downtime, and the " +
			"newest archive of the stack still available. Needs stack.export, stack.files.download and stack.definition.read; a " +
			"volume the caller may not download (volume.files.download) is marked and must be left out. Changes nothing.",
		Tags: []string{tagStacks}, Errors: errs,
	}, Capability: CapStackExport, Scope: ScopeResource, AuditAction: "stack.export.preview"}, h.previewExport)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-export", Method: http.MethodPost, Path: one + "/exports", Summary: "Export a stack as an archive",
		Description: "Re-runs the check (409 stack_archive_blocked with the blockers in details) and starts a stack.export job (202; " +
			"the export ID is the job ID). The stack's running services stop, the project folder and the included volumes are read " +
			"from the host (checksummed per chunk and as a whole) into one tar.gz on the manager, and the services start again. The " +
			"archive is kept for a day (GET .../exports/{exportId}).",
		Tags: []string{tagStacks}, Errors: errs, DefaultStatus: http.StatusAccepted,
	}, Capability: CapStackExport, Scope: ScopeResource, Idempotency: IdempotencyJob}, h.startExport)

	dl := huma.Operation{
		OperationID: "download-stack-export", Method: http.MethodGet, Path: one + "/exports/{exportId}",
		Summary: "Download a stack archive",
		Description: "Streams a finished export's archive (application/gzip, Content-Length, ETag = its SHA-256, a single Range: " +
			"206 or 416). 404 once it expired (a day after the export). The archive holds the stack's files with their .env values " +
			"and the volumes' data: keep it safe.",
		Tags: []string{tagStacks}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
			http.StatusRequestedRangeNotSatisfiable},
	}
	dl.Responses = map[string]*huma.Response{
		"200": {Description: "The archive.", Content: map[string]*huma.MediaType{"application/gzip": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}},
		"206": {Description: "The requested range.", Content: map[string]*huma.MediaType{"application/gzip": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}},
	}
	Register(a, Operation{Operation: dl, Capability: CapStackExport, Scope: ScopeResource, Audit: AuditAlways}, h.download)

	up := huma.Operation{
		OperationID: "create-stack-archive", Method: http.MethodPost, Path: BasePath + "/stack-archives",
		Summary: "Upload a stack archive", DefaultStatus: http.StatusCreated,
		Description: "Uploads an archive written by a stack export (raw body, Content-Length required; 413 above " +
			"DOCKER_MANAGER_STACK_ARCHIVE_MAX_MB). It is stored on the manager and validated on the way (422 invalid_stack_archive), " +
			"nothing reaches a host. Create a stack from it with POST /stack-archives/{archiveId}/imports within a day; only the " +
			"uploader sees it. Needs stack.create in at least one environment; at most five uploads per user wait at once: " +
			"the oldest one no stack is being created from makes room (409 too_many_stack_archives when none can); 507 manager_space when the manager's disk is too full. The reverse proxy's body limit " +
			"must allow the archive's size.",
		Tags: []string{tagStacks}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict,
			http.StatusLengthRequired, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity,
			http.StatusInsufficientStorage, http.StatusBadRequest},
		Middlewares: huma.Middlewares{captureHeaders, archiveBody},
	}
	up.RequestBody = &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{
		"application/octet-stream": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}}
	Register(a, Operation{Operation: up, Capability: CapStackCreate, Scope: ScopeInstance}, h.upload)

	arch := BasePath + "/stack-archives/{archiveId}"
	Register(a, Operation{Operation: huma.Operation{
		OperationID: "get-stack-archive", Method: http.MethodGet, Path: arch, Summary: "Get an uploaded stack archive",
		Description: "The caller's own upload: what the archive holds (stack, volumes, what was left out at export).",
		Tags:        []string{tagStacks}, Errors: []int{http.StatusUnauthorized, http.StatusNotFound},
	}, Capability: CapabilityAuthenticated, Scope: ScopeNone}, h.get)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "delete-stack-archive", Method: http.MethodDelete, Path: arch, Summary: "Discard an uploaded stack archive",
		Description: "Removes the caller's own upload from the manager (409 stack_archive_in_use while a stack is created from it).",
		Tags:        []string{tagStacks}, Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict},
		DefaultStatus: http.StatusNoContent,
	}, Capability: CapabilityAuthenticated, Scope: ScopeNone}, h.remove)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-archive-import-preview", Method: http.MethodPost, Path: arch + "/import-previews",
		Summary: "Check creating a stack from an archive",
		Description: "Checks the environment before anything is written: the stack name and Compose project, the folder, containers, " +
			"volumes and networks the stack would create (names that follow the project follow the new name), published ports, " +
			"external networks and volumes, free space, a project name pinned by the Compose file, and images built on the source. " +
			"Needs stack.create in the environment, volume.create when the archive has volumes and stack.deploy to deploy. " +
			"Changes nothing.",
		Tags: []string{tagStacks}, Errors: errs,
	}, Capability: CapStackCreate, Scope: ScopeEnvironment, AuditAction: "stack.import_archive.preview"}, h.previewImport)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-stack-archive-import", Method: http.MethodPost, Path: arch + "/imports",
		Summary: "Create a stack from an archive", DefaultStatus: http.StatusAccepted,
		Description: "Re-runs the check (409 stack_archive_blocked) and starts a stack.import_archive job (202). The stack is " +
			"created at once (the job's target, undeployed); the job copies the project folder into a new folder of the " +
			"environment's stacks volume, reads the Compose definition back as the stack's first revision, creates each volume as " +
			"Compose would for the new stack and fills it, and queues a stack.deploy job when asked (the stack is kept from then on: " +
			"a deploy that cannot be queued fails the job but undoes nothing). A failure before that removes what the job wrote and " +
			"forgets the stack. 501 agent_unsupported for older agents.",
		Tags: []string{tagStacks}, Errors: append(errs, http.StatusNotImplemented),
	}, Capability: CapStackCreate, Scope: ScopeEnvironment, Idempotency: IdempotencyStored}, h.startImport)
}
