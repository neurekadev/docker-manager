// Package resources serves the agent side of #6: the named requests that
// read containers, images, volumes and networks (plus image.tag) and the
// job executors of the container.*, image.pull/remove, volume.* and
// network.* kinds. Every Engine call goes through the engine.Engine adapter
// (#21); the manager sends only these typed operations, never an Engine API
// passthrough.
//
// The agent re-checks what it can decide locally before every destructive
// step, whatever the manager decided: containers of a Docker Manager-managed
// Compose stack are not updated or removed directly (stack_managed), and
// images, volumes and networks still in use are not removed (in_use).
// Wiring: runtime.Options.Requests and .Executors (internal/agent/runtime).
package resources

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/protect"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Options configures the service.
type Options struct {
	// Engine returns the connected Engine adapter, or nil while the Engine
	// is unreachable (runtime.Agent.Engine).
	Engine func() engine.Engine
	// ManagedStackDir reports whether a Compose project working directory
	// lies inside a verified stack root (#28), i.e. the stack is managed by
	// Docker Manager (#7). nil: no stack is managed.
	ManagedStackDir func(dir string) bool
	// Guard identifies Docker Manager's own resources (#32); nil protects only
	// what the labels show (no self container, no stacks volume).
	Guard  *protect.Guard
	Logger *slog.Logger
}

// Service implements the requests and executors.
type Service struct {
	opts  Options
	log   *slog.Logger
	guard *protect.Guard
}

// New returns a Service.
func New(opts Options) *Service {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	guard := opts.Guard
	if guard == nil {
		guard = protect.New(protect.Options{Logger: log})
	}
	return &Service{opts: opts, log: log.With("component", "resources"), guard: guard}
}

// protected lists the Engine's containers and identifies Docker Manager's own
// resources among them (#32).
func (s *Service) protected(ctx context.Context, eng engine.Engine) (*protect.Set, error) {
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return nil, err
	}
	return s.guard.Identify(ctx, eng, cs), nil
}

// errEngineUnavailable is returned while the Engine is not connected.
var errEngineUnavailable = &engine.Error{Op: "engine", Code: engine.CodeEngineUnavailable, Message: "the Docker Engine is not connected"}

func (s *Service) engine() (engine.Engine, error) {
	if s.opts.Engine == nil {
		return nil, errEngineUnavailable
	}
	if e := s.opts.Engine(); e != nil {
		return e, nil
	}
	return nil, errEngineUnavailable
}

// stackOf returns the Compose project of an object's labels (nil when it
// is not part of one). Managed says whether Docker Manager manages it (the
// project's working directory is in a verified stack root).
func (s *Service) stackOf(labels map[string]string) *protocol.StackRef {
	project := labels[protocol.ComposeProjectLabel]
	if project == "" {
		return nil
	}
	ref := &protocol.StackRef{Project: project, Service: labels[protocol.ComposeServiceLabel]}
	if dir := labels[protocol.ComposeWorkingDirLabel]; dir != "" && s.opts.ManagedStackDir != nil && path.IsAbs(dir) {
		ref.Managed = s.opts.ManagedStackDir(path.Clean(dir))
	}
	return ref
}

// managedProjects returns the Compose projects with at least one container
// in a verified stack root: their volumes and networks belong to a
// Docker Manager-managed stack.
func (s *Service) managedProjects(cs []engine.Container) map[string]bool {
	out := map[string]bool{}
	for _, c := range cs {
		if st := s.stackOf(c.Labels); st != nil && st.Managed {
			out[st.Project] = true
		}
	}
	return out
}

// objectStack is the stack of a volume or network from its Compose
// project label and the managed projects.
func objectStack(labels map[string]string, managed map[string]bool) *protocol.StackRef {
	p := labels[protocol.ComposeProjectLabel]
	if p == "" {
		return nil
	}
	return &protocol.StackRef{Project: p, Managed: managed[p]}
}

func containerName(c engine.Container) string {
	if len(c.Names) > 0 {
		return c.Names[0]
	}
	if len(c.ID) > 12 {
		return c.ID[:12]
	}
	return c.ID
}

func ref(c engine.Container) protocol.ContainerRef {
	return protocol.ContainerRef{ID: c.ID, Name: containerName(c), State: c.State}
}

// Operation errors: a stable class (the job's error class, #6) and recovery
// guidance for operators.

// OpError is a classified failure of a request or job step.
type OpError struct {
	Class    string
	Message  string
	recovery string
	err      error
}

func (e *OpError) Error() string { return e.Message }

// Unwrap returns the underlying adapter error.
func (e *OpError) Unwrap() error { return e.err }

