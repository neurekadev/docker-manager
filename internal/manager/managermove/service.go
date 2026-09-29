// Package managermove moves Docker Manager to a new server
// (docs/internal/architecture/manager-move.md): the apps move first (an
// environment migration), then the manager hands itself over (a live
// handoff between two managers).
//
// Old manager: the owner creates a move with this server's and the new
// server's address; the service creates an enrollment token for the new
// server's agent and renders the new server's compose.yaml and .env (the
// move code and the token, shown once). The new manager starts in waiting
// mode and asks for the handoff every 10 s (authenticated with an HMAC of
// the code, never the code itself); the handoff answers not_ready until
// manager.move (Move everything) migrated the apps and made the move
// ready. Then the old manager drains (read-only while jobs finish), tells
// the agents it can place the new address (manager.redirect), refuses
// agents, copies its state (VACUUM INTO; in the copy the instance's
// generation goes up by one and the move's row becomes arrived) and
// streams the package encrypted under the code. The new manager checks
// the copy, stages it as a restore of kind move and restarts; at start it
// finishes the arrival (sessions, API tokens and agents are kept) and
// confirms the move to the old manager until it answers.
//
// The move code is a secret: it is never logged, audited, returned after
// creation or put in job inputs; the database keeps it only sealed with
// the secret key.
package managermove

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/buildinfo"
	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authsep"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/migrations"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/movelock"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/secrets"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// Timing.
const (
	// CodeLifetime ends a move that was not handed off (a large migration
	// may take hours; the code stays valid until the move is confirmed or
	// cancelled, at most this long).
	CodeLifetime = 7 * 24 * time.Hour
	// EnrollmentLifetime is the new server's enrollment token's lifetime
	// (the longest an enrollment token lives).
	EnrollmentLifetime = 24 * time.Hour
	// HandoffRetryAfter is the Retry-After of a handoff refused while jobs
	// still run or the apps move.
	HandoffRetryAfter = 10 * time.Second
	// CheckInFresh is how recent the waiting manager's last request must
	// be for Move everything.
	CheckInFresh = 2 * time.Minute
	// confirmBackoffMin/Max bound the retries of the confirmation.
	confirmBackoffMin = 5 * time.Second
	confirmBackoffMax = 5 * time.Minute
)

// Data directory entries.
const (
	outgoingDirName = "move-outgoing"
	incomingDirName = "move-incoming"
)

// OwnerGuard enforces owner-only actions with step-up (*auth.Service).
type OwnerGuard interface {
	RequireOwner(ctx context.Context, recent bool) (userID string, err error)
}

// AuditRecorder records non-request events (*audit.Log).
type AuditRecorder interface {
	Record(ctx context.Context, ev domain.AuditEvent) error
}

// Enrollments creates and reads the new server's enrollment and its agent
// (*agents.Service).
type Enrollments interface {
	CreateEnrollment(ctx context.Context, r domain.EnrollmentSpec) (domain.CreatedEnrollment, error)
	GetEnrollment(ctx context.Context, id string) (domain.Enrollment, error)
	RevokeEnrollment(ctx context.Context, id string) (domain.Enrollment, error)
	GetAgent(ctx context.Context, id string) (domain.Agent, error)
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
}

// AgentHub reaches connected agents (*agents.Hub).
type AgentHub interface {
	Online(environmentID string) bool
	EnvironmentServes(environmentID, name string) bool
	RequestEnvironment(ctx context.Context, environmentID, name string, input any, timeout time.Duration) (json.RawMessage, error)
}

// EnvironmentMigrator moves the apps (*migrations.Service).
type EnvironmentMigrator interface {
	StartEnvironment(ctx context.Context, p authz.Principal, source string, r migrations.EnvironmentRequest) (domain.Job, domain.EnvironmentMigration, error)
	GetEnvironmentMigration(ctx context.Context, id string) (domain.EnvironmentMigration, error)
	RetainedSources(ctx context.Context, environmentID string) ([]migrations.RetainedSource, error)
}

// WaitingConfig starts a manager in waiting mode
// (DOCKER_MANAGER_MOVE_FROM, DOCKER_MANAGER_MOVE_CODE on an empty data
// directory).
type WaitingConfig struct {
	// From is the old manager's address (http or https origin).
	From *url.URL
	// Code is the move code (memory only).
	Code string
}

