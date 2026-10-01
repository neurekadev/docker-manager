package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/alerts"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
)

// Alerts (#159): problems the manager raises by itself (disks, RAID, host
// temperature, disk space and memory, environments offline, failed
// scheduled jobs, available updates). An
// alert is shown to whoever may see its source (authz.AlertVisible: the
// environment's system information, the environment, the job, the update
// policy) and dismissed for everyone with alert.dismiss scoped like that
// source. The flows live in internal/manager/alerts.

const tagAlerts = "Alerts"

// maxDismissals bounds one "Dismiss all".
const maxDismissals = 500

// AlertService is the alert service as seen by the API (implemented by
// *alerts.Service).
type AlertService interface {
	List(ctx context.Context, f domain.AlertFilter, beforeID string, limit int) ([]domain.Alert, error)
	Get(ctx context.Context, id string) (domain.Alert, error)
	Dismiss(ctx context.Context, id, userID string) (domain.Alert, error)
	DismissMany(ctx context.Context, ids []string, userID string, strict bool) ([]domain.Alert, error)
	// EnvironmentName names an environment ("" when unknown).
	EnvironmentName(ctx context.Context, id string) string
}

// AlertUser names who dismissed an alert.
type AlertUser struct {
	ID   string `json:"id" example:"0190a6e0-7b1c-7cc3-9d52-4f3a2b1c0d9e"`
	Name string `json:"name,omitempty" example:"Alex"`
}

// Alert is one alert.
type Alert struct {
	ID            string `json:"id" example:"0192f0c4-1a2b-7c3d-8e4f-5a6b7c8d9e0f"`
	Kind          string `json:"kind" enum:"disk_health,raid,temperature,disk_space,memory,environment_offline,updates,job_failed" example:"disk_health"`
	Severity      string `json:"severity" enum:"info,warning,critical" example:"critical"`
	State         string `json:"state" enum:"firing,resolved" example:"firing"`
	EnvironmentID string `json:"environmentId,omitempty" doc:"The environment the problem is in (absent for manager jobs)."`
	ResourceType  string `json:"resourceType" enum:"disk,raid_array,zfs_pool,environment,filesystem,job,update_policy" example:"disk" doc:"What the alert is about."`
	ResourceID    string `json:"resourceId" example:"/dev/sda" doc:"The disk's path, the array's or pool's name, the environment's ID, the filesystem (docker, stacks, bind-1, ...), the last failed job's ID or the update policy's ID."`
	Title         string `json:"title" example:"Disk /dev/sda on homelab is failing"`
	Detail        string `json:"detail,omitempty" example:"SMART self-assessment failed, 8 reallocated sectors. Model WDC WD40EFZX." doc:"One or two sentences about the problem (never serial numbers or error texts)."`
	// Facts are small values about the problem, by kind: disks device,
	// deviceType, model, state and counters; arrays array or pool,
	// arrayKind (md, zfs), level, state, health, progress; jobs jobId,
	// jobKind, jobState, origin, errorClass, policyId, target; updates
	// count, services, target; offline since.
	Facts      map[string]string   `json:"facts" doc:"Small, non-secret values about the problem (disks: device, deviceType, model, state and counters; arrays: array or pool, arrayKind md or zfs, level, state, health, progress; temperature: sensor, celsius (the peak), warningAt, criticalAt; disk space: mount, usedPercent (the peak), freeBytes, totalBytes, warningAt, criticalAt; memory: usedPercent, usedBytes, totalBytes, warningAt, criticalAt; jobs: jobId, jobKind, jobState, origin, errorClass, policyId, target, and for update checks failedItems and itemErrorClass; updates: count, services, target; offline: since)."`
	Fields     []NotificationField `json:"fields" doc:"Labelled values, as messages show them."`
	Link       string              `json:"link" example:"/environments/0190a6e0-7b1c-7cc3-9d52-4f3a2b1c0d9e?tab=system" doc:"Path of the page in Docker Manager the alert is about."`
	StartedAt  time.Time           `json:"startedAt" doc:"When it started firing."`
	UpdatedAt  time.Time           `json:"updatedAt"`
	ResolvedAt *time.Time          `json:"resolvedAt,omitempty"`
	Resolution string              `json:"resolution,omitempty" enum:"resolved,removed,expired,archived" doc:"Why it stopped firing: resolved (the problem is gone), removed (the disk, array or policy is gone), expired (a failed job without a new run for 7 days), archived (the environment was archived)."`
	// Dismissed alerts left the bell and "Needs attention" for everyone.
	Dismissed   bool       `json:"dismissed" doc:"Dismissed for everyone; it opens again when it gets worse."`
	DismissedAt *time.Time `json:"dismissedAt,omitempty"`
	DismissedBy *AlertUser `json:"dismissedBy,omitempty"`
	Escalation  int        `json:"escalation" example:"1" doc:"How often the alert got worse (a higher severity or a new problem): it changes exactly when a dismissed alert opens again, so a dismissal kept by a client is keyed by it. Other changes (progress, counters) leave it."`
	Revision    int64      `json:"revision"`
	Actions     []string   `json:"actions" example:"alert.dismiss" doc:"What the caller may do: alert.dismiss while it fires and the caller may dismiss it."`
}

