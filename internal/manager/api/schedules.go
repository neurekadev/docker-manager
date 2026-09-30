package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/cron"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/scheduler"
)

// The shared cron scheduler (#13): editable schedule defaults, previews
// and the cross-policy schedule view. Policy CRUD (#10, #14, #20) owns each
// policy's cron, time zone and enabled fields; job history is /jobs.

const tagSchedules = "Schedules"

// Capabilities of the schedule routes.
const (
	CapScheduleRead   Capability = "schedule.read"
	CapSettingsRead   Capability = "settings.read"
	CapSettingsManage Capability = "settings.manage"
)

// ScheduleService is the scheduler as seen by the API (implemented by
// *scheduler.Service).
type ScheduleService interface {
	Defaults(ctx context.Context) (domain.ScheduleDefaults, error)
	UpdateDefaults(ctx context.Context, revision int64, p domain.ScheduleDefaultsPatch) (before, after domain.ScheduleDefaults, err error)
	Preview(ctx context.Context, req scheduler.PreviewRequest) (scheduler.Preview, error)
	List(ctx context.Context, f domain.ScheduleFilter) ([]domain.Schedule, error)
	Runs(ctx context.Context, scheduleID string, limit int) ([]domain.ScheduleRun, error)
	Kind(key string) (scheduler.Kind, bool)
	NextRun(sc domain.Schedule) (scheduler.Run, bool)
}

// ScheduleKindDefault is the default expression of one schedule kind.
type ScheduleKindDefault struct {
	Kind      string `json:"kind" example:"backup" doc:"Schedule kind (backup, update_check, update_run, prune, backup_verification, ...)."`
	Label     string `json:"label" example:"Backups"`
	Cron      string `json:"cron" example:"0 2 * * *" doc:"Default five-field expression new policies of this kind start with."`
	Suggested string `json:"suggested" example:"0 2 * * *" doc:"Docker Manager's shipped suggestion."`
	CatchUp   string `json:"catchUp" enum:"once,skip" doc:"Missed runs after manager downtime: once = one catch-up run, skip = recorded, not run."`
}

// ScheduleDefaults are the instance's schedule defaults.
type ScheduleDefaults struct {
	TimeZone  string                `json:"timeZone" example:"Europe/Berlin" doc:"Default IANA time zone of new policies."`
	Kinds     []ScheduleKindDefault `json:"kinds"`
	Revision  int64                 `json:"revision"`
	UpdatedAt time.Time             `json:"updatedAt"`
}

func newScheduleDefaults(d domain.ScheduleDefaults) ScheduleDefaults {
	out := ScheduleDefaults{TimeZone: d.TimeZone, Revision: d.Revision, UpdatedAt: d.UpdatedAt, Kinds: []ScheduleKindDefault{}}
	for _, k := range d.Kinds {
		out.Kinds = append(out.Kinds, ScheduleKindDefault{Kind: k.Kind, Label: k.Label, Cron: k.Cron, Suggested: k.Suggested, CatchUp: string(k.CatchUp)})
	}
	return out
}

type scheduleDefaultsOutput struct {
	ETagHeader
	Body ScheduleDefaults
}

type patchScheduleDefaultsInput struct {
	IfMatchParam
	Body struct {
		TimeZone *string           `json:"timeZone,omitempty" minLength:"1" maxLength:"64" example:"Europe/Berlin" doc:"New default IANA time zone."`
		Crons    map[string]string `json:"crons,omitempty" maxProperties:"32" doc:"New default expressions by kind (e.g. {\"backup\": \"0 1 * * *\"}); send the suggested value to go back to it."`
	}
}

// ScheduleRunTime is one run time with its local wall clock and DST note.
type ScheduleRunTime struct {
	At      time.Time `json:"at" doc:"Instant of the run, with the time zone's offset."`
	UTC     time.Time `json:"utc"`
	Local   string    `json:"local" example:"2026-03-08T02:30" doc:"The local wall-clock time the expression selected (differs from at's clock only in a DST gap)."`
	DST     string    `json:"dst" enum:"none,gap,repeated" doc:"gap: the local time does not exist (runs at the first instant after the gap); repeated: it occurs twice (runs once, at the first occurrence)."`
	DSTNote string    `json:"dstNote,omitempty"`
}

