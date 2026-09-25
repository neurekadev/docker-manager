package api

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/streammux"
)

// Scoped file manager (#15): the same routes under
// /api/v1/stacks/{stackId}/files… and
// /api/v1/environments/{environmentId}/volumes/{volumeId}/files…
// (docs/api/files.md). Capabilities are per root type (stack.files.*,
// volume.files.*, #17); a stack's Compose sources (compose.yaml, override
// files, .env at the project root) additionally need
// stack.definition.read / stack.definition.write.

const tagFiles = "Files"

// File error codes (#15).
const (
	CodeFileExists             = "file_exists"
	CodeFileConflict           = "file_conflict"
	CodeFileTypeMismatch       = "file_type_mismatch"
	CodeFileUnsupported        = "file_unsupported"
	CodeVolumeFilesUnsupported = "volume_files_unsupported"
	CodeContentDigestMismatch  = "content_digest_mismatch"
	CodeLengthRequired         = "length_required"
	CodeRangeNotSatisfiable    = "range_not_satisfiable"
)

// File manager limits.
const (
	// DefaultMaxUpload bounds one uploaded file (DOCKYARD_FILES_MAX_UPLOAD).
	DefaultMaxUpload = 2 << 30
	// MaxFilesPerJob bounds the paths of one copy/move/delete/archive/
	// metadata request.
	MaxFilesPerJob = 256
)

// FileRoot is a resolved file scope: the agent-protocol scope and the
// environment whose agent serves it.
type FileRoot struct {
	Scope         protocol.FileScope
	EnvironmentID string
}

// FilesService is the manager's file service (internal/manager/files).
// It does not authorize; the handlers here do.
type FilesService interface {
	// StackRoot resolves a stack (domain.ErrFileScopeNotFound).
	StackRoot(ctx context.Context, stackID string) (FileRoot, error)
	List(ctx context.Context, r FileRoot, in protocol.FilesListInput) (protocol.FilesListOutput, error)
	Stat(ctx context.Context, r FileRoot, path string, etag bool) (protocol.FileEntry, error)
	Read(ctx context.Context, r FileRoot, in protocol.FilesReadInput) (protocol.FilesReadOutput, error)
	Write(ctx context.Context, r FileRoot, in protocol.FilesWriteInput) (protocol.FileEntry, error)
	Mkdir(ctx context.Context, r FileRoot, in protocol.FilesMkdirInput) (protocol.FileEntry, error)
	Preview(ctx context.Context, r FileRoot, in protocol.FilesPreviewInput) (protocol.FilesPreviewOutput, error)
	Download(ctx context.Context, r FileRoot, in protocol.FilesDownloadInput) (*streammux.Stream, error)
	// StreamError maps an error read from a download stream.
	StreamError(err error) error
	Upload(ctx context.Context, r FileRoot, in protocol.FilesUploadInput, body io.Reader) (protocol.FilesUploadResult, error)
	StartJob(ctx context.Context, r FileRoot, kind domain.JobKind, p authz.Principal, in protocol.FilesJobInput, idempotencyKey string) (domain.Job, error)
}

// definitionRE matches Compose source file names at a project root.
var definitionRE = regexp.MustCompile(`^(docker-)?compose(\.[^/]+)?\.ya?ml$`)

// IsDefinitionFile reports whether a root-relative path is one of a
// stack's Compose sources: compose.yaml/.yml, docker-compose.*, override
// files (compose.<name>.yaml) and .env at the project root. They need the
// stack.definition.* capabilities in addition to the file capabilities
// (#17), and saving them records a stack revision (#7).
func IsDefinitionFile(rel string) bool {
	if strings.Contains(rel, "/") {
		return false
	}
	return rel == ".env" || definitionRE.MatchString(rel)
}

// FileEntry is the metadata of one file, directory or symlink.
type FileEntry struct {
	Name       string    `json:"name" example:"compose.yaml"`
	Path       string    `json:"path" example:"config/app.env" doc:"Root-relative, slash-separated; \".\" is the root."`
	Type       string    `json:"type" enum:"file,dir,symlink,other"`
	Size       int64     `json:"size" doc:"Bytes (regular files; 0 otherwise)."`
	Mode       string    `json:"mode" example:"0644" doc:"Octal permission and special bits, host semantics."`
	UID        uint32    `json:"uid" doc:"Numeric host owner."`
	GID        uint32    `json:"gid" doc:"Numeric host group."`
	ModifiedAt time.Time `json:"modifiedAt"`
	Links      uint64    `json:"links,omitempty" doc:"Hard link count. Regular files with more than one link are listed, but their content is not served (another name may lie outside the root)."`
	LinkTarget string    `json:"linkTarget,omitempty" doc:"Symlink target as stored (never followed out of the root)."`
	LinkStatus string    `json:"linkStatus,omitempty" enum:"inside,outside,dangling,loop" doc:"Where a symlink resolves; outside targets are never followed."`
	ETag       string    `json:"etag,omitempty" doc:"Content revision of a regular file (strong ETag, quoted): send it as If-Match when saving."`
}

func newFileEntry(e protocol.FileEntry) FileEntry {
	out := FileEntry{Name: e.Name, Path: e.Path, Type: e.Type, Size: e.Size, Mode: fmt.Sprintf("%04o", e.Mode), UID: e.UID, GID: e.GID,
		ModifiedAt: e.ModTime, Links: e.Links, LinkTarget: e.LinkTarget, LinkStatus: e.LinkStatus}
	if e.ETag != "" {
		out.ETag = ETag(e.ETag)
	}
	return out
}

// FileListing is one page of a directory listing.
type FileListing struct {
	Dir        FileEntry   `json:"dir" doc:"The listed directory."`
	Items      []FileEntry `json:"items"`
	NextCursor string      `json:"nextCursor,omitempty"`
	Total      int64       `json:"total" doc:"Entries matching the filters."`
	Truncated  bool        `json:"truncated" doc:"The directory holds more than 100 000 entries; the listing covers the first ones read."`
}

// FileContent is a bounded slice of a regular file.
type FileContent struct {
	Entry FileEntry `json:"entry"`
	// Content is set for UTF-8 text, ContentBase64 for binary data.
	Content       string `json:"content,omitempty" doc:"UTF-8 text (absent for binary files)."`
	ContentBase64 string `json:"contentBase64,omitempty" doc:"Binary content, base64 (only for binary files)."`
	Binary        bool   `json:"binary" doc:"The file is not UTF-8 text (NUL bytes or invalid UTF-8): offer a download instead of the editor."`
	Truncated     bool   `json:"truncated" doc:"The file continues after the returned bytes (at most 512 KiB per request): not editable in place."`
	Offset        int64  `json:"offset"`
}

// FileConflictDTO is a destination that already exists.
type FileConflictDTO struct {
	Source      string    `json:"source"`
	Destination string    `json:"destination"`
	Existing    FileEntry `json:"existing"`
}

// FileImpactDTO counts what an operation touches.
type FileImpactDTO struct {
	Entries   int   `json:"entries"`
	Files     int   `json:"files"`
	Dirs      int   `json:"dirs"`
	Symlinks  int   `json:"symlinks"`
	Other     int   `json:"other"`
	Bytes     int64 `json:"bytes"`
	Truncated bool  `json:"truncated" doc:"Counting stopped at 100 000 entries."`
}

// FilePreview is the answer of a conflict preview.
type FilePreview struct {
	Conflicts          []FileConflictDTO `json:"conflicts"`
	ConflictsTruncated bool              `json:"conflictsTruncated" doc:"More than 1000 conflicts; only the first are listed."`
	Impact             FileImpactDTO     `json:"impact"`
}

// FileUploadResult is the answer of an upload.
type FileUploadResult struct {
	Entry   FileEntry `json:"entry" doc:"The written file (its name may differ with conflict=keep_both)."`
	Skipped bool      `json:"skipped" doc:"conflict=skip and the name existed: nothing was written."`
}

// FilesStackScope is the path scope of the stack file routes.
type FilesStackScope struct {
	StackID string `path:"stackId" maxLength:"64" doc:"Stack ID."`
}

func (s *FilesStackScope) scopeRef() fileScopeRef {
	return fileScopeRef{kind: protocol.ScopeStack, id: s.StackID}
}

// FilesVolumeScope is the path scope of the volume file routes.
type FilesVolumeScope struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	VolumeID      string `path:"volumeId" maxLength:"255" doc:"Docker volume name."`
}

func (s *FilesVolumeScope) scopeRef() fileScopeRef {
	return fileScopeRef{kind: protocol.ScopeVolume, id: s.VolumeID, env: s.EnvironmentID}
}

