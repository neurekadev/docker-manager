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

// Notifications (finished backups and restores, prunes and update runs)
// and the alert thresholds of host usage. A notification is shown to
// whoever may read its job; the thresholds are the owner's (Settings →
// Notifications). The flows live in internal/manager/alerts.

// NotificationEventService is the notification and threshold service as
// seen by the API (implemented by *alerts.Service).
type NotificationEventService interface {
	Notifications(ctx context.Context, f domain.NotificationFilter, beforeID string, limit int) ([]domain.Notification, error)
	Settings(ctx context.Context) (domain.AlertSettings, error)
	UpdateSettings(ctx context.Context, revision int64, next domain.AlertSettings) (domain.AlertSettings, error)
	// EnvironmentName names an environment ("" when unknown).
	EnvironmentName(ctx context.Context, id string) string
}

// Notification is one finished run.
type Notification struct {
	ID            string `json:"id" example:"0192f0c4-1a2b-7c3d-8e4f-5a6b7c8d9e0f"`
	Kind          string `json:"kind" enum:"backup,prune,updates" example:"backup" doc:"backup (a backup or restore), prune or updates (an update run)."`
	Outcome       string `json:"outcome" enum:"success,warning,failure" example:"success" doc:"How it went: success; warning (a backup that saved everything but needs a look); failure (failed, partly failed or interrupted)."`
	EnvironmentID string `json:"environmentId,omitempty" doc:"The environment it ran on (absent for Docker Manager's own backup)."`
	JobID         string `json:"jobId" doc:"The run's job."`
	JobKind       string `json:"jobKind" example:"backup.run"`
	Title         string `json:"title" example:"Backup Nightly succeeded"`
	Detail        string `json:"detail,omitempty" example:"5 of 5 items backed up (12.4 GiB read) in 3 min 12 s." doc:"One or two sentences about it; a failure says what went wrong and what to do (never error texts)."`
	// Fields are the message's labeled values.
	Fields    []NotificationField `json:"fields" doc:"Labeled values, as messages show them (the environment, sizes per kind of object, items, duration, who started it)."`
	Facts     map[string]string   `json:"facts" doc:"Small, non-secret values the fields are built from."`
	Link      string              `json:"link" example:"/jobs/0192f0c4-1a2b-7c3d-8e4f-5a6b7c8d9e0f" doc:"Path of the run's job in Docker Manager."`
	CreatedAt time.Time           `json:"createdAt"`
}

// NotificationField is one labeled value.
type NotificationField struct {
	Name   string `json:"name" example:"Reclaimed"`
	Value  string `json:"value" example:"4.2 GiB"`
	Inline bool   `json:"inline,omitempty" doc:"Short: shown beside others."`
}

func newNotification(n domain.Notification, envName string) Notification {
	facts := n.Facts
	if facts == nil {
		facts = map[string]string{}
	}
	fields := []NotificationField{}
	for _, f := range alerts.NotificationFields(n, envName) {
		fields = append(fields, NotificationField{Name: f.Name, Value: f.Value, Inline: f.Inline})
	}
	return Notification{ID: n.ID, Kind: string(n.Kind), Outcome: string(n.Outcome), EnvironmentID: n.EnvironmentID,
		JobID: n.JobID, JobKind: string(n.JobKind), Title: n.Title, Detail: alerts.NotificationDetail(n), Fields: fields,
		Facts: facts, Link: alerts.NotificationLink(n), CreatedAt: n.CreatedAt}
}

// AlertThresholds are the warning and critical levels of host usage
// alerts; 0 is off.
type AlertThresholds struct {
	TemperatureWarning  int `json:"temperatureWarning" minimum:"0" maximum:"150" example:"80" doc:"°C; 0 is off."`
	TemperatureCritical int `json:"temperatureCritical" minimum:"0" maximum:"150" example:"90" doc:"°C; 0 is off."`
	DiskSpaceWarning    int `json:"diskSpaceWarning" minimum:"0" maximum:"100" example:"85" doc:"Percent of a filesystem used; 0 is off."`
	DiskSpaceCritical   int `json:"diskSpaceCritical" minimum:"0" maximum:"100" example:"95" doc:"Percent of a filesystem used; 0 is off."`
	MemoryWarning       int `json:"memoryWarning" minimum:"0" maximum:"100" example:"90" doc:"Percent of memory used; 0 is off."`
	MemoryCritical      int `json:"memoryCritical" minimum:"0" maximum:"100" example:"95" doc:"Percent of memory used; 0 is off."`
}

