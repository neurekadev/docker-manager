package domain

import "errors"

// Container logs and exec sessions (#8). Transport failures use DockerError
// codes (environment_offline, engine_unavailable, timeout, ...).
var (
	// ErrContainerGone: the followed container was removed.
	ErrContainerGone = errors.New("the container was removed")
	// ErrExecSessionNotFound: no such exec session for this caller (unknown,
	// expired, ended, another principal's, or a wrong ticket).
	ErrExecSessionNotFound = errors.New("exec session not found")
	// ErrExecSessionLimit: the caller or the container has too many open
	// exec sessions.
	ErrExecSessionLimit = errors.New("too many exec sessions")
)
