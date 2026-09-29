package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/logging"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authsep"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/managermove"
)

// Moving the manager to a new server (docs/internal/architecture/manager-move.md).
// Old manager: the owner creates, reads and cancels the move; the new
// manager calls the handoff and the confirmation with the move code
// (Authorization: Bearer dmm_<id>_<secret>) over a secure origin. New
// manager: first-run setup starts manager.receive; the owner reads the
// arrived move and its finish checklist.

// Move error codes.
const (
	CodeManagerMoved          = "manager_moved"
	CodeJobsRunning           = "jobs_running"
	CodeManagerMoveExists     = "manager_move_exists"
	CodeManagerMoveState      = "manager_move_state"
	CodeMoveCodeInvalid       = "move_code_invalid"
	CodeManagerMoveInProgress = "manager_move_in_progress"
)

const managerMovedMessage = "Docker Manager is moving (or moved) to a new server: this manager is read-only. Sign in on the new server's manager."

// capManagerMove is the owner-only capability of moves.
const capManagerMove = "manager.move"

// ManagerMoveService is the move service (internal/manager/managermove).
type ManagerMoveService interface {
	CreateMove(ctx context.Context) (domain.ManagerMove, string, error)
	Current(ctx context.Context) (managermove.View, error)
	Cancel(ctx context.Context, req managermove.CancelRequest) (domain.ManagerMove, error)
	Handoff(ctx context.Context, code string) (*managermove.Package, error)
	Confirm(ctx context.Context, code string) (domain.ManagerMove, error)
	StartReceive(ctx context.Context, sourceURL, code string) (domain.Job, error)
	LatestReceive(ctx context.Context) (*managermove.ReceiveStatus, error)
}

// MoveLock is the manager-move lock as the API sees it.
type MoveLock interface {
	ReadOnly() bool
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
	"create-setup-move":                          true,
}

// AllowedWhileMoved reports whether operationID is served while the
// manager is read-only because it moves (tests, documentation).
func AllowedWhileMoved(operationID string) bool { return allowedWhileMoved[operationID] }

// moveGuard refuses the operation while the manager moves to a new server.
func moveGuard(ctx huma.Context, next func(huma.Context)) {
	if deps, _ := depsFrom(ctx.Context()); deps.MoveLock != nil && deps.MoveLock.ReadOnly() {
		audit.SetErrorClass(ctx.Context(), CodeManagerMoved)
		writeHumaError(ctx, Conflict(CodeManagerMoved, managerMovedMessage))
		return
	}
	next(ctx)
}

// ManagerMove is a move of the manager to a new server. It never carries
// the move code.
type ManagerMove struct {
	ID          string     `json:"id" example:"0190a6e0-0000-7000-8000-000000000035"`
	State       string     `json:"state" enum:"open,draining,handed_off,confirmed,cancelled,expired,arrived" example:"open" doc:"Old manager: open (code created), draining (read-only, jobs finish), handed_off (the state was copied; agents refused), confirmed (the new manager runs the instance), cancelled, expired. New manager: arrived (this manager runs the moved instance)."`
	CreatedAt   time.Time  `json:"createdAt"`
	ExpiresAt   time.Time  `json:"expiresAt" doc:"An open or draining move ends at this time (one hour after the code was created)."`
	DrainingAt  *time.Time `json:"drainingAt,omitempty"`
	HandedOffAt *time.Time `json:"handedOffAt,omitempty"`
	ConfirmedAt *time.Time `json:"confirmedAt,omitempty" doc:"Old manager: when the new manager confirmed. Arrived: when the old manager accepted the confirmation."`
	EndedAt     *time.Time `json:"endedAt,omitempty" doc:"When the move was cancelled or expired."`
	// HandoffAddress is the new manager's address as the handoff saw it.
	HandoffAddress string `json:"handoffAddress,omitempty" example:"203.0.113.7" doc:"Client IP of the handoff request."`
	JobsRunning    int    `json:"jobsRunning" example:"0" doc:"Draining: the jobs the handoff waits for."`
	// Arrived move (new manager).
	SourceURL           string                `json:"sourceUrl,omitempty" example:"https://docker.example.com" doc:"Arrived: the old manager's address."`
	ArrivedAt           *time.Time            `json:"arrivedAt,omitempty"`
	OldManagerConfirmed bool                  `json:"oldManagerConfirmed" doc:"Arrived: the old manager accepted the confirmation (it stays locked for good)."`
	ConfirmAttempts     int                   `json:"confirmAttempts" doc:"Arrived: confirmation attempts so far (retried in the background until the old manager answers)."`
	ConfirmError        string                `json:"confirmError,omitempty" example:"unreachable" doc:"Arrived: the last failed confirmation: unreachable, insecure_origin, http_<status> (retried), code_invalid or state_refused (the old manager refused: make sure it no longer runs the instance)."`
	LastConfirmAt       *time.Time            `json:"lastConfirmAt,omitempty"`
	Checklist           *ManagerMoveChecklist `json:"checklist,omitempty" doc:"Arrived: what is left to finish the move."`
}