func newRunTime(r scheduler.Run) ScheduleRunTime {
	dst := string(r.DST)
	if dst == "" {
		dst = "none"
	}
	return ScheduleRunTime{At: r.At, UTC: r.At.UTC(), Local: r.Nominal, DST: dst, DSTNote: r.DSTNotes}
}

type schedulePreviewInput struct {
	Body struct {
		Cron     string     `json:"cron" minLength:"1" maxLength:"256" example:"0 2 * * *" doc:"Five-field cron expression (minute hour day-of-month month day-of-week)."`
		TimeZone string     `json:"timeZone,omitempty" maxLength:"64" example:"Europe/Berlin" doc:"IANA time zone; default: the instance's default time zone."`
		Kind     string     `json:"kind,omitempty" maxLength:"32" example:"prune" doc:"Schedule kind, to explain its missed-run behavior."`
		From     *time.Time `json:"from,omitempty" doc:"Evaluate runs after this instant (default: now)."`
		Count    int        `json:"count,omitempty" minimum:"1" maximum:"50" default:"5" doc:"Number of runs."`
	}
}

// SchedulePreview is the evaluation of an expression; nothing is saved or run.
type SchedulePreview struct {
	Cron     string            `json:"cron" doc:"The normalized expression."`
	TimeZone string            `json:"timeZone"`
	Kind     string            `json:"kind,omitempty"`
	CatchUp  string            `json:"catchUp,omitempty" enum:"once,skip"`
	From     time.Time         `json:"from"`
	Runs     []ScheduleRunTime `json:"runs"`
	Notes    []string          `json:"notes" doc:"How DST transitions, missed runs, manager restarts, overlaps and offline environments affect this schedule."`
}

type schedulePreviewOutput struct{ Body SchedulePreview }

// ScheduleRunJob is a job a scheduled run enqueued.
type ScheduleRunJob struct {
	JobID         string `json:"jobId"`
	Kind          string `json:"kind"`
	State         string `json:"state"`
	ErrorClass    string `json:"errorClass,omitempty"`
	BlockedReason string `json:"blockedReason,omitempty"`
}

// ScheduleRun is one processed scheduled instant.
type ScheduleRun struct {
	ScheduledFor time.Time        `json:"scheduledFor"`
	Outcome      string           `json:"outcome" enum:"pending,enqueued,missed,skipped,rejected,failed"`
	Result       string           `json:"result" doc:"The outcome for runs without jobs; for enqueued runs active while a job is not finished, otherwise the (worst) job state."`
	CatchUp      bool             `json:"catchUp,omitempty" doc:"A late run replacing runs missed while the manager was not running."`
	MissedCount  int              `json:"missedCount,omitempty"`
	MissedFrom   *time.Time       `json:"missedFrom,omitempty"`
	Reason       string           `json:"reason,omitempty"`
	ErrorClass   string           `json:"errorClass,omitempty" doc:"missed, previous_run_active, nothing_to_run, policy_not_found, policy_disabled, target_not_found, job_kind_unavailable, invalid_job, internal, ..."`
	Jobs         []ScheduleRunJob `json:"jobs"`
}

func newScheduleRun(r domain.ScheduleRun) ScheduleRun {
	out := ScheduleRun{ScheduledFor: r.ScheduledFor, Outcome: string(r.Outcome), Result: r.Result(), CatchUp: r.CatchUp,
		MissedCount: r.MissedCount, MissedFrom: r.MissedFrom, Reason: r.Reason, ErrorClass: r.ErrorClass, Jobs: []ScheduleRunJob{}}
	for _, j := range r.Jobs {
		out.Jobs = append(out.Jobs, ScheduleRunJob{JobID: j.JobID, Kind: string(j.Kind), State: string(j.State), ErrorClass: j.ErrorClass,
			BlockedReason: j.BlockedReason})
	}
	return out
}

