package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/logging"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/server/sse"
)

const tagJobs = "Jobs"

// Job capabilities (#17).
const (
	CapJobRead   Capability = "job.read"
	CapJobCancel Capability = "job.cancel"
)

// JobService is the job engine as seen by the API (implemented by
// internal/manager/jobs.Engine).
type JobService interface {
	List(ctx context.Context, f domain.JobFilter) ([]domain.Job, error)
	Get(ctx context.Context, id string) (domain.Job, error)
	Cancel(ctx context.Context, id string) (domain.Job, error)
	Events(ctx context.Context, jobID string, afterSeq int64, limit int) ([]domain.JobEvent, error)
	Subscribe(jobID string) (<-chan struct{}, func())
}

// DefaultSSEHeartbeat is the keep-alive interval of event streams
// (DOCKER_MANAGER_STREAM_HEARTBEAT overrides it via Deps.SSEHeartbeat).
const DefaultSSEHeartbeat = sse.DefaultHeartbeat

// JobTarget is a resource a job acts on.
type JobTarget struct {
	Type          string `json:"type" enum:"stack,container,volume,image,network,repository,path,destination_path,build_definition,maintenance_policy" doc:"Target resource type."`
	ID            string `json:"id" doc:"Resource identifier within its environment (stack ID, container, volume or network name, image reference, repository ID, absolute path, build definition ID)."`
	EnvironmentID string `json:"environmentId,omitempty" doc:"Environment of the target when it differs from the job's (migrations)."`
}

// JobLock is one entry of a job's lock set (see docs/architecture/job-engine.md).
type JobLock struct {
	Scope         string `json:"scope" enum:"host,stack,container,volume,image,network,file_path,repository"`
	EnvironmentID string `json:"environmentId,omitempty" doc:"Absent for instance-wide scopes (repository)."`
	Name          string `json:"name,omitempty" doc:"Resource name; * locks every resource of the scope in the environment; absent for host."`
	Mode          string `json:"mode" enum:"shared,exclusive"`
}

// JobProgress is the latest progress report.
type JobProgress struct {
	Percent *int   `json:"percent,omitempty" minimum:"0" maximum:"100" doc:"Completion percentage; absent when unknown."`
	Step    string `json:"step,omitempty" doc:"Current step of the kind's plan."`
	Message string `json:"message,omitempty"`
}

// JobItem is the result for one item of a multi-item job.
type JobItem struct {
	Name    string `json:"name"`
	Status  string `json:"status" enum:"succeeded,failed,skipped"`
	Message string `json:"message,omitempty"`
}

// JobError explains a non-successful outcome.
type JobError struct {
	Class    string `json:"class" doc:"Stable error class, e.g. agent_offline, authorization_revoked, step_failed, unknown_outcome, journal_lost, cancelled." example:"agent_offline"`
	Message  string `json:"message"`
	Recovery string `json:"recovery" doc:"What the operator should do next."`
}

// JobBlockedBy says why a waiting job has not started.
type JobBlockedBy struct {
	Reason string `json:"reason" enum:"lock,agent_offline,concurrency_limit"`
	JobID  string `json:"jobId,omitempty" doc:"The job holding (or queued first for) the conflicting lock or concurrency slot."`
}

