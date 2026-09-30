package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/managermove"
)

// Moving the manager to a new server (docs/internal/architecture/manager-move.md).
// Old manager: the owner creates the move (the new server's files), runs
// Move everything (manager.move), reads and cancels the move; the waiting
// new manager calls the handoff and the confirmation, signed with the move
// code (Authorization: DMM ...). New manager: GET /move/status is the
// waiting mode's public status; the owner reads the arrived move ("Move
// complete").

// Move error codes.
const (
	CodeManagerMoved                = "manager_moved"
	CodeJobsRunning                 = "jobs_running"
	CodeManagerMoveExists           = "manager_move_exists"
	CodeManagerMoveState            = "manager_move_state"
	CodeMoveCodeInvalid             = "move_code_invalid"
	CodeMoveClockSkew               = "move_clock_skew"
	CodeManagerMoveNotReady         = "manager_move_not_ready"
	CodeManagerMoveNewServerMissing = "manager_move_new_server_missing"
	CodeManagerMoveWaiting          = "manager_move_waiting"
)

const managerMovedMessage = "Docker Manager is moving (or moved) to a new server: this manager is read-only. Sign in on the new server's manager."

// capManagerMove is the owner-only capability of moves.
const capManagerMove = "manager.move"

// ManagerMoveService is the move service (internal/manager/managermove).
type ManagerMoveService interface {
	Defaults(ctx context.Context) (managermove.Defaults, error)
	CreateMove(ctx context.Context, req managermove.CreateRequest) (managermove.Created, error)
	NewSetupFiles(ctx context.Context) (managermove.Created, error)
	Current(ctx context.Context) (managermove.View, error)
	StartRun(ctx context.Context) (domain.Job, error)
	Cancel(ctx context.Context, req managermove.CancelRequest) (domain.ManagerMove, error)
	CheckIn(ctx context.Context, a managermove.MoveAuth) (managermove.CheckIn, error)
	Handoff(ctx context.Context, a managermove.MoveAuth) (*managermove.Package, error)
	Confirm(ctx context.Context, a managermove.MoveAuth) (domain.ManagerMove, error)
	AcknowledgeConfirmation(ctx context.Context) (managermove.View, error)
	LockStatus(ctx context.Context) (managermove.LockStatus, error)
	WaitStatus(ctx context.Context) (managermove.WaitStatus, error)
}

// MoveLock is the manager-move lock as the API sees it.
type MoveLock interface {
	ReadOnly() bool
	Waiting() bool
}

// allowedWhileMoved are the non-GET operations a moving (or moved)
// manager still serves: sign-in, sign-out and the move routes. Every
// other non-GET /api/v1 operation answers 409 manager_moved.
var allowedWhileMoved = map[string]bool{
	"create-auth-session":                        true,
	"delete-auth-session":                        true,
	"create-auth-step-up":                        true,
	"create-passkey-authentication-options":      true,
	"create-passkey-authentication-verification": true,
	"create-recovery-code-redemption":            true,
	"create-manager-move":                        true,
	"create-manager-move-cancellation":           true,
	"create-manager-move-handoff":                true,
	"create-manager-move-confirmation":           true,
}

// allowedWhileWaiting are the operations a manager in waiting mode serves
// (the move status and health); every other operation answers 503
// manager_move_waiting.
var allowedWhileWaiting = map[string]bool{
	"get-health":       true,
	"get-health-ready": true,
	"get-capabilities": true,
	"get-move-status":  true,
}

// AllowedWhileMoved reports whether operationID is served while the
// manager is read-only because it moves (tests, documentation).
func AllowedWhileMoved(operationID string) bool { return allowedWhileMoved[operationID] }

// AllowedWhileWaiting reports whether operationID is served while the
// manager waits for a move's handoff (tests, documentation).
func AllowedWhileWaiting(operationID string) bool { return allowedWhileWaiting[operationID] }

// moveGuard refuses the operation while the manager moves to a new server.
func moveGuard(ctx huma.Context, next func(huma.Context)) {
	if deps, _ := depsFrom(ctx.Context()); deps.MoveLock != nil && deps.MoveLock.ReadOnly() {
		if deps.MoveLock.Waiting() {
			writeHumaError(ctx, waitingError())
			return
		}
		audit.SetErrorClass(ctx.Context(), CodeManagerMoved)
		writeHumaError(ctx, Conflict(CodeManagerMoved, managerMovedMessage))
		return
	}
	next(ctx)
}

// waitingGuard closes every operation but the move status and health
// while the manager waits for a move's handoff.
func waitingGuard(ctx huma.Context, next func(huma.Context)) {
	if deps, _ := depsFrom(ctx.Context()); deps.MoveLock != nil && deps.MoveLock.Waiting() {
		writeHumaError(ctx, waitingError())
		return
	}
	next(ctx)
}

func waitingError() *Error {
	return Unavailable(CodeManagerMoveWaiting, "this Docker Manager waits for the move from the old server; follow it on GET /api/v1/move/status").
		WithHeader("Retry-After", "10")
}