type fileScopeRef struct{ kind, id, env string }

// Common inputs (embedded next to a scope).

// FilesPathQuery is the root-relative path query parameter.
type FilesPathQuery struct {
	Path string `query:"path" maxLength:"4096" doc:"Root-relative path (slash-separated, percent-encoded in the URL; no leading /, no . or .. segments). Empty or . is the root."`
}

// FilesListQuery is the input of the listing routes.
type FilesListQuery struct {
	FilesPathQuery
	PageParams
	Sort   string `query:"sort" enum:"name,-name,size,-size,modified,-modified,type,-type" doc:"Order (default name; ties by name; type puts directories first)."`
	Q      string `query:"q" maxLength:"256" doc:"Only names containing this text (case-insensitive)."`
	Hidden bool   `query:"hidden" doc:"Include dot files."`
}

func (q *FilesListQuery) common() *FilesListQuery { return q }

// FilesContentQuery is the input of the content read routes.
type FilesContentQuery struct {
	FilesPathQuery
	Offset int64 `query:"offset" minimum:"0" doc:"Byte offset for paging through large files."`
}

func (q *FilesContentQuery) common() *FilesContentQuery { return q }

// FilesReplaceInput is the input of the content save routes.
type FilesReplaceInput struct {
	FilesPathQuery
	IfMatchParam
	IfNoneMatch string `header:"If-None-Match" maxLength:"8" doc:"* creates the file only if it does not exist (412 otherwise)."`
	Body        struct {
		Content       *string `json:"content,omitempty" doc:"New content as UTF-8 text (at most 512 KiB)."`
		ContentBase64 *string `json:"contentBase64,omitempty" doc:"New content as base64 (binary; at most 512 KiB decoded)."`
	}
}

func (q *FilesReplaceInput) common() *FilesReplaceInput { return q }

// FilesDownloadQuery is the input of the download routes.
type FilesDownloadQuery struct {
	Paths  []string `query:"path,explode" maxItems:"256" doc:"Paths to download (repeat the parameter). One regular file downloads raw unless format is set; several paths or a directory download as an archive."`
	Format string   `query:"format" enum:"zip,tar.gz" doc:"Archive format (default zip for archives)."`
	Range  string   `header:"Range" maxLength:"128" doc:"Single byte range (bytes=start-end) for resuming a single-file download."`
}

func (q *FilesDownloadQuery) common() *FilesDownloadQuery { return q }

// FilesCreateEntryInput is the input of the entry creation routes.
type FilesCreateEntryInput struct {
	Body struct {
		Path          string  `json:"path,omitempty" maxLength:"4096" doc:"Root-relative path of the new entry (its parent must exist)."`
		Type          string  `json:"type,omitempty" enum:"file,dir"`
		Content       *string `json:"content,omitempty" doc:"Initial content of a new file (UTF-8)."`
		ContentBase64 *string `json:"contentBase64,omitempty"`
	}
}

func (q *FilesCreateEntryInput) common() *FilesCreateEntryInput { return q }

// FilesUploadQuery is the input of the upload routes (the body is streamed).
type FilesUploadQuery struct {
	FilesPathQuery
	Name          string `query:"name" maxLength:"255" doc:"File name in the target directory (path)."`
	Conflict      string `query:"conflict" enum:"overwrite,skip,keep_both" doc:"What to do when the name exists, instead of If-Match/If-None-Match."`
	IfMatch       string `header:"If-Match" maxLength:"1024" doc:"Replace exactly this revision (ETag)."`
	IfNoneMatch   string `header:"If-None-Match" maxLength:"8" doc:"* creates only (412 when the name exists)."`
	ContentLength int64  `header:"Content-Length" doc:"Required (411 otherwise); at most DOCKYARD_FILES_MAX_UPLOAD (default 2 GiB, 413)."`
	ContentSHA256 string `header:"X-DockYard-Content-SHA256" maxLength:"64" doc:"Optional hex SHA-256 of the body, verified before the file is committed (422 content_digest_mismatch)."`
}

func (q *FilesUploadQuery) common() *FilesUploadQuery { return q }

// FilesPreviewInput is the input of the conflict preview routes.
type FilesPreviewInput struct {
	Body struct {
		Operation   string   `json:"operation,omitempty" enum:"copy,move,delete,upload,extract,archive,metadata"`
		Paths       []string `json:"paths,omitempty" maxItems:"1000" doc:"Sources (copy, move, delete, metadata, archive) or the archive (extract)."`
		Destination string   `json:"destination,omitempty" maxLength:"4096" doc:"Target directory (copy, move, upload, extract) or archive file (archive)."`
		Names       []string `json:"names,omitempty" maxItems:"1000" doc:"File names to upload into destination."`
		Recursive   bool     `json:"recursive,omitempty" doc:"metadata: count recursively."`
	}
}

func (q *FilesPreviewInput) common() *FilesPreviewInput { return q }

// FilesTransferInput is the input of the copy and move routes.
type FilesTransferInput struct {
	IdempotencyKeyParam
	Body struct {
		Paths       []string `json:"paths,omitempty" maxItems:"256" doc:"Sources (root-relative)."`
		Destination string   `json:"destination,omitempty" maxLength:"4096" doc:"Target directory."`
		Name        string   `json:"name,omitempty" maxLength:"255" doc:"New name of a single source (rename: move into the source's own directory under this name)."`
		Conflict    string   `json:"conflict,omitempty" enum:"fail,overwrite,skip,keep_both" doc:"Per top-level item when the name exists in the destination (default fail: that item fails). Apply-to-all is this one setting; the UI asks per item and sends one request per decision group."`
	}
}

func (q *FilesTransferInput) common() *FilesTransferInput { return q }

// FilesDeletionInput is the input of the deletion routes.
type FilesDeletionInput struct {
	IdempotencyKeyParam
	Body struct {
		Paths []string `json:"paths,omitempty" maxItems:"256" doc:"Entries to delete, recursively (symlinks are removed, never followed). Preview first with a conflict preview of operation delete."`
	}
}

func (q *FilesDeletionInput) common() *FilesDeletionInput { return q }

// FilesArchiveInput is the input of the archive routes.
type FilesArchiveInput struct {
	IdempotencyKeyParam
	Body struct {
		Paths       []string `json:"paths,omitempty" maxItems:"256"`
		Destination string   `json:"destination,omitempty" maxLength:"4096" doc:"Archive file to create (root-relative)."`
		Format      string   `json:"format,omitempty" enum:"zip,tar.gz" doc:"Default zip."`
		Conflict    string   `json:"conflict,omitempty" enum:"fail,overwrite,keep_both"`
	}
}

func (q *FilesArchiveInput) common() *FilesArchiveInput { return q }

// FilesExtractionInput is the input of the extraction routes.
type FilesExtractionInput struct {
	IdempotencyKeyParam
	Body struct {
		Path        string `json:"path,omitempty" maxLength:"4096" doc:"The zip or tar.gz archive (root-relative)."`
		Destination string `json:"destination,omitempty" maxLength:"4096" doc:"Directory to extract into (created when missing)."`
		Conflict    string `json:"conflict,omitempty" enum:"fail,overwrite,skip,keep_both" doc:"Per entry when the name exists (default fail: the entry is reported and skipped)."`
	}
}

func (q *FilesExtractionInput) common() *FilesExtractionInput { return q }

// ChmodDTO sets permission bits.
type ChmodDTO struct {
	Mode    string `json:"mode" pattern:"^0?[0-7]{3}$" example:"0644" doc:"Octal permission bits (special bits such as setuid are refused)."`
	DirMode string `json:"dirMode,omitempty" pattern:"^0?[0-7]{3}$" example:"0755" doc:"Octal bits for directories (default: mode)."`
}

// ChownDTO sets the numeric owner and/or group.
type ChownDTO struct {
	UID *uint32 `json:"uid,omitempty" maximum:"2147483647"`
	GID *uint32 `json:"gid,omitempty" maximum:"2147483647"`
}

// FilesMetadataInput is the input of the chmod/chown routes.
type FilesMetadataInput struct {
	IdempotencyKeyParam
	Body struct {
		Paths     []string  `json:"paths,omitempty" maxItems:"256"`
		Recursive bool      `json:"recursive,omitempty" doc:"Apply below directories too (symlinks are never followed)."`
		Chmod     *ChmodDTO `json:"chmod,omitempty" doc:"Needs <root>.files.chmod."`
		Chown     *ChownDTO `json:"chown,omitempty" doc:"Needs <root>.files.chown."`
	}
}