// Job is a durable, manager-owned job (#26).
type Job struct {
	ID       string `json:"id" example:"0190a6e0-0000-7000-8000-000000000001"`
	Kind     string `json:"kind" example:"stack.deploy" doc:"Job kind from the catalog in docs/architecture/job-engine.md."`
	State    string `json:"state" enum:"queued,blocked,dispatched,running,cancelling,succeeded,failed,partial,cancelled,interrupted"`
	Origin   string `json:"origin" enum:"manual,scheduled,api_token" doc:"Why the job exists. Audit metadata, not an access-control owner."`
	Executor string `json:"executor" enum:"agent,manager"`
	// Initiator fields are audit metadata only.
	InitiatorUserID  string        `json:"initiatorUserId,omitempty" doc:"User who requested the job (audit metadata only)."`
	InitiatorTokenID string        `json:"initiatorTokenId,omitempty" doc:"API token used to request the job (audit metadata only)."`
	PolicyID         string        `json:"policyId,omitempty" doc:"Policy that scheduled the job."`
	EnvironmentID    string        `json:"environmentId,omitempty"`
	Targets          []JobTarget   `json:"targets"`
	Attempt          int           `json:"attempt" minimum:"1" doc:"Dispatch attempt; increases when an interrupted job resumes or a lost command is re-sent."`
	Progress         JobProgress   `json:"progress"`
	Items            []JobItem     `json:"items"`
	Error            *JobError     `json:"error,omitempty" doc:"Present for failed, partial, cancelled and interrupted jobs."`
	BlockedBy        *JobBlockedBy `json:"blockedBy,omitempty" doc:"Present while the job is blocked."`
	Locks            []JobLock     `json:"locks" doc:"The job's lock scopes (held while dispatched, running or cancelling)."`
	LocksHeld        bool          `json:"locksHeld"`
	CancelRequested  bool          `json:"cancelRequested"`
	Cancellable      bool          `json:"cancellable" doc:"False once the job reached a terminal state."`
	CreatedAt        time.Time     `json:"createdAt"`
	UpdatedAt        time.Time     `json:"updatedAt"`
	DispatchedAt     *time.Time    `json:"dispatchedAt,omitempty"`
	StartedAt        *time.Time    `json:"startedAt,omitempty" doc:"When the current attempt was acknowledged by its executor."`
	FinishedAt       *time.Time    `json:"finishedAt,omitempty"`
}

// NewJob converts a domain job to its transport form.
func NewJob(j domain.Job) Job {
	out := Job{
		ID: j.ID, Kind: string(j.Kind), State: string(j.State), Origin: string(j.Origin), Executor: string(j.Executor),
		InitiatorUserID: j.InitiatorUserID, InitiatorTokenID: j.InitiatorTokenID, PolicyID: j.PolicyID,
		EnvironmentID: j.EnvironmentID, Targets: []JobTarget{}, Attempt: j.Attempt, Items: []JobItem{}, Locks: []JobLock{},
		LocksHeld: j.State.Active(), CancelRequested: j.CancelRequested, Cancellable: !j.State.Terminal(),
		CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt, DispatchedAt: j.DispatchedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
		Progress: JobProgress{Step: j.Progress.Step, Message: j.Progress.Message},
	}
	if j.Progress.Percent >= 0 {
		p := j.Progress.Percent
		out.Progress.Percent = &p
	}
	for _, t := range j.Targets {
		out.Targets = append(out.Targets, JobTarget{Type: string(t.Type), ID: t.ID, EnvironmentID: t.EnvironmentID})
	}
	for _, it := range j.Items {
		out.Items = append(out.Items, JobItem(it))
	}
	for _, l := range j.Locks {
		out.Locks = append(out.Locks, JobLock{Scope: string(l.Scope), EnvironmentID: l.EnvironmentID, Name: l.Name, Mode: string(l.Mode)})
	}
	if j.State.Terminal() && j.State != domain.JobSucceeded {
		out.Error = &JobError{Class: j.ErrorClass, Message: j.ErrorMessage, Recovery: j.Recovery}
	}
	if j.State == domain.JobBlocked && j.BlockedReason != "" {
		out.BlockedBy = &JobBlockedBy{Reason: j.BlockedReason, JobID: j.BlockedBy}
	}
	return out
}

// JobAccepted is the 202 response of operations that start a job: the
// job and its URL in Location.
type JobAccepted struct {
	Location string `header:"Location" doc:"URL of the job."`
	Body     Job
}

// Accepted builds a JobAccepted response. Register the operation with
// DefaultStatus: http.StatusAccepted.
func Accepted(j domain.Job) *JobAccepted {
	return &JobAccepted{Location: BasePath + "/jobs/" + j.ID, Body: NewJob(j)}
}

// JobErrorFor maps job engine errors to API errors with stable codes.
// Feature operations that enqueue jobs return JobErrorFor(err).
func JobErrorFor(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrJobNotFound):
		return NotFound("job not found")
	case errors.Is(err, domain.ErrJobFinished):
		return Conflict(CodeJobFinished, "the job has already finished")
	case errors.Is(err, domain.ErrJobIdempotencyConflict):
		return Conflict(CodeIdempotencyKeyReused, "the Idempotency-Key was already used for a different request")
	case errors.Is(err, domain.ErrJobForbidden):
		return Forbidden("not permitted to run this job")
	case errors.Is(err, domain.ErrJobInvalid), errors.Is(err, domain.ErrJobUnknownKind):
		return Invalid(err.Error())
	case errors.Is(err, domain.ErrJobKindUnavailable):
		return NewError(http.StatusNotImplemented, CodeJobKindUnavailable, "this manager cannot run this kind of job yet")
	case errors.Is(err, domain.ErrEnvironmentArchived):
		return Conflict(CodeEnvironmentArchived, "the environment is archived; re-attach it to operate it")
	case errors.Is(err, domain.ErrRestoreInProgress):
		return Conflict(CodeRestoreInProgress,
			"a restore is running on this data: it starts the containers that were running when it ends; wait for it")
	}
	return Internal(err)
}