// ManagerMove is a move of the manager to a new server. It never carries
// the move code.
type ManagerMove struct {
	ID                string     `json:"id" example:"0190a6e0-0000-7000-8000-000000000035"`
	State             string     `json:"state" enum:"open,moving,ready,draining,handed_off,confirmed,cancelled,expired,arrived" example:"open" doc:"Old manager: open (waiting for the new server, then for Move everything), moving (manager.move moves the apps), ready (the apps moved; the new manager gets the handoff when it asks next), draining (read-only, jobs finish), handed_off (the state was copied; agents refused), confirmed (the new manager runs the instance), cancelled, expired. New manager: arrived (this manager runs the moved instance)."`
	CreatedAt         time.Time  `json:"createdAt"`
	ExpiresAt         time.Time  `json:"expiresAt" doc:"A move that was not handed off ends at this time (seven days after it was created)."`
	ThisServerAddress string     `json:"thisServerAddress,omitempty" example:"192.168.1.10:8080" doc:"Old manager: this server's address (host:port) as the new server reaches it."`
	NewServerAddress  string     `json:"newServerAddress,omitempty" example:"192.168.1.20:8080" doc:"Old manager: the new server's address (host:port)."`
	StatusURL         string     `json:"statusUrl,omitempty" example:"http://192.168.1.20:8080" doc:"Old manager: where the new manager shows the move's progress."`
	ReadyAt           *time.Time `json:"readyAt,omitempty"`
	DrainingAt        *time.Time `json:"drainingAt,omitempty"`
	HandedOffAt       *time.Time `json:"handedOffAt,omitempty"`
	ConfirmedAt       *time.Time `json:"confirmedAt,omitempty" doc:"Old manager: when the new manager confirmed. Arrived: when the old manager accepted the confirmation."`
	EndedAt           *time.Time `json:"endedAt,omitempty" doc:"When the move was cancelled or expired."`
	// HandoffAddress is the waiting manager's address as its requests show.
	HandoffAddress    string                  `json:"handoffAddress,omitempty" example:"192.168.1.20" doc:"Client IP of the waiting manager's requests."`
	JobsRunning       int                     `json:"jobsRunning" example:"0" doc:"Draining: the jobs the handoff waits for."`
	NewServer         *ManagerMoveNewServer   `json:"newServer,omitempty" doc:"Old manager: where the new server stands."`
	SourceEnvironment *ManagerMoveEnvironment `json:"sourceEnvironment,omitempty" doc:"Old manager: the environment next to this manager, whose apps move. Absent when none is known (only Docker Manager moves)."`
	Progress          *ManagerMoveProgress    `json:"progress,omitempty" doc:"Old manager: the latest Move everything (manager.move) and its environment migration."`
	Redirects         []ManagerMoveRedirect   `json:"redirects" doc:"The agents told the new manager's address just before the handoff (empty before). On an arrived move with connected and needsFix."`
	// Arrived move (new manager).
	SourceURL             string                     `json:"sourceUrl,omitempty" example:"http://192.168.1.10:8080" doc:"Arrived: the old manager's address."`
	ArrivedAt             *time.Time                 `json:"arrivedAt,omitempty"`
	OldManagerConfirmed   bool                       `json:"oldManagerConfirmed" doc:"Arrived: the old manager accepted the confirmation (it stays locked for good)."`
	ConfirmAttempts       int                        `json:"confirmAttempts" doc:"Arrived: confirmation attempts so far (retried in the background until the old manager answers)."`
	ConfirmError          string                     `json:"confirmError,omitempty" example:"unreachable" doc:"Arrived: the last failed confirmation: unreachable, clock_skew, http_<status> (retried), code_invalid or state_refused (the old manager refused: make sure it no longer runs the instance)."`
	LastConfirmAt         *time.Time                 `json:"lastConfirmAt,omitempty"`
	ConfirmAcknowledgedAt *time.Time                 `json:"confirmAcknowledgedAt,omitempty" doc:"Arrived: when the owner stated that the old manager is stopped or no longer uses this instance although it never confirmed (the confirmation stopped)."`
	OldEnvironment        *ManagerMoveOldEnvironment `json:"oldEnvironment,omitempty" doc:"Arrived: the old server's environment: remove the moved stacks' stopped copies there, then archive it."`
}

// ManagerMoveNewServer is where the new server stands (old manager).
type ManagerMoveNewServer struct {
	EnvironmentID    string     `json:"environmentId,omitempty" doc:"The new server's environment, once its agent enrolled."`
	EnvironmentName  string     `json:"environmentName,omitempty" example:"192.168.1.20"`
	EnrollmentState  string     `json:"enrollmentState,omitempty" enum:"pending,used,expired,revoked" doc:"The new server's enrollment token (24 hours; expired: create new setup files)."`
	Online           bool       `json:"online" doc:"The new server's agent is connected."`
	ManagerCheckedIn bool       `json:"managerCheckedIn" doc:"The new server's Docker Manager (waiting mode) asked for the handoff within the last two minutes."`
	CheckedInAt      *time.Time `json:"checkedInAt,omitempty" doc:"Its last request."`
}

// ManagerMoveEnvironment is the environment whose apps move.
type ManagerMoveEnvironment struct {
	EnvironmentID string `json:"environmentId" example:"0190a6e0-0000-7000-8000-000000000001"`
	Name          string `json:"name" example:"old-server"`
	Online        bool   `json:"online"`
	StackCount    int    `json:"stackCount" doc:"Its stacks (Docker Manager's own stack stays and moves with the manager)."`
}