// ManagerMoveChecklist is the finish checklist of an arrived move.
type ManagerMoveChecklist struct {
	NewEnvironmentAdded bool                     `json:"newEnvironmentAdded" doc:"An environment was added after the move (the new server's own agent). False: add it (Add environment, the co-located install command)."`
	Environments        []ManagerMoveEnvironment `json:"environments" doc:"Environments with something left to do."`
}

// ManagerMoveEnvironment is one environment of the checklist.
type ManagerMoveEnvironment struct {
	EnvironmentID string   `json:"environmentId" example:"0190a6e0-0000-7000-8000-000000000001"`
	Name          string   `json:"name" example:"old-server"`
	Online        bool     `json:"online"`
	ManagerURL    string   `json:"managerUrl" example:"http://docker-manager:8080" doc:"The manager address the agent last dialed."`
	StackCount    int      `json:"stackCount"`
	Actions       []string `json:"actions" enum:"set_manager_url,migrate,archive" doc:"set_manager_url: set DOCKER_AGENT_MANAGER_URL to the public URL; migrate: move its stacks to the new server's environment; archive: it is empty, archive it."`
}

func newManagerMove(v managermove.View) ManagerMove {
	m := v.Move
	out := ManagerMove{ID: m.ID, State: string(m.State), CreatedAt: m.CreatedAt, ExpiresAt: m.ExpiresAt, DrainingAt: m.DrainingAt,
		HandedOffAt: m.HandedOffAt, ConfirmedAt: m.ConfirmedAt, EndedAt: m.EndedAt, HandoffAddress: m.HandoffAddress, JobsRunning: v.JobsRunning,
		SourceURL: m.SourceURL, ArrivedAt: m.ArrivedAt, ConfirmAttempts: m.ConfirmAttempts, ConfirmError: m.ConfirmError, LastConfirmAt: m.LastConfirmAt}
	if m.State == domain.MoveArrived {
		out.OldManagerConfirmed = m.ConfirmedAt != nil
	}
	if c := v.Checklist; c != nil {
		out.Checklist = &ManagerMoveChecklist{NewEnvironmentAdded: c.NewEnvironmentAdded, Environments: []ManagerMoveEnvironment{}}
		for _, e := range c.Environments {
			out.Checklist.Environments = append(out.Checklist.Environments, ManagerMoveEnvironment{EnvironmentID: e.ID, Name: e.Name,
				Online: e.Online, ManagerURL: e.ManagerURL, StackCount: e.StackCount, Actions: append([]string{}, e.Actions...)})
		}
	}
	return out
}

type managerMoveOutput struct {
	Body ManagerMove
}

// CreatedManagerMove is a new move with its code, shown once.
type CreatedManagerMove struct {
	Move ManagerMove `json:"move"`
	Code string      `json:"code" example:"dmm_0190a6e0-0000-7000-8000-000000000035_3q2-7wEXAMPLEEXAMPLEEXAMPLEEXAMPLEEXAMPLE" doc:"The move code, shown once: enter it on the new manager's setup page within the hour. Never stored, logged or shown again."`
}

type createdManagerMoveOutput struct {
	Body CreatedManagerMove
}

type cancelManagerMoveInput struct {
	Body *struct {
		ResumeHere   bool   `json:"resumeHere,omitempty" doc:"Required after the handoff: the new manager never started with the copy, and this manager takes over again."`
		InstanceName string `json:"instanceName,omitempty" maxLength:"64" example:"Docker Manager" doc:"Required with resumeHere: this Docker Manager's name, typed."`
	}
}

type setupMoveInput struct {
	Body struct {
		SourceURL string `json:"sourceUrl" minLength:"1" maxLength:"2048" example:"https://docker.example.com" doc:"The old Docker Manager's public HTTPS address."`
		Code      string `json:"code" minLength:"1" maxLength:"256" writeOnly:"true" doc:"The move code created on the old manager. Never returned, logged, stored or audited."`
	}
}

// SetupManagerMove is the newest receive's progress (setup status).
type SetupManagerMove struct {
	JobID          string `json:"jobId"`
	State          string `json:"state" enum:"queued,waiting,running,succeeded,failed,cancelled,interrupted"`
	Step           string `json:"step,omitempty" enum:"handoff,verify,stage" doc:"The step running (or failed)."`
	WaitingJobs    int    `json:"waitingJobs" doc:"Jobs the old manager still finishes before it hands off its state."`
	BytesReceived  int64  `json:"bytesReceived"`
	BytesTotal     int64  `json:"bytesTotal" doc:"0 until the transfer starts."`
	ErrorCode      string `json:"errorCode,omitempty" example:"move_code_invalid"`
	Recovery       string `json:"recovery,omitempty" doc:"What to do after a failure."`
	RestartPending bool   `json:"restartPending" doc:"The state is staged; the manager restarts to run it. Sign in afterwards with the old manager's accounts."`
}

