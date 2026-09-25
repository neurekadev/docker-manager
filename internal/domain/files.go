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
