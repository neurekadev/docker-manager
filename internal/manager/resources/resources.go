// Package resources is the manager side of #6: containers, images, volumes
// and networks of each environment. Reads are named agent requests
// (container.list, image.inspect, ...) through the session hub; every
// mutation is a job of the #26 engine (container.*, image.pull/remove,
// volume.*, network.*) whose agent executor re-checks what it can decide
// locally. There is no Engine API passthrough.
//
// The service also
//   - validates inputs before a job exists (422 instead of a failed job),
//     refuses conflicting edits of DockYard-managed stack containers
//     (stack_managed) and removals of objects in use;
//   - saves the recreate specification of standalone containers created
//     through DockYard (sealed, table managed_containers) for automatic
//     updates (#20) and labels them with its ID and the manager instance;
//   - forgets the exact permission rules of objects it removed (#17) and
//     keeps a small cache of Compose stack membership for the permission
//     Locators;
//   - resolves registry connections for pulls through RegistryResolver (#19)
//     and Compose projects to stacks through StackResolver (#7).
//
// Every lookup is scoped by environment ID: the agent of that environment
// answers, so an ID or name from another environment is simply not found.
package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/secrets"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Requester sends named requests to an environment's agent
// (agents.Hub.RequestEnvironment).
type Requester interface {
	RequestEnvironment(ctx context.Context, environmentID, name string, input any, timeout time.Duration) (json.RawMessage, error)
}

// JobEngine is the part of the #26 engine the service uses.
type JobEngine interface {
	Enqueue(ctx context.Context, req jobs.Request) (domain.Job, bool, error)
	Get(ctx context.Context, id string) (domain.Job, error)
	Subscribe(jobID string) (<-chan struct{}, func())
}

// Forgetter drops the exact permission rules of a removed resource
// (permissions.Service.ForgetResource, #17).
type Forgetter interface {
	ForgetResource(ctx context.Context, ref authz.ResourceRef) (int, error)
}

// RegistryResolver selects the manager-owned registry connection of a pull
// (#19, registries.Service.Select): the explicit connection, else the
// matching one (bindings, priority), nil Selected for anonymous access.
// Errors: *domain.AmbiguousRegistryError, domain.ErrRegistryConnectionNotFound,
// domain.ErrRegistryConnectionRevoked, domain.ErrRegistryConnectionMismatch.
// Only the connection ID travels in the job input; the job engine resolves
// it to the credential at every dispatch.
type RegistryResolver interface {
	Select(ctx context.Context, req domain.RegistrySelectRequest) (domain.RegistrySelection, error)
}

// StackResolver maps the Compose projects of an environment to DockYard
// stack IDs (#7). Projects it returns are DockYard-managed stacks.
type StackResolver interface {
	StackIDs(ctx context.Context, environmentID string) (map[string]string, error)
}

// RetainedProjects maps the Compose projects of an environment DockYard
// keeps although no stack manages them to the reason (the stopped sources
// of migrated stacks until their removal is confirmed, #35,
// migrations.Service.RetainedProjects). Their containers, volumes and
// networks are refused like a stack's (stack_managed).
type RetainedProjects func(ctx context.Context, environmentID string) (map[string]string, error)

// Options configures the service.
type Options struct {
	DB          *bun.DB
	Keyring     *secrets.Keyring
	Agents      Requester
	Jobs        JobEngine
	Permissions Forgetter
	// InstanceID is the manager instance ID, set as ownership label on
	// containers DockYard creates.
	InstanceID string
	// ManagerContainerID is the manager's own container ID (selfid, #32;
	// "" when it does not run in a container).
	ManagerContainerID string
	Clock              clock.Clock
	Logger             *slog.Logger
	// Registries (#19) and Stacks (#7) are optional until those
	// workstreams provide them.
	Registries RegistryResolver
	Stacks     StackResolver
	// Retained (#35) is optional (SetRetainedProjects).
	Retained RetainedProjects
	// RequestTimeout bounds agent requests (0: the hub's default).
	RequestTimeout time.Duration
}

// Service is the Docker resource service.
type Service struct {
	opts Options
	clk  clock.Clock
	log  *slog.Logger

	lifetime context.Context
	stop     context.CancelFunc
	wg       sync.WaitGroup

	mu    sync.Mutex
	stack map[cacheKey]protocol.StackRef
}

type cacheKey struct{ typ, env, name string }

