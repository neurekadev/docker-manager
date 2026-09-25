// Package prune is the agent side of Docker maintenance (#14): the
// maintenance.preview request and the prune.run job executor.
//
// Candidates are computed by listing the Engine's objects and filtering
// them with the policy's rules, DockYard's self-protection (#32,
// internal/agent/protect) and the protections the manager sends (DockYard
// stacks, saved container specifications, backup destinations). Every
// candidate is removed with a targeted call (container, image, network or
// volume remove; a build cache prune restricted to one record ID) after it
// was revalidated against a fresh read of the Engine: an object that
// became used, protected, excluded or too recent since the preview is
// skipped with the reason. The broad Engine prune endpoints are never
// called, so an item-level exclusion can never be widened.
package prune

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/protect"
	"github.com/neurekadev/dockyard/internal/agent/session"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Options configures the service.
type Options struct {
	// Engine returns the connected Engine (nil while unreachable).
	Engine func() engine.Engine
	// ManagedStackDir reports whether a Compose working directory lies in
	// a verified stack root (the project is a DockYard stack, #7).
	ManagedStackDir func(dir string) bool
	// Guard identifies DockYard's own resources (#32).
	Guard  *protect.Guard
	Clock  clock.Clock
	Logger *slog.Logger
}

// Service implements the request and the executor.
type Service struct {
	opts  Options
	log   *slog.Logger
	guard *protect.Guard
	clk   clock.Clock
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
	clk := opts.Clock
	if clk == nil {
		clk = clock.Real()
	}
	return &Service{opts: opts, log: log.With("component", "prune"), guard: guard, clk: clk}
}

var errEngineUnavailable = &engine.Error{Op: "engine", Code: engine.CodeEngineUnavailable, Message: "the Docker Engine is not connected"}

func (s *Service) engine() (engine.Engine, error) {
	if s.opts.Engine != nil {
		if e := s.opts.Engine(); e != nil {
			return e, nil
		}
	}
	return nil, errEngineUnavailable
}

// Requests returns the maintenance.preview handler.
func (s *Service) Requests() map[string]session.RequestHandler {
	return map[string]session.RequestHandler{protocol.ReqMaintenancePreview: s.preview}
}

func decodeInput(raw []byte) (protocol.PruneInput, error) {
	var in protocol.PruneInput
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, &protocol.FieldError{Field: "input", Message: "malformed prune input"}
	}
	return in, in.Validate()
}

func (s *Service) preview(ctx context.Context, raw json.RawMessage) (any, error) {
	in, err := decodeInput(raw)
	if err != nil {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: err.Error()}
	}
	eng, err := s.engine()
	if err != nil {
		return nil, handlerErr(err)
	}
	p, err := s.plan(ctx, eng, in)
	if err != nil {
		return nil, handlerErr(err)
	}
	return p.preview(), nil
}

// handlerErr maps an Engine error to a request error frame.
func handlerErr(err error) error {
	msg := err.Error()
	if len(msg) > 512 {
		msg = msg[:512]
	}
	switch engine.CodeOf(err) {
	case engine.CodeEngineUnavailable:
		return &session.HandlerError{Code: protocol.CodeEngineUnavailable, Message: msg, Retryable: true}
	case engine.CodeTimeout:
		return &session.HandlerError{Code: protocol.CodeDeadlineExceeded, Message: msg, Retryable: true}
	case engine.CodeCanceled:
		return &session.HandlerError{Code: protocol.CodeCancelled, Message: msg}
	case engine.CodeUnsupportedAPIVersion:
		return &session.HandlerError{Code: protocol.CodeUnsupportedAPIVersion, Message: msg}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &session.HandlerError{Code: protocol.CodeDeadlineExceeded, Message: msg, Retryable: true}
	}
	return &session.HandlerError{Code: protocol.CodeEngineError, Message: msg}
}