// Schedule is one policy's schedule in the cross-policy view.
type Schedule struct {
	ID            string           `json:"id"`
	Kind          string           `json:"kind" example:"prune"`
	KindLabel     string           `json:"kindLabel" example:"Docker prune"`
	PolicyID      string           `json:"policyId"`
	PolicyName    string           `json:"policyName"`
	EnvironmentID string           `json:"environmentId,omitempty"`
	Cron          string           `json:"cron" example:"0 3 * * 0"`
	TimeZone      string           `json:"timeZone" example:"Europe/Berlin"`
	Enabled       bool             `json:"enabled"`
	CatchUp       string           `json:"catchUp" enum:"once,skip"`
	InvalidReason string           `json:"invalidReason,omitempty" doc:"Why the saved expression or zone cannot run (the schedule never runs until the policy is fixed)."`
	NextRun       *ScheduleRunTime `json:"nextRun,omitempty"`
	RecentRuns    []ScheduleRun    `json:"recentRuns" doc:"Newest first (at most 10): enqueued, missed, skipped, rejected and failed runs."`
	UpdatedAt     time.Time        `json:"updatedAt"`
}

type listSchedulesInput struct {
	PageParams
	Kind          string `query:"kind" maxLength:"32" doc:"Only this schedule kind."`
	EnvironmentID string `query:"environmentId" maxLength:"64"`
	Enabled       string `query:"enabled" enum:"true,false" doc:"Only enabled (true) or disabled (false) schedules."`
}

type listSchedulesOutput struct{ Body Page[Schedule] }

type schedulesAPI struct {
	svc   ScheduleService
	authz authz.Authorizer
}

func (h *schedulesAPI) service() (ScheduleService, error) {
	if h.svc == nil {
		return nil, Unavailable(CodeUnavailable, "the scheduler is not available")
	}
	return h.svc, nil
}

func (h *schedulesAPI) instanceCheck(ctx context.Context, capability Capability) (ScheduleService, error) {
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	if !c.Can(string(capability), authz.Instance()).Allowed {
		return nil, Forbidden("requires " + string(capability))
	}
	return h.service()
}