// Options configures the service.
type Options struct {
	DB      *bun.DB
	Keyring *secrets.Keyring
	Clock   clock.Clock
	Logger  *slog.Logger
	// Lock is the shared move lock the service keeps in step with the
	// current move.
	Lock *movelock.Lock
	// Jobs runs manager.move and counts the jobs a handoff waits for.
	Jobs  *jobs.Engine
	Guard OwnerGuard
	Audit AuditRecorder
	// Instance is this manager's instance (ID and generation).
	Instance domain.Instance
	// DataDir holds the outgoing package and the incoming transfer.
	DataDir string
	// PublicURL is DOCKER_MANAGER_PUBLIC_URL (the new server's .env, the
	// lock's address).
	PublicURL *url.URL
	// TrustedProxies is DOCKER_MANAGER_TRUSTED_PROXIES as the new server's
	// .env repeats it ("" for the Quickstart's).
	TrustedProxies string
	Build          buildinfo.Info
	// SchemaMigrations lists the applied migrations (state.json).
	SchemaMigrations func(ctx context.Context) ([]string, error)
	// CheckSchema reports whether this build can run a database copy
	// (store.ErrUnknownMigrations for a newer schema).
	CheckSchema func(ctx context.Context, db bun.IDB) error
	// HTTPClient reaches the old manager (tests); nil builds the default
	// client.
	HTTPClient *http.Client
	// RequestRestart applies a staged copy, or a resumed old manager's new
	// generation (controlled restart).
	RequestRestart func()
	// Old manager: the new server's enrollment, its agents, the apps'
	// migration and the new server's files.
	Enrollments Enrollments
	Hub         AgentHub
	Migrations  EnvironmentMigrator
	Render      func(RenderInput) Files
	// New manager: waiting mode (nil: not waiting), and whether the move
	// variables are set at all (the status page says to remove them after
	// the move).
	Waiting          *WaitingConfig
	MoveVariablesSet bool
	// Bus receives the move's live events (ManagerMoveUpdated,
	// ManagerMoveLockChanged) and is followed for what changes elsewhere
	// (live.go); nil publishes nothing.
	Bus *events.Bus
}

// Service is the move service.
type Service struct {
	opts   Options
	db     *bun.DB
	log    *slog.Logger
	http   *http.Client
	replay *replayCache

	// mu serializes state transitions; buildMu serializes handoffs (a
	// package build runs without mu).
	mu      sync.Mutex
	buildMu sync.Mutex
	wake    chan struct{}

	// colocated are the environments whose agent runs next to this
	// manager (manager.identity answers).
	colocMu   sync.Mutex
	colocated map[string]bool

	// waitMu guards the waiting mode's status.
	waitMu sync.Mutex
	wait   WaitStatus

	// Confirmation retries (new manager).
	confirmMu      sync.Mutex
	nextConfirm    time.Time
	confirmBackoff time.Duration

	// staleAnnounced is the stale check-in last announced (live.go).
	staleMu        sync.Mutex
	staleAnnounced time.Time
}

