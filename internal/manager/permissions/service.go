// Package permissions is DockYard's authorization service (#17): rule
// storage for groups and user overrides, the Authorizer every route and
// the job engine call, the resource graph (Locators) and the owner-only
// management flows (groups, default group, rule documents, effective
// permissions and "view as" previews).
//
// Evaluation (package authz/policy): owner bypass; otherwise the most
// specific user rule, then the most specific group rule, then deny. Rules
// are read from the database for every check (one load per Checker, i.e.
// per request for lists), so a change applies to the next check at once.
// Changes also end the affected users' open requests and streams and drop
// their stored idempotent responses (Invalidator), so nothing keeps
// serving the old access.
package permissions

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/manager/authz/policy"
)

// Guard checks the caller of owner-only flows (the identity service,
// auth.Service.RequireOwner): the owner's full session, and a recent
// step-up when recent is true.
type Guard interface {
	RequireOwner(ctx context.Context, recent bool) (userID string, err error)
}

// Invalidator is told when users' effective permissions may have changed
// (auth.Service.AccessChanged): it ends their open requests and streams
// and forgets their stored idempotent responses.
type Invalidator interface {
	AccessChanged(ctx context.Context, userIDs []string)
}

// Location is where a resource currently is: Found (false for deleted or
// unknown resources), its EnvironmentID and its Parents, nearest first.
type Location = policy.Location

// Locator resolves the resource graph for one resource type: its current
// environment and parents (container -> service -> stack). Feature
// workstreams register one per resource type they own
// (Service.RegisterLocator). Locate must be cheap (it runs per check when a
// Resource comes without Parents) and must not call the Authorizer.
type Locator interface {
	Locate(ctx context.Context, ref authz.ResourceRef) (Location, error)
}

// LocatorFunc adapts a function to Locator.
type LocatorFunc func(ctx context.Context, ref authz.ResourceRef) (Location, error)

// Locate implements Locator.
func (f LocatorFunc) Locate(ctx context.Context, ref authz.ResourceRef) (Location, error) {
	return f(ctx, ref)
}

// TokenScopes returns the scope of an API token (#31): allow rules only.
// ok is false for unknown, expired or revoked tokens (deny everything).
type TokenScopes func(ctx context.Context, tokenID string) (rules []domain.PermissionRule, ok bool, err error)

// Options configures the service.
type Options struct {
	DB     *bun.DB
	Clock  clock.Clock
	Logger *slog.Logger
	// Catalog defaults to catalog.Default().
	Catalog *catalog.Catalog
	// Guard is required for the management flows.
	Guard Guard
	// Invalidator is optional (tests).
	Invalidator Invalidator
}

// Service implements authz.Authorizer, authz.Compiler and the #17
// management flows.
type Service struct {
	db    *bun.DB
	clk   clock.Clock
	log   *slog.Logger
	cat   *catalog.Catalog
	guard Guard
	inval Invalidator

	mu       sync.RWMutex
	locators map[string]Locator
	tokens   TokenScopes
}

// New returns the service.
func New(o Options) (*Service, error) {
	if o.DB == nil {
		return nil, errors.New("permissions: database is required")
	}
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Catalog == nil {
		o.Catalog = catalog.Default()
	}
	return &Service{db: o.DB, clk: o.Clock, log: o.Logger, cat: o.Catalog, guard: o.Guard, inval: o.Invalidator,
		locators: map[string]Locator{}}, nil
}

// Catalog returns the permission catalog.
func (s *Service) Catalog() *catalog.Catalog { return s.cat }

// RegisterLocator installs the Locator of a resource type (replacing a
// previous one). Call it during startup.
func (s *Service) RegisterLocator(resourceType string, l Locator) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locators[resourceType] = l
}

// SetTokenScopes installs the API token scope source (#31). Without it
// every API-token principal is denied.
func (s *Service) SetTokenScopes(f TokenScopes) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = f
}

func (s *Service) locator(typ string) Locator {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.locators[typ]
}

func (s *Service) tokenScopes() TokenScopes {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tokens
}

// locate resolves a resource's location through the registered Locator
// (policy.Checker adds the built-in service and per-environment-name
// rules when it does not know the resource).
func (s *Service) locate(ctx context.Context, ref authz.ResourceRef) Location {
	l := s.locator(ref.Type)
	if l == nil {
		return Location{}
	}
	loc, err := l.Locate(ctx, ref)
	if err != nil {
		s.log.Warn("locate resource for authorization", "resource_type", ref.Type, "error", err)
		return Location{}
	}
	return loc
}

func (s *Service) invalidate(ctx context.Context, userIDs []string) {
	if s.inval != nil && len(userIDs) > 0 {
		s.inval.AccessChanged(context.WithoutCancel(ctx), userIDs)
	}
}