// ManagerMoveProgress is the latest Move everything.
type ManagerMoveProgress struct {
	JobID        string `json:"jobId,omitempty" doc:"The manager.move job."`
	JobState     string `json:"jobState,omitempty" enum:"queued,blocked,dispatched,running,cancelling,succeeded,failed,partial,cancelled,interrupted"`
	ErrorCode    string `json:"errorCode,omitempty" example:"manager_move_apps_not_moved"`
	Recovery     string `json:"recovery,omitempty" doc:"What to do after a failure (then Move everything again)."`
	MigrationID  string `json:"migrationId,omitempty" doc:"The environment migration moving the apps (GET /api/v1/environments/{sourceEnvironment.environmentId}/migrations/{migrationId})."`
	StacksMoved  int    `json:"stacksMoved"`
	StacksTotal  int    `json:"stacksTotal"`
	CurrentStack string `json:"currentStack,omitempty" doc:"The stack moving now."`
}

// ManagerMoveRedirect is one manager.redirect sent before the handoff.
type ManagerMoveRedirect struct {
	EnvironmentID   string `json:"environmentId"`
	EnvironmentName string `json:"environmentName"`
	Role            string `json:"role" enum:"new_server,old_server" doc:"new_server: the new server's agent (told http://docker-manager:8080); old_server: the agent next to the old manager (told the new server's address)."`
	URL             string `json:"url" example:"http://192.168.1.20:8080" doc:"The address the agent was told to dial (the one to set by hand when needsFix)."`
	Sent            bool   `json:"sent" doc:"The agent accepted the new address."`
	ErrorCode       string `json:"errorCode,omitempty" enum:"offline,unsupported,timeout,refused" doc:"Why it was not sent."`
	Connected       bool   `json:"connected" doc:"Arrived: the agent is online here or connected since the move."`
	NeedsFix        bool   `json:"needsFix" doc:"Arrived: not told the new address and not connected since: set DOCKER_AGENT_MANAGER_URL to url (with DOCKER_AGENT_MANAGER_ALLOW_HTTP=true) on that server and restart the agent; on the new server, removing the move lines from .env does it."`
}

// ManagerMoveOldEnvironment is the old server's environment after the move.
type ManagerMoveOldEnvironment struct {
	EnvironmentID string `json:"environmentId"`
	Name          string `json:"name"`
	Online        bool   `json:"online"`
	Archived      bool   `json:"archived"`
	StackCount    int    `json:"stackCount" doc:"Stacks still managed there (did not move)."`
	StoppedCopies int    `json:"stoppedCopies" doc:"Moved stacks' stopped copies still there: remove them (the stack's migration, Remove source), then archive the environment."`
	MigrationID   string `json:"migrationId,omitempty" doc:"The environment migration that moved the apps."`
}

func newManagerMove(v managermove.View) ManagerMove {
	m := v.Move
	out := ManagerMove{ID: m.ID, State: string(m.State), CreatedAt: m.CreatedAt, ExpiresAt: m.ExpiresAt, ThisServerAddress: m.ThisServerAddress,
		NewServerAddress: m.NewServerAddress, ReadyAt: m.ReadyAt, DrainingAt: m.DrainingAt, HandedOffAt: m.HandedOffAt,
		ConfirmedAt: m.ConfirmedAt, EndedAt: m.EndedAt, HandoffAddress: m.HandoffAddress, JobsRunning: v.JobsRunning,
		Redirects: []ManagerMoveRedirect{}, SourceURL: m.SourceURL, ArrivedAt: m.ArrivedAt, ConfirmAttempts: m.ConfirmAttempts,
		ConfirmError: m.ConfirmError, LastConfirmAt: m.LastConfirmAt, ConfirmAcknowledgedAt: m.ConfirmAcknowledgedAt}
	if m.NewServerAddress != "" && m.State != domain.MoveArrived {
		out.StatusURL = "http://" + m.NewServerAddress
	}
	if m.State == domain.MoveArrived {
		out.OldManagerConfirmed = m.ConfirmedAt != nil
	}
	if n := v.NewServer; n != nil {
		out.NewServer = &ManagerMoveNewServer{EnvironmentID: n.EnvironmentID, EnvironmentName: n.EnvironmentName, EnrollmentState: n.EnrollmentState,
			Online: n.Online, ManagerCheckedIn: n.ManagerCheckedIn, CheckedInAt: n.CheckedInAt}
	}
	if s := v.Source; s != nil {
		out.SourceEnvironment = &ManagerMoveEnvironment{EnvironmentID: s.EnvironmentID, Name: s.Name, Online: s.Online, StackCount: s.StackCount}
	}
	if p := v.Progress; p != nil {
		out.Progress = &ManagerMoveProgress{JobID: p.JobID, JobState: string(p.JobState), ErrorCode: p.ErrorClass, Recovery: p.Recovery,
			MigrationID: p.MigrationID, StacksMoved: p.StacksMoved, StacksTotal: p.StacksTotal, CurrentStack: p.CurrentStack}
	}
	redirect := func(r domain.ManagerMoveRedirect) ManagerMoveRedirect {
		return ManagerMoveRedirect{EnvironmentID: r.EnvironmentID, EnvironmentName: r.EnvironmentName, Role: r.Role, URL: r.URL, Sent: r.Sent,
			ErrorCode: r.ErrorClass}
	}
	if c := v.Complete; c != nil {
		for _, r := range c.Redirects {
			d := redirect(r.ManagerMoveRedirect)
			d.Connected, d.NeedsFix = r.Connected, r.NeedsFix
			out.Redirects = append(out.Redirects, d)
		}
		if o := c.OldEnvironment; o != nil {
			out.OldEnvironment = &ManagerMoveOldEnvironment{EnvironmentID: o.EnvironmentID, Name: o.Name, Online: o.Online, Archived: o.Archived,
				StackCount: o.StackCount, StoppedCopies: o.StoppedCopies, MigrationID: o.MigrationID}
		}
	} else {
		for _, r := range m.Redirects {
			out.Redirects = append(out.Redirects, redirect(r))
		}
	}
	return out
}