// AlertThresholdOverride changes thresholds of one environment; an absent
// level keeps the default.
type AlertThresholdOverride struct {
	EnvironmentID       string `json:"environmentId" minLength:"1" maxLength:"128"`
	TemperatureWarning  *int   `json:"temperatureWarning,omitempty" minimum:"0" maximum:"150"`
	TemperatureCritical *int   `json:"temperatureCritical,omitempty" minimum:"0" maximum:"150"`
	DiskSpaceWarning    *int   `json:"diskSpaceWarning,omitempty" minimum:"0" maximum:"100"`
	DiskSpaceCritical   *int   `json:"diskSpaceCritical,omitempty" minimum:"0" maximum:"100"`
	MemoryWarning       *int   `json:"memoryWarning,omitempty" minimum:"0" maximum:"100"`
	MemoryCritical      *int   `json:"memoryCritical,omitempty" minimum:"0" maximum:"100"`
}

// AlertSettings are the thresholds and the environments' overrides.
type AlertSettings struct {
	Thresholds AlertThresholds          `json:"thresholds"`
	Overrides  []AlertThresholdOverride `json:"overrides" maxItems:"500"`
	Revision   int64                    `json:"revision" doc:"Edit revision (the ETag)."`
	UpdatedAt  time.Time                `json:"updatedAt" readOnly:"true"`
}

func newAlertSettings(s domain.AlertSettings) AlertSettings {
	t := s.Thresholds
	out := AlertSettings{Thresholds: AlertThresholds{TemperatureWarning: t.TemperatureWarning, TemperatureCritical: t.TemperatureCritical,
		DiskSpaceWarning: t.DiskSpaceWarning, DiskSpaceCritical: t.DiskSpaceCritical, MemoryWarning: t.MemoryWarning,
		MemoryCritical: t.MemoryCritical}, Overrides: []AlertThresholdOverride{}, Revision: s.Revision, UpdatedAt: s.UpdatedAt}
	for _, o := range s.Overrides {
		out.Overrides = append(out.Overrides, AlertThresholdOverride{EnvironmentID: o.EnvironmentID,
			TemperatureWarning: o.TemperatureWarning, TemperatureCritical: o.TemperatureCritical,
			DiskSpaceWarning: o.DiskSpaceWarning, DiskSpaceCritical: o.DiskSpaceCritical,
			MemoryWarning: o.MemoryWarning, MemoryCritical: o.MemoryCritical})
	}
	return out
}

type notificationEventsAPI struct {
	svc   NotificationEventService
	authz authz.Authorizer
}

func (h *notificationEventsAPI) service() (NotificationEventService, error) {
	if h.svc == nil {
		return nil, Unavailable(CodeUnavailable, "notifications are not available")
	}
	return h.svc, nil
}

// owner returns the service for the thresholds (the owner's).
func (h *notificationEventsAPI) owner(ctx context.Context) (NotificationEventService, error) {
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	if !c.Can(capNotificationChannelManage, authz.Instance()).Allowed {
		return nil, Forbidden("only the instance owner may change alert thresholds")
	}
	return svc, nil
}

type listNotificationsInput struct {
	PageParams
	Kind          string `query:"kind" enum:"backup,prune,updates" doc:"Only notifications of this kind."`
	Outcome       string `query:"outcome" enum:"success,warning,failure" doc:"Only notifications with this outcome."`
	EnvironmentID string `query:"environmentId" maxLength:"128" doc:"Only notifications of this environment."`
}

type notificationListOutput struct{ Body Page[Notification] }

type alertSettingsOutput struct {
	ETagHeader
	Body AlertSettings
}

type putAlertSettingsInput struct {
	IfMatchParam
	Body struct {
		Thresholds AlertThresholds          `json:"thresholds"`
		Overrides  []AlertThresholdOverride `json:"overrides" maxItems:"500" doc:"Every environment's override (replaces them all; at most one per environment)."`
	}
}

func (h *notificationEventsAPI) list(ctx context.Context, in *listNotificationsInput) (*notificationListOutput, error) {
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	f := domain.NotificationFilter{Kind: domain.NotificationEventKind(in.Kind), Outcome: domain.NotificationOutcome(in.Outcome),
		EnvironmentID: in.EnvironmentID}
	fp := QueryFingerprint("notifications", in.Kind, in.Outcome, in.EnvironmentID)
	var after alertCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fp, &after); err != nil {
			return nil, err
		}
	}
	items, next, err := ScanPage(ctx, Scan[domain.Notification]{
		Limit: in.PageLimit(), After: after.ID,
		Fetch: func(ctx context.Context, before string, n int) ([]domain.Notification, error) {
			return svc.Notifications(ctx, f, before, n)
		},
		Position: func(n domain.Notification) string { return n.ID },
		Visible:  func(n domain.Notification) bool { return authz.NotificationVisible(c, n) },
	})
	if err != nil {
		return nil, Internal(err)
	}
	names := map[string]string{}
	out := make([]Notification, 0, len(items))
	for _, n := range items {
		name, ok := names[n.EnvironmentID]
		if !ok {
			name = svc.EnvironmentName(ctx, n.EnvironmentID)
			names[n.EnvironmentID] = name
		}
		out = append(out, newNotification(n, name))
	}
	cursor := ""
	if next != "" {
		if cursor, err = CursorFor(fp, alertCursor{ID: next}); err != nil {
			return nil, err
		}
	}
	return &notificationListOutput{Body: NewPage(out, cursor, nil)}, nil
}