func newAlert(c authz.Checker, a domain.Alert, envName string) Alert {
	facts := a.Facts
	if facts == nil {
		facts = map[string]string{}
	}
	out := Alert{
		ID: a.ID, Kind: string(a.Kind), Severity: string(a.Severity), State: string(a.State), EnvironmentID: a.EnvironmentID,
		ResourceType: a.ResourceType, ResourceID: a.ResourceID, Title: a.Title, Detail: alerts.Detail(a), Facts: facts,
		Fields: []NotificationField{},
		Link:   alerts.Link(a), StartedAt: a.StartedAt, UpdatedAt: a.UpdatedAt, ResolvedAt: a.ResolvedAt, Resolution: a.Resolution,
		Dismissed: a.Dismissed(), DismissedAt: a.DismissedAt, Escalation: a.Escalation, Revision: a.Revision, Actions: []string{},
	}
	if a.DismissedBy != "" {
		out.DismissedBy = &AlertUser{ID: a.DismissedBy, Name: a.DismissedByName}
	}
	for _, f := range alerts.Fields(a, envName) {
		out.Fields = append(out.Fields, NotificationField{Name: f.Name, Value: f.Value, Inline: f.Inline})
	}
	if a.State == domain.AlertFiring && authz.AlertDismissible(c, a) {
		out.Actions = append(out.Actions, authz.CapAlertDismiss)
	}
	return out
}

type alertsAPI struct {
	svc   AlertService
	authz authz.Authorizer
}

func (h *alertsAPI) service() (AlertService, error) {
	if h.svc == nil {
		return nil, Unavailable(CodeUnavailable, "alerts are not available")
	}
	return h.svc, nil
}

func alertError(err error) error {
	switch {
	case errors.Is(err, domain.ErrAlertNotFound):
		return NotFound("alert not found")
	case errors.Is(err, domain.ErrAlertNotFiring):
		return Conflict(CodeAlertNotFiring, "the alert is resolved; only firing alerts can be dismissed")
	}
	return Internal(err)
}

// --- inputs and outputs ---

type listAlertsInput struct {
	PageParams
	State         string `query:"state" enum:"active,dismissed,firing,resolved" doc:"active: firing and not dismissed; dismissed: firing and dismissed; firing: both; resolved: no longer firing. Default: every alert."`
	Kind          string `query:"kind" enum:"disk_health,raid,temperature,disk_space,memory,environment_offline,updates,job_failed" doc:"Only alerts of this kind."`
	EnvironmentID string `query:"environmentId" maxLength:"128" doc:"Only alerts of this environment."`
}

type alertListOutput struct{ Body Page[Alert] }

type alertIDInput struct {
	AlertID string `path:"alertId" maxLength:"64" doc:"Alert ID."`
}

type alertOutput struct{ Body Alert }

