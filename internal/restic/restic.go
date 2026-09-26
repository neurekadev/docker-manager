// Package restic runs the pinned restic executable for Docker Manager backups
// (#10): manager-state snapshots on the manager, stack and volume data on
// the agents. Both images ship the same checksum-verified restic release at
// DefaultBinary; nothing depends on a host-installed restic.
//
// The package is the only place in Docker Manager that executes a process for
// backups (the lint exclusion names runner.go). Its rules:
//
//   - Secrets never appear in arguments: the repository password (the
//     Recovery Key) reaches restic through RESTIC_PASSWORD_FILE pointing at
//     an inherited pipe (/dev/fd/N on Linux) or, elsewhere, a 0600 file in
//     a private temporary directory that is removed when the process ends.
//     S3 credentials are set only in the child's environment. The child
//     environment is built from scratch (nothing of the parent's
//     environment leaks in, except SYSTEMROOT on Windows).
//   - Output is parsed from restic's JSON messages; error text taken from
//     stderr is bounded and scrubbed of every secret of the call.
//   - Exit codes are classified into stable error codes (Error.Code).
//   - Cancellation sends SIGINT (restic removes its locks) and kills the
//     process after a grace period.
//
// Callers use the Repo interface (Opener.Open); tests of the executors use
// the in-memory implementation in restictest.
package restic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// DefaultBinary is where both Docker Manager images install restic.
const DefaultBinary = "/usr/local/bin/restic"

// Version is the pinned restic release (deploy/docker/*.Dockerfile).
const Version = "0.19.1"

// Location addresses one physical restic repository.
type Location struct {
	// Repository is a local absolute path or "s3:<endpoint>/<bucket>/<prefix>".
	Repository string
	// S3 settings (Repository starts with "s3:").
	S3 *S3
}

// S3 carries the S3 connection settings of a Location. The credentials are
// passed to restic in the child's environment only.
type S3 struct {
	AccessKeyID     string
	SecretAccessKey string
	Region          string
	// PathStyle selects path-style bucket addressing (MinIO and most
	// self-hosted S3 servers).
	PathStyle bool
}

// String describes the location without credentials.
func (l Location) String() string { return l.Repository }

// GoString hides the credentials (%#v).
func (l Location) GoString() string { return "restic.Location{" + l.Repository + "}" }

// Secrets returns the secret values of the location (for scrubbing).
func (l Location) Secrets() []string {
	if l.S3 == nil {
		return nil
	}
	return []string{l.S3.AccessKeyID, l.S3.SecretAccessKey}
}

// Opener opens repositories.
type Opener interface {
	// Open returns a handle on the repository at loc using password (the
	// Recovery Key). Nothing is executed until a method is called.
	Open(loc Location, password string) Repo
}

// Repo is one physical repository opened with one password.
type Repo interface {
	// Init creates the repository and returns its restic repository ID.
	Init(ctx context.Context) (string, error)
	// Config reads the repository configuration (proves the password and
	// the location work) and returns the repository ID.
	Config(ctx context.Context) (Config, error)
	// Backup creates a snapshot.
	Backup(ctx context.Context, req BackupRequest) (BackupSummary, error)
	// Snapshots lists snapshots, optionally filtered by tags (all of them
	// must match) and host.
	Snapshots(ctx context.Context, f SnapshotFilter) ([]Snapshot, error)
	// Ls lists the nodes of a snapshot below dir ("" or "/" = everything),
	// at most limit nodes (0 = DefaultMaxNodes); Truncated reports more.
	Ls(ctx context.Context, snapshotID, dir string, recursive bool, limit int) (Listing, error)
	// Dump writes one file of a snapshot to w.
	Dump(ctx context.Context, snapshotID, file string, w io.Writer) error
	// Restore restores a snapshot (or parts of it) into a target directory.
	Restore(ctx context.Context, req RestoreRequest) (RestoreSummary, error)
	// Forget removes the given snapshots (no pruning).
	Forget(ctx context.Context, snapshotIDs []string) error
	// Prune removes data no snapshot references.
	Prune(ctx context.Context) error
	// Stats reports the repository's raw data size.
	Stats(ctx context.Context) (Stats, error)
	// Check verifies the repository structure and, with ReadDataSubset,
	// part of the data. A damaged repository is an *Error with
	// CodeRepositoryDamaged.
	Check(ctx context.Context, req CheckRequest) (CheckResult, error)
	// Keys lists the repository's keys.
	Keys(ctx context.Context) ([]Key, error)
	// AddKey adds newPassword as an additional key.
	AddKey(ctx context.Context, newPassword string) error
	// RemoveKey removes a key by ID (never the one used to open).
	RemoveKey(ctx context.Context, id string) error
	// Unlock removes stale locks left by crashed processes.
	Unlock(ctx context.Context) error
}