func registerSchedules(a huma.API, deps Deps) {
	h := &schedulesAPI{svc: deps.Schedules, authz: deps.Authorizer}

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-schedule-defaults", Method: http.MethodGet, Path: BasePath + "/schedule-defaults",
			Summary:     "Get the schedule defaults",
			Description: "The default IANA time zone and, per schedule kind, the default cron expression new policies start with (and Docker Manager's suggestion). Defaults only prefill new policies; existing policies keep their saved expression and zone.",
			Tags:        []string{tagSchedules}, Errors: []int{http.StatusForbidden},
		},
		Capability: CapSettingsRead, Scope: ScopeInstance,
	}, func(ctx context.Context, _ *struct{}) (*scheduleDefaultsOutput, error) {
		svc, err := h.instanceCheck(ctx, CapSettingsRead)
		if err != nil {
			return nil, err
		}
		d, err := svc.Defaults(ctx)
		if err != nil {
			return nil, Internal(err)
		}
		return &scheduleDefaultsOutput{ETagHeader: ETagHeader{ETag: RevisionETag(d.Revision)}, Body: newScheduleDefaults(d)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-schedule-defaults", Method: http.MethodPatch, Path: BasePath + "/schedule-defaults",
			Summary: "Change the schedule defaults",
			Description: "Changes the default time zone and/or default expressions by kind. Requires If-Match. Affects only policies created afterwards; " +
				"existing policies keep their saved schedule.",
			Tags: []string{tagSchedules},
			Errors: []int{http.StatusForbidden, http.StatusPreconditionFailed, http.StatusPreconditionRequired,
				http.StatusUnprocessableEntity},
		},
		Capability: CapSettingsManage, Scope: ScopeInstance,
	}, func(ctx context.Context, in *patchScheduleDefaultsInput) (*scheduleDefaultsOutput, error) {
		svc, err := h.instanceCheck(ctx, CapSettingsManage)
		if err != nil {
			return nil, err
		}
		cur, err := svc.Defaults(ctx)
		if err != nil {
			return nil, Internal(err)
		}
		if err := in.CheckIfMatch(RevisionETag(cur.Revision)); err != nil {
			return nil, err
		}
		before, after, err := svc.UpdateDefaults(ctx, cur.Revision, domain.ScheduleDefaultsPatch{TimeZone: in.Body.TimeZone, Crons: in.Body.Crons})
		if errors.Is(err, domain.ErrRevisionConflict) {
			latest, gerr := svc.Defaults(ctx)
			if gerr != nil {
				return nil, Internal(gerr)
			}
			return nil, CheckIfMatch(RevisionETag(cur.Revision), RevisionETag(latest.Revision), true)
		}
		if err != nil {
			return nil, scheduleError(err)
		}
		auditDefaultsDiff(ctx, before, after)
		return &scheduleDefaultsOutput{ETagHeader: ETagHeader{ETag: RevisionETag(after.Revision)}, Body: newScheduleDefaults(after)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-schedule-preview", Method: http.MethodPost, Path: BasePath + "/schedules/previews",
			Summary: "Preview a schedule",
			Description: "Validates a five-field cron expression and IANA time zone and returns the next runs with their local times and " +
				"DST annotations, plus how missed runs, restarts, overlaps and offline environments behave. Nothing is saved or run. " +
				"Invalid input is a 422 whose details name body.cron (or the failing field of the expression) and body.timeZone.",
			Tags: []string{tagSchedules}, Errors: []int{http.StatusUnprocessableEntity},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, in *schedulePreviewInput) (*schedulePreviewOutput, error) {
		if _, _, err := CheckerFor(ctx, h.authz); err != nil {
			return nil, err
		}
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		req := scheduler.PreviewRequest{Cron: in.Body.Cron, TimeZone: in.Body.TimeZone, Kind: in.Body.Kind, Count: in.Body.Count}
		if in.Body.From != nil {
			req.From = *in.Body.From
		}
		p, err := svc.Preview(ctx, req)
		if err != nil {
			return nil, scheduleError(err)
		}
		out := SchedulePreview{Cron: p.Cron, TimeZone: p.TimeZone, From: p.From, Notes: p.Notes, Runs: []ScheduleRunTime{}}
		if p.Kind != nil {
			out.Kind, out.CatchUp = p.Kind.Key, string(p.Kind.CatchUp)
		}
		for _, r := range p.Runs {
			out.Runs = append(out.Runs, newRunTime(r))
		}
		return &schedulePreviewOutput{Body: out}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-schedules", Method: http.MethodGet, Path: BasePath + "/schedules",
			Summary: "List schedules",
			Description: "Cross-policy view of every scheduled policy (backups, verification, update checks and runs, prune): expression, zone, " +
				"enabled state, next run and recent run history (missed, skipped, rejected and enqueued runs with their jobs' states). " +
				"An entry is listed only when the caller holds schedule.read and the policy's own read capability (e.g. " +
				"maintenance_policy.read) on it. Ordered by ID; no total.",
			Tags: []string{tagSchedules},
		},
		Capability: CapScheduleRead, Scope: ScopeResource,
	}, func(ctx context.Context, in *listSchedulesInput) (*listSchedulesOutput, error) {
		c, _, err := CheckerFor(ctx, h.authz)
		if err != nil {
			return nil, err
		}
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		f := domain.ScheduleFilter{Kind: in.Kind, EnvironmentID: in.EnvironmentID}
		if in.Enabled != "" {
			on := in.Enabled == "true"
			f.Enabled = &on
		}
		fingerprint := QueryFingerprint(in.Kind, in.EnvironmentID, in.Enabled)
		var after struct {
			After string `json:"a"`
		}
		if in.Cursor != "" {
			if err := DecodeCursorFor(in.Cursor, fingerprint, &after); err != nil {
				return nil, err
			}
		}
		items, next, err := ScanPage(ctx, Scan[domain.Schedule]{
			Limit: in.PageLimit(), After: after.After,
			Fetch: func(ctx context.Context, afterID string, n int) ([]domain.Schedule, error) {
				f.AfterID, f.Limit = afterID, n
				return svc.List(ctx, f)
			},
			Position: func(s domain.Schedule) string { return s.ID },
			Visible:  func(s domain.Schedule) bool { return scheduleVisible(c, svc, s) },
		})
		if err != nil {
			return nil, Internal(err)
		}
		out := make([]Schedule, 0, len(items))
		for _, sc := range items {
			runs, err := svc.Runs(ctx, sc.ID, 10)
			if err != nil {
				return nil, Internal(err)
			}
			out = append(out, newSchedule(svc, sc, runs))
		}
		cursor := ""
		if next != "" {
			if cursor, err = CursorFor(fingerprint, struct {
				After string `json:"a"`
			}{next}); err != nil {
				return nil, Internal(err)
			}
		}
		return &listSchedulesOutput{Body: NewPage(out, cursor, nil)}, nil
	})
}