// New returns a Service. Close stops its background watchers.
func New(opts Options) (*Service, error) {
	if opts.Agents == nil || opts.Jobs == nil || opts.DB == nil || opts.Keyring == nil {
		return nil, errors.New("resources: DB, Keyring, Agents and Jobs are required")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{opts: opts, clk: opts.Clock, log: opts.Logger, lifetime: ctx, stop: cancel, stack: map[cacheKey]protocol.StackRef{}}, nil
}

// SetStackResolver installs the #7 stack resolver.
func (s *Service) SetStackResolver(r StackResolver) { s.opts.Stacks = r }

// SetRetainedProjects installs the #35 hold on migrated stacks' sources
// (call while wiring, before the service is used).
func (s *Service) SetRetainedProjects(fn RetainedProjects) { s.opts.Retained = fn }

// SetRegistryResolver installs the #19 registry resolver.
func (s *Service) SetRegistryResolver(r RegistryResolver) { s.opts.Registries = r }

// Close stops the watchers of running removal jobs.
func (s *Service) Close() {
	s.stop()
	s.wg.Wait()
}

// request sends a named request and decodes its output.
func request[O any](ctx context.Context, s *Service, env, name string, input any) (O, error) {
	var out O
	raw, err := s.opts.Agents.RequestEnvironment(ctx, env, name, input, s.opts.RequestTimeout)
	if err != nil {
		return out, agentErr(err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, &domain.DockerError{Code: domain.DockerEngineError, Message: "the agent answered with a malformed " + name + " response"}
	}
	return out, nil
}

// agentErr maps transport and agent errors to stable Docker errors.
func agentErr(err error) error {
	var re protocol.CodedError
	switch {
	case errors.Is(err, jobs.ErrAgentOffline):
		return &domain.DockerError{Code: domain.DockerEnvironmentOffline,
			Message: "the environment's agent is not connected; operations resume when it reconnects"}
	case errors.Is(err, protocol.ErrRequestTimeout), errors.Is(err, context.DeadlineExceeded):
		return &domain.DockerError{Code: domain.DockerTimeout, Message: "the environment's agent did not answer in time"}
	case errors.As(err, &re):
		msg := re.ProtocolMessage()
		switch re.ProtocolCode() {
		case protocol.CodeNotFound:
			return &domain.DockerError{Code: domain.DockerNotFound, Message: msg}
		case protocol.CodeConflict:
			return &domain.DockerError{Code: domain.DockerConflict, Message: msg}
		case protocol.CodeInvalidArgument:
			return &domain.DockerError{Code: domain.DockerInvalid, Message: msg}
		case protocol.CodeUnsupportedAPIVersion:
			return &domain.DockerError{Code: domain.DockerUnsupportedAPIVersion,
				Message: "the environment's Docker Engine API version is too old for this operation (Docker Engine 25.0 or newer is required)"}
		case protocol.CodeEngineUnavailable:
			return &domain.DockerError{Code: domain.DockerEngineUnavailable, Message: "the agent cannot reach its Docker Engine"}
		case protocol.CodeDeadlineExceeded:
			return &domain.DockerError{Code: domain.DockerTimeout, Message: "the Docker Engine did not answer in time"}
		case protocol.CodeUnsupportedRequest:
			return &domain.DockerError{Code: domain.DockerAgentUnsupported,
				Message: "the environment's agent does not support this operation; upgrade the agent"}
		case protocol.CodeBusy:
			return &domain.DockerError{Code: domain.DockerBusy, Message: "the agent is busy; retry"}
		case protocol.CodeEngineError:
			return &domain.DockerError{Code: domain.DockerEngineError, Message: "the Docker Engine refused the operation: " + msg}
		}
		return &domain.DockerError{Code: domain.DockerEngineError, Message: "the agent failed (" + re.ProtocolCode() + ")"}
	}
	return err
}

// IsNotFound reports whether err is a Docker not_found error.
func IsNotFound(err error) bool {
	var de *domain.DockerError
	return errors.As(err, &de) && de.Code == domain.DockerNotFound
}

func dockerErr(code, format string, args ...any) *domain.DockerError {
	return &domain.DockerError{Code: code, Message: fmt.Sprintf(format, args...)}
}

func invalid(field, format string, args ...any) *domain.DockerError {
	return &domain.DockerError{Code: domain.DockerInvalid, Field: field, Message: fmt.Sprintf(format, args...)}
}

// fieldErr converts a protocol validation error of a body member.
func fieldErr(prefix string, err error) error {
	var fe *protocol.FieldError
	if errors.As(err, &fe) {
		return invalid(prefix+fe.Field, "%s", fe.Message)
	}
	return err
}

// enqueue starts a job for the principal.
func (s *Service) enqueue(ctx context.Context, p authz.Principal, env string, kind domain.JobKind, targets []domain.JobTarget, input any, key string) (domain.Job, bool, error) {
	return s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: kind, Principal: p, EnvironmentID: env, Targets: targets, Input: input, IdempotencyKey: key})
}

// followUpTimeout bounds the follow-up of a succeeded removal job.
const followUpTimeout = 30 * time.Second

// afterSuccess runs fn once the job succeeded (never when it fails or is
// cancelled). The watcher lives until the job ends or the service closes;
// a manager restart in between skips fn (the rules or record it would
// drop stay until the resource's next removal through DockYard).
func (s *Service) afterSuccess(jobID string, fn func(ctx context.Context)) {
	ch, cancel := s.opts.Jobs.Subscribe(jobID)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		for {
			j, err := s.opts.Jobs.Get(s.lifetime, jobID)
			if err != nil {
				return
			}
			if j.State.Terminal() {
				if j.State == domain.JobSucceeded {
					// Finish the follow-up even while the service closes.
					ctx, cancel := context.WithTimeout(context.WithoutCancel(s.lifetime), followUpTimeout)
					fn(ctx)
					cancel()
				}
				return
			}
			select {
			case <-ch:
			case <-s.lifetime.Done():
				return
			}
		}
	}()
}

// forget drops the exact permission rules of a removed resource.
func (s *Service) forget(ctx context.Context, ref authz.ResourceRef) {
	if s.opts.Permissions == nil {
		return
	}
	if _, err := s.opts.Permissions.ForgetResource(ctx, ref); err != nil && ctx.Err() == nil {
		s.log.Warn("could not forget the permission rules of a removed resource", "type", ref.Type, "environment_id", ref.EnvironmentID, "error", err)
	}
}

// specID derives the recreate-specification ID of a create request. With
// an idempotency key it is stable, so a repeated request carries the same
// input (and the job engine returns the existing job).
func specID(p authz.Principal, env, name, key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(p.UserID + "\x00" + p.TokenID + "\x00" + env + "\x00" + name + "\x00" + key))
	h := hex.EncodeToString(sum[:16])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