type managerMoveOutput struct {
	Body ManagerMove
}

type createManagerMoveInput struct {
	Body struct {
		ThisServerAddress  string `json:"thisServerAddress" minLength:"1" maxLength:"300" example:"192.168.1.10" doc:"This server's IP address or host name as the new server reaches it, optionally with the port (8080 when absent)."`
		NewServerAddress   string `json:"newServerAddress" minLength:"1" maxLength:"300" example:"192.168.1.20" doc:"The new server's IP address or host name, optionally with the port (8080 when absent)."`
		NewEnvironmentName string `json:"newEnvironmentName,omitempty" maxLength:"63" example:"new-server" doc:"The new server's environment name (default: its address)."`
	}
}

// CreatedManagerMove is a new move with the new server's files, shown once.
type CreatedManagerMove struct {
	Move          ManagerMove `json:"move"`
	ComposeYAML   string      `json:"composeYaml" doc:"The new server's compose.yaml (no secrets)."`
	Env           string      `json:"env" doc:"The new server's .env: it carries the move code and the agent's enrollment token. Shown once; never stored in clear, logged or shown again."`
	StatusURL     string      `json:"statusUrl" example:"http://192.168.1.20:8080" doc:"Where the new manager shows the move's progress."`
	AgentEnrolled bool        `json:"agentEnrolled" doc:"New setup files: the new server's agent already enrolled, so the .env has no enrollment token (the agent keeps its credential in its volume: replace the .env in the same folder and run docker compose up -d). Always false on creation."`
}

func newCreatedManagerMove(c managermove.Created) CreatedManagerMove {
	return CreatedManagerMove{Move: newManagerMove(managermove.View{Move: c.Move}), ComposeYAML: c.Files.ComposeYAML, Env: c.Files.Env,
		StatusURL: c.StatusURL, AgentEnrolled: c.AgentEnrolled}
}

type createdManagerMoveOutput struct {
	Body CreatedManagerMove
}

// ManagerMoveDefaults prefills the create form.
type ManagerMoveDefaults struct {
	ThisServerAddress string                  `json:"thisServerAddress,omitempty" example:"192.168.1.10" doc:"The service address of the environment next to this manager, when set."`
	SourceEnvironment *ManagerMoveEnvironment `json:"sourceEnvironment,omitempty" doc:"The environment next to this manager (its apps move). Absent when none is known: no agent runs next to Docker Manager, or it has not connected since the manager started."`
}

type managerMoveDefaultsOutput struct {
	Body ManagerMoveDefaults
}

type cancelManagerMoveInput struct {
	Body *struct {
		ResumeHere   bool   `json:"resumeHere,omitempty" doc:"Required after the handoff: the new manager never started with the copy, and this manager takes over again."`
		InstanceName string `json:"instanceName,omitempty" maxLength:"64" example:"Docker Manager" doc:"Required with resumeHere: this Docker Manager's name, typed."`
	}
}

// SessionManagerMove is the move lock of this manager as every signed-in
// user sees it (GET /auth/session): no move ID, no secrets.
type SessionManagerMove struct {
	State   string `json:"state" enum:"none,moving,moved" doc:"none: nothing is locked. moving: this manager is read-only while it hands over to a new server. moved: the new server runs Docker Manager; this one stays read-only for good."`
	Address string `json:"address,omitempty" example:"https://docker.example.com" doc:"moving and moved: the address that leads to the new Docker Manager once it is pointed there (the public URL both share)."`
}

// sessionManagerMove reads the move lock for the session (nil when the
// move service is missing or cannot be read: the 409 manager_moved of a
// refused change still tells).
func sessionManagerMove(ctx context.Context, svc ManagerMoveService) *SessionManagerMove {
	if svc == nil {
		return nil
	}
	st, err := svc.LockStatus(ctx)
	if err != nil {
		logging.FromContext(ctx).Warn("could not read the manager move lock for the session", "error", err)
		return nil
	}
	return &SessionManagerMove{State: st.State, Address: st.Address}
}

// ManagerMoveCheckIn answers the waiting manager's check-in: the move's
// state and the apps' progress (no secrets, no content).
type ManagerMoveCheckIn struct {
	State        string `json:"state" example:"moving" enum:"open,moving,ready,draining,handed_off,confirmed" doc:"open, moving: keep checking in; ready, draining, handed_off: call the handoff; confirmed: the move is over."`
	StacksMoved  int    `json:"stacksMoved" example:"3"`
	StacksTotal  int    `json:"stacksTotal" example:"8"`
	CurrentStack string `json:"currentStack,omitempty" example:"traefik"`
	JobsRunning  int    `json:"jobsRunning" example:"0" doc:"draining: the jobs the handoff waits for."`
}

type managerMoveCheckInOutput struct {
	Body ManagerMoveCheckIn
}