func (q *FilesMetadataInput) common() *FilesMetadataInput { return q }

// Inputs per scope (the embedded scope provides scopeRef, the embedded
// common part common()).

type (
	listStackFilesInput struct {
		FilesStackScope
		FilesListQuery
	}
	listVolumeFilesInput struct {
		FilesVolumeScope
		FilesListQuery
	}
	getStackContentInput struct {
		FilesStackScope
		FilesContentQuery
	}
	getVolumeContentInput struct {
		FilesVolumeScope
		FilesContentQuery
	}
	replaceStackContentInput struct {
		FilesStackScope
		FilesReplaceInput
	}
	replaceVolumeContentInput struct {
		FilesVolumeScope
		FilesReplaceInput
	}
	downloadStackInput struct {
		FilesStackScope
		FilesDownloadQuery
	}
	downloadVolumeInput struct {
		FilesVolumeScope
		FilesDownloadQuery
	}
	createStackEntryInput struct {
		FilesStackScope
		FilesCreateEntryInput
	}
	createVolumeEntryInput struct {
		FilesVolumeScope
		FilesCreateEntryInput
	}
	uploadStackInput struct {
		FilesStackScope
		FilesUploadQuery
	}
	uploadVolumeInput struct {
		FilesVolumeScope
		FilesUploadQuery
	}
	previewStackInput struct {
		FilesStackScope
		FilesPreviewInput
	}
	previewVolumeInput struct {
		FilesVolumeScope
		FilesPreviewInput
	}
	transferStackInput struct {
		FilesStackScope
		FilesTransferInput
	}
	transferVolumeInput struct {
		FilesVolumeScope
		FilesTransferInput
	}
	deletionStackInput struct {
		FilesStackScope
		FilesDeletionInput
	}
	deletionVolumeInput struct {
		FilesVolumeScope
		FilesDeletionInput
	}
	archiveStackInput struct {
		FilesStackScope
		FilesArchiveInput
	}
	archiveVolumeInput struct {
		FilesVolumeScope
		FilesArchiveInput
	}
	extractionStackInput struct {
		FilesStackScope
		FilesExtractionInput
	}
	extractionVolumeInput struct {
		FilesVolumeScope
		FilesExtractionInput
	}
	metadataStackInput struct {
		FilesStackScope
		FilesMetadataInput
	}
	metadataVolumeInput struct {
		FilesVolumeScope
		FilesMetadataInput
	}
)

// Outputs.
type (
	fileListingOutput struct{ Body FileListing }
	fileContentOutput struct {
		ETagHeader
		Body FileContent
	}
	fileEntryOutput struct {
		ETagHeader
		Body FileEntry
	}
	filePreviewOutput struct{ Body FilePreview }
	fileUploadOutput  struct {
		ETagHeader
		Body FileUploadResult
	}
)

// filesAPI serves the file routes.
type filesAPI struct {
	svc       FilesService
	authz     authz.Authorizer
	maxUpload int64
}

// fileCtx is an authorized request on one root.
type fileCtx struct {
	root FileRoot
	res  authz.Resource
	c    authz.Checker
	p    authz.Principal
	kind string
	// unavailable: no file service (checked after authorization, so
	// callers without the grant still get 403/404).
	unavailable bool
}

// cap returns the root's capability key for a verb (stack.files.read).
func (f *fileCtx) capKey(verb string) string { return f.kind + ".files." + verb }

// can reports whether the caller holds the root capability for verb.
func (f *fileCtx) can(verb string) bool { return f.c.Can(f.capKey(verb), f.res).Allowed }

// require answers 403 unless every verb is granted.
func (f *fileCtx) require(verbs ...string) error {
	for _, v := range verbs {
		if !f.can(v) {
			return Forbidden("not permitted: " + f.capKey(v))
		}
	}
	if f.unavailable {
		return Unavailable(CodeUnavailable, "the file service is not available")
	}
	return nil
}

// requireDefinition checks stack.definition.read/write for operations on
// a stack's Compose sources.
func (f *fileCtx) requireDefinition(read, write bool) error {
	if f.kind != protocol.ScopeStack {
		return nil
	}
	if read && !f.c.Can("stack.definition.read", f.res).Allowed {
		return Forbidden("not permitted: stack.definition.read (compose.yaml, override files and .env)")
	}
	if write && !f.c.Can("stack.definition.write", f.res).Allowed {
		return Forbidden("not permitted: stack.definition.write (compose.yaml, override files and .env)")
	}
	return nil
}

// open resolves the root and the caller's view of it: 404 when the
// caller may not see it (or it does not exist), 401 without a principal.
func (h *filesAPI) open(ctx context.Context, ref fileScopeRef) (*fileCtx, error) {
	c, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	f := &fileCtx{c: c, p: p, kind: ref.kind, unavailable: h.svc == nil}
	switch ref.kind {
	case protocol.ScopeVolume:
		if !protocol.ValidVolumeName(ref.id) {
			return nil, NotFound("volume not found")
		}
		f.root = FileRoot{Scope: protocol.FileScope{Kind: protocol.ScopeVolume, ID: ref.id}, EnvironmentID: ref.env}
		f.res = authz.Resource{Type: catalog.TypeVolume, ID: ref.id, EnvironmentID: ref.env}
	case protocol.ScopeStack:
		if h.svc == nil {
			return nil, NotFound("stack not found")
		}
		r, err := h.svc.StackRoot(ctx, ref.id)
		if errors.Is(err, domain.ErrFileScopeNotFound) {
			return nil, NotFound("stack not found")
		}
		if err != nil {
			return nil, Internal(err)
		}
		f.root = r
		f.res = authz.Resource{Type: catalog.TypeStack, ID: ref.id, EnvironmentID: r.EnvironmentID}
	}
	if !authz.ViewOf(c, f.res).Visible() {
		return nil, NotFound(ref.kind + " not found")
	}
	return f, nil
}

// cleanPath validates a path parameter (422 on field).
func cleanPath(p, field string) (string, error) {
	rel, ok := protocol.CleanRelativePath(p)
	if !ok {
		return "", Invalid("invalid path", Field(field, "use a root-relative, slash-separated path without a leading /, . or .. segments, backslashes or control characters"))
	}
	return rel, nil
}

// cleanPaths validates a list of paths.
func cleanPaths(ps []string, field string, maxItems int) ([]string, error) {
	if len(ps) == 0 {
		return nil, Invalid("paths are required", Field(field, "at least one path"))
	}
	if len(ps) > maxItems {
		return nil, Invalid("too many paths", Field(field, fmt.Sprintf("at most %d", maxItems)))
	}
	out := make([]string, 0, len(ps))
	for i, p := range ps {
		rel, err := cleanPath(p, fmt.Sprintf("%s[%d]", field, i))
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, rel) {
			out = append(out, rel)
		}
	}
	return out, nil
}

// touchesDefinition reports whether any path is a Compose source or the
// root itself (whose download/archive/copy includes them).
func touchesDefinition(paths ...string) bool {
	for _, p := range paths {
		if p == "." || IsDefinitionFile(p) {
			return true
		}
	}
	return false
}

// fileErr maps a file service error. field names the path input the
// error is about; conflict412 turns a revision conflict into 412.
func fileErr(err error, field string, conflict412 bool) error {
	var fe *domain.FileError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrFileScopeNotFound):
		return NotFound("not found")
	case errors.Is(err, domain.ErrFileAgentOffline):
		return Unavailable(CodeUnavailable, "the environment's agent is offline")
	case errors.Is(err, domain.ErrFileAgentTimeout):
		return NewError(http.StatusGatewayTimeout, CodeTimeout, "the agent did not answer in time")
	case errors.As(err, &fe):
		switch fe.Code {
		case protocol.CodeNotFound:
			return NotFound(fe.Message)
		case protocol.CodeForbiddenPath:
			return Invalid("the path is not allowed", Field(field, fe.Message))
		case protocol.CodeConflict:
			if conflict412 {
				return PreconditionFailed("the file was changed since you loaded it", Field("header.If-Match", fe.Message))
			}
			return Conflict(CodeFileConflict, fe.Message)
		case protocol.CodeAlreadyExists:
			if conflict412 {
				return PreconditionFailed("the file already exists", Field("header.If-None-Match", fe.Message))
			}
			return Conflict(CodeFileExists, fe.Message)
		case protocol.CodeNotDirectory, protocol.CodeIsDirectory:
			return Conflict(CodeFileTypeMismatch, fe.Message)
		case protocol.CodeUnsupportedFile:
			return Conflict(CodeFileUnsupported, fe.Message)
		case protocol.CodeUnsupportedVolume:
			return Conflict(CodeVolumeFilesUnsupported, fe.Message)
		case protocol.CodeTooLarge:
			return NewError(http.StatusRequestEntityTooLarge, CodePayloadTooLarge, fe.Message)
		case protocol.CodeDigestMismatch:
			return NewError(http.StatusUnprocessableEntity, CodeContentDigestMismatch, fe.Message,
				Field("header.X-DockYard-Content-SHA256", "does not match the received bytes"))
		case protocol.CodeUnsupportedRequest, protocol.CodeUnsupportedStream:
			return NewError(http.StatusNotImplemented, CodeNotImplemented, "this environment's agent does not serve the file manager; upgrade it")
		case protocol.CodeInvalidFrame:
			return Invalid(fe.Message)
		case protocol.CodeDeadlineExceeded:
			return NewError(http.StatusGatewayTimeout, CodeTimeout, "the agent did not finish in time")
		case protocol.CodeEngineUnavailable, protocol.CodeBusy, protocol.CodeCancelled, protocol.CodeStreamLimit:
			return Unavailable(CodeUnavailable, fe.Message)
		}
		return Internal(err)
	}
	if je := JobErrorFor(err); !isInternal(je) {
		return je
	}
	return Internal(err)
}

