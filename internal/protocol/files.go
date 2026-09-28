package protocol

import (
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Scoped file operations (#15): the inputs and outputs of the files.*
// requests, the files.upload/files.download streams and the files.* job
// kinds. The manager authorizes (#17) and resolves the scope; the agent
// confines every path to the scope root (docs/internal/protocol/agent-v1.md,
// "Scoped files").

// File operation bounds shared by manager and agent.
const (
	// MaxInlineContent bounds the content of files.read and files.write
	// (bytes; base64 keeps the frame below MaxFrameSize). Larger files use
	// the files.download and files.upload streams (so does the manager's
	// editor for files above it, when its edit limit is raised).
	MaxInlineContent = 512 << 10
	// MaxETagSize: regular files up to this size carry a content ETag.
	MaxETagSize = 256 << 20
	// MaxListScan bounds the entries one directory listing reads.
	MaxListScan = 100_000
	// MaxListPage bounds the entries of one files.list answer.
	MaxListPage = 500
	// MaxConflicts bounds the conflicts one preview lists.
	MaxConflicts = 1000
	// MaxPreviewEntries bounds the entries a preview walks.
	MaxPreviewEntries = 100_000
	// MaxOperationPaths bounds the paths of one operation.
	MaxOperationPaths = 1000
)

// Caps of the limits a manager may send in FileLimits: an agent lowers
// larger values to these.
const (
	// MaxFileLimitBytes caps uploads, downloads, created archives and the
	// bytes one extraction writes (1 TiB).
	MaxFileLimitBytes = 1 << 40
	// MaxFileLimitEntries caps the entries of an archive read or written
	// (the entries one recursive operation walks are bounded as well).
	MaxFileLimitEntries = 1_000_000
	// MaxFileLimitRatio caps the expansion ratio of an extraction.
	MaxFileLimitRatio = 10_000
)

// FeatureFileLimits is the capabilities feature of agents that apply the
// limits of FileLimits (files.upload and files.download inputs, extract
// previews, files.archive and files.extract jobs). The manager sends
// limits only to them; other agents keep their built-in defaults.
const FeatureFileLimits = "files.limits"

// FileLimits are the file manager limits the manager configured
// (DOCKER_MANAGER_FILES_*) for one operation. A zero field keeps the
// agent's default; larger values than the caps above are lowered to them.
type FileLimits struct {
	// MaxUpload bounds one uploaded file (bytes).
	MaxUpload int64 `json:"maxUpload,omitempty"`
	// MaxDownload bounds one download or created archive (bytes).
	MaxDownload int64 `json:"maxDownload,omitempty"`
	// MaxExtractBytes bounds the bytes one extraction writes;
	// MaxExtractRatio the bytes written per archive byte.
	MaxExtractBytes int64 `json:"maxExtractBytes,omitempty"`
	MaxExtractRatio int64 `json:"maxExtractRatio,omitempty"`
	// MaxArchiveEntries bounds the entries of an archive read or written.
	MaxArchiveEntries int `json:"maxArchiveEntries,omitempty"`
}

// File scope kinds. Template scopes are served by the manager itself (a
// template's draft in its data directory); agents refuse them.
const (
	ScopeStack    = "stack"
	ScopeVolume   = "volume"
	ScopeTemplate = "template"
)

// IsAbsHostPath reports whether p, a slash-separated host path reported by
// an agent, is absolute on the agent's platform: a Linux path in
// production (exactly path.IsAbs there), also a drive path (C:/...) when
// tests run the agent on Windows.
func IsAbsHostPath(p string) bool {
	return path.IsAbs(p) || filepath.IsAbs(filepath.FromSlash(p))
}

// FileScope names the root every path of an operation is relative to.
type FileScope struct {
	// Kind is stack, volume or template.
	Kind string `json:"kind"`
	// ID is the stack ID, the Docker volume name or the template ID.
	ID string `json:"id"`
	// Dir is the stack's project directory (absolute host path, identical
	// inside the agent, #28); the agent refuses it unless it lies in a
	// verified stack root. Empty for volumes (the agent resolves the
	// volume's mountpoint itself).
	Dir string `json:"dir,omitempty"`
}

// Validate checks the scope's shape (the agent still resolves and checks
// the root itself).
func (s FileScope) Validate() error {
	switch s.Kind {
	case ScopeStack:
		// filepath.IsAbs: the agent's platform decides (Linux in production;
		// drive paths in tests on Windows).
		if s.ID == "" || len(s.ID) > 255 || !filepath.IsAbs(filepath.FromSlash(s.Dir)) || path.Clean(s.Dir) != s.Dir || s.Dir == "/" {
			return invalid("stack scope needs an ID and a clean absolute project directory")
		}
	case ScopeVolume:
		if !ValidVolumeName(s.ID) || s.Dir != "" {
			return invalid("volume scope needs a valid volume name and no dir")
		}
	case ScopeTemplate:
		if s.ID == "" || len(s.ID) > 64 || s.Dir != "" {
			return invalid("template scope needs a template ID and no dir")
		}
	default:
		return invalid("scope kind %q", s.Kind)
	}
	return nil
}

// ValidVolumeName reports whether n is a Docker volume name
// ([a-zA-Z0-9][a-zA-Z0-9_.-]*, at most 255 bytes).
func ValidVolumeName(n string) bool {
	if n == "" || len(n) > 255 {
		return false
	}
	for i, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case i > 0 && (r == '_' || r == '.' || r == '-'):
		default:
			return false
		}
	}
	return true
}

