package domain

import "errors"

// Scoped file manager errors (#15).
var (
	// ErrFileScopeNotFound: the stack (or volume scope) does not exist.
	ErrFileScopeNotFound = errors.New("file scope not found")
	// ErrFileAgentOffline: the environment's agent is not connected.
	ErrFileAgentOffline = errors.New("the environment's agent is offline")
	// ErrFileAgentTimeout: the agent did not answer in time.
	ErrFileAgentTimeout = errors.New("the agent did not answer in time")
)

// FileError is a file operation the agent refused or failed, with its
// agent protocol error code (not_found, forbidden_path, conflict,
// already_exists, too_large, unsupported_file, unsupported_volume, ...).
// Messages name root-relative paths only.
type FileError struct {
	Code    string
	Message string
}

func (e *FileError) Error() string { return e.Code + ": " + e.Message }

// FileLimits are the file manager's limits (#15): the manager's
// configuration (DOCKER_MANAGER_FILES_*), and the limits in effect for
// one file root (an older agent keeps its built-in defaults; template
// drafts have the template size limits). Sizes are bytes.
type FileLimits struct {
	// Edit is the largest file the editor opens for editing: the most
	// one read returns and one save accepts.
	Edit int64
	// Upload bounds one uploaded file.
	Upload int64
	// Download bounds one download or archive created in the root.
	Download int64
	// ExtractBytes bounds the bytes one extraction writes; ExtractRatio
	// the bytes written per archive byte (decompression bombs).
	ExtractBytes int64
	ExtractRatio int64
	// ArchiveEntries bounds the entries of an archive read or written.
	ArchiveEntries int
}