// Config is the repository configuration.
type Config struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

// BackupRequest describes a snapshot.
type BackupRequest struct {
	// Paths are absolute paths to back up (ignored with Stdin).
	Paths []string
	// Excludes are restic exclude patterns (--exclude).
	Excludes []string
	Tags     []string
	// Host is recorded as the snapshot's hostname (--host).
	Host string
	// Stdin backs up one stream as StdinFilename.
	Stdin         io.Reader
	StdinFilename string
	// Dir is the working directory (relative Paths are stored relative).
	Dir string
	// Time overrides the snapshot time (--time), zero = now.
	Time time.Time
	// Progress receives status updates (may be nil).
	Progress func(Progress)
}

// Progress is a status update of a backup or restore.
type Progress struct {
	Percent    float64
	FilesDone  int64
	FilesTotal int64
	BytesDone  int64
	BytesTotal int64
}

// BackupSummary is the result of a backup.
type BackupSummary struct {
	SnapshotID          string
	FilesNew            int64
	FilesChanged        int64
	FilesUnmodified     int64
	DataAdded           int64
	TotalFilesProcessed int64
	TotalBytesProcessed int64
	// Incomplete: some source files could not be read (restic exit code
	// 3); the snapshot exists but lacks them. Errors lists them (bounded).
	Incomplete bool
	Errors     []string
}

// SnapshotFilter selects snapshots.
type SnapshotFilter struct {
	Tags []string
	Host string
}

// Snapshot is a restic snapshot.
type Snapshot struct {
	ID       string    `json:"id"`
	ShortID  string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Paths    []string  `json:"paths"`
	Tags     []string  `json:"tags"`
	Summary  *struct {
		TotalFilesProcessed int64 `json:"total_files_processed"`
		TotalBytesProcessed int64 `json:"total_bytes_processed"`
		DataAdded           int64 `json:"data_added"`
	} `json:"summary,omitempty"`
}