// JobEvent is one entry of a job's event stream.
type JobEvent struct {
	Seq     int64     `json:"seq" doc:"Monotonic per job; also the SSE event id."`
	At      time.Time `json:"at"`
	Type    string    `json:"type" enum:"state,progress,item,log,warning"`
	State   string    `json:"state,omitempty"`
	Message string    `json:"message,omitempty"`
	Percent *int      `json:"percent,omitempty" minimum:"0" maximum:"100"`
	Step    string    `json:"step,omitempty"`
	Item    *JobItem  `json:"item,omitempty"`
}

func newJobEvent(e domain.JobEvent) JobEvent {
	out := JobEvent{Seq: e.Seq, At: e.At, Type: e.Type, State: string(e.State), Message: e.Message, Step: e.Step}
	if e.Type == domain.JobEventProgress && e.Percent >= 0 {
		p := e.Percent
		out.Percent = &p
	}
	if e.Item != nil {
		it := JobItem(*e.Item)
		out.Item = &it
	}
	return out
}

type listJobsInput struct {
	PageParams
	State         []string `query:"state" enum:"queued,blocked,dispatched,running,cancelling,succeeded,failed,partial,cancelled,interrupted" doc:"Only jobs in these states."`
	Kind          string   `query:"kind" maxLength:"64" doc:"Only jobs of this kind."`
	Origin        []string `query:"origin,explode" enum:"manual,scheduled,api_token" doc:"Only jobs with these origins (repeat the parameter)."`
	EnvironmentID string   `query:"environmentId" maxLength:"128" doc:"Only jobs in (or targeting) this environment."`
	Target        string   `query:"target" maxLength:"1100" doc:"Only jobs with this target, as type:id (e.g. stack:0190a6e0-...)."`
}

type jobIDInput struct {
	JobID string `path:"jobId" maxLength:"64" doc:"Job ID."`
}

type jobOutput struct{ Body Job }

type listJobsOutput struct{ Body Page[Job] }

type jobsCursor struct {
	Before string `json:"b"`
}

type streamJobEventsInput struct {
	JobID       string `path:"jobId" maxLength:"64" doc:"Job ID."`
	LastEventID string `header:"Last-Event-ID" maxLength:"20" doc:"Resume after this event sequence number (sent automatically by EventSource on reconnect)."`
}

type jobsAPI struct {
	svc       JobService
	authz     authz.Authorizer
	deps      Deps
	heartbeat time.Duration
}

func (h *jobsAPI) principal(ctx context.Context) (authz.Principal, error) {
	p, ok := authz.PrincipalFrom(ctx)
	if !ok {
		return p, Unauthenticated("authentication required")
	}
	if h.svc == nil {
		return p, Unavailable(CodeUnavailable, "the job engine is not available")
	}
	return p, nil
}

func (h *jobsAPI) can(ctx context.Context, p authz.Principal, c Capability, j domain.Job) bool {
	return h.authz.Can(ctx, p, string(c), authz.JobResource(j)).Allowed
}

// visibleJob loads a job the caller may read (404 otherwise, so existence
// does not leak).
func (h *jobsAPI) visibleJob(ctx context.Context, id string) (authz.Principal, domain.Job, error) {
	p, err := h.principal(ctx)
	if err != nil {
		return p, domain.Job{}, err
	}
	j, err := h.svc.Get(ctx, id)
	if err != nil {
		return p, domain.Job{}, JobErrorFor(err)
	}
	if !h.can(ctx, p, CapJobRead, j) {
		return p, domain.Job{}, NotFound("job not found")
	}
	return p, j, nil
}

func parseTarget(s string) (*domain.JobTarget, error) {
	typ, id, ok := strings.Cut(s, ":")
	if !ok || typ == "" || id == "" {
		return nil, Invalid("invalid target filter", Field("query.target", "want type:id"))
	}
	for _, t := range domain.TargetTypes() {
		if string(t) == typ {
			return &domain.JobTarget{Type: t, ID: id}, nil
		}
	}
	return nil, Invalid("invalid target filter", Field("query.target", "unknown target type "+typ))
}