// New creates the service and registers manager.move with the job engine
// (call before the engine's Recover).
func New(opts Options) (*Service, error) {
	if opts.DB == nil || opts.Keyring == nil {
		return nil, errors.New("managermove: DB and Keyring are required")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Service{opts: opts, db: opts.DB, log: opts.Logger, http: opts.HTTPClient, wake: make(chan struct{}, 1), replay: newReplayCache(),
		colocated: map[string]bool{}, wait: WaitStatus{Phase: WaitNone}}
	if s.http == nil {
		s.http = defaultHTTPClient()
	}
	if opts.Waiting != nil {
		s.wait = WaitStatus{Phase: WaitConnecting}
	}
	if opts.Jobs != nil {
		if err := opts.Jobs.RegisterManagerExecutor(s.moveExecutor()); err != nil {
			return nil, err
		}
		opts.Jobs.OnFinish(moveKind, s.onMoveFinished)
	}
	return s, nil
}

func (s *Service) now() time.Time { return s.opts.Clock.Now().UTC().Truncate(time.Microsecond) }

func (s *Service) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// LevelFor is the lock level of a move state: the manager stays normal
// while the apps move (open, moving, ready), read-only while it drains,
// and refuses agents once its state was copied out.
func LevelFor(state domain.ManagerMoveState) movelock.Level {
	switch state {
	case domain.MoveDraining:
		return movelock.ReadOnly
	case domain.MoveHandedOff, domain.MoveConfirmed:
		return movelock.AgentsRefused
	}
	return movelock.Open
}

// LockLevel reads the lock level of the current move (at start, before
// anything is served: a restarted old manager stays locked). A move whose
// code expired no longer locks.
func LockLevel(ctx context.Context, db bun.IDB, now time.Time) (movelock.Level, error) {
	m, found, err := store.ActiveManagerMove(ctx, db)
	if err != nil || !found {
		return movelock.Open, err
	}
	if expirable(m.State) && !now.Before(m.ExpiresAt) {
		return movelock.Open, nil
	}
	return LevelFor(m.State), nil
}

// expirable: the move ends when its code runs out (it was not handed off).
func expirable(s domain.ManagerMoveState) bool {
	switch s {
	case domain.MoveOpen, domain.MoveMoving, domain.MoveReady, domain.MoveDraining:
		return true
	}
	return false
}

// syncLock sets the lock from the current move (under mu).
func (s *Service) syncLock(ctx context.Context) {
	lvl, err := LockLevel(ctx, s.db, s.now())
	if err != nil {
		s.log.Error("could not read the manager move state; keeping the lock", "error", err)
		return
	}
	s.opts.Lock.Set(lvl)
}

// expireDue ends a move whose code expired (under mu).
func (s *Service) expireDue(ctx context.Context) error {
	m, found, err := store.ActiveManagerMove(ctx, s.db)
	if err != nil || !found || !expirable(m.State) {
		return err
	}
	if s.now().Before(m.ExpiresAt) {
		return nil
	}
	from := m.State
	if err := s.endMove(ctx, &m, domain.MoveExpired); err != nil {
		return err
	}
	s.log.Info("the manager move code expired; the move ended and nothing is locked", "move_id", m.ID, "state", string(from))
	return nil
}

// endMove cancels or expires m (under mu): the state, the sealed code
// (forgotten), a running manager.move (cancelled), an unused enrollment
// token (revoked), the outgoing package (deleted) and the lock (open).
// When agents were already told the new manager's address (redirects,
// or a copy that left with the generation raised by one), the instance's
// generation goes up by two and the manager restarts: those agents accept
// this manager again, and a new manager started from the copy is refused
// by agents that met this one.
func (s *Service) endMove(ctx context.Context, m *domain.ManagerMove, to domain.ManagerMoveState) error {
	from, now := m.State, s.now()
	m.State, m.EndedAt, m.UpdatedAt = to, &now, now
	raise := from == domain.MoveHandedOff || redirected(m.Redirects)
	var gen int64
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := store.UpdateManagerMove(ctx, tx, m, from); err != nil {
			return err
		}
		if err := store.SetManagerMoveSealedCode(ctx, tx, m.ID, ""); err != nil {
			return err
		}
		if raise {
			if _, err := store.BumpInstanceGeneration(ctx, tx); err != nil {
				return err
			}
			var err error
			gen, err = store.BumpInstanceGeneration(ctx, tx)
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.opts.Lock.Set(movelock.Open)
	_ = os.RemoveAll(s.outgoingDir(m.ID))
	if from == domain.MoveMoving && m.MoveJobID != "" && s.opts.Jobs != nil {
		if _, err := s.opts.Jobs.Cancel(ctx, m.MoveJobID); err != nil && !errors.Is(err, domain.ErrJobFinished) {
			s.log.Warn("could not cancel the move's manager.move job", "move_id", m.ID, "job_id", m.MoveJobID, "error", err)
		}
	}
	if m.EnrollmentID != "" && m.TargetEnvironmentID == "" && s.opts.Enrollments != nil {
		if _, err := s.opts.Enrollments.RevokeEnrollment(ctx, m.EnrollmentID); err != nil {
			s.log.Debug("the move's enrollment token was not revoked", "move_id", m.ID, "error", err)
		}
	}
	s.published(*m, from)
	if raise {
		s.log.Warn("agents were told the new manager's address: the instance's generation went up so they accept this manager again; restarting",
			"move_id", m.ID, "generation", gen)
		if s.opts.RequestRestart != nil {
			s.opts.RequestRestart()
		}
	}
	return nil
}

func redirected(rs []domain.ManagerMoveRedirect) bool {
	for _, r := range rs {
		if r.Sent {
			return true
		}
	}
	return false
}

// Expire ends a move whose code expired (the Run loop; tests).
func (s *Service) Expire(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expireDue(ctx)
}

func (s *Service) outgoingDir(moveID string) string {
	return filepath.Join(s.opts.DataDir, outgoingDirName, moveID)
}

// ObserveColocation records whether an environment's agent runs next to
// this manager (its manager.identity answer, after every connect).
func (s *Service) ObserveColocation(environmentID string, colocated bool) {
	s.colocMu.Lock()
	defer s.colocMu.Unlock()
	if colocated {
		s.colocated[environmentID] = true
	} else {
		delete(s.colocated, environmentID)
	}
}

// colocatedEnvironment returns the active environment next to this
// manager (false when none is known: no agent runs there, or it has not
// connected since this manager started).
func (s *Service) colocatedEnvironment(ctx context.Context) (domain.Environment, bool) {
	s.colocMu.Lock()
	ids := make([]string, 0, len(s.colocated))
	for id := range s.colocated {
		ids = append(ids, id)
	}
	s.colocMu.Unlock()
	for _, id := range ids {
		e, err := store.GetEnvironment(ctx, s.db, id)
		if err == nil && e.Status == domain.EnvironmentActive {
			return e, true
		}
	}
	return domain.Environment{}, false
}

// Defaults are the create form's prefill.
type Defaults struct {
	// ThisServerAddress is the co-located environment's service address
	// ("" unknown).
	ThisServerAddress string
	// Source is the environment next to this manager (its apps move).
	Source *SourceStatus
}

// Defaults returns the create form's prefill (owner).
func (s *Service) Defaults(ctx context.Context) (Defaults, error) {
	if _, err := s.requireOwner(ctx, false); err != nil {
		return Defaults{}, err
	}
	e, ok := s.colocatedEnvironment(ctx)
	if !ok {
		return Defaults{}, nil
	}
	src, err := s.sourceStatus(ctx, e.ID)
	if err != nil {
		return Defaults{}, err
	}
	return Defaults{ThisServerAddress: e.ServiceAddress, Source: src}, nil
}

// CreateRequest creates a move.
type CreateRequest struct {
	// ThisServerAddress and NewServerAddress: IP address or host name,
	// optionally with a port (8080 when absent).
	ThisServerAddress string
	NewServerAddress  string
	// NewEnvironmentName names the new server's environment (default: the
	// new server's host).
	NewEnvironmentName string
}

// Created is a new move with the new server's files (shown once).
type Created struct {
	Move  domain.ManagerMove
	Files Files
	// StatusURL is where the new manager shows its progress.
	StatusURL string
	// AgentEnrolled (new setup files): the new server's agent already
	// enrolled; the .env has no enrollment token and the agent keeps its
	// credential.
	AgentEnrolled bool
}

// CreateMove starts a move (owner, recent step-up): it creates the move
// (and its code, sealed), an enrollment token for the new server's agent
// and the new server's files. Another open or in-progress move answers
// domain.ErrManagerMoveExists.
func (s *Service) CreateMove(ctx context.Context, req CreateRequest) (Created, error) {
	uid, err := s.requireOwner(ctx, true)
	if err != nil {
		return Created{}, err
	}
	if s.opts.Enrollments == nil || s.opts.Render == nil {
		return Created{}, errors.New("managermove: moving needs the agents service")
	}
	thisAddr, err := NormalizeServerAddress("thisServerAddress", req.ThisServerAddress)
	if err != nil {
		return Created{}, err
	}
	newAddr, err := NormalizeServerAddress("newServerAddress", req.NewServerAddress)
	if err != nil {
		return Created{}, err
	}
	if thisAddr == newAddr {
		return Created{}, &domain.FieldError{Field: "newServerAddress", Message: "the new server must be another server than this one"}
	}
	name := strings.TrimSpace(req.NewEnvironmentName)
	if name == "" {
		name = hostOf(newAddr)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.expireDue(ctx); err != nil {
		return Created{}, err
	}
	if _, found, err := store.ActiveManagerMove(ctx, s.db); err != nil {
		return Created{}, err
	} else if found {
		return Created{}, domain.ErrManagerMoveExists
	}
	now := s.now()
	m := domain.ManagerMove{ID: ids.New(), State: domain.MoveOpen, CreatedBy: uid, CreatedAt: now, ExpiresAt: now.Add(CodeLifetime),
		ThisServerAddress: thisAddr, NewServerAddress: newAddr, UpdatedAt: now}
	if e, ok := s.colocatedEnvironment(ctx); ok {
		m.SourceEnvironmentID = e.ID
	}
	minted, err := authsep.MintMoveCode(m.ID)
	if err != nil {
		return Created{}, err
	}
	sealed, err := s.opts.Keyring.Seal([]byte(minted.Token), SealContext(m.ID))
	if err != nil {
		return Created{}, err
	}
	en, err := s.opts.Enrollments.CreateEnrollment(ctx, domain.EnrollmentSpec{Intent: domain.IntentNew, EnvironmentName: name,
		TTL: EnrollmentLifetime, CreatedBy: uid})
	if err != nil {
		var fe *domain.FieldError
		if errors.As(err, &fe) && fe.Field == "environmentName" {
			return Created{}, &domain.FieldError{Field: "newEnvironmentName", Message: fe.Message}
		}
		return Created{}, err
	}
	m.EnrollmentID = en.Enrollment.ID
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, found, err := store.ActiveManagerMove(ctx, tx); err != nil {
			return err
		} else if found {
			return domain.ErrManagerMoveExists
		}
		return store.InsertManagerMove(ctx, tx, &m, sealed)
	})
	if err != nil {
		_, _ = s.opts.Enrollments.RevokeEnrollment(ctx, en.Enrollment.ID)
		return Created{}, err
	}
	public := ""
	if s.opts.PublicURL != nil {
		public = strings.TrimSuffix(s.opts.PublicURL.String(), "/")
	}
	files := s.opts.Render(RenderInput{PublicURL: public, OldManagerURL: serverURL(thisAddr), MoveCode: minted.Token,
		EnrollmentToken: en.Token, EnvironmentName: name})
	audit.AddTarget(ctx, domain.AuditTarget{Type: auditTargetType, ID: m.ID})
	audit.SetDetail(ctx, "expiresAt", m.ExpiresAt)
	audit.SetDetail(ctx, "thisServerAddress", thisAddr)
	audit.SetDetail(ctx, "newServerAddress", newAddr)
	audit.SetDetail(ctx, "enrollmentId", en.Enrollment.ID)
	audit.SetDetail(ctx, "sourceEnvironmentId", m.SourceEnvironmentID)
	s.log.Info("a move of this manager to a new server was created", "move_id", m.ID, "new_server", newAddr)
	s.publish(m.ID)
	s.notify()
	return Created{Move: m, Files: files, StatusURL: serverURL(newAddr)}, nil
}

// auditTargetType names moves in audit targets.
const auditTargetType = "manager_move"

func (s *Service) requireOwner(ctx context.Context, recent bool) (string, error) {
	if s.opts.Guard == nil {
		return "", domain.ErrForbidden
	}
	return s.opts.Guard.RequireOwner(ctx, recent)
}

// NewServerStatus is where the new server stands (old manager).
type NewServerStatus struct {
	// EnvironmentID and EnvironmentName: the new server's environment
	// ("" until its agent enrolled).
	EnvironmentID   string
	EnvironmentName string
	// EnrollmentState is the token's state (pending, used, expired,
	// revoked).
	EnrollmentState string
	// Online: its agent is connected.
	Online bool
	// ManagerCheckedIn: the waiting manager asked for the handoff within
	// CheckInFresh; CheckedInAt is its last request, ManagerAddress its
	// client IP.
	ManagerCheckedIn bool
	CheckedInAt      *time.Time
	ManagerAddress   string
}

// SourceStatus is the environment next to the old manager (its apps move).
type SourceStatus struct {
	EnvironmentID string
	Name          string
	Online        bool
	StackCount    int
}

// Progress is the latest manager.move and its environment migration.
type Progress struct {
	JobID      string
	JobState   domain.JobState
	ErrorClass string
	// Recovery is the failure's guidance (never secrets).
	Recovery     string
	MigrationID  string
	StacksMoved  int
	StacksTotal  int
	CurrentStack string
}

// View is the current move with what the owner needs next.
type View struct {
	Move domain.ManagerMove
	// JobsRunning counts the jobs a draining move waits for.
	JobsRunning int
	// Old manager (not arrived): the new server, the apps' environment and
	// the apps' progress.
	NewServer *NewServerStatus
	Source    *SourceStatus
	Progress  *Progress
	// Complete is the arrived move's "Move complete" data (new manager).
	Complete *Complete
}

// Current returns the open or in-progress move, else the move this
// manager arrived by (with the "Move complete" data);
// domain.ErrManagerMoveNotFound when there is neither.
func (s *Service) Current(ctx context.Context) (View, error) {
	s.mu.Lock()
	err := s.expireDue(ctx)
	s.mu.Unlock()
	if err != nil {
		return View{}, err
	}
	m, found, err := store.ActiveManagerMove(ctx, s.db)
	if err != nil {
		return View{}, err
	}
	if found {
		return s.viewOf(ctx, m)
	}
	a, found, err := store.LatestArrivedManagerMove(ctx, s.db)
	if err != nil {
		return View{}, err
	}
	if !found {
		return View{}, domain.ErrManagerMoveNotFound
	}
	c, err := s.complete(ctx, a)
	if err != nil {
		return View{}, err
	}
	return View{Move: a, Complete: &c}, nil
}

// viewOf builds the old manager's view of an active move.
func (s *Service) viewOf(ctx context.Context, m domain.ManagerMove) (View, error) {
	m = s.resolveTarget(ctx, m)
	v := View{Move: m, NewServer: s.newServerStatus(ctx, m)}
	var err error
	if m.State == domain.MoveDraining {
		if v.JobsRunning, err = s.activeJobs(ctx); err != nil {
			return View{}, err
		}
	}
	source := m.SourceEnvironmentID
	if source == "" && (m.State == domain.MoveOpen || m.State == domain.MoveReady) {
		if e, ok := s.colocatedEnvironment(ctx); ok {
			source = e.ID
		}
	}
	if source != "" {
		if v.Source, err = s.sourceStatus(ctx, source); err != nil {
			return View{}, err
		}
	}
	v.Progress = s.progress(ctx, m)
	return v, nil
}

// resolveTarget records the new server's environment once its agent
// enrolled with the move's token.
func (s *Service) resolveTarget(ctx context.Context, m domain.ManagerMove) domain.ManagerMove {
	if m.TargetEnvironmentID != "" || m.EnrollmentID == "" || s.opts.Enrollments == nil {
		return m
	}
	en, err := s.opts.Enrollments.GetEnrollment(ctx, m.EnrollmentID)
	if err != nil || en.AgentID == "" {
		return m
	}
	a, err := s.opts.Enrollments.GetAgent(ctx, en.AgentID)
	if err != nil || a.EnvironmentID == "" {
		return m
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := store.GetManagerMove(ctx, s.db, m.ID)
	if err != nil || cur.TargetEnvironmentID != "" {
		return m
	}
	cur.TargetEnvironmentID, cur.UpdatedAt = a.EnvironmentID, s.now()
	if err := store.UpdateManagerMove(ctx, s.db, &cur, cur.State); err != nil {
		return m
	}
	s.publish(cur.ID)
	return cur
}

func (s *Service) newServerStatus(ctx context.Context, m domain.ManagerMove) *NewServerStatus {
	st := &NewServerStatus{EnvironmentID: m.TargetEnvironmentID, CheckedInAt: m.CheckedInAt, ManagerAddress: m.HandoffAddress}
	st.ManagerCheckedIn = s.checkInFresh(m.CheckedInAt)
	if s.opts.Enrollments != nil && m.EnrollmentID != "" {
		if en, err := s.opts.Enrollments.GetEnrollment(ctx, m.EnrollmentID); err == nil {
			st.EnrollmentState = string(en.State(s.opts.Clock.Now()))
			st.EnvironmentName = en.EnvironmentName
		}
	}
	if m.TargetEnvironmentID != "" {
		if e, err := store.GetEnvironment(ctx, s.db, m.TargetEnvironmentID); err == nil {
			st.EnvironmentName = e.Name
		}
		st.Online = s.online(m.TargetEnvironmentID)
	}
	return st
}

func (s *Service) online(environmentID string) bool {
	return s.opts.Hub != nil && s.opts.Hub.Online(environmentID)
}

func (s *Service) sourceStatus(ctx context.Context, environmentID string) (*SourceStatus, error) {
	e, err := store.GetEnvironment(ctx, s.db, environmentID)
	if errors.Is(err, domain.ErrEnvironmentNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	stacks, err := store.ListStacks(ctx, s.db, domain.StackFilter{EnvironmentID: e.ID})
	if err != nil {
		return nil, err
	}
	return &SourceStatus{EnvironmentID: e.ID, Name: e.Name, Online: s.online(e.ID), StackCount: len(stacks)}, nil
}

// progress reads the latest manager.move job and its environment
// migration (nil before the first Move everything).
func (s *Service) progress(ctx context.Context, m domain.ManagerMove) *Progress {
	if m.MoveJobID == "" && m.MigrationID == "" {
		return nil
	}
	p := &Progress{JobID: m.MoveJobID, MigrationID: m.MigrationID}
	if m.MoveJobID != "" && s.opts.Jobs != nil {
		if j, err := s.opts.Jobs.Get(ctx, m.MoveJobID); err == nil {
			p.JobState, p.ErrorClass, p.Recovery = j.State, j.ErrorClass, j.Recovery
		}
	}
	p.StacksMoved, p.StacksTotal, p.CurrentStack = s.migrationProgress(ctx, m.MigrationID)
	return p
}

// migrationProgress counts the moved stacks of an environment migration
// and names the one moving now.
func (s *Service) migrationProgress(ctx context.Context, id string) (moved, total int, current string) {
	if id == "" || s.opts.Migrations == nil {
		return 0, 0, ""
	}
	rec, err := s.opts.Migrations.GetEnvironmentMigration(ctx, id)
	if err != nil {
		return 0, 0, ""
	}
	for _, st := range rec.Stacks {
		switch st.State {
		case domain.EnvironmentStackMoved:
			moved++
		case domain.EnvironmentStackMoving:
			current = st.Name
		}
	}
	return moved, len(rec.Stacks), current
}

// Lock states as every signed-in user sees them (LockStatus).
const (
	// LockNone: nothing is locked (no move, or a move not handed over yet).
	LockNone = "none"
	// LockMoving: the manager is read-only while it hands over (draining,
	// handed_off).
	LockMoving = "moving"
	// LockMoved: the new manager confirmed; this one stays locked for good.
	LockMoved = "moved"
)

// LockStatus is the move lock of this manager in words: no secrets, no
// move ID.
type LockStatus struct {
	State string
	// Address leads to the new manager once it is pointed there (the
	// public URL both managers share); "" while nothing is locked.
	Address string
}

// LockStatus reads the move lock from the current move (read-only: a
// move whose code expired no longer locks).
func (s *Service) LockStatus(ctx context.Context) (LockStatus, error) {
	m, found, err := store.ActiveManagerMove(ctx, s.db)
	if err != nil || !found {
		return LockStatus{State: LockNone}, err
	}
	if expirable(m.State) && !s.opts.Clock.Now().Before(m.ExpiresAt) {
		return LockStatus{State: LockNone}, nil
	}
	st := LockStatus{}
	switch m.State {
	case domain.MoveDraining, domain.MoveHandedOff:
		st.State = LockMoving
	case domain.MoveConfirmed:
		st.State = LockMoved
	default:
		return LockStatus{State: LockNone}, nil
	}
	if s.opts.PublicURL != nil {
		st.Address = strings.TrimSuffix(s.opts.PublicURL.String(), "/")
	}
	return st, nil
}

// activeJobs counts the jobs a handoff waits for: dispatched, running or
// cancelling (queued jobs are not dispatched while the manager moves; they
// travel in the copy).
func (s *Service) activeJobs(ctx context.Context) (int, error) {
	if s.opts.Jobs == nil {
		return 0, nil
	}
	n, err := s.opts.Jobs.Count(ctx, domain.JobFilter{States: []domain.JobState{domain.JobDispatched, domain.JobRunning, domain.JobCancelling}})
	return int(n), err
}

// CancelRequest cancels the current move.
type CancelRequest struct {
	// ResumeHere and InstanceName (the instance's name, typed) are
	// required once the state was handed off: the owner states the new
	// manager never started with the copy.
	ResumeHere   bool
	InstanceName string
}

// Cancel errors (validation).
var (
	ErrResumeRequired       = errors.New("the state was already handed to a new manager: resume here only if it never started with the copy")
	ErrInstanceNameMismatch = errors.New("the typed name is not this Docker Manager's name")
)

// Cancel ends the current move (owner, recent step-up): until the handoff
// at any time (a running manager.move is cancelled; stacks it moved stay
// on the new server), a handed-off move only with ResumeHere and the
// typed instance name, a confirmed move never (domain.ErrManagerMoveState).
// Resuming after agents heard the new address raises the generation and
// restarts this manager (endMove).
func (s *Service) Cancel(ctx context.Context, req CancelRequest) (domain.ManagerMove, error) {
	if _, err := s.requireOwner(ctx, true); err != nil {
		return domain.ManagerMove{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.expireDue(ctx); err != nil {
		return domain.ManagerMove{}, err
	}
	m, found, err := store.ActiveManagerMove(ctx, s.db)
	if err != nil {
		return domain.ManagerMove{}, err
	}
	if !found {
		return domain.ManagerMove{}, domain.ErrManagerMoveNotFound
	}
	switch m.State {
	case domain.MoveOpen, domain.MoveMoving, domain.MoveReady, domain.MoveDraining:
	case domain.MoveHandedOff:
		if !req.ResumeHere {
			return domain.ManagerMove{}, ErrResumeRequired
		}
		set, err := store.GetInstanceSettings(ctx, s.db)
		if err != nil {
			return domain.ManagerMove{}, err
		}
		if strings.TrimSpace(req.InstanceName) != set.Name {
			return domain.ManagerMove{}, ErrInstanceNameMismatch
		}
	default:
		return domain.ManagerMove{}, domain.ErrManagerMoveState
	}
	from := m.State
	if err := s.endMove(ctx, &m, domain.MoveCancelled); err != nil {
		return domain.ManagerMove{}, err
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: auditTargetType, ID: m.ID})
	audit.SetDetail(ctx, "fromState", string(from))
	audit.SetDetail(ctx, "resumeHere", req.ResumeHere)
	if from == domain.MoveHandedOff {
		s.log.Warn("the owner resumed this manager after its state was handed off", "move_id", m.ID)
	}
	s.notify()
	return m, nil
}

// Run expires moves when their code runs out, confirms an arrived move to
// the old manager until it answers and, in waiting mode, asks the old
// manager for the handoff; it returns when ctx ends.
func (s *Service) Run(ctx context.Context) {
	var waiting sync.WaitGroup
	if s.opts.Waiting != nil {
		waiting.Go(func() { s.runWaiting(ctx) })
	}
	waiting.Go(func() { s.followBus(ctx) })
	defer waiting.Wait()
	for {
		if err := s.Expire(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("could not expire the manager move", "error", err)
		}
		s.confirmDue(ctx)
		s.announceStaleCheckIn(ctx)
		wait := s.nextWake(ctx)
		t := s.opts.Clock.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		case <-s.wake:
			t.Stop()
		}
	}
}

// idleWake bounds the Run loop's sleep.
const idleWake = time.Hour

// nextWake is the time until the next expiry, confirmation attempt or
// check-in that stops counting.
func (s *Service) nextWake(ctx context.Context) time.Duration {
	now := s.opts.Clock.Now()
	wait := idleWake
	if m, found, err := store.ActiveManagerMove(ctx, s.db); err == nil && found && expirable(m.State) {
		wait = min(wait, m.ExpiresAt.Sub(now))
		if at := checkInStaleAt(m); at.After(now) {
			wait = min(wait, at.Sub(now)+time.Millisecond)
		}
	}
	s.confirmMu.Lock()
	next := s.nextConfirm
	s.confirmMu.Unlock()
	if !next.IsZero() {
		wait = min(wait, next.Sub(now))
	}
	return max(wait, time.Millisecond)
}

// SealContext binds the sealed move code to its move.
func SealContext(moveID string) string { return "manager_moves/" + moveID + "/code" }