type dismissAlertsInput struct {
	Body struct {
		AlertIDs []string `json:"alertIds,omitempty" maxItems:"500" example:"0192f0c4-1a2b-7c3d-8e4f-5a6b7c8d9e0f" doc:"The alerts to dismiss (those shown to the caller). Default: every active alert the caller may dismiss."`
	}
}

// AlertDismissals is the outcome of "Dismiss all".
type AlertDismissals struct {
	Dismissed int      `json:"dismissed" example:"3" doc:"How many alerts are dismissed now (already dismissed ones included)."`
	AlertIDs  []string `json:"alertIds" example:"0192f0c4-1a2b-7c3d-8e4f-5a6b7c8d9e0f"`
}

type alertDismissalsOutput struct{ Body AlertDismissals }

// alertCursor continues a list after an alert (newest first).
type alertCursor struct {
	ID string `json:"i"`
}

// --- handlers ---

func (h *alertsAPI) list(ctx context.Context, in *listAlertsInput) (*alertListOutput, error) {
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	f := domain.AlertFilter{State: in.State, Kind: domain.NotificationEventKind(in.Kind), EnvironmentID: in.EnvironmentID}
	fp := QueryFingerprint("alerts", in.State, in.Kind, in.EnvironmentID)
	var after alertCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fp, &after); err != nil {
			return nil, err
		}
	}
	items, next, err := ScanPage(ctx, Scan[domain.Alert]{
		Limit: in.PageLimit(), After: after.ID,
		Fetch: func(ctx context.Context, before string, n int) ([]domain.Alert, error) {
			return svc.List(ctx, f, before, n)
		},
		Position: func(a domain.Alert) string { return a.ID },
		Visible:  func(a domain.Alert) bool { return authz.AlertVisible(c, a) },
	})
	if err != nil {
		return nil, Internal(err)
	}
	out := make([]Alert, 0, len(items))
	names := map[string]string{}
	for _, a := range items {
		name, ok := names[a.EnvironmentID]
		if !ok {
			name = svc.EnvironmentName(ctx, a.EnvironmentID)
			names[a.EnvironmentID] = name
		}
		out = append(out, newAlert(c, a, name))
	}
	cursor := ""
	if next != "" {
		if cursor, err = CursorFor(fp, alertCursor{ID: next}); err != nil {
			return nil, err
		}
	}
	return &alertListOutput{Body: NewPage(out, cursor, nil)}, nil
}

// visible loads an alert the caller may see (404 otherwise).
func (h *alertsAPI) visible(ctx context.Context, id string) (AlertService, authz.Checker, authz.Principal, domain.Alert, error) {
	svc, err := h.service()
	if err != nil {
		return nil, nil, authz.Principal{}, domain.Alert{}, err
	}
	c, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, nil, p, domain.Alert{}, err
	}
	a, err := svc.Get(ctx, id)
	if err != nil {
		return nil, nil, p, domain.Alert{}, alertError(err)
	}
	if !authz.AlertVisible(c, a) {
		return nil, nil, p, domain.Alert{}, NotFound("alert not found")
	}
	return svc, c, p, a, nil
}

func (h *alertsAPI) get(ctx context.Context, in *alertIDInput) (*alertOutput, error) {
	svc, c, _, a, err := h.visible(ctx, in.AlertID)
	if err != nil {
		return nil, err
	}
	return &alertOutput{Body: newAlert(c, a, svc.EnvironmentName(ctx, a.EnvironmentID))}, nil
}

func (h *alertsAPI) dismiss(ctx context.Context, in *alertIDInput) (*alertOutput, error) {
	svc, c, p, a, err := h.visible(ctx, in.AlertID)
	if err != nil {
		return nil, err
	}
	if !authz.AlertDismissible(c, a) {
		return nil, Forbidden("you may not dismiss this alert")
	}
	audit.SetDetail(ctx, "kind", string(a.Kind))
	audit.SetDetail(ctx, "severity", string(a.Severity))
	if a.EnvironmentID != "" {
		audit.SetDetail(ctx, "environmentId", a.EnvironmentID)
	}
	out, err := svc.Dismiss(ctx, a.ID, p.UserID)
	if err != nil {
		return nil, alertError(err)
	}
	return &alertOutput{Body: newAlert(c, out, svc.EnvironmentName(ctx, out.EnvironmentID))}, nil
}