func (h *jobsAPI) list(ctx context.Context, in *listJobsInput) (*listJobsOutput, error) {
	p, err := h.principal(ctx)
	if err != nil {
		return nil, err
	}
	f := domain.JobFilter{EnvironmentID: in.EnvironmentID}
	for _, s := range in.State {
		f.States = append(f.States, domain.JobState(s))
	}
	if in.Kind != "" {
		f.Kinds = []domain.JobKind{domain.JobKind(in.Kind)}
	}
	for _, o := range in.Origin {
		f.Origins = append(f.Origins, domain.JobOrigin(o))
	}
	if in.Target != "" {
		if f.Target, err = parseTarget(in.Target); err != nil {
			return nil, err
		}
	}
	fingerprint := QueryFingerprint(strings.Join(in.State, ","), in.Kind, in.EnvironmentID, in.Target, strings.Join(in.Origin, ","))
	var after jobsCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fingerprint, &after); err != nil {
			return nil, err
		}
	}
	c := authz.For(ctx, h.authz, p) // one rule load for the whole page (#17)
	jobs, next, err := ScanPage(ctx, Scan[domain.Job]{
		Limit: in.PageLimit(), After: after.Before,
		Fetch: func(ctx context.Context, before string, n int) ([]domain.Job, error) {
			f.BeforeID, f.Limit = before, n // newest first: "after" in list order is an older ID
			return h.svc.List(ctx, f)
		},
		Position: func(j domain.Job) string { return j.ID },
		Visible:  func(j domain.Job) bool { return c.Can(string(CapJobRead), authz.JobResource(j)).Allowed }, // per-item filtering (#17)
	})
	if err != nil {
		return nil, Internal(err)
	}
	items := make([]Job, 0, len(jobs))
	for _, j := range jobs {
		items = append(items, NewJob(j))
	}
	cursor := ""
	if next != "" {
		if cursor, err = CursorFor(fingerprint, jobsCursor{Before: next}); err != nil {
			return nil, Internal(err)
		}
	}
	return &listJobsOutput{Body: NewPage(items, cursor, nil)}, nil
}

func (h *jobsAPI) get(ctx context.Context, in *jobIDInput) (*jobOutput, error) {
	_, j, err := h.visibleJob(ctx, in.JobID)
	if err != nil {
		return nil, err
	}
	return &jobOutput{Body: NewJob(j)}, nil
}

func (h *jobsAPI) cancel(ctx context.Context, in *jobIDInput) (*JobAccepted, error) {
	p, j, err := h.visibleJob(ctx, in.JobID)
	if err != nil {
		return nil, err
	}
	if !h.can(ctx, p, CapJobCancel, j) {
		return nil, Forbidden("not permitted to cancel this job")
	}
	j, err = h.svc.Cancel(ctx, j.ID)
	if err != nil {
		return nil, JobErrorFor(err)
	}
	logging.FromContext(ctx).Info("job cancellation requested", "job_id", j.ID, "kind", j.Kind, "principal", p.Key())
	return Accepted(j), nil
}

// sseBatch bounds the events read per database query while streaming.
const sseBatch = 100

func (h *jobsAPI) stream(ctx context.Context, in *streamJobEventsInput) (*huma.StreamResponse, error) {
	p, j, err := h.visibleJob(ctx, in.JobID)
	if err != nil {
		return nil, err
	}
	var after int64
	if in.LastEventID != "" {
		n, err := strconv.ParseInt(in.LastEventID, 10, 64)
		if err != nil || n < 0 {
			return nil, Invalid("invalid Last-Event-ID", Field("header.Last-Event-ID", "must be a non-negative event sequence number"))
		}
		after = n
	}
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		h.runStream(hctx, p, j, after)
	}}, nil
}