// MoveStatus is the waiting mode's status (public; never the code or any
// content).
type MoveStatus struct {
	Phase               string     `json:"phase" enum:"none,connecting,waiting,finishing_jobs,copying,checking,staging,restarting,failed,complete" doc:"none: not moving. connecting: asking the old manager (errorCode after a failed attempt; it keeps asking). waiting: the old manager answered; the apps have not moved yet (oldState open: press Move everything there; moving: stacksMoved of stacksTotal). finishing_jobs: the old manager is read-only while its jobs finish. copying, checking, staging: the state arrives. restarting: this manager restarts as the moved Docker Manager. failed: the copy was refused (recovery); restart this manager after fixing. complete: this manager runs the moved Docker Manager; point DNS at this server and remove the move lines from .env."`
	OldManager          string     `json:"oldManager,omitempty" example:"http://192.168.1.10:8080" doc:"DOCKER_MANAGER_MOVE_FROM."`
	OldState            string     `json:"oldState,omitempty" enum:"open,moving,ready,draining,handed_off" doc:"The old move's state from its last answer."`
	StacksMoved         int        `json:"stacksMoved"`
	StacksTotal         int        `json:"stacksTotal"`
	CurrentStack        string     `json:"currentStack,omitempty"`
	JobsRunning         int        `json:"jobsRunning" doc:"finishing_jobs: jobs still running on the old manager."`
	BytesReceived       int64      `json:"bytesReceived"`
	BytesTotal          int64      `json:"bytesTotal" doc:"0 until the copy starts."`
	LastContactAt       *time.Time `json:"lastContactAt,omitempty" doc:"When the old manager last answered."`
	ErrorCode           string     `json:"errorCode,omitempty" enum:"move_code_invalid,move_clock_skew,manager_move_unreachable,manager_move_refused,manager_move_transfer_failed,manager_move_state_invalid,manager_move_schema_incompatible,manager_move_not_handed_off,manager_move_stage_failed"`
	Recovery            string     `json:"recovery,omitempty" doc:"What to do about errorCode."`
	PublicURL           string     `json:"publicUrl,omitempty" example:"https://docker.example.com" doc:"The address to point at this server once the move is done."`
	OldManagerConfirmed bool       `json:"oldManagerConfirmed" doc:"complete: the old manager accepted the confirmation."`
}

type moveStatusOutput struct {
	Body MoveStatus
}

// moveError maps move errors.
func moveError(err error) error {
	var fe *domain.FieldError
	var jr *domain.JobsRunningError
	var nr *domain.MoveNotReadyError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &fe):
		return Invalid("invalid input", Field("body."+fe.Field, fe.Message))
	case errors.As(err, &jr):
		secs := max(int(jr.RetryAfter/time.Second), 1)
		return Conflict(CodeJobsRunning, strconv.Itoa(jr.Count)+" jobs are still running on this manager; retry when they finished").
			WithHeader("Retry-After", strconv.Itoa(secs)).WithHeader(managermove.JobsRunningHeader, strconv.Itoa(jr.Count))
	case errors.As(err, &nr):
		secs := max(int(nr.RetryAfter/time.Second), 1)
		e := Conflict(CodeManagerMoveNotReady, "the apps have not moved yet: press Move everything on this manager; ask again later").
			WithHeader("Retry-After", strconv.Itoa(secs)).WithHeader(managermove.MoveStateHeader, string(nr.State)).
			WithHeader(managermove.MoveStacksHeader, strconv.Itoa(nr.StacksMoved)+"/"+strconv.Itoa(nr.StacksTotal))
		if c := headerSafe(nr.CurrentStack); c != "" {
			e = e.WithHeader(managermove.MoveCurrentStackHeader, c)
		}
		return e
	case errors.Is(err, managermove.ErrResumeRequired):
		return Invalid(err.Error(), Field("body.resumeHere", "must be true once the state was handed off"))
	case errors.Is(err, managermove.ErrInstanceNameMismatch):
		return Invalid(err.Error(), Field("body.instanceName", "type this Docker Manager's name"))
	case errors.Is(err, domain.ErrManagerMoveNotFound):
		return NotFound("no move of this manager")
	case errors.Is(err, domain.ErrManagerMoveExists):
		return Conflict(CodeManagerMoveExists, "a move of this manager is already open or in progress; cancel it first")
	case errors.Is(err, domain.ErrManagerMoveState):
		return Conflict(CodeManagerMoveState, "the move is not in a state that allows this")
	case errors.Is(err, domain.ErrManagerMoveNewServerMissing):
		return Conflict(CodeManagerMoveNewServerMissing, "the new server is not ready: start it with the setup files (or create new ones), "+
			"and wait until its agent is connected and its Docker Manager has checked in")
	case errors.Is(err, domain.ErrMoveCodeInvalid):
		return NewError(http.StatusUnauthorized, CodeMoveCodeInvalid, "the move's authentication is not valid (wrong code, replayed, expired or cancelled move)").
			WithHeader("WWW-Authenticate", managermove.AuthScheme)
	case errors.Is(err, domain.ErrMoveClockSkew):
		return NewError(http.StatusUnauthorized, CodeMoveClockSkew, "the clocks of the two servers differ by more than five minutes; set them right (NTP)").
			WithHeader("WWW-Authenticate", managermove.AuthScheme)
	case errors.Is(err, domain.ErrManagerMoved):
		return Conflict(CodeManagerMoved, managerMovedMessage)
	case errors.Is(err, domain.ErrForbidden), errors.Is(err, domain.ErrStepUpRequired), errors.Is(err, domain.ErrNotAuthenticated):
		return identityError(err)
	}
	return JobErrorFor(err)
}

// headerSafe keeps a value that fits a response header (stack names are
// Compose project names; anything else is dropped).
func headerSafe(v string) string {
	if len(v) > 200 {
		return ""
	}
	for _, r := range v {
		if r < 0x21 || r > 0x7e {
			return ""
		}
	}
	return v
}