func isInternal(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == CodeInternal
}

// decodeContent returns the body content of a write (text or base64).
func decodeContent(text, b64 *string, field string) ([]byte, error) {
	switch {
	case text != nil && b64 != nil:
		return nil, Invalid("send content or contentBase64, not both", Field(field, "one of content, contentBase64"))
	case text != nil:
		if len(*text) > protocol.MaxInlineContent {
			return nil, NewError(http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "content exceeds 512 KiB; upload the file instead")
		}
		return []byte(*text), nil
	case b64 != nil:
		b, err := base64.StdEncoding.DecodeString(*b64)
		if err != nil {
			return nil, Invalid("invalid base64", Field(field+"Base64", "not valid base64"))
		}
		if len(b) > protocol.MaxInlineContent {
			return nil, NewError(http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "content exceeds 512 KiB; upload the file instead")
		}
		return b, nil
	}
	return nil, nil
}

// ifMatchTags parses If-Match into the agent's ETag tokens ("*" kept).
func ifMatchTags(h string) []string {
	var out []string
	for _, tag := range strings.Split(h, ",") {
		tag = strings.TrimSpace(tag)
		switch {
		case tag == "*":
			out = append(out, "*")
		case strings.HasPrefix(tag, `"`) && strings.HasSuffix(tag, `"`) && len(tag) >= 2:
			out = append(out, strings.Trim(tag, `"`))
		}
	}
	return out
}

