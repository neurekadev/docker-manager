// Package agents is the manager side of agent enrollment, the agent session
// transport and the environment model (#3):
//
//   - enrollment tokens (one-use, short-lived, verifier-only storage, an
//     intent fixed at creation: new, replace:<agentId>, reattach:<envId>)
//     and their exchange for an agent credential on POST /agent/v1/enroll;
//   - agents and environments: one active agent per Docker Engine, Engine
//     ID plus agent-generated install ID as the installation identity,
//     editable environment names and service addresses, archive and
//     re-attach, credential rotation and revocation;
//   - the session hub: GET /agent/v1/session WebSockets authenticated by
//     the agent's bearer credential, the hello/welcome/capabilities
//     handshake with the N-1 version window, heartbeats, job dispatch
//     (jobs.AgentDispatcher) and job frame routing into the job engine,
//     named requests, and relayed Docker events and file invalidations
//     published on the internal event bus (internal/manager/events).
//
// Protocol: docs/internal/protocol/agent-v1.md. Usage for feature workstreams: the
// docs/internal/conventions/agent-transport.md.
package agents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/api"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/movelock"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/secrets"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// Enrollment lifetimes.
const (
	DefaultEnrollmentTTL = time.Hour
	MinEnrollmentTTL     = time.Minute
	MaxEnrollmentTTL     = 24 * time.Hour
	// enrollmentRetention: enrollments are deleted this long after they
	// expired (their verifiers can no longer be used).
	enrollmentRetention = 7 * 24 * time.Hour
)

// DefaultAgentImage is the agent image used in generated install commands.
const DefaultAgentImage = "code.neureka.dev/docker-manager/docker-agent:edge"

// Options configures a Service.
type Options struct {
	DB     *bun.DB
	Clock  clock.Clock
	Logger *slog.Logger
	// Keyring seals pending rotation credentials until the agent confirms
	// them (secrets.Keyring).
	Keyring *secrets.Keyring
	// Bus receives environment/agent/enrollment events; nil drops them.
	Bus *events.Bus
	// ManagerVersion is compared with agent versions (N-1 window).
	ManagerVersion string
	// PublicURL is DOCKER_MANAGER_PUBLIC_URL, printed in install commands.
	PublicURL *url.URL
	// AgentImage overrides DefaultAgentImage in install commands.
	AgentImage string
	// Session tunes the session protocol (zero: the protocol defaults).
	Session SessionOptions
	// Attempts are the per-token/credential attempt limits (zero: defaults).
	Attempts AttemptLimits
	// Audit records /agent/v1 events (#30); nil records nothing.
	Audit AuditLog
	// Generation is the instance's generation, sent in every welcome: an
	// agent that has seen a higher one refuses this manager
	// (docs/internal/architecture/manager-move.md). 0 sends none.
	Generation int64
	// MoveLock is the manager-move lock: once agents are refused (the
	// state was handed to a new manager) enrollment and session upgrades
	// answer 503 with Retry-After and every open session closes with 1012
	// (service restart). Never 401, 4401, 4403 or 4409: agents would drop
	// their credential or stop. nil: never locked.
	MoveLock *movelock.Lock
}

// Service implements enrollment, the agent/environment model and the
// session hub. Create it with New; call AttachJobs before serving.
type Service struct {
	db      *bun.DB
	clk     clock.Clock
	log     *slog.Logger
	keyring *secrets.Keyring
	bus     *events.Bus
	audit   AuditLog
	opts    Options
	hub     *Hub
	// archiveHook runs in the archive transaction (#34).
	archiveHook ArchiveHook
}

// New creates the service.
func New(opts Options) (*Service, error) {
	if opts.DB == nil {
		return nil, errors.New("agents: DB is required")
	}
	if opts.Keyring == nil {
		return nil, errors.New("agents: keyring is required")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.ManagerVersion == "" {
		return nil, errors.New("agents: manager version is required")
	}
	if opts.AgentImage == "" {
		opts.AgentImage = DefaultAgentImage
	}
	s := &Service{db: opts.DB, clk: opts.Clock, log: opts.Logger, keyring: opts.Keyring, bus: opts.Bus, audit: opts.Audit, opts: opts}
	s.hub = newHub(s, opts.Session.withDefaults())
	opts.MoveLock.OnChange(func(l movelock.Level) {
		if l >= movelock.AgentsRefused {
			s.hub.closeAll(websocket.StatusServiceRestart, "Docker Manager moved to a new server")
		}
	})
	return s, nil
}

var _ api.AgentService = (*Service)(nil)

// Hub returns the session hub (the job engine's AgentDispatcher).
func (s *Service) Hub() *Hub { return s.hub }

// AttachJobs connects the job engine: job frames from agents go to it and
// it is woken when environments come online. Call once before serving.
func (s *Service) AttachJobs(j JobEngine) { s.hub.attachJobs(j) }

func (s *Service) now() time.Time { return s.clk.Now().UTC().Truncate(time.Microsecond) }

func (s *Service) tx(ctx context.Context, fn func(ctx context.Context, tx bun.Tx) error) error {
	return s.db.RunInTx(ctx, nil, fn)
}

func (s *Service) publish(evs ...events.Event) {
	for _, e := range evs {
		s.bus.Publish(e)
	}
}

// ResetOnline marks every environment offline. The manager calls it at
// startup: no agent session survives a manager restart, and environments
// come back online only after their agent reconnected and its jobs were
// reconciled.
func (s *Service) ResetOnline(ctx context.Context) error {
	var ids []string
	err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		var err error
		ids, err = store.SetEnvironmentsOffline(ctx, tx, s.now())
		return err
	})
	if err != nil {
		return err
	}
	for _, id := range ids {
		s.publish(events.Event{Type: events.EnvironmentOffline, ResourceType: events.ResourceEnvironment, ResourceID: id, EnvironmentID: id})
	}
	return nil
}

func inputErr(field, format string, args ...any) error {
	return &domain.InputError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// validName checks an environment name: 1-63 characters, no control
// characters, no surrounding space.
func validName(name string) error {
	switch {
	case name == "":
		return errors.New("must not be empty")
	case len(name) > 63 || !utf8.ValidString(name):
		return errors.New("must be at most 63 characters")
	case strings.TrimSpace(name) != name:
		return errors.New("must not start or end with whitespace")
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		return errors.New("must not contain control characters")
	}
	return nil
}

// sanitizeName turns an agent-reported name into a valid environment name
// (or "" when nothing usable is left).
func sanitizeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(name, ""))
	name = strings.TrimSpace(name)
	for len(name) > 63 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = strings.TrimSpace(name[:len(name)-size])
	}
	return name
}