// runStream writes the SSE stream: a "job" snapshot (no id), then every
// event after the Last-Event-ID in order (id = seq), live events as they
// happen, ": heartbeat" comments, and closes after the terminal state's
// events were sent.
func (h *jobsAPI) runStream(hctx huma.Context, p authz.Principal, j domain.Job, after int64) {
	ctx := hctx.Context()
	stream := StartSSE(hctx)
	defer stream.CloseIfRevoked(ctx)
	changed, unsubscribe := h.svc.Subscribe(j.ID)
	defer unsubscribe()
	clk := h.deps.clock()
	hb := clk.NewTicker(h.heartbeat)
	defer hb.Stop()

	if stream.Event("job", "", NewJob(j)) != nil {
		return
	}
	for {
		events, err := h.svc.Events(ctx, j.ID, after, sseBatch)
		if err != nil {
			return
		}
		for _, e := range events {
			if stream.Event(e.Type, strconv.FormatInt(e.Seq, 10), newJobEvent(e)) != nil {
				return
			}
			after = e.Seq
		}
		if len(events) == sseBatch {
			continue
		}
		cur, err := h.svc.Get(ctx, j.ID)
		if err != nil {
			return // deleted by retention
		}
		if !h.can(ctx, p, CapJobRead, cur) {
			_ = stream.Event("close", "", CloseEvent{Reason: "permissions_changed"})
			return
		}
		if cur.State.Terminal() && after >= cur.LastEventSeq {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-hb.C():
			if stream.Heartbeat() != nil {
				return
			}
		}
	}
}

func registerJobs(a huma.API, deps Deps) {
	h := &jobsAPI{svc: deps.Jobs, authz: authz.OrDenyAll(deps.Authorizer), deps: deps, heartbeat: deps.SSEHeartbeat}
	if h.heartbeat <= 0 {
		h.heartbeat = DefaultSSEHeartbeat
	}
	errs := []int{http.StatusUnauthorized, http.StatusNotFound}

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-jobs", Method: http.MethodGet, Path: BasePath + "/jobs",
			Summary: "List jobs",
			Description: "Jobs visible to the caller, newest first. Visibility is decided per job by job.read on every target of the " +
				"job, or by holding the job kind's own capability on every target (a restart-only user sees restarts of that container) " +
				"- never by who created it - so pages may hold fewer than limit items; follow nextCursor until it is absent.",
			Tags: []string{tagJobs}, Errors: []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
		},
		Capability: CapJobRead, Scope: ScopeResource,
	}, h.list)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-job", Method: http.MethodGet, Path: BasePath + "/jobs/{jobId}",
			Summary: "Get a job", Tags: []string{tagJobs}, Errors: errs,
		},
		Capability: CapJobRead, Scope: ScopeResource,
	}, h.get)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-job-cancellation", Method: http.MethodPost, Path: BasePath + "/jobs/{jobId}/cancellations",
			Summary: "Cancel a job",
			Description: "Queued and blocked jobs are cancelled immediately. Running jobs move to cancelling and stop at the next " +
				"cancellation safe point of their kind; compensating steps (such as restarting containers stopped for a backup) always run. " +
				"A job that finishes before reaching a safe point keeps its outcome. Repeating the request is harmless. " +
				"Answers 202 with the job and its URL in Location. 409 job_finished when the job already finished.",
			Tags:   []string{tagJobs},
			Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
		},
		Capability: CapJobCancel, Scope: ScopeResource,
	}, h.cancel)

	eventSchema := a.OpenAPI().Components.Schemas.Schema(reflect.TypeOf(JobEvent{}), true, "")
	jobSchema := a.OpenAPI().Components.Schemas.Schema(reflect.TypeOf(Job{}), true, "")
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "stream-job-events", Method: http.MethodGet, Path: BasePath + "/jobs/{jobId}/events/stream",
			Summary: "Stream job events (SSE)",
			Description: "Server-sent events. The first message is `event: job` with the current Job (no id). Then every retained event " +
				"after Last-Event-ID in order, as `id: <seq>`, `event: <type>` (state, progress, item, log, warning), `data: <JobEvent>`. " +
				"Comments `: heartbeat` keep the connection alive. The stream closes after the job's terminal events. " +
				"The event log per job is bounded; replay covers only retained events.",
			Tags: []string{tagJobs}, Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
			Responses: map[string]*huma.Response{
				"200": {
					Description: "Event stream",
					Content: map[string]*huma.MediaType{
						"text/event-stream": {Schema: &huma.Schema{
							Description: "Each data line is a JobEvent (or a Job for the initial event: job).",
							OneOf:       []*huma.Schema{eventSchema, jobSchema},
						}},
					},
				},
			},
		},
		Capability: CapJobRead, Scope: ScopeResource,
	}, h.stream)
}