func (h *filesAPI) list(ctx context.Context, ref fileScopeRef, in *FilesListQuery) (*fileListingOutput, error) {
	f, err := h.open(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := f.require("read"); err != nil {
		return nil, err
	}
	rel, err := cleanPath(in.Path, "query.path")
	if err != nil {
		return nil, err
	}
	sortKey, desc := strings.TrimPrefix(in.Sort, "-"), strings.HasPrefix(in.Sort, "-")
	fingerprint := QueryFingerprint(ref.kind, ref.id, ref.env, rel, in.Sort, in.Q, strconv.FormatBool(in.Hidden))
	req := protocol.FilesListInput{Path: rel, Sort: sortKey, Desc: desc, Query: in.Q, Hidden: in.Hidden, Limit: in.PageLimit()}
	if in.Cursor != "" {
		var after protocol.FilesListCursor
		if err := DecodeCursorFor(in.Cursor, fingerprint, &after); err != nil {
			return nil, err
		}
		req.After = &after
	}
	out, err := h.svc.List(ctx, f.root, req)
	if err != nil {
		return nil, fileErr(err, "query.path", false)
	}
	body := FileListing{Dir: newFileEntry(out.Dir), Items: make([]FileEntry, 0, len(out.Entries)), Total: int64(out.Total), Truncated: out.Truncated}
	for _, e := range out.Entries {
		body.Items = append(body.Items, newFileEntry(e))
	}
	if out.Next != nil {
		if body.NextCursor, err = CursorFor(fingerprint, out.Next); err != nil {
			return nil, Internal(err)
		}
	}
	return &fileListingOutput{Body: body}, nil
}

func (h *filesAPI) getContent(ctx context.Context, ref fileScopeRef, in *FilesContentQuery) (*fileContentOutput, error) {
	f, err := h.open(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := f.require("read"); err != nil {
		return nil, err
	}
	rel, err := cleanPath(in.Path, "query.path")
	if err != nil {
		return nil, err
	}
	if err := f.requireDefinition(IsDefinitionFile(rel), false); err != nil {
		return nil, err
	}
	out, err := h.svc.Read(ctx, f.root, protocol.FilesReadInput{Path: rel, Offset: in.Offset})
	if err != nil {
		return nil, fileErr(err, "query.path", false)
	}
	body := FileContent{Entry: newFileEntry(out.Entry), Binary: out.Binary, Truncated: out.Truncated, Offset: in.Offset}
	if out.Binary || !utf8.Valid(out.Data) {
		body.ContentBase64 = base64.StdEncoding.EncodeToString(out.Data)
	} else {
		body.Content = string(out.Data)
	}
	audit.SetDetail(ctx, "path", rel)
	return &fileContentOutput{ETagHeader: ETagHeader{ETag: body.Entry.ETag}, Body: body}, nil
}

func (h *filesAPI) replaceContent(ctx context.Context, ref fileScopeRef, in *FilesReplaceInput) (*fileEntryOutput, error) {
	f, err := h.open(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := f.require("write"); err != nil {
		return nil, err
	}
	rel, err := cleanPath(in.Path, "query.path")
	if err != nil {
		return nil, err
	}
	if err := f.requireDefinition(false, IsDefinitionFile(rel)); err != nil {
		return nil, err
	}
	data, err := decodeContent(in.Body.Content, in.Body.ContentBase64, "body.content")
	if err != nil {
		return nil, err
	}
	w := protocol.FilesWriteInput{Path: rel, Data: data}
	switch {
	case strings.TrimSpace(in.IfNoneMatch) == "*" && in.IfMatch == "":
		w.CreateOnly = true
	case in.IfMatch != "":
		if w.IfMatch = ifMatchTags(in.IfMatch); len(w.IfMatch) == 0 {
			return nil, h.stale(ctx, f, rel)
		}
	default:
		return nil, PreconditionRequired("saving a file requires If-Match with the ETag you loaded (or If-None-Match: * to create it)",
			Field("header.If-Match", "send the file's ETag"))
	}
	audit.SetDetail(ctx, "path", rel)
	audit.SetDetail(ctx, "bytes", len(data))
	e, err := h.svc.Write(ctx, f.root, w)
	if err != nil {
		var fe *domain.FileError
		if errors.As(err, &fe) && fe.Code == protocol.CodeConflict {
			return nil, h.stale(ctx, f, rel)
		}
		return nil, fileErr(err, "query.path", true)
	}
	out := newFileEntry(e)
	return &fileEntryOutput{ETagHeader: ETagHeader{ETag: out.ETag}, Body: out}, nil
}

// stale answers 412 with the file's current ETag (when it still exists).
func (h *filesAPI) stale(ctx context.Context, f *fileCtx, rel string) error {
	e := PreconditionFailed("the file was changed since you loaded it: compare, reload or save as another name",
		Field("header.If-Match", "stale revision"))
	if cur, err := h.svc.Stat(ctx, f.root, rel, true); err == nil && cur.ETag != "" {
		e = e.WithHeader("ETag", ETag(cur.ETag))
	}
	return e
}

func (h *filesAPI) createEntry(ctx context.Context, ref fileScopeRef, in *FilesCreateEntryInput) (*fileEntryOutput, error) {
	f, err := h.open(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := f.require("write"); err != nil {
		return nil, err
	}
	rel, err := cleanPath(in.Body.Path, "body.path")
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return nil, Invalid("invalid path", Field("body.path", "the root already exists"))
	}
	if err := f.requireDefinition(false, IsDefinitionFile(rel)); err != nil {
		return nil, err
	}
	if in.Body.Type != protocol.FileTypeFile && in.Body.Type != protocol.FileTypeDir {
		return nil, Invalid("invalid type", Field("body.type", "file or dir"))
	}
	data, err := decodeContent(in.Body.Content, in.Body.ContentBase64, "body.content")
	if err != nil {
		return nil, err
	}
	if in.Body.Type == protocol.FileTypeDir && data != nil {
		return nil, Invalid("a directory has no content", Field("body.content", "only for files"))
	}
	audit.SetDetail(ctx, "path", rel)
	e, err := h.svc.Mkdir(ctx, f.root, protocol.FilesMkdirInput{Path: rel, Type: in.Body.Type, Data: data})
	if err != nil {
		return nil, fileErr(err, "body.path", false)
	}
	out := newFileEntry(e)
	return &fileEntryOutput{ETagHeader: ETagHeader{ETag: out.ETag}, Body: out}, nil
}

func (h *filesAPI) preview(ctx context.Context, ref fileScopeRef, in *FilesPreviewInput) (*filePreviewOutput, error) {
	f, err := h.open(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := f.require("read"); err != nil {
		return nil, err
	}
	b := in.Body
	req := protocol.FilesPreviewInput{Operation: b.Operation, Recursive: b.Recursive, Names: b.Names}
	if len(b.Paths) > 0 {
		if req.Paths, err = cleanPaths(b.Paths, "body.paths", protocol.MaxOperationPaths); err != nil {
			return nil, err
		}
	}
	if b.Destination != "" || b.Operation == protocol.FileOpUpload {
		if req.Destination, err = cleanPath(b.Destination, "body.destination"); err != nil {
			return nil, err
		}
	}
	for i, n := range b.Names {
		if !protocol.ValidFileName(n) {
			return nil, Invalid("invalid file name", Field(fmt.Sprintf("body.names[%d]", i), "a single path component"))
		}
	}
	out, err := h.svc.Preview(ctx, f.root, req)
	if err != nil {
		return nil, fileErr(err, "body.paths", false)
	}
	body := FilePreview{Conflicts: make([]FileConflictDTO, 0, len(out.Conflicts)), ConflictsTruncated: out.ConflictsTruncated,
		Impact: FileImpactDTO(out.Impact)}
	for _, c := range out.Conflicts {
		body.Conflicts = append(body.Conflicts, FileConflictDTO{Source: c.Source, Destination: c.Destination, Existing: newFileEntry(c.Existing)})
	}
	return &filePreviewOutput{Body: body}, nil
}

// startJob enqueues a files job after validation and answers 202.
func (h *filesAPI) startJob(ctx context.Context, f *fileCtx, kind domain.JobKind, in protocol.FilesJobInput, key string) (*JobAccepted, error) {
	audit.SetDetail(ctx, "paths", in.Paths)
	if in.Destination != "" {
		audit.SetDetail(ctx, "destination", in.Destination)
	}
	j, err := h.svc.StartJob(ctx, f.root, kind, f.p, in, key)
	if err != nil {
		return nil, fileErr(err, "body.paths", false)
	}
	return Accepted(j), nil
}

func (h *filesAPI) transfer(kind domain.JobKind, verb string) func(context.Context, fileScopeRef, *FilesTransferInput) (*JobAccepted, error) {
	return func(ctx context.Context, ref fileScopeRef, in *FilesTransferInput) (*JobAccepted, error) {
		f, err := h.open(ctx, ref)
		if err != nil {
			return nil, err
		}
		if err := f.require(verb); err != nil {
			return nil, err
		}
		paths, err := cleanPaths(in.Body.Paths, "body.paths", MaxFilesPerJob)
		if err != nil {
			return nil, err
		}
		dest, err := cleanPath(in.Body.Destination, "body.destination")
		if err != nil {
			return nil, err
		}
		// Copying reads the sources (and a copy of compose.yaml is readable
		// with files.read); moving changes them; landing a Compose source
		// name in the root writes one.
		name := in.Body.Name
		if name != "" && (len(paths) != 1 || !protocol.ValidFileName(name)) {
			return nil, Invalid("invalid name", Field("body.name", "a single path component, with exactly one source"))
		}
		read := kind == jobspec.FilesCopy && touchesDefinition(paths...)
		write := kind == jobspec.FilesMove && touchesDefinition(paths...)
		if dest == "." {
			for _, p := range paths {
				target := path.Base(p)
				if name != "" {
					target = name
				}
				write = write || IsDefinitionFile(target)
			}
		}
		if err := f.requireDefinition(read, write); err != nil {
			return nil, err
		}
		if !protocol.ValidConflict(in.Body.Conflict) {
			return nil, Invalid("invalid conflict policy", Field("body.conflict", "fail, overwrite, skip or keep_both"))
		}
		return h.startJob(ctx, f, kind, protocol.FilesJobInput{Paths: paths, Destination: dest, Name: name, Conflict: in.Body.Conflict},
			in.IdempotencyKey)
	}
}

func (h *filesAPI) deletion(ctx context.Context, ref fileScopeRef, in *FilesDeletionInput) (*JobAccepted, error) {
	f, err := h.open(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := f.require("delete"); err != nil {
		return nil, err
	}
	paths, err := cleanPaths(in.Body.Paths, "body.paths", MaxFilesPerJob)
	if err != nil {
		return nil, err
	}
	if slices.Contains(paths, ".") {
		return nil, Invalid("the root cannot be deleted", Field("body.paths", "delete its entries instead"))
	}
	if err := f.requireDefinition(false, touchesDefinition(paths...)); err != nil {
		return nil, err
	}
	return h.startJob(ctx, f, jobspec.FilesDelete, protocol.FilesJobInput{Paths: paths}, in.IdempotencyKey)
}

func (h *filesAPI) archive(ctx context.Context, ref fileScopeRef, in *FilesArchiveInput) (*JobAccepted, error) {
	f, err := h.open(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := f.require("archive"); err != nil {
		return nil, err
	}
	paths, err := cleanPaths(in.Body.Paths, "body.paths", MaxFilesPerJob)
	if err != nil {
		return nil, err
	}
	dest, err := cleanPath(in.Body.Destination, "body.destination")
	if err != nil || dest == "." {
		return nil, Invalid("invalid destination", Field("body.destination", "the archive file to create"))
	}
	if err := f.requireDefinition(touchesDefinition(paths...), IsDefinitionFile(dest)); err != nil {
		return nil, err
	}
	format := in.Body.Format
	if format == "" {
		format = protocol.FormatZip
	}
	if in.Body.Conflict == protocol.ConflictSkip || !protocol.ValidConflict(in.Body.Conflict) {
		return nil, Invalid("invalid conflict policy", Field("body.conflict", "fail, overwrite or keep_both"))
	}
	return h.startJob(ctx, f, jobspec.FilesArchive, protocol.FilesJobInput{Paths: paths, Destination: dest, Format: format, Conflict: in.Body.Conflict},
		in.IdempotencyKey)
}

func (h *filesAPI) extraction(ctx context.Context, ref fileScopeRef, in *FilesExtractionInput) (*JobAccepted, error) {
	f, err := h.open(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := f.require("extract"); err != nil {
		return nil, err
	}
	src, err := cleanPath(in.Body.Path, "body.path")
	if err != nil || src == "." {
		return nil, Invalid("invalid archive path", Field("body.path", "the archive file"))
	}
	dest, err := cleanPath(in.Body.Destination, "body.destination")
	if err != nil {
		return nil, err
	}
	// Extracting into the project root may write Compose sources.
	if err := f.requireDefinition(false, dest == "."); err != nil {
		return nil, err
	}
	if !protocol.ValidConflict(in.Body.Conflict) {
		return nil, Invalid("invalid conflict policy", Field("body.conflict", "fail, overwrite, skip or keep_both"))
	}
	return h.startJob(ctx, f, jobspec.FilesExtract, protocol.FilesJobInput{Paths: []string{src}, Destination: dest, Conflict: in.Body.Conflict},
		in.IdempotencyKey)
}

var octalRE = regexp.MustCompile(`^0?[0-7]{3}$`)

func parseMode(s, field string) (uint32, error) {
	if !octalRE.MatchString(s) {
		return 0, Invalid("invalid mode", Field(field, "octal permission bits such as 0644 (special bits are not allowed)"))
	}
	v, _ := strconv.ParseUint(s, 8, 32)
	return uint32(v), nil
}

func (h *filesAPI) metadata(ctx context.Context, ref fileScopeRef, in *FilesMetadataInput) (*JobAccepted, error) {
	f, err := h.open(ctx, ref)
	if err != nil {
		return nil, err
	}
	// Refuse before validating when neither change is granted at all.
	if !f.can("chmod") && !f.can("chown") {
		return nil, Forbidden("not permitted: " + f.capKey("chmod") + " or " + f.capKey("chown"))
	}
	b := in.Body
	if b.Chmod == nil && b.Chown == nil {
		return nil, Invalid("nothing to change", Field("body", "chmod and/or chown"))
	}
	job := protocol.FilesJobInput{Recursive: b.Recursive}
	if b.Chmod != nil {
		if err := f.require("chmod"); err != nil {
			return nil, err
		}
		m, err := parseMode(b.Chmod.Mode, "body.chmod.mode")
		if err != nil {
			return nil, err
		}
		job.Chmod = &protocol.ChmodSpec{Mode: m}
		if b.Chmod.DirMode != "" {
			dm, err := parseMode(b.Chmod.DirMode, "body.chmod.dirMode")
			if err != nil {
				return nil, err
			}
			job.Chmod.DirMode = &dm
		}
		audit.SetAction(ctx, f.capKey("chmod"))
	}
	if b.Chown != nil {
		if err := f.require("chown"); err != nil {
			return nil, err
		}
		if b.Chown.UID == nil && b.Chown.GID == nil {
			return nil, Invalid("nothing to change", Field("body.chown", "uid and/or gid"))
		}
		job.Chown = &protocol.ChownSpec{UID: b.Chown.UID, GID: b.Chown.GID}
		audit.SetAction(ctx, f.capKey("chown"))
	}
	if job.Paths, err = cleanPaths(b.Paths, "body.paths", MaxFilesPerJob); err != nil {
		return nil, err
	}
	if err := f.requireDefinition(false, touchesDefinition(job.Paths...)); err != nil {
		return nil, err
	}
	return h.startJob(ctx, f, jobspec.FilesMetadata, job, in.IdempotencyKey)
}

// filesBodyKey carries the raw request body of upload operations.
type filesBodyKey struct{}

// uploadBody is an operation middleware exposing the request body to the
// upload handler without buffering it (Huma reads bodies only for inputs
// with a Body/RawBody field).
func uploadBody(ctx huma.Context, next func(huma.Context)) {
	next(huma.WithValue(ctx, filesBodyKey{}, ctx.BodyReader()))
}

func (h *filesAPI) upload(ctx context.Context, ref fileScopeRef, in *FilesUploadQuery) (*fileUploadOutput, error) {
	f, err := h.open(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := f.require("write"); err != nil {
		return nil, err
	}
	dir, err := cleanPath(in.Path, "query.path")
	if err != nil {
		return nil, err
	}
	if !protocol.ValidFileName(in.Name) {
		return nil, Invalid("invalid file name", Field("query.name", "a single path component without / or control characters"))
	}
	rel := joinRel(dir, in.Name)
	if err := f.requireDefinition(false, IsDefinitionFile(rel) || (dir == "." && in.Conflict == protocol.ConflictKeepBoth && IsDefinitionFile(in.Name))); err != nil {
		return nil, err
	}
	if ct, _, _ := mime.ParseMediaType(ctxHeader(ctx, "Content-Type")); ct != "" && ct != "application/octet-stream" {
		return nil, NewError(http.StatusUnsupportedMediaType, CodeUnsupportedMediaType, "send the file as application/octet-stream")
	}
	if ctxHeader(ctx, "Content-Length") == "" || in.ContentLength < 0 {
		return nil, NewError(http.StatusLengthRequired, CodeLengthRequired, "uploads need a Content-Length")
	}
	if in.ContentLength > h.maxUpload {
		return nil, NewError(http.StatusRequestEntityTooLarge, CodePayloadTooLarge, fmt.Sprintf("uploads are limited to %d bytes", h.maxUpload))
	}
	req := protocol.FilesUploadInput{Dir: dir, Name: in.Name, Size: in.ContentLength}
	switch {
	case in.IfMatch != "" && (in.IfNoneMatch != "" || in.Conflict != ""), in.IfNoneMatch != "" && in.Conflict != "":
		return nil, Invalid("use one precondition", Field("header.If-Match", "If-Match, If-None-Match: * or conflict, not several"))
	case in.IfMatch != "":
		if req.IfMatch = ifMatchTags(in.IfMatch); len(req.IfMatch) == 0 {
			return nil, h.stale(ctx, f, rel)
		}
	case strings.TrimSpace(in.IfNoneMatch) == "*":
		req.CreateOnly = true
	case in.Conflict != "":
		req.Conflict = in.Conflict
	default:
		return nil, PreconditionRequired("uploads need If-None-Match: * (create), If-Match (replace a revision) or a conflict policy",
			Field("header.If-None-Match", "send * to create the file"))
	}
	if in.ContentSHA256 != "" {
		if b, err := hex.DecodeString(in.ContentSHA256); err != nil || len(b) != 32 {
			return nil, Invalid("invalid digest", Field("header.X-DockYard-Content-SHA256", "64 hex digits"))
		}
		req.SHA256 = strings.ToLower(in.ContentSHA256)
	}
	body, _ := ctx.Value(filesBodyKey{}).(io.Reader)
	if body == nil {
		return nil, Internal(errors.New("upload body reader missing"))
	}
	audit.SetDetail(ctx, "path", rel)
	audit.SetDetail(ctx, "bytes", in.ContentLength)
	res, err := h.svc.Upload(ctx, f.root, req, body)
	if err != nil {
		var fe *domain.FileError
		if errors.As(err, &fe) && fe.Code == protocol.CodeConflict {
			return nil, h.stale(ctx, f, rel)
		}
		return nil, fileErr(err, "query.path", req.CreateOnly || len(req.IfMatch) > 0)
	}
	out := FileUploadResult{Entry: newFileEntry(res.Entry), Skipped: res.Skipped}
	if res.Skipped {
		audit.SetDetail(ctx, "skipped", true)
	}
	return &fileUploadOutput{ETagHeader: ETagHeader{ETag: out.Entry.ETag}, Body: out}, nil
}

func joinRel(dir, name string) string {
	if dir == "." {
		return name
	}
	return dir + "/" + name
}

// headersKey carries request headers huma does not bind for us.
type headersKey struct{}

// captureHeaders exposes selected request headers to handlers.
func captureHeaders(ctx huma.Context, next func(huma.Context)) {
	h := http.Header{}
	for _, k := range []string{"Content-Type", "Content-Length"} {
		if v := ctx.Header(k); v != "" {
			h.Set(k, v)
		}
	}
	next(huma.WithValue(ctx, headersKey{}, h))
}

func ctxHeader(ctx context.Context, k string) string {
	h, _ := ctx.Value(headersKey{}).(http.Header)
	return h.Get(k)
}

// byteRange parses a single "bytes=a-b" range for a file of size n.
func byteRange(h string, n int64) (start, length int64, ok bool, err error) {
	if h == "" {
		return 0, n, false, nil
	}
	spec, found := strings.CutPrefix(strings.TrimSpace(h), "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, n, false, nil // unsupported: ignore (full content)
	}
	a, b, _ := strings.Cut(spec, "-")
	switch a {
	case "": // suffix
		k, perr := strconv.ParseInt(b, 10, 64)
		if perr != nil || k <= 0 {
			return 0, 0, false, errRange
		}
		k = min(k, n)
		return n - k, k, true, nil
	default:
		s, perr := strconv.ParseInt(a, 10, 64)
		if perr != nil || s < 0 || s >= n {
			return 0, 0, false, errRange
		}
		e := n - 1
		if b != "" {
			if e, perr = strconv.ParseInt(b, 10, 64); perr != nil || e < s {
				return 0, 0, false, errRange
			}
			e = min(e, n-1)
		}
		return s, e - s + 1, true, nil
	}
}

var errRange = errors.New("range not satisfiable")

func contentDisposition(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	return mime.FormatMediaType("attachment", map[string]string{"filename": ascii}) + "; filename*=UTF-8''" + urlPathEscape(name)
}

func urlPathEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c < 0x80 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func (h *filesAPI) download(ctx context.Context, ref fileScopeRef, in *FilesDownloadQuery) (*huma.StreamResponse, error) {
	f, err := h.open(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := f.require("download"); err != nil {
		return nil, err
	}
	paths, err := cleanPaths(in.Paths, "query.path", MaxFilesPerJob)
	if err != nil {
		return nil, err
	}
	if err := f.requireDefinition(touchesDefinition(paths...), false); err != nil {
		return nil, err
	}
	audit.SetDetail(ctx, "paths", paths)
	req := protocol.FilesDownloadInput{Paths: paths, Format: in.Format}
	var entry protocol.FileEntry
	status := http.StatusOK
	var start, length int64
	partial := false
	if req.Format == "" && len(paths) == 1 {
		entry, err = h.svc.Stat(ctx, f.root, paths[0], true)
		if err != nil {
			return nil, fileErr(err, "query.path", false)
		}
		if entry.Type == protocol.FileTypeFile {
			req.Format = protocol.FormatRaw
			start, length, partial, err = byteRange(in.Range, entry.Size)
			if err != nil {
				return nil, NewError(http.StatusRequestedRangeNotSatisfiable, CodeRangeNotSatisfiable, "the range is outside the file").
					WithHeader("Content-Range", fmt.Sprintf("bytes */%d", entry.Size))
			}
			req.Offset, req.Length = start, length
			if partial {
				status = http.StatusPartialContent
			}
		}
	}
	if req.Format == "" {
		req.Format = protocol.FormatZip
	}
	audit.SetDetail(ctx, "format", req.Format)

	st, err := h.svc.Download(ctx, f.root, req) // aborted when the request ends
	if err != nil {
		return nil, fileErr(err, "query.path", false)
	}
	// Wait for the first bytes (or the agent's refusal) so failures before
	// the download starts are ordinary JSON errors.
	first := make([]byte, 64<<10)
	n, rerr := io.ReadFull(st, first)
	if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
		st.Abort(protocol.CloseReasonCancelled, protocol.CodeCancelled, "")
		return nil, fileErr(h.svc.StreamError(rerr), "query.path", false)
	}
	first = first[:n]
	done := rerr != nil // the whole content fit into the first read
	name := "download.zip"
	ctype := "application/zip"
	switch req.Format {
	case protocol.FormatRaw:
		name, ctype = path.Base(paths[0]), "application/octet-stream"
	case protocol.FormatTarGz:
		name, ctype = "download.tar.gz", "application/gzip"
	}
	if len(paths) == 1 && req.Format != protocol.FormatRaw {
		base := path.Base(paths[0])
		if paths[0] == "." {
			base = ref.id
		}
		name = base + map[string]string{protocol.FormatZip: ".zip", protocol.FormatTarGz: ".tar.gz"}[req.Format]
	}
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		defer func() { _ = st.CloseWrite() }()
		// The client going away stops the agent's stream at once.
		stop := context.AfterFunc(hctx.Context(), func() { st.Abort(protocol.CloseReasonCancelled, protocol.CodeCancelled, "") })
		defer stop()
		hctx.SetHeader("Content-Type", ctype)
		hctx.SetHeader("Content-Disposition", contentDisposition(name))
		hctx.SetHeader("X-Content-Type-Options", "nosniff")
		if req.Format == protocol.FormatRaw {
			hctx.SetHeader("Accept-Ranges", "bytes")
			hctx.SetHeader("Content-Length", strconv.FormatInt(length, 10))
			if entry.ETag != "" {
				hctx.SetHeader("ETag", ETag(entry.ETag))
			}
			if partial {
				hctx.SetHeader("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, entry.Size))
			}
		}
		hctx.SetStatus(status)
		w := hctx.BodyWriter()
		if _, err := w.Write(first); err != nil {
			st.Abort(protocol.CloseReasonCancelled, protocol.CodeCancelled, "")
			return
		}
		if done {
			return
		}
		if _, err := io.Copy(w, st); err != nil {
			// A failure after the first byte aborts the connection: the
			// client sees a truncated download, never a "complete" one.
			st.Abort(protocol.CloseReasonCancelled, protocol.CodeCancelled, "")
			audit.SetErrorClass(hctx.Context(), "aborted")
			panic(http.ErrAbortHandler)
		}
	}}, nil
}

// registerFiles registers the file routes for both roots.
func registerFiles(a huma.API, deps Deps) {
	h := &filesAPI{svc: deps.Files, authz: authz.OrDenyAll(deps.Authorizer), maxUpload: deps.FilesMaxUpload}
	if h.maxUpload <= 0 {
		h.maxUpload = DefaultMaxUpload
	}
	scopes := []struct {
		kind, prefix, idName string
	}{
		{protocol.ScopeStack, BasePath + "/stacks/{stackId}/files", "stack"},
		{protocol.ScopeVolume, BasePath + "/environments/{environmentId}/volumes/{volumeId}/files", "volume"},
	}
	std := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout, http.StatusNotImplemented}
	op := func(sc struct{ kind, prefix, idName string }, id, method, suffix, summary, desc, verb string, errs []int) Operation {
		o := Operation{Operation: huma.Operation{OperationID: id, Method: method, Path: sc.prefix + suffix, Summary: summary,
			Description: desc, Tags: []string{tagFiles}, Errors: errs,
			// 512 KiB of content may be JSON-escaped or base64-encoded.
			MaxBodyBytes: 4 << 20}, Capability: Capability(sc.kind + ".files." + verb), Scope: ScopeResource}
		return o
	}
	for _, sc := range scopes {
		kind := sc.idName
		what := "the stack's project directory"
		defNote := " Compose sources (compose.yaml, override files, .env at the root) additionally need stack.definition.read / stack.definition.write."
		if kind == "volume" {
			what = "the volume"
			defNote = " Only local-driver volumes are served (non-local drivers and DockYard's own volumes answer 409 volume_files_unsupported)."
		}
		registerFileOp(a, h, op(sc, "list-"+kind+"-files", http.MethodGet, "", "List a directory of "+what,
			"One page of a directory listing (entries sorted by sort, ties by name; cursor pagination). Symlinks are shown with their target and where "+
				"it resolves, never followed out of the root. Path encoding and limits: docs/api/files.md."+defNote, "read", std),
			h.list, func(in *listStackFilesInput) (fileScopeRef, *FilesListQuery) { return in.scopeRef(), in.common() },
			func(in *listVolumeFilesInput) (fileScopeRef, *FilesListQuery) { return in.scopeRef(), in.common() })

		registerFileOp(a, h, op(sc, "get-"+kind+"-file-content", http.MethodGet, "/content", "Read a file of "+what,
			"At most 512 KiB of a regular file from offset, as text or base64 (binary). The ETag header is the file's content revision; "+
				"send it as If-Match when saving."+defNote, "read", std),
			h.getContent, func(in *getStackContentInput) (fileScopeRef, *FilesContentQuery) { return in.scopeRef(), in.common() },
			func(in *getVolumeContentInput) (fileScopeRef, *FilesContentQuery) { return in.scopeRef(), in.common() })

		registerFileOp(a, h, op(sc, "replace-"+kind+"-file-content", http.MethodPut, "/content", "Save a file of "+what,
			"Replaces a file's content atomically (temporary file, then rename) when If-Match names its current ETag (412 with the current "+
				"ETag otherwise: an external change or another editor saved first; never overwrite silently), or creates it with "+
				"If-None-Match: *. Without either: 428."+defNote,
			"write", append(std, http.StatusPreconditionFailed, http.StatusPreconditionRequired, http.StatusRequestEntityTooLarge)),
			h.replaceContent, func(in *replaceStackContentInput) (fileScopeRef, *FilesReplaceInput) {
				return in.scopeRef(), in.common()
			},
			func(in *replaceVolumeContentInput) (fileScopeRef, *FilesReplaceInput) {
				return in.scopeRef(), in.common()
			})

		dl := op(sc, "download-"+kind+"-files", http.MethodGet, "/downloads", "Download files of "+what,
			"One regular file downloads raw (Content-Length, ETag, single Range requests); several paths or a directory stream as a zip "+
				"(default) or tar.gz archive without Content-Length. Escaping symlinks, hard-linked and special files are left out and listed "+
				"in DOCKYARD-SKIPPED.txt. A failure after the first byte aborts the connection. Protocol: docs/api/streams.md."+defNote,
			"download", append(std, http.StatusRequestedRangeNotSatisfiable, http.StatusRequestEntityTooLarge))
		dl.Audit = AuditAlways
		dl.Responses = map[string]*huma.Response{
			"200": {Description: "File or archive bytes", Content: map[string]*huma.MediaType{
				"application/octet-stream": {Schema: &huma.Schema{Type: "string", Format: "binary"}},
				"application/zip":          {Schema: &huma.Schema{Type: "string", Format: "binary"}},
				"application/gzip":         {Schema: &huma.Schema{Type: "string", Format: "binary"}},
			}},
			"206": {Description: "Requested range of a single file", Content: map[string]*huma.MediaType{
				"application/octet-stream": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}},
		}
		registerFileOp(a, h, dl, h.download,
			func(in *downloadStackInput) (fileScopeRef, *FilesDownloadQuery) { return in.scopeRef(), in.common() },
			func(in *downloadVolumeInput) (fileScopeRef, *FilesDownloadQuery) { return in.scopeRef(), in.common() })

		ce := op(sc, "create-"+kind+"-file-entry", http.MethodPost, "/entries", "Create a file or directory in "+what,
			"Creates an empty directory or a new file (optional initial content up to 512 KiB). 409 file_exists when the name exists."+defNote,
			"write", append(std, http.StatusRequestEntityTooLarge))
		ce.DefaultStatus = http.StatusCreated
		registerFileOp(a, h, ce, h.createEntry,
			func(in *createStackEntryInput) (fileScopeRef, *FilesCreateEntryInput) {
				return in.scopeRef(), in.common()
			},
			func(in *createVolumeEntryInput) (fileScopeRef, *FilesCreateEntryInput) {
				return in.scopeRef(), in.common()
			})

		up := op(sc, "upload-"+kind+"-files", http.MethodPost, "/uploads", "Upload a file into "+what,
			"Streams the raw request body (application/octet-stream, Content-Length required) into path/name: into a temporary file, "+
				"verified (size, optional X-DockYard-Content-SHA256), then moved into place. Preconditions: If-None-Match: * (create, 412 when "+
				"the name exists), If-Match (replace that revision, 412 otherwise) or conflict=overwrite|skip|keep_both; none of them: 428. "+
				"At most DOCKYARD_FILES_MAX_UPLOAD bytes (default 2 GiB, 413). One request per file; upload an archive and extract it for "+
				"many files."+defNote,
			"write", append(std, http.StatusLengthRequired, http.StatusPreconditionFailed, http.StatusPreconditionRequired,
				http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType))
		up.DefaultStatus = http.StatusCreated
		up.Middlewares = huma.Middlewares{uploadBody}
		up.RequestBody = &huma.RequestBody{Required: true, Description: "The file's bytes.", Content: map[string]*huma.MediaType{
			"application/octet-stream": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}}
		registerFileOp(a, h, up, h.upload,
			func(in *uploadStackInput) (fileScopeRef, *FilesUploadQuery) { return in.scopeRef(), in.common() },
			func(in *uploadVolumeInput) (fileScopeRef, *FilesUploadQuery) { return in.scopeRef(), in.common() })

		registerFileOp(a, h, op(sc, "create-"+kind+"-file-conflict-preview", http.MethodPost, "/conflict-previews",
			"Preview conflicts and impact in "+what,
			"Lists the destinations an operation would overwrite (for per-item overwrite / skip / keep both decisions; apply-to-all is off "+
				"by default in the UI) and counts what it touches, recursively where the operation recurses (delete, recursive chmod/chown).",
			"read", std),
			h.preview, func(in *previewStackInput) (fileScopeRef, *FilesPreviewInput) { return in.scopeRef(), in.common() },
			func(in *previewVolumeInput) (fileScopeRef, *FilesPreviewInput) { return in.scopeRef(), in.common() })

		jobErrs := append(std, http.StatusConflict)
		for _, t := range []struct{ id, suffix, verb, summary, desc string }{
			{"create-" + kind + "-file-copy", "/copies", "copy", "Copy files in " + what,
				"Starts a files.copy job (202 + job): sources are copied recursively into destination; symlinks are copied as symlinks (never " +
					"followed), hard-linked and special files fail per item. conflict applies per top-level item."},
			{"create-" + kind + "-file-move", "/moves", "move", "Move or rename files in " + what,
				"Starts a files.move job (202 + job): each source is renamed into destination (a rename is a move into the same directory " +
					"under a new name: use conflict and one source). conflict applies per item."},
		} {
			o := op(sc, t.id, http.MethodPost, t.suffix, t.summary, t.desc+defNote, t.verb, jobErrs)
			o.Idempotency = IdempotencyJob
			kindOf := map[string]domain.JobKind{"copy": jobspec.FilesCopy, "move": jobspec.FilesMove}[t.verb]
			registerFileOp(a, h, o, h.transfer(kindOf, t.verb),
				func(in *transferStackInput) (fileScopeRef, *FilesTransferInput) { return in.scopeRef(), in.common() },
				func(in *transferVolumeInput) (fileScopeRef, *FilesTransferInput) { return in.scopeRef(), in.common() })
		}

		del := op(sc, "create-"+kind+"-file-deletion", http.MethodPost, "/deletions", "Delete files in "+what,
			"Starts a files.delete job (202 + job) deleting each path recursively; symlinks are removed, never followed. Preview the impact "+
				"first (conflict preview, operation delete)."+defNote, "delete", jobErrs)
		del.Idempotency = IdempotencyJob
		registerFileOp(a, h, del, h.deletion,
			func(in *deletionStackInput) (fileScopeRef, *FilesDeletionInput) { return in.scopeRef(), in.common() },
			func(in *deletionVolumeInput) (fileScopeRef, *FilesDeletionInput) { return in.scopeRef(), in.common() })

		arc := op(sc, "create-"+kind+"-file-archive", http.MethodPost, "/archives", "Create an archive in "+what,
			"Starts a files.archive job (202 + job) packing the paths into a zip or tar.gz file inside the root (escaping symlinks, "+
				"hard-linked and special files are left out and listed in DOCKYARD-SKIPPED.txt)."+defNote, "archive", jobErrs)
		arc.Idempotency = IdempotencyJob
		registerFileOp(a, h, arc, h.archive,
			func(in *archiveStackInput) (fileScopeRef, *FilesArchiveInput) { return in.scopeRef(), in.common() },
			func(in *archiveVolumeInput) (fileScopeRef, *FilesArchiveInput) { return in.scopeRef(), in.common() })

		ext := op(sc, "create-"+kind+"-file-extraction", http.MethodPost, "/extractions", "Extract an archive in "+what,
			"Starts a files.extract job (202 + job) unpacking a zip or tar.gz archive: entries escaping the destination (../, absolute, "+
				"drive letters), symlinks leaving the root, hard links to files outside the archive and device files are refused per entry; "+
				"setuid bits are dropped; bytes actually written are limited (10 GiB and 100x the archive size) as well as the entry "+
				"count (100 000). Nested archives are not extracted."+defNote, "extract", jobErrs)
		ext.Idempotency = IdempotencyJob
		registerFileOp(a, h, ext, h.extraction,
			func(in *extractionStackInput) (fileScopeRef, *FilesExtractionInput) {
				return in.scopeRef(), in.common()
			},
			func(in *extractionVolumeInput) (fileScopeRef, *FilesExtractionInput) {
				return in.scopeRef(), in.common()
			})

		md := op(sc, "update-"+kind+"-file-metadata", http.MethodPatch, "/metadata", "Change permissions or ownership in "+what,
			"Starts a files.metadata job (202 + job): chmod (needs "+kind+".files.chmod) and/or chown (needs "+kind+".files.chown) of the "+
				"paths, optionally recursive. Symlinks are skipped (never followed), special and hard-linked files fail per item; changes "+
				"go through the opened file (fchmod/fchown). Preview the count with a conflict preview of operation metadata."+defNote,
			"{change}", jobErrs)
		md.Capability = Capability(sc.kind + ".files.{change}")
		md.CapabilityValues = []Capability{Capability(sc.kind + ".files.chmod"), Capability(sc.kind + ".files.chown")}
		md.Idempotency = IdempotencyJob
		registerFileOp(a, h, md, h.metadata,
			func(in *metadataStackInput) (fileScopeRef, *FilesMetadataInput) { return in.scopeRef(), in.common() },
			func(in *metadataVolumeInput) (fileScopeRef, *FilesMetadataInput) { return in.scopeRef(), in.common() })
	}
}

// registerFileOp registers op for the scope its path belongs to, with a
// handler taking the scope and the common input part. S is the stack
// input, V the volume input; the one matching op.Path is registered.
func registerFileOp[S, V, C, O any](a huma.API, _ *filesAPI, op Operation, fn func(context.Context, fileScopeRef, *C) (*O, error),
	stack func(*S) (fileScopeRef, *C), volume func(*V) (fileScopeRef, *C)) {
	op.Middlewares = append(huma.Middlewares{captureHeaders}, op.Middlewares...)
	if strings.HasPrefix(op.Path, BasePath+"/stacks/") {
		Register(a, op, func(ctx context.Context, in *S) (*O, error) {
			ref, c := stack(in)
			return fn(ctx, ref, c)
		})
		return
	}
	Register(a, op, func(ctx context.Context, in *V) (*O, error) {
		ref, c := volume(in)
		return fn(ctx, ref, c)
	})
}