func newSetupManagerMove(st *managermove.ReceiveStatus) *SetupManagerMove {
	return &SetupManagerMove{JobID: st.JobID, State: string(st.State), Step: st.Step, WaitingJobs: st.WaitingJobs, BytesReceived: st.Bytes,
		BytesTotal: st.TotalBytes, ErrorCode: st.ErrorClass, Recovery: st.Recovery, RestartPending: st.RestartPending}
}

// moveError maps move errors.
func moveError(err error) error {
	var fe *domain.FieldError
	var jr *domain.JobsRunningError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &fe):
		return Invalid("invalid input", Field("body."+fe.Field, fe.Message))
	case errors.As(err, &jr):
		secs := max(int(jr.RetryAfter/time.Second), 1)
		return Conflict(CodeJobsRunning, strconv.Itoa(jr.Count)+" jobs are still running on this manager; retry when they finished").
			WithHeader("Retry-After", strconv.Itoa(secs)).WithHeader(managermove.JobsRunningHeader, strconv.Itoa(jr.Count))
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
	case errors.Is(err, domain.ErrMoveCodeInvalid):
		return NewError(http.StatusUnauthorized, CodeMoveCodeInvalid, "the move code is not valid (wrong, expired, cancelled or already used)").
			WithHeader("WWW-Authenticate", `Bearer error="invalid_token"`)
	case errors.Is(err, domain.ErrManagerMoveInProgress):
		return Conflict(CodeManagerMoveInProgress, "a move or a backup import is already running or staged on this manager; follow it in the setup status")
	case errors.Is(err, domain.ErrManagerMoved):
		return Conflict(CodeManagerMoved, managerMovedMessage)
	case errors.Is(err, domain.ErrForbidden), errors.Is(err, domain.ErrStepUpRequired), errors.Is(err, domain.ErrNotAuthenticated):
		return identityError(err)
	}
	var ie *domain.InsecureOriginError
	if errors.As(err, &ie) {
		return identityError(err)
	}
	return JobErrorFor(err)
}

// moveCodeKey carries the move code of the handoff and confirmation
// requests from moveCodeMiddleware to the handler.
type moveCodeKey struct{}

// moveCodeMiddleware reads the move code from Authorization: Bearer (the
// identity middleware leaves such requests anonymous).
func moveCodeMiddleware(ctx huma.Context, next func(huma.Context)) {
	h := http.Header{}
	h.Set("Authorization", ctx.Header("Authorization"))
	tok, ok := authsep.BearerToken(h)
	if !ok || !authsep.IsMoveCode(tok) {
		tok = ""
	}
	next(huma.WithValue(ctx, moveCodeKey{}, tok))
}

func moveCodeFrom(ctx context.Context) string {
	s, _ := ctx.Value(moveCodeKey{}).(string)
	return s
}