// ValidFileName reports whether n is one path component a file may be
// created with: not empty, not . or .., no slash, backslash, NUL or other
// control character, valid UTF-8, at most 255 bytes.
func ValidFileName(n string) bool {
	if n == "" || n == "." || n == ".." || len(n) > 255 || !utf8.ValidString(n) {
		return false
	}
	for _, r := range n {
		if r == '/' || r == '\\' || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// File entry types.
const (
	FileTypeFile    = "file"
	FileTypeDir     = "dir"
	FileTypeSymlink = "symlink"
	FileTypeOther   = "other"
)

// FileEntry is the metadata of one directory entry (never its content).
type FileEntry struct {
	Name string `json:"name"`
	// Path is root-relative and slash-separated ("." is the root).
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	// Mode is the permission and special bits (Unix semantics, e.g.
	// 0o755); UID/GID are numeric host IDs.
	Mode    uint32    `json:"mode"`
	UID     uint32    `json:"uid"`
	GID     uint32    `json:"gid"`
	ModTime time.Time `json:"modTime"`
	// Links is the hard link count (0 when the platform does not report
	// it). Content of regular files with more than one link is not served:
	// another name of the same inode may lie outside the root.
	Links uint64 `json:"links,omitempty"`
	// LinkTarget is a symlink's target as stored (never followed out of
	// the root); LinkStatus says where it resolves: inside, outside
	// (escapes the root), dangling or loop.
	LinkTarget string `json:"linkTarget,omitempty"`
	LinkStatus string `json:"linkStatus,omitempty"`
	// ETag is the content revision of a regular file ("f1-" + hex):
	// SHA-256 over content, size and modification time. Set by stat
	// (when asked), read and write; absent for files over MaxETagSize.
	ETag string `json:"etag,omitempty"`
}

// Symlink resolution states (FileEntry.LinkStatus).
const (
	LinkInside   = "inside"
	LinkOutside  = "outside"
	LinkDangling = "dangling"
	LinkLoop     = "loop"
)

// Sort keys of files.list.
const (
	SortName     = "name"
	SortSize     = "size"
	SortModified = "modified"
	SortType     = "type"
)

// FilesListCursor is the position after the last returned entry.
type FilesListCursor struct {
	Name string `json:"name"`
	// Key is the sort key of that entry for size/modified (decimal).
	Key string `json:"key,omitempty"`
}

// FilesListInput is the input of files.list.
type FilesListInput struct {
	Scope FileScope `json:"scope"`
	Path  string    `json:"path"`
	// Sort is name (default), size, modified or type; Desc reverses it.
	// Ties are broken by name.
	Sort string `json:"sort,omitempty"`
	Desc bool   `json:"desc,omitempty"`
	// Query keeps entries whose name contains it (case-insensitive).
	Query string `json:"query,omitempty"`
	// Hidden includes dot files.
	Hidden bool             `json:"hidden,omitempty"`
	After  *FilesListCursor `json:"after,omitempty"`
	Limit  int              `json:"limit,omitempty"`
}

// FilesListOutput is one page of a directory listing.
type FilesListOutput struct {
	Dir     FileEntry   `json:"dir"`
	Entries []FileEntry `json:"entries"`
	// Total counts the matching entries (after filters).
	Total int `json:"total"`
	// Truncated: the directory has more entries than the agent scans
	// (MaxListScan); the listing covers the first ones read.
	Truncated bool             `json:"truncated,omitempty"`
	Next      *FilesListCursor `json:"next,omitempty"`
}

// FilesStatInput is the input of files.stat.
type FilesStatInput struct {
	Scope FileScope `json:"scope"`
	Path  string    `json:"path"`
	// ETag computes the content ETag of a regular file.
	ETag bool `json:"etag,omitempty"`
}

// FilesReadInput is the input of files.read: at most MaxInlineContent
// bytes from Offset (Length 0: as much as allowed).
type FilesReadInput struct {
	Scope  FileScope `json:"scope"`
	Path   string    `json:"path"`
	Offset int64     `json:"offset,omitempty"`
	Length int64     `json:"length,omitempty"`
}

// FilesReadOutput carries a bounded slice of a regular file.
type FilesReadOutput struct {
	Entry FileEntry `json:"entry"`
	// Data is base64 in JSON.
	Data []byte `json:"data"`
	// Binary: the file is not UTF-8 text (NUL bytes or invalid UTF-8).
	Binary bool `json:"binary"`
	// Truncated: the file continues after the returned bytes.
	Truncated bool `json:"truncated"`
}

// FilesWriteInput is the input of files.write: replace (or create) a
// regular file with Data, atomically (temporary file, then rename).
type FilesWriteInput struct {
	Scope FileScope `json:"scope"`
	Path  string    `json:"path"`
	Data  []byte    `json:"data"`
	// IfMatch lists acceptable current ETags ("*": any existing file);
	// CreateOnly requires that the file does not exist; Overwrite
	// replaces whatever regular file exists. Exactly one is required.
	IfMatch    []string `json:"ifMatch,omitempty"`
	CreateOnly bool     `json:"createOnly,omitempty"`
	Overwrite  bool     `json:"overwrite,omitempty"`
}

// FilesMkdirInput is the input of files.mkdir: create an empty directory
// or a new regular file (with optional initial Data).
type FilesMkdirInput struct {
	Scope FileScope `json:"scope"`
	Path  string    `json:"path"`
	Type  string    `json:"type"` // dir | file
	Data  []byte    `json:"data,omitempty"`
}

// File operations of conflict previews and jobs.
const (
	FileOpCopy     = "copy"
	FileOpMove     = "move"
	FileOpDelete   = "delete"
	FileOpUpload   = "upload"
	FileOpExtract  = "extract"
	FileOpArchive  = "archive"
	FileOpMetadata = "metadata"
)

// Conflict policies.
const (
	ConflictFail      = "fail"
	ConflictOverwrite = "overwrite"
	ConflictSkip      = "skip"
	ConflictKeepBoth  = "keep_both"
)

// ValidConflict reports whether c is a conflict policy ("" = fail).
func ValidConflict(c string) bool {
	switch c {
	case "", ConflictFail, ConflictOverwrite, ConflictSkip, ConflictKeepBoth:
		return true
	}
	return false
}

// FilesPreviewInput is the input of files.conflict_preview: what an
// operation would overwrite and how much it touches.
type FilesPreviewInput struct {
	Scope     FileScope `json:"scope"`
	Operation string    `json:"operation"`
	// Paths are the sources (copy, move, delete, metadata, archive) or the
	// archive (extract).
	Paths []string `json:"paths,omitempty"`
	// Destination is the target directory (copy, move, upload, extract)
	// or the archive file to create (archive).
	Destination string `json:"destination,omitempty"`
	// Names are the file names to upload, or the one new name of a single
	// copied or moved source.
	Names     []string `json:"names,omitempty"`
	Recursive bool     `json:"recursive,omitempty"`
	// Limits (FeatureFileLimits) bound the archive an extract preview
	// reads.
	Limits *FileLimits `json:"limits,omitempty"`
}

// FileConflict is one destination that already exists.
type FileConflict struct {
	// Source is the root-relative source (or upload name / archive entry).
	Source      string    `json:"source"`
	Destination string    `json:"destination"`
	Existing    FileEntry `json:"existing"`
}

// FileImpact counts what an operation touches (recursively where it
// recurses; bounded by MaxPreviewEntries).
type FileImpact struct {
	Entries   int   `json:"entries"`
	Files     int   `json:"files"`
	Dirs      int   `json:"dirs"`
	Symlinks  int   `json:"symlinks"`
	Other     int   `json:"other"`
	Bytes     int64 `json:"bytes"`
	Truncated bool  `json:"truncated,omitempty"`
}

// FilesPreviewOutput is the answer of files.conflict_preview.
type FilesPreviewOutput struct {
	Conflicts []FileConflict `json:"conflicts"`
	// ConflictsTruncated: more conflicts exist than listed (MaxConflicts).
	ConflictsTruncated bool       `json:"conflictsTruncated,omitempty"`
	Impact             FileImpact `json:"impact"`
}

// FilesUploadInput is the input of a files.upload stream: the data is the
// stream's bytes (exactly Size of them).
type FilesUploadInput struct {
	Scope FileScope `json:"scope"`
	// Dir is the target directory, Name the file name in it.
	Dir  string `json:"dir"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	// SHA256 (hex), when set, must match the received bytes.
	SHA256 string `json:"sha256,omitempty"`
	// Preconditions: IfMatch (ETags or "*"), CreateOnly, or a Conflict
	// policy (overwrite, skip, keep_both). Exactly one is required.
	IfMatch    []string `json:"ifMatch,omitempty"`
	CreateOnly bool     `json:"createOnly,omitempty"`
	Conflict   string   `json:"conflict,omitempty"`
	// Limits (FeatureFileLimits) bound the upload's size.
	Limits *FileLimits `json:"limits,omitempty"`
}

// FilesUploadResult is the result of the agent's final stream_close.
type FilesUploadResult struct {
	// Entry is the written file (Entry.Name may differ from the requested
	// name with keep_both).
	Entry FileEntry `json:"entry"`
	// Skipped: the name existed and the policy was skip; nothing written.
	Skipped bool `json:"skipped,omitempty"`
}

// Download formats.
const (
	FormatRaw   = "raw"
	FormatZip   = "zip"
	FormatTarGz = "tar.gz"
)

// FilesDownloadInput is the input of a files.download stream.
type FilesDownloadInput struct {
	Scope FileScope `json:"scope"`
	Paths []string  `json:"paths"`
	// Format raw streams one regular file (with Offset/Length for range
	// requests); zip and tar.gz stream an archive of the paths.
	Format string `json:"format"`
	Offset int64  `json:"offset,omitempty"`
	Length int64  `json:"length,omitempty"`
	// Limits (FeatureFileLimits) bound the download's size and entries.
	Limits *FileLimits `json:"limits,omitempty"`
}

// SkippedListName is the archive entry listing what a download or archive
// left out (escaping symlinks, hard-linked and special files).
const SkippedListName = "DOCKER-MANAGER-SKIPPED.txt"

// ChmodSpec sets permission bits (0..0o777; special bits are refused).
// DirMode, when set, applies to directories instead of Mode.
type ChmodSpec struct {
	Mode    uint32  `json:"mode"`
	DirMode *uint32 `json:"dirMode,omitempty"`
}

// ChownSpec sets the numeric owner and/or group (nil keeps it).
type ChownSpec struct {
	UID *uint32 `json:"uid,omitempty"`
	GID *uint32 `json:"gid,omitempty"`
}

// FilesJobInput is the input of the files.* job kinds (#26). The chmod
// and chown keys select the capabilities of files.metadata.
type FilesJobInput struct {
	Scope FileScope `json:"scope"`
	// Paths are the sources (archive, copy, move, delete, metadata) or the
	// archive to extract (extract, exactly one).
	Paths []string `json:"paths"`
	// Destination: the directory (copy, move, extract) or the archive file
	// to create (archive).
	Destination string `json:"destination,omitempty"`
	// Name is the new name of a single copied or moved source (a rename is
	// a move into the source's own directory under Name).
	Name      string     `json:"name,omitempty"`
	Format    string     `json:"format,omitempty"`
	Conflict  string     `json:"conflict,omitempty"`
	Recursive bool       `json:"recursive,omitempty"`
	Chmod     *ChmodSpec `json:"chmod,omitempty"`
	Chown     *ChownSpec `json:"chown,omitempty"`
	// Limits (FeatureFileLimits) bound archives (files.archive) and
	// extractions (files.extract).
	Limits *FileLimits `json:"limits,omitempty"`
}

// CleanRelativePath normalizes a user-supplied root-relative path ("" and
// "." are the root; one trailing slash is dropped) and reports whether it
// is acceptable: no leading slash, no empty, "." or ".." segments, no
// backslashes, NUL or control characters, valid UTF-8, at most 4096 bytes.
func CleanRelativePath(p string) (string, bool) {
	if len(p) > 4096 || !utf8.ValidString(p) || strings.HasPrefix(p, "/") {
		return "", false
	}
	p = strings.TrimSuffix(p, "/")
	if p == "" || p == "." {
		return ".", true
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
		for _, r := range seg {
			if r == '\\' || r < 0x20 || r == 0x7f {
				return "", false
			}
		}
	}
	return p, ValidRelativePath(p)
}