func (h *notificationEventsAPI) getSettings(ctx context.Context, _ *struct{}) (*alertSettingsOutput, error) {
	svc, err := h.owner(ctx)
	if err != nil {
		return nil, err
	}
	s, err := svc.Settings(ctx)
	if err != nil {
		return nil, Internal(err)
	}
	return &alertSettingsOutput{ETagHeader: ETagHeader{ETag: RevisionETag(s.Revision)}, Body: newAlertSettings(s)}, nil
}

func (h *notificationEventsAPI) putSettings(ctx context.Context, in *putAlertSettingsInput) (*alertSettingsOutput, error) {
	svc, err := h.owner(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := svc.Settings(ctx)
	if err != nil {
		return nil, Internal(err)
	}
	if err := in.CheckIfMatch(RevisionETag(cur.Revision)); err != nil {
		return nil, err
	}
	t := in.Body.Thresholds
	next := domain.AlertSettings{Thresholds: domain.AlertThresholds{TemperatureWarning: t.TemperatureWarning,
		TemperatureCritical: t.TemperatureCritical, DiskSpaceWarning: t.DiskSpaceWarning, DiskSpaceCritical: t.DiskSpaceCritical,
		MemoryWarning: t.MemoryWarning, MemoryCritical: t.MemoryCritical}}
	for _, o := range in.Body.Overrides {
		next.Overrides = append(next.Overrides, domain.AlertThresholdOverride{EnvironmentID: o.EnvironmentID,
			TemperatureWarning: o.TemperatureWarning, TemperatureCritical: o.TemperatureCritical,
			DiskSpaceWarning: o.DiskSpaceWarning, DiskSpaceCritical: o.DiskSpaceCritical,
			MemoryWarning: o.MemoryWarning, MemoryCritical: o.MemoryCritical})
	}
	after, err := svc.UpdateSettings(ctx, cur.Revision, next)
	var fe *domain.FieldError
	switch {
	case errors.Is(err, domain.ErrRevisionConflict):
		latest, gerr := svc.Settings(ctx)
		if gerr != nil {
			return nil, Internal(gerr)
		}
		return nil, CheckIfMatch(RevisionETag(cur.Revision), RevisionETag(latest.Revision), true)
	case errors.As(err, &fe):
		return nil, Invalid("invalid alert thresholds", Field("body."+fe.Field, fe.Message))
	case err != nil:
		return nil, Internal(err)
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: "alert_settings", ID: "instance"})
	return &alertSettingsOutput{ETagHeader: ETagHeader{ETag: RevisionETag(after.Revision)}, Body: newAlertSettings(after)}, nil
}

func registerNotificationEvents(a huma.API, deps Deps) {
	h := &notificationEventsAPI{authz: authz.OrDenyAll(deps.Authorizer)}
	if deps.NotificationEvents != nil {
		h.svc = deps.NotificationEvents
	}

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-notifications", Method: http.MethodGet, Path: BasePath + "/notifications",
			Summary: "List notifications",
			Description: "Finished backups and restores, prunes and update runs, newest first (kept 90 days), with how they went and " +
				"their numbers. Each is shown to whoever may read its job (job.read).",
			Tags:   []string{tagAlerts},
			Errors: []int{http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, h.list)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-alert-settings", Method: http.MethodGet, Path: BasePath + "/alert-settings",
			Summary:     "Get the alert thresholds",
			Description: "The warning and critical levels of temperature, disk space and memory alerts, and every environment's override. " + ownerOnly,
			Tags:        []string{tagAlerts}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusServiceUnavailable},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, h.getSettings)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-alert-settings", Method: http.MethodPut, Path: BasePath + "/alert-settings",
			Summary: "Change the alert thresholds",
			Description: "Replaces the thresholds and every environment's override. A level of 0 is off; a warning level must be below " +
				"the critical one. Requires If-Match. " + ownerOnly,
			Tags: []string{tagAlerts}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusPreconditionFailed, http.StatusPreconditionRequired,
				http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, h.putSettings)
}