func (h *alertsAPI) dismissAll(ctx context.Context, in *dismissAlertsInput) (*alertDismissalsOutput, error) {
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	c, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	var candidates []domain.Alert
	if len(in.Body.AlertIDs) > 0 {
		seen := map[string]bool{}
		for _, id := range in.Body.AlertIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			a, err := svc.Get(ctx, id)
			if errors.Is(err, domain.ErrAlertNotFound) {
				continue
			}
			if err != nil {
				return nil, Internal(err)
			}
			candidates = append(candidates, a)
		}
	} else {
		all, err := svc.List(ctx, domain.AlertFilter{State: domain.AlertListActive}, "", 0)
		if err != nil {
			return nil, Internal(err)
		}
		candidates = all
	}
	var ids []string
	for _, a := range candidates {
		if a.State == domain.AlertFiring && authz.AlertDismissible(c, a) && len(ids) < maxDismissals {
			ids = append(ids, a.ID)
		}
	}
	out := AlertDismissals{AlertIDs: []string{}}
	if len(ids) > 0 {
		done, err := svc.DismissMany(ctx, ids, p.UserID, false)
		if err != nil {
			return nil, alertError(err)
		}
		for _, a := range done {
			out.AlertIDs = append(out.AlertIDs, a.ID)
		}
	}
	out.Dismissed = len(out.AlertIDs)
	audit.SetDetail(ctx, "alertCount", out.Dismissed)
	audit.SetDetail(ctx, "alertIds", out.AlertIDs)
	return &alertDismissalsOutput{Body: out}, nil
}

func registerAlerts(a huma.API, deps Deps) {
	h := &alertsAPI{authz: authz.OrDenyAll(deps.Authorizer)}
	if deps.Alerts != nil {
		h.svc = deps.Alerts
	}
	path := BasePath + "/alerts"
	visibility := " Each alert is shown to whoever may see its source: environment.system.read for disks and RAID, " +
		"environment.metrics.read for temperature, disk space and memory, the environment for offline alerts, job.read on the " +
		"job for failed jobs, update_policy.read for updates."

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-alerts", Method: http.MethodGet, Path: path, Summary: "List alerts",
			Description: "Alerts newest first: firing ones (active, or dismissed for everyone) and resolved ones (kept 90 days)." +
				visibility,
			Tags:   []string{tagAlerts},
			Errors: []int{http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, h.list)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-alert", Method: http.MethodGet, Path: path + "/{alertId}", Summary: "Get an alert",
			Description: "One alert (404 when the caller may not see its source)." + visibility,
			Tags:        []string{tagAlerts},
			Errors:      []int{http.StatusNotFound, http.StatusServiceUnavailable},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, h.get)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-alert-dismissal", Method: http.MethodPost, Path: path + "/{alertId}/dismissals",
			Summary: "Dismiss an alert", DefaultStatus: http.StatusOK,
			Description: "Dismisses a firing alert for everyone: it leaves the bell, the dashboard and the environment's notice but " +
				"stays listed (dismissed) until it resolves, and opens again when it gets worse (a higher severity or a new " +
				"problem). Needs alert.dismiss on the alert's source (the environment, every target of the failed job, or the " +
				"update policy). Dismissing a dismissed alert changes nothing; 409 alert_not_firing for a resolved one.",
			Tags:   []string{tagAlerts},
			Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable},
		},
		Capability: authz.CapAlertDismiss, Scope: ScopeResource,
	}, h.dismiss)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-alert-dismissals", Method: http.MethodPost, Path: path + "/dismissals",
			Summary: "Dismiss alerts", DefaultStatus: http.StatusOK,
			Description: "Dismisses several firing alerts for everyone (\"Dismiss all\"): the listed ones, or every active alert, " +
				"of those the caller may see and dismiss (others are left alone). At most 500 at once.",
			Tags:   []string{tagAlerts},
			Errors: []int{http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
		},
		Capability: authz.CapAlertDismiss, Scope: ScopeResource,
	}, h.dismissAll)
}