type managerMoveAPI struct {
	authz authz.Authorizer
	svc   ManagerMoveService
	ident IdentityService
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

const moveCodeAuth = " Authenticated by the move code (Authorization: Bearer dmm_<id>_<secret>), not by a session; refused with " +
	"403 insecure_origin unless the request reaches this manager over HTTPS on DOCKER_MANAGER_PUBLIC_URL."

func registerManagerMove(a huma.API, deps Deps) {
	h := &managerMoveAPI{authz: deps.Authorizer, svc: deps.ManagerMove, ident: deps.Identity}
	const tag = "Manager move"
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-manager-move", Method: http.MethodPost, Path: BasePath + "/manager/moves",
			Summary: "Create a move code (move Docker Manager to a new server)", DefaultStatus: http.StatusCreated,
			Description: "Starts a move of this manager to a new server and returns the move code once: enter it with this manager's " +
				"address on the new manager's setup page within one hour. Nothing is locked until the new manager asks for the handoff. " +
				"One move at a time (409 manager_move_exists). Requires a recent step-up. " + ownerOnly,
			Tags: []string{tag}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusServiceUnavailable},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance, AuditAction: "manager.move.create",
	}, func(ctx context.Context, _ *struct{}) (*createdManagerMoveOutput, error) {
		svc, err := h.owner(ctx)
		if err != nil {
			return nil, err
		}
		m, code, err := svc.CreateMove(ctx)
		if err != nil {
			return nil, moveError(err)
		}
		return &createdManagerMoveOutput{Body: CreatedManagerMove{Move: newManagerMove(managermove.View{Move: m}), Code: code}}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-manager-move", Method: http.MethodGet, Path: BasePath + "/manager/move",
			Summary: "Get the current move",
			Description: "The move that is open or in progress on this manager (with the jobs a draining move waits for), else the move " +
				"this manager arrived by, with whether the old manager confirmed and the finish checklist. 404 when there is none. " + ownerOnly,
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
			OperationID: "create-manager-move-cancellation", Method: http.MethodPost, Path: BasePath + "/manager/move/cancellations",
			Summary: "Cancel the current move",
			Description: "Ends an open or draining move and unlocks this manager. After the handoff only with resumeHere and the typed " +
				"instance name: you state that the new manager never started with the copy (agents that met the new manager refuse this " +
				"one). A confirmed move cannot be cancelled (409 manager_move_state). Requires a recent step-up. " + ownerOnly,
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
			OperationID: "create-manager-move-handoff", Method: http.MethodPost, Path: BasePath + "/manager/move/handoff",
			Summary: "Hand this manager's state to a new manager",
			Description: "Called by the new manager (manager.receive). The first call makes this manager read-only (no new job, no " +
				"schedule; running jobs finish). While jobs run it answers 409 jobs_running with Retry-After and " +
				managermove.JobsRunningHeader + " (the count). Then agents are refused, the state is copied (the copy's generation goes up " +
				"by one) and the response streams a tar: state.json, docker-manager.db, secret-key.sealed (the secret key sealed under " +
				"the code), templates.tar.gz, then manifest.json (length and SHA-256 of every part). Repeating it after the handoff " +
				"streams the same copy. 401 move_code_invalid for a wrong, expired or cancelled code." + moveCodeAuth,
			Tags: []string{tag}, Middlewares: huma.Middlewares{moveCodeMiddleware},
			Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusServiceUnavailable},
			Responses: map[string]*huma.Response{"200": {Description: "The handoff package (tar)",
				Content: map[string]*huma.MediaType{managermove.PackageContentType: {Schema: &huma.Schema{Type: "string", Format: "binary"}}}}},
		},
		Capability: CapabilityPublic, Scope: ScopeNone, AuditAction: "manager.move.handoff",
	}, func(ctx context.Context, _ *struct{}) (*huma.StreamResponse, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		pkg, err := svc.Handoff(ctx, moveCodeFrom(ctx))
		if err != nil {
			return nil, moveError(err)
		}
		audit.SetDetail(ctx, "bytes", pkg.Size())
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			hctx.SetHeader("Content-Type", managermove.PackageContentType)
			hctx.SetHeader("X-Content-Type-Options", "nosniff")
			hctx.SetHeader("X-Docker-Manager-Move-Size", strconv.FormatInt(pkg.Size(), 10))
			hctx.SetStatus(http.StatusOK)
			if err := pkg.Write(hctx.BodyWriter()); err != nil {
				logging.FromContext(hctx.Context()).Warn("the handoff stream broke off; the new manager retries", "error", err)
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
			Tags: []string{tag}, Middlewares: huma.Middlewares{moveCodeMiddleware},
			Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusServiceUnavailable},
		},
		Capability: CapabilityPublic, Scope: ScopeNone, AuditAction: "manager.move.confirm",
	}, func(ctx context.Context, _ *struct{}) (*managerMoveOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		m, err := svc.Confirm(ctx, moveCodeFrom(ctx))
		if err != nil {
			return nil, moveError(err)
		}
		return &managerMoveOutput{Body: newManagerMove(managermove.View{Move: m})}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-setup-move", Method: http.MethodPost, Path: BasePath + "/setup/move",
			Summary: "Receive a moving manager's state (first-run setup)",
			Description: "Before an owner exists only (then 409 setup_complete), over HTTPS on DOCKER_MANAGER_PUBLIC_URL (403 " +
				"insecure_origin). Queues manager.receive (202 + job): it asks the old manager (sourceUrl, https only) for the handoff, " +
				"waits up to 30 minutes for its running jobs, stores the package and checks it (sums, the code opens the secret key, the " +
				"database's integrity, instance, schema and settings), stages it and restarts as the moved instance. Follow the " +
				"progress with GET /api/v1/setup/status (managerMove). Sessions, API tokens and agents of the old manager keep working.",
			Tags: []string{tagSetup}, DefaultStatus: http.StatusAccepted,
			Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests},
		},
		Capability: CapabilityPublic, Scope: ScopeNone,
	}, func(ctx context.Context, in *setupMoveInput) (*JobAccepted, error) {
		if h.ident == nil {
			return nil, Unavailable(CodeUnavailable, "setup is not available")
		}
		if err := h.ident.SetupOpen(ctx); err != nil {
			return nil, identityError(err)
		}
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		j, err := svc.StartReceive(ctx, strings.TrimSpace(in.Body.SourceURL), in.Body.Code)
		if err != nil {
			return nil, moveError(err)
		}
		return Accepted(j), nil
	})
}
