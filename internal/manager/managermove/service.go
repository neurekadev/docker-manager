// Package managermove moves Docker Manager to a new server
// (docs/internal/architecture/manager-move.md): a live handoff between
// two managers.
//
// On the old manager the owner creates a move (a one-hour move code,
// shown once); the new manager, started empty, calls the handoff with the
// code: the old manager locks itself (read-only while jobs drain, then
// agents refused), copies its state (VACUUM INTO; in the copy the
// instance's generation goes up by one and the move's row becomes
// arrived), seals its secret key under the code and streams the package.
// The new manager (manager.receive) checks the copy, stages it as a
// restore of kind move and restarts; at start it finishes the arrival
// (sessions, API tokens and agents are kept) and confirms the move to the
// old manager until it answers.
//
// The move code is a secret: it is never logged, audited, returned after
// creation, stored (only its verifier; the new manager keeps it sealed
// until the confirmation) or put in job inputs.
package managermove

import (
	"context"
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
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/movelock"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/requestinfo"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/secrets"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// Timing.
const (
	// CodeLifetime ends an open or draining move.
	CodeLifetime = time.Hour
	// HandoffRetryAfter is the Retry-After of a handoff refused while jobs
	// still run.
	HandoffRetryAfter = 10 * time.Second
	// ReceiveWait bounds how long manager.receive waits for the old
	// manager's jobs.
	ReceiveWait = 30 * time.Minute
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

// Options configures the service.
type Options struct {
	DB      *bun.DB
	Keyring *secrets.Keyring
	Clock   clock.Clock
	Logger  *slog.Logger
	// Lock is the shared move lock the service keeps in step with the
	// current move.
	Lock *movelock.Lock
	// Jobs runs manager.receive and counts the jobs a handoff waits for.
	Jobs  *jobs.Engine
	Guard OwnerGuard
	Audit AuditRecorder
	// Instance is this manager's instance (ID and generation).
	Instance domain.Instance
	// DataDir holds the outgoing package and the incoming transfer.
	DataDir string
	// PublicURL and LocalDevelopment decide the secure origin of the
	// handoff and confirmation routes, and the checklist's address check.
	PublicURL        *url.URL
	LocalDevelopment bool
	Build            buildinfo.Info
	// Migrations lists the applied migrations (state.json).
	Migrations func(ctx context.Context) ([]string, error)
	// CheckSchema reports whether this build can run a database copy
	// (store.ErrUnknownMigrations for a newer schema).
	CheckSchema func(ctx context.Context, db bun.IDB) error
	// HTTPClient reaches the old manager (tests trust a fake's
	// certificate); nil builds the default client (TLS verified).
	HTTPClient *http.Client
	// RequestRestart applies a staged copy (controlled restart).
	RequestRestart func()
}

// Service is the move service.
type Service struct {
	opts Options
	db   *bun.DB
	log  *slog.Logger
	http *http.Client

	// mu serializes state transitions; buildMu serializes handoff package
	// builds (a build runs without mu).
	mu      sync.Mutex
	buildMu sync.Mutex
	wake    chan struct{}

	// Receive jobs: the move code (memory only) and the live progress.
	recvMu   sync.Mutex
	codes    map[string]string
	progress map[string]*receiveProgress

	// Confirmation retries (new manager).
	confirmMu      sync.Mutex
	nextConfirm    time.Time
	confirmBackoff time.Duration
}

// New creates the service and registers manager.receive with the job
// engine (call before the engine's Recover).
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
	s := &Service{opts: opts, db: opts.DB, log: opts.Logger, http: opts.HTTPClient, wake: make(chan struct{}, 1),
		codes: map[string]string{}, progress: map[string]*receiveProgress{}}
	if s.http == nil {
		s.http = defaultHTTPClient()
	}
	if opts.Jobs != nil {
		if err := opts.Jobs.RegisterManagerExecutor(s.receiveExecutor()); err != nil {
			return nil, err
		}
		opts.Jobs.OnFinish(receiveKind, s.onReceiveFinished)
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

// LevelFor is the lock level of a move state.
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

func expirable(s domain.ManagerMoveState) bool {
	return s == domain.MoveOpen || s == domain.MoveDraining
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

// expireDue ends an open or draining move whose code expired (under mu).
func (s *Service) expireDue(ctx context.Context) error {
	m, found, err := store.ActiveManagerMove(ctx, s.db)
	if err != nil || !found || !expirable(m.State) {
		return err
	}
	now := s.now()
	if now.Before(m.ExpiresAt) {
		return nil
	}
	from := m.State
	m.State, m.EndedAt, m.UpdatedAt = domain.MoveExpired, &now, now
	if err := store.UpdateManagerMove(ctx, s.db, &m, from); err != nil {
		return err
	}
	s.opts.Lock.Set(movelock.Open)
	_ = os.RemoveAll(s.outgoingDir(m.ID))
	s.log.Info("the manager move code expired; the move ended and nothing is locked", "move_id", m.ID, "state", string(from))
	return nil
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

// CreateMove starts a move (owner, recent step-up): the move code is
// returned once. Another open or in-progress move answers
// domain.ErrManagerMoveExists.
func (s *Service) CreateMove(ctx context.Context) (domain.ManagerMove, string, error) {
	uid, err := s.requireOwner(ctx)
	if err != nil {
		return domain.ManagerMove{}, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.expireDue(ctx); err != nil {
		return domain.ManagerMove{}, "", err
	}
	now := s.now()
	m := domain.ManagerMove{ID: ids.New(), State: domain.MoveOpen, CreatedBy: uid, CreatedAt: now, ExpiresAt: now.Add(CodeLifetime), UpdatedAt: now}
	minted, err := authsep.MintMoveCode(m.ID)
	if err != nil {
		return domain.ManagerMove{}, "", err
	}
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, found, err := store.ActiveManagerMove(ctx, tx); err != nil {
			return err
		} else if found {
			return domain.ErrManagerMoveExists
		}
		return store.InsertManagerMove(ctx, tx, &m, minted.Verifier)
	})
	if err != nil {
		return domain.ManagerMove{}, "", err
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: auditTargetType, ID: m.ID})
	audit.SetDetail(ctx, "expiresAt", m.ExpiresAt)
	s.notify()
	return m, minted.Token, nil
}

// auditTargetType names moves in audit targets.
const auditTargetType = "manager_move"

func (s *Service) requireOwner(ctx context.Context) (string, error) {
	if s.opts.Guard == nil {
		return "", domain.ErrForbidden
	}
	return s.opts.Guard.RequireOwner(ctx, true)
}

// View is the current move with what the owner needs next.
type View struct {
	Move domain.ManagerMove
	// JobsRunning counts the jobs a draining move waits for.
	JobsRunning int
	// Checklist is the finish checklist of an arrived move.
	Checklist *Checklist
}

// Current returns the open or in-progress move, else the move this
// manager arrived by (with the finish checklist);
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
		v := View{Move: m}
		if m.State == domain.MoveDraining {
			if v.JobsRunning, err = s.activeJobs(ctx); err != nil {
				return View{}, err
			}
		}
		return v, nil
	}
	a, found, err := store.LatestArrivedManagerMove(ctx, s.db)
	if err != nil {
		return View{}, err
	}
	if !found {
		return View{}, domain.ErrManagerMoveNotFound
	}
	c, err := s.checklist(ctx, a)
	if err != nil {
		return View{}, err
	}
	return View{Move: a, Checklist: &c}, nil
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

// Cancel ends the current move (owner, recent step-up): open and draining
// moves at any time, a handed-off move only with ResumeHere and the typed
// instance name, a confirmed move never (domain.ErrManagerMoveState).
func (s *Service) Cancel(ctx context.Context, req CancelRequest) (domain.ManagerMove, error) {
	if _, err := s.requireOwner(ctx); err != nil {
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
	case domain.MoveOpen, domain.MoveDraining:
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
	from, now := m.State, s.now()
	m.State, m.EndedAt, m.UpdatedAt = domain.MoveCancelled, &now, now
	if err := store.UpdateManagerMove(ctx, s.db, &m, from); err != nil {
		return domain.ManagerMove{}, err
	}
	s.opts.Lock.Set(movelock.Open)
	_ = os.RemoveAll(s.outgoingDir(m.ID))
	audit.AddTarget(ctx, domain.AuditTarget{Type: auditTargetType, ID: m.ID})
	audit.SetDetail(ctx, "fromState", string(from))
	audit.SetDetail(ctx, "resumeHere", req.ResumeHere)
	if from == domain.MoveHandedOff {
		s.log.Warn("the owner resumed this manager after its state was handed off; agents that met the new manager refuse this one",
			"move_id", m.ID)
	}
	s.notify()
	return m, nil
}

// checkOrigin refuses handoff and confirmation requests that did not
// arrive over a secure origin (like first-run setup).
func (s *Service) checkOrigin(ctx context.Context) error {
	info, _ := requestinfo.From(ctx)
	if err := requestinfo.CheckSecureOrigin(s.opts.PublicURL, s.opts.LocalDevelopment, info); err != nil {
		var ie *requestinfo.InsecureOriginError
		if errors.As(err, &ie) {
			return &domain.InsecureOriginError{Reason: ie.Reason, Explanation: ie.Explanation}
		}
		return &domain.InsecureOriginError{Reason: "unknown", Explanation: err.Error()}
	}
	return nil
}

// verifyCode resolves a move code to its move; unknown IDs and wrong
// secrets are indistinguishable (domain.ErrMoveCodeInvalid).
func (s *Service) verifyCode(ctx context.Context, code string) (string, error) {
	id, secret, ok := authsep.ParseMoveCode(code)
	if !ok {
		return "", domain.ErrMoveCodeInvalid
	}
	verifier, err := store.ManagerMoveVerifier(ctx, s.db, id)
	if errors.Is(err, domain.ErrManagerMoveNotFound) {
		return "", domain.ErrMoveCodeInvalid
	}
	if err != nil {
		return "", err
	}
	if !authsep.VerifierMatches(verifier, secret) {
		return "", domain.ErrMoveCodeInvalid
	}
	return id, nil
}

// Run expires moves when their code runs out and confirms an arrived move
// to the old manager until it answers; it returns when ctx ends.
func (s *Service) Run(ctx context.Context) {
	for {
		if err := s.Expire(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("could not expire the manager move", "error", err)
		}
		s.confirmDue(ctx)
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

// nextWake is the time until the next expiry or confirmation attempt.
func (s *Service) nextWake(ctx context.Context) time.Duration {
	now := s.opts.Clock.Now()
	wait := idleWake
	if m, found, err := store.ActiveManagerMove(ctx, s.db); err == nil && found && expirable(m.State) {
		wait = min(wait, m.ExpiresAt.Sub(now))
	}
	s.confirmMu.Lock()
	next := s.nextConfirm
	s.confirmMu.Unlock()
	if !next.IsZero() {
		wait = min(wait, next.Sub(now))
	}
	return max(wait, time.Millisecond)
}
