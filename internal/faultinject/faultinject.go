// Package faultinject provides named fault points for crash/failure tests
// (#26, #29).
//
// Production code marks stage boundaries with
//
//	if err := faultinject.Point(ctx, "engine.dispatch.after_commit"); err != nil {
//		return err
//	}
//
// In normal builds Point is a no-op that always returns nil and is inlined
// away. Built with `-tags faultinject`, a point can be armed through the
// environment:
//
//	DOCKYARD_FAULTPOINT=<name>:<action>[,<name>:<action>...]
//
// where action is crash (os.Exit(ExitCode) immediately, like a kill -9 at
// that line), error (Point returns an error wrapping ErrInjected) or block
// (Point blocks until ctx is done, simulating a hang). With
// DOCKYARD_FAULTPOINT_TRACE=<file> every point reached is appended to the
// file, one name per line, so tests can enumerate the stages of a flow.
//
// Point names are dotted lowercase identifiers without ':' or ','.
package faultinject

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Environment variables read by fault-injection builds.
const (
	EnvVar   = "DOCKYARD_FAULTPOINT"
	TraceEnv = "DOCKYARD_FAULTPOINT_TRACE"
)

// ExitCode is the process exit status of the crash action.
const ExitCode = 86

// ErrInjected is wrapped by errors returned from armed error points.
var ErrInjected = errors.New("faultinject: injected fault")

// Action is what an armed point does.
type Action string

// Actions.
const (
	Crash Action = "crash"
	Error Action = "error"
	Block Action = "block"
)

var nameRE = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_-]+)*$`)

// ValidName reports whether name is a valid point name.
func ValidName(name string) bool { return nameRE.MatchString(name) }

// ParseSpec parses a DOCKYARD_FAULTPOINT value.
func ParseSpec(s string) (map[string]Action, error) {
	out := map[string]Action{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		i := strings.LastIndexByte(part, ':')
		if i <= 0 {
			return nil, fmt.Errorf("faultinject: %q: want name:action", part)
		}
		name, action := part[:i], Action(part[i+1:])
		if !ValidName(name) {
			return nil, fmt.Errorf("faultinject: invalid point name %q", name)
		}
		switch action {
		case Crash, Error, Block:
		default:
			return nil, fmt.Errorf("faultinject: %q: action must be crash, error or block", part)
		}
		out[name] = action
	}
	return out, nil
}