// scheduleVisible: schedule.read where the policy lives and the policy's
// own read capability on it (#17).
func scheduleVisible(c authz.Checker, svc ScheduleService, sc domain.Schedule) bool {
	k, ok := svc.Kind(sc.Kind)
	if !ok {
		return false
	}
	where := authz.Instance()
	if sc.EnvironmentID != "" {
		where = authz.EnvironmentResource(sc.EnvironmentID)
	}
	if !c.Can(string(CapScheduleRead), where).Allowed {
		return false
	}
	policy := authz.Resource{Type: k.PolicyType, ID: sc.PolicyID, EnvironmentID: sc.EnvironmentID, Parents: []authz.ResourceRef{}}
	return c.Can(k.ReadCapability, policy).Allowed
}

func newSchedule(svc ScheduleService, sc domain.Schedule, runs []domain.ScheduleRun) Schedule {
	out := Schedule{ID: sc.ID, Kind: sc.Kind, PolicyID: sc.PolicyID, PolicyName: sc.Name, EnvironmentID: sc.EnvironmentID,
		Cron: sc.Cron, TimeZone: sc.TimeZone, Enabled: sc.Enabled, InvalidReason: sc.InvalidReason, UpdatedAt: sc.UpdatedAt,
		RecentRuns: []ScheduleRun{}}
	if k, ok := svc.Kind(sc.Kind); ok {
		out.KindLabel, out.CatchUp = k.Label, string(k.CatchUp)
	}
	if r, ok := svc.NextRun(sc); ok {
		rt := newRunTime(r)
		out.NextRun = &rt
	}
	for _, r := range runs {
		out.RecentRuns = append(out.RecentRuns, newScheduleRun(r))
	}
	return out
}

// scheduleError maps scheduler validation errors to 422 details.
func scheduleError(err error) error {
	var ie *scheduler.InvalidError
	var de *scheduler.DefaultsError
	switch {
	case errors.As(err, &ie):
		details := make([]ErrorDetail, 0, len(ie.Problems))
		for _, p := range ie.Problems {
			details = append(details, cronDetail("body.cron", "body.timeZone", p))
		}
		return Invalid("invalid schedule", details...)
	case errors.As(err, &de):
		var details []ErrorDetail
		for _, p := range de.Problems {
			if p.Kind == "" {
				details = append(details, cronDetail("", "body.timeZone", p.Problem))
			} else {
				details = append(details, cronDetail("body.crons."+p.Kind, "", p.Problem))
			}
		}
		for _, k := range de.UnknownKinds {
			details = append(details, Field("body.crons."+k, "unknown schedule kind"))
		}
		return Invalid("invalid schedule defaults", details...)
	case errors.Is(err, domain.ErrScheduleKindUnknown):
		return Invalid("unknown schedule kind", Field("body.kind", "unknown schedule kind"))
	}
	return Internal(err)
}

func cronDetail(cronField, tzField string, p *cron.ParseError) ErrorDetail {
	if p.Field == cron.FieldTimeZone {
		return Field(tzField, p.Message)
	}
	return Field(cronField, p.Error())
}

// ValidateSchedule checks a policy's cron expression and time zone for
// policy create/update handlers (#10, #14, #20); fieldPrefix is the
// request's field path of the schedule object (e.g. "body.schedule"), and
// the returned 422 names <prefix>.cron / <prefix>.timeZone.
func ValidateSchedule(cronExpr, tz, fieldPrefix string) error {
	err := scheduler.ValidateSpec(cronExpr, tz)
	var ie *scheduler.InvalidError
	if !errors.As(err, &ie) {
		return err
	}
	details := make([]ErrorDetail, 0, len(ie.Problems))
	for _, p := range ie.Problems {
		details = append(details, cronDetail(fieldPrefix+".cron", fieldPrefix+".timeZone", p))
	}
	return Invalid("invalid schedule", details...)
}

func auditDefaultsDiff(ctx context.Context, before, after domain.ScheduleDefaults) {
	flat := func(d domain.ScheduleDefaults) map[string]any {
		m := map[string]any{"timeZone": d.TimeZone}
		for _, k := range d.Kinds {
			m["cron."+k.Kind] = k.Cron
		}
		return m
	}
	audit.SetDiff(ctx, flat(before), flat(after))
}