// moveAuthKey carries a move request's authentication from
// moveAuthMiddleware to the handler.
type moveAuthKey struct{}

// moveAuthMiddleware reads the move request's Authorization header, method
// and path (the identity middleware leaves such requests anonymous).
func moveAuthMiddleware(ctx huma.Context, next func(huma.Context)) {
	a := managermove.MoveAuth{Header: ctx.Header("Authorization"), Method: ctx.Method(), Path: ctx.URL().Path}
	next(huma.WithValue(ctx, moveAuthKey{}, a))
}

func moveAuthFrom(ctx context.Context) managermove.MoveAuth {
	a, _ := ctx.Value(moveAuthKey{}).(managermove.MoveAuth)
	return a
}

type managerMoveAPI struct {
	authz authz.Authorizer
	svc   ManagerMoveService
}

// owner checks the owner-only move capability before availability.
func (h *managerMoveAPI) owner(ctx context.Context) (ManagerMoveService, error) {
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	if !c.Can(capManagerMove, authz.Instance()).Allowed {
		return nil, Forbidden("only the instance owner may move Docker Manager")
	}
	if h.svc == nil {
		return nil, Unavailable(CodeUnavailable, "moving Docker Manager is not available")
	}
	return h.svc, nil
}

func (h *managerMoveAPI) service() (ManagerMoveService, error) {
	if h.svc == nil {
		return nil, Unavailable(CodeUnavailable, "moving Docker Manager is not available")
	}
	return h.svc, nil
}

const moveCodeAuth = " Authenticated by a signature of the move code (Authorization: DMM <moveId>:<unix time>:<nonce>:<HMAC-SHA256>, " +
	"docs/internal/architecture/manager-move.md), never by a session or the code itself: 401 move_code_invalid for a wrong signature, " +
	"a replayed nonce or an ended move, 401 move_clock_skew when the time is more than five minutes off. Plain HTTP is accepted " +
	"(the signature and the package encryption protect the exchange)."