// HasTag reports whether the snapshot carries tag.
func (s Snapshot) HasTag(tag string) bool {
	for _, t := range s.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// DefaultMaxNodes bounds Ls.
const DefaultMaxNodes = 10000

// Node is an entry of a snapshot.
type Node struct {
	Name  string    `json:"name"`
	Type  string    `json:"type"`
	Path  string    `json:"path"`
	Size  int64     `json:"size"`
	Mode  uint32    `json:"mode"`
	UID   int       `json:"uid"`
	GID   int       `json:"gid"`
	MTime time.Time `json:"mtime"`
}

// Listing is the result of Ls.
type Listing struct {
	Nodes     []Node
	Truncated bool
}

// RestoreRequest describes a restore.
type RestoreRequest struct {
	SnapshotID string
	// Target is the directory the snapshot's paths are restored below
	// (restic keeps the original absolute paths below it).
	Target string
	// Include / Exclude are restic patterns (--include / --exclude).
	Include []string
	Exclude []string
	// Overwrite is restic's --overwrite mode: always, if-changed,
	// if-newer or never (default if-changed).
	Overwrite string
	// Delete removes files in the target that are not in the snapshot
	// (--delete) within the restored paths.
	Delete   bool
	Progress func(Progress)
}

// RestoreSummary is the result of a restore.
type RestoreSummary struct {
	TotalFiles    int64
	FilesRestored int64
	FilesSkipped  int64
	TotalBytes    int64
	BytesRestored int64
}

// Stats is the repository size.
type Stats struct {
	TotalSize        int64 `json:"total_size"`
	TotalUncompSize  int64 `json:"total_uncompressed_size"`
	SnapshotsCount   int64 `json:"snapshots_count"`
	TotalBlobCount   int64 `json:"total_blob_count"`
	CompressionRatio float64
}

// CheckRequest configures a check.
type CheckRequest struct {
	// ReadDataSubset reads part of the data ("10%", "1/5", "500M"); empty
	// checks structure and metadata only.
	ReadDataSubset string
}

// CheckResult is a successful check.
type CheckResult struct {
	// ReadData reports whether pack data was read.
	ReadData bool
}

// Key is a repository key.
type Key struct {
	ID       string `json:"id"`
	Current  bool   `json:"current"`
	UserName string `json:"userName"`
	HostName string `json:"hostName"`
	// Created is restic's local timestamp text ("2006-01-02 15:04:05").
	Created string `json:"created"`
}

// Error codes (Error.Code). They are stable: job error classes and API
// error details use them.
const (
	// CodeRepositoryNotFound: nothing (or no restic repository) exists at
	// the location.
	CodeRepositoryNotFound = "repository_not_found"
	// CodeKeyRejected: the password (Recovery Key) does not open the
	// repository.
	CodeKeyRejected = "recovery_key_rejected"
	// CodeLocked: another process holds an exclusive lock.
	CodeLocked = "repository_locked"
	// CodeAccessDenied: the storage refused the credentials.
	CodeAccessDenied = "storage_access_denied"
	// CodeUnreachable: the storage could not be reached.
	CodeUnreachable = "storage_unreachable"
	// CodeRepositoryDamaged: check found errors (corrupt or truncated
	// data, missing packs).
	CodeRepositoryDamaged = "repository_damaged"
	// CodeSnapshotNotFound: the snapshot (or path in it) does not exist.
	CodeSnapshotNotFound = "snapshot_not_found"
	// CodeRepositoryExists: init found an existing repository.
	CodeRepositoryExists = "repository_exists"
	// CodeUnavailable: the restic executable could not be run.
	CodeUnavailable = "restic_unavailable"
	// CodeCancelled: the operation was cancelled.
	CodeCancelled = "cancelled"
	// CodeFailed: any other failure.
	CodeFailed = "restic_failed"
)

// Error is a failed restic operation. Message never contains secrets.
type Error struct {
	Op       string
	Code     string
	ExitCode int
	Message  string
}

func (e *Error) Error() string {
	msg := "restic " + e.Op + ": " + e.Code
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// ErrorClass implements jobexec.ClassedError: the code is the job's error
// class.
func (e *Error) ErrorClass() string { return e.Code }

// Recovery implements jobexec.ClassedError.
func (e *Error) Recovery() string { return RecoveryFor(e.Code) }

// RecoveryFor returns operator guidance for an error code.
func RecoveryFor(code string) string {
	switch code {
	case CodeRepositoryNotFound:
		return "No restic repository exists at this location. Check the path, bucket and prefix, or initialize the repository."
	case CodeKeyRejected:
		return "The Recovery Key does not open this repository. Enter the Recovery Key that was current when the repository was last used; " +
			"after an interrupted key rotation it may still use the previous key."
	case CodeLocked:
		return "Another backup, restore or maintenance run holds the repository lock. Wait for it to finish and try again."
	case CodeAccessDenied:
		return "The storage refused the credentials. Check the S3 access key, its permissions (read, write, delete) and the bucket policy."
	case CodeUnreachable:
		return "The storage could not be reached. Check the endpoint, DNS and network access from the executing host."
	case CodeRepositoryDamaged:
		return "The repository check found damaged or missing data. Keep the repository unchanged, run a new backup to another repository, " +
			"and consult the restic documentation on repairing repositories."
	case CodeSnapshotNotFound:
		return "The snapshot no longer exists (it may have been removed by retention). Choose another snapshot."
	case CodeRepositoryExists:
		return "A repository already exists at this location; connect it instead of initializing a new one."
	case CodeUnavailable:
		return "The restic executable is missing from the image. Use the official Docker Manager images."
	case CodeCancelled:
		return "The operation was cancelled; run it again when needed."
	}
	return "Check the job details and the repository, then run the operation again."
}

// CodeOf returns the error code of err ("" when err is not an *Error).
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// IsCode reports whether err is an *Error with code.
func IsCode(err error, code string) bool { return CodeOf(err) == code }

func errorf(op, code string, format string, args ...any) *Error {
	return &Error{Op: op, Code: code, Message: fmt.Sprintf(format, args...)}
}