// ErrorClass implements jobexec.ClassedError.
func (e *OpError) ErrorClass() string { return e.Class }

// Recovery implements jobexec.ClassedError.
func (e *OpError) Recovery() string { return e.recovery }

// Error classes of Docker resource jobs besides the Engine codes (#6).
const (
	ClassStackManaged   = "stack_managed"
	ClassImageInUse     = "image_in_use"
	ClassVolumeInUse    = "volume_in_use"
	ClassNetworkInUse   = "network_in_use"
	ClassNetworkBuiltin = "network_builtin"
	ClassNameTaken      = "resource_name_taken"
	ClassRecreated      = "container_replaced"
	ClassInvalidInput   = "invalid_input"
)

func refuse(class, recovery, format string, args ...any) *OpError {
	return &OpError{Class: class, Message: fmt.Sprintf(format, args...), recovery: recovery}
}

// recoveries for the Engine codes.
var recoveries = map[engine.Code]string{
	engine.CodeNotFound: "The object does not exist (any more) on this Engine. Refresh the inventory; pull a missing image first.",
	engine.CodeConflict: "The Engine refused the change because of the object's current state (name in use, running, in use). Refresh the inventory and retry.",
	engine.CodeUnauthorized: "The registry refused the credentials (or requires them). Check the registry connection selected for this pull " +
		"(or add one for a private image) and retry.",
	engine.CodeForbidden:           "The registry or Engine denied access. Check the account's permissions on the repository.",
	engine.CodeRateLimited:         "The registry's rate limit was reached. Wait before retrying; an authenticated connection may raise the limit but does not remove it.",
	engine.CodeRegistryUnavailable: "The registry is unavailable. Retry later.",
	engine.CodeUnsupportedAPIVersion: "The Docker Engine's API version is too old for this operation. Upgrade Docker Engine to 25.0 or newer " +
		"(see the support matrix).",
	engine.CodeUnsupported:       "This Engine does not support the operation.",
	engine.CodeEngineUnavailable: "The Docker Engine is not reachable from the agent. Check that Docker is running on the host, then retry.",
	engine.CodeTimeout:           "The Engine did not answer in time. Check the host's load and retry.",
	engine.CodeInvalidArgument:   "The Engine rejected the input. Correct it and retry.",
}

// engineErr classifies an adapter error for a job step.
func engineErr(err error) error {
	if err == nil {
		return nil
	}
	var op *OpError
	if errors.As(err, &op) {
		return err
	}
	code := engine.CodeOf(err)
	rec := recoveries[code]
	if rec == "" {
		rec = "The Engine reported an error. Check the message, fix the cause and retry."
	}
	return &OpError{Class: string(code), Message: err.Error(), recovery: rec, err: err}
}

// handlerErr maps an error to a request error frame.
func handlerErr(err error) error {
	var op *OpError
	if errors.As(err, &op) {
		code := protocol.CodeConflict
		if op.Class == ClassInvalidInput {
			code = protocol.CodeInvalidArgument
		}
		return &session.HandlerError{Code: code, Message: op.Message}
	}
	var fe *protocol.FieldError
	if errors.As(err, &fe) {
		return &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: fe.Error()}
	}
	msg := err.Error()
	switch engine.CodeOf(err) {
	case engine.CodeNotFound:
		return &session.HandlerError{Code: protocol.CodeNotFound, Message: msg}
	case engine.CodeConflict:
		return &session.HandlerError{Code: protocol.CodeConflict, Message: msg}
	case engine.CodeInvalidArgument:
		return &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: msg}
	case engine.CodeUnsupportedAPIVersion:
		return &session.HandlerError{Code: protocol.CodeUnsupportedAPIVersion, Message: msg}
	case engine.CodeEngineUnavailable:
		return &session.HandlerError{Code: protocol.CodeEngineUnavailable, Message: msg, Retryable: true}
	case engine.CodeTimeout:
		return &session.HandlerError{Code: protocol.CodeDeadlineExceeded, Message: msg, Retryable: true}
	case engine.CodeCanceled:
		return &session.HandlerError{Code: protocol.CodeCancelled, Message: msg}
	}
	return &session.HandlerError{Code: protocol.CodeEngineError, Message: bound(msg)}
}

func bound(s string) string {
	if len(s) > 512 {
		return s[:512]
	}
	return s
}

func namesOf(cs []protocol.ContainerRef) string {
	n := make([]string, 0, len(cs))
	for _, c := range cs {
		n = append(n, c.Name)
	}
	slices.Sort(n)
	if len(n) > 5 {
		return strings.Join(n[:5], ", ") + fmt.Sprintf(" and %d more", len(n)-5)
	}
	return strings.Join(n, ", ")
}