func registerManagerMove(a huma.API, deps Deps) {
	h := &managerMoveAPI{authz: deps.Authorizer, svc: deps.ManagerMove}
	const tag = "Manager move"
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-manager-move-defaults", Method: http.MethodGet, Path: BasePath + "/manager/move/defaults",
			Summary: "Prefill a move of Docker Manager",
			Description: "This server's address (the service address of the environment next to this manager, when set) and that " +
				"environment, whose apps move. " + ownerOnly,
			Tags: []string{tag}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusServiceUnavailable},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, _ *struct{}) (*managerMoveDefaultsOutput, error) {
		svc, err := h.owner(ctx)
		if err != nil {
			return nil, err
		}
		d, err := svc.Defaults(ctx)
		if err != nil {
			return nil, moveError(err)
		}
		out := &managerMoveDefaultsOutput{Body: ManagerMoveDefaults{ThisServerAddress: d.ThisServerAddress}}
		if s := d.Source; s != nil {
			out.Body.SourceEnvironment = &ManagerMoveEnvironment{EnvironmentID: s.EnvironmentID, Name: s.Name, Online: s.Online, StackCount: s.StackCount}
		}
		return out, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-manager-move", Method: http.MethodPost, Path: BasePath + "/manager/moves",
			Summary: "Move Docker Manager to a new server", DefaultStatus: http.StatusCreated,
			Description: "Starts a move of this manager (with the apps of the environment next to it) to a new server: creates the move " +
				"code (valid until the move is confirmed or cancelled, at most seven days) and an enrollment token for the new server's " +
				"agent (24 hours), and returns the new server's compose.yaml and .env once (the .env carries both secrets). Nothing is " +
				"locked. One move at a time (409 manager_move_exists). Requires a recent step-up. " + ownerOnly,
			Tags: []string{tag}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance, AuditAction: "manager.move.create",
	}, func(ctx context.Context, in *createManagerMoveInput) (*createdManagerMoveOutput, error) {
		svc, err := h.owner(ctx)
		if err != nil {
			return nil, err
		}
		c, err := svc.CreateMove(ctx, managermove.CreateRequest{ThisServerAddress: in.Body.ThisServerAddress,
			NewServerAddress: in.Body.NewServerAddress, NewEnvironmentName: strings.TrimSpace(in.Body.NewEnvironmentName)})
		if err != nil {
			return nil, moveError(err)
		}
		return &createdManagerMoveOutput{Body: newCreatedManagerMove(c)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-manager-move-setup-files", Method: http.MethodPost, Path: BasePath + "/manager/move/setup-files",
			Summary: "Create new setup files for the new server",
			Description: "Returns the new server's compose.yaml and .env again (the same shape as the move's creation, once) with a new " +
				"move code: the previous code stops working at once (a new manager started with the previous .env is refused with " +
				"move_code_invalid). Unless the new server's agent already enrolled, the previous enrollment token is revoked and the .env " +
				"carries a new one (24 hours); when it enrolled, its environment is kept and the .env has no enrollment token " +
				"(agentEnrolled: the agent keeps its credential in its volume; replace the .env in the same folder and run docker " +
				"compose up -d). The last check-in is forgotten: the new manager checks in again with the new code. Allowed while the " +
				"move is open or ready; 409 manager_move_state while the apps move (the new server's agent would restart) and from the " +
				"handoff on; 404 without a move. The move's expiry does not change. Requires a recent step-up. " + ownerOnly,
			Tags: []string{tag}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance, AuditAction: "manager.move.setup_files",
	}, func(ctx context.Context, _ *struct{}) (*createdManagerMoveOutput, error) {
		svc, err := h.owner(ctx)
		if err != nil {
			return nil, err
		}
		c, err := svc.NewSetupFiles(ctx)
		if err != nil {
			return nil, moveError(err)
		}
		return &createdManagerMoveOutput{Body: newCreatedManagerMove(c)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-manager-move", Method: http.MethodGet, Path: BasePath + "/manager/move",
			Summary: "Get the current move",
			Description: "The move that is open or in progress on this manager (the new server's state, the apps' environment, the " +
				"progress of Move everything, the jobs a draining move waits for, the redirects), else the move this manager arrived " +
				"by (Move complete: the confirmation, the redirects with the agents that still need the new address, the old server's " +
				"environment). 404 when there is none. " + ownerOnly,
			Tags: []string{tag}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, _ *struct{}) (*managerMoveOutput, error) {
		svc, err := h.owner(ctx)
		if err != nil {
			return nil, err
		}
		v, err := svc.Current(ctx)
		if err != nil {
			return nil, moveError(err)
		}
		return &managerMoveOutput{Body: newManagerMove(v)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-manager-move-run", Method: http.MethodPost, Path: BasePath + "/manager/move/runs",
			Summary: "Move everything",
			Description: "Queues manager.move (202 + job): an environment migration of every stack of the environment next to this " +
				"manager to the new server's environment, then the move is ready and the new manager gets the handoff when it asks " +
				"next. Needs an open or ready move whose new server's agent is connected and whose new manager checked in (409 " +
				"manager_move_new_server_missing); a move that is already moving answers 409 manager_move_state. When the migration " +
				"does not complete the move stays open; run it again to move what is left. Requires a recent step-up. " + ownerOnly,
			Tags: []string{tag}, Security: cookieOnly, DefaultStatus: http.StatusAccepted,
			Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance, AuditAction: "manager.move.run",
	}, func(ctx context.Context, _ *struct{}) (*JobAccepted, error) {
		svc, err := h.owner(ctx)
		if err != nil {
			return nil, err
		}
		j, err := svc.StartRun(ctx)
		if err != nil {
			return nil, moveError(err)
		}
		return Accepted(j), nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-manager-move-cancellation", Method: http.MethodPost, Path: BasePath + "/manager/move/cancellations",
			Summary: "Cancel the current move",
			Description: "Ends a move that was not handed off and unlocks this manager (a running Move everything is cancelled; stacks " +
				"that moved stay on the new server). After the handoff only with resumeHere and the typed instance name: you state " +
				"that the new manager never started with the copy. When agents were already told the new address, the instance's " +
				"generation goes up and this manager restarts, so they accept it again. A confirmed move cannot be cancelled (409 " +
				"manager_move_state). Requires a recent step-up. " + ownerOnly,
			Tags: []string{tag}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance, AuditAction: "manager.move.cancel",
	}, func(ctx context.Context, in *cancelManagerMoveInput) (*managerMoveOutput, error) {
		svc, err := h.owner(ctx)
		if err != nil {
			return nil, err
		}
		var req managermove.CancelRequest
		if in.Body != nil {
			req = managermove.CancelRequest{ResumeHere: in.Body.ResumeHere, InstanceName: in.Body.InstanceName}
		}
		m, err := svc.Cancel(ctx, req)
		if err != nil {
			return nil, moveError(err)
		}
		return &managerMoveOutput{Body: newManagerMove(managermove.View{Move: m})}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-manager-move-acknowledgement", Method: http.MethodPost, Path: BasePath + "/manager/move/acknowledgements",
			Summary: "Acknowledge a missing confirmation of the old manager",
			Description: "On a manager that arrived by a move whose old manager never confirmed (it refused the confirmation, or cannot " +
				"be reached): you state that the old Docker Manager is stopped or no longer uses this instance. The confirmation stops " +
				"(the move code is forgotten) and Move complete counts it as done (confirmAcknowledgedAt). Allowed after at least one " +
				"failed confirmation attempt (409 manager_move_state before, and once the old manager confirmed); 404 without an " +
				"arrived move. Repeating it is harmless. Requires a recent step-up. " + ownerOnly,
			Tags: []string{tag}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance, AuditAction: "manager.move.acknowledge",
	}, func(ctx context.Context, _ *struct{}) (*managerMoveOutput, error) {
		svc, err := h.owner(ctx)
		if err != nil {
			return nil, err
		}
		v, err := svc.AcknowledgeConfirmation(ctx)
		if err != nil {
			return nil, moveError(err)
		}
		return &managerMoveOutput{Body: newManagerMove(v)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-manager-move-check-in", Method: http.MethodGet, Path: BasePath + "/manager/move/check-in",
			Summary: "Check in as the waiting new manager",
			Description: "Called every 10 s by the new manager in waiting mode: records its check-in (the old manager's page shows it) " +
				"and answers the move's state and the apps' progress; once the state is ready the new manager calls the handoff. " +
				"A read: not audited." + moveCodeAuth,
			Tags: []string{tag}, Middlewares: huma.Middlewares{moveAuthMiddleware},
			Errors: []int{http.StatusUnauthorized, http.StatusServiceUnavailable},
		},
		Capability: CapabilityPublic, Scope: ScopeNone,
	}, func(ctx context.Context, _ *struct{}) (*managerMoveCheckInOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		c, err := svc.CheckIn(ctx, moveAuthFrom(ctx))
		if err != nil {
			return nil, moveError(err)
		}
		return &managerMoveCheckInOutput{Body: ManagerMoveCheckIn{State: string(c.State), StacksMoved: c.StacksMoved, StacksTotal: c.StacksTotal,
			CurrentStack: c.CurrentStack, JobsRunning: c.JobsRunning}}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-manager-move-handoff", Method: http.MethodPost, Path: BasePath + "/manager/move/handoff",
			Summary: "Hand this manager's state to the waiting new manager",
			Description: "Called by the new manager in waiting mode once its check-in reports the move ready (each call records its " +
				"check-in too). Until Move everything made the move ready: 409 manager_move_not_ready with Retry-After and the progress in " + managermove.MoveStateHeader +
				" (open, moving), " + managermove.MoveStacksHeader + " (<moved>/<total>) and " + managermove.MoveCurrentStackHeader +
				". The first call of a ready move makes this manager read-only (no new job, no schedule; running jobs finish); while " +
				"jobs run it answers 409 jobs_running with Retry-After and " + managermove.JobsRunningHeader + ". Then the agents it " +
				"can place are told the new address (manager.redirect), agents are refused, the state is copied (the copy's generation " +
				"goes up by one) and the response streams the package encrypted with XChaCha20-Poly1305 under a key derived from the " +
				"code (a tar of state.json, docker-manager.db, secret-key.sealed, templates.tar.gz and manifest.json). Repeating it " +
				"after the handoff streams the same copy." + moveCodeAuth,
			Tags: []string{tag}, Middlewares: huma.Middlewares{moveAuthMiddleware},
			Errors: []int{http.StatusUnauthorized, http.StatusConflict, http.StatusServiceUnavailable},
			Responses: map[string]*huma.Response{"200": {Description: "The encrypted handoff package",
				Content: map[string]*huma.MediaType{managermove.PackageContentType: {Schema: &huma.Schema{Type: "string", Format: "binary"}}}}},
		},
		Capability: CapabilityPublic, Scope: ScopeNone, AuditAction: "manager.move.handoff",
	}, func(ctx context.Context, _ *struct{}) (*huma.StreamResponse, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		pkg, err := svc.Handoff(ctx, moveAuthFrom(ctx))
		if err != nil {
			return nil, moveError(err)
		}
		audit.SetDetail(ctx, "bytes", pkg.Size())
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			hctx.SetHeader("Content-Type", managermove.PackageContentType)
			hctx.SetHeader("X-Content-Type-Options", "nosniff")
			hctx.SetHeader(managermove.PackageSizeHeader, strconv.FormatInt(pkg.Size(), 10))
			hctx.SetStatus(http.StatusOK)
			if err := pkg.Write(hctx.BodyWriter()); err != nil {
				logging.FromContext(hctx.Context()).Warn("the handoff stream broke off; the new manager asks again", "error", err)
			}
		}}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-manager-move-confirmation", Method: http.MethodPost, Path: BasePath + "/manager/move/confirm",
			Summary: "Confirm that the new manager runs the instance",
			Description: "Called by the new manager after it started with the copy: handed_off becomes confirmed (repeating it is " +
				"harmless). This manager stays read-only and keeps refusing agents. 409 manager_move_state in any other state (the move " +
				"was cancelled or resumed here, or this address reaches the new manager)." + moveCodeAuth,
			Tags: []string{tag}, Middlewares: huma.Middlewares{moveAuthMiddleware},
			Errors: []int{http.StatusUnauthorized, http.StatusConflict, http.StatusServiceUnavailable},
		},
		Capability: CapabilityPublic, Scope: ScopeNone, AuditAction: "manager.move.confirm",
	}, func(ctx context.Context, _ *struct{}) (*managerMoveOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		m, err := svc.Confirm(ctx, moveAuthFrom(ctx))
		if err != nil {
			return nil, moveError(err)
		}
		return &managerMoveOutput{Body: newManagerMove(managermove.View{Move: m})}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-move-status", Method: http.MethodGet, Path: BasePath + "/move/status",
			Summary: "The move's progress on the new server",
			Description: "Public and read-only, for the status page of a new manager in waiting mode (DOCKER_MANAGER_MOVE_FROM and " +
				"DOCKER_MANAGER_MOVE_CODE on an empty data directory), the only API route it serves besides health: the phase, the old " +
				"manager's progress it last heard, the bytes copied, and the last failure with what to do. After the move, while the " +
				"move variables are still set, phase complete. phase none on any other manager. Never the code or any content.",
			Tags: []string{tag},
		},
		Capability: CapabilityPublic, Scope: ScopeNone,
	}, func(ctx context.Context, _ *struct{}) (*moveStatusOutput, error) {
		if h.svc == nil {
			return &moveStatusOutput{Body: MoveStatus{Phase: managermove.WaitNone}}, nil
		}
		st, err := h.svc.WaitStatus(ctx)
		if err != nil {
			return nil, Internal(err)
		}
		return &moveStatusOutput{Body: MoveStatus{Phase: st.Phase, OldManager: st.OldManager, OldState: st.OldState, StacksMoved: st.StacksMoved,
			StacksTotal: st.StacksTotal, CurrentStack: st.CurrentStack, JobsRunning: st.JobsRunning, BytesReceived: st.Bytes,
			BytesTotal: st.TotalBytes, LastContactAt: st.LastContactAt, ErrorCode: st.ErrorCode, Recovery: st.Recovery, PublicURL: st.PublicURL,
			OldManagerConfirmed: st.OldManagerConfirmed}}, nil
	})
}
