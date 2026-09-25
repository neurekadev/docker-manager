package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/manager/settings"
)

// Instance settings (#4): the editable display name plus a read-only view
// of the deployment configuration. The sign-in policy (/settings/security,
// owner only), schedule defaults (/schedule-defaults) and maintenance
// defaults (/maintenance-defaults) are separate revisioned resources.

// SettingsInstanceID is the audit target ID (type settings) of the instance
// settings.
const SettingsInstanceID = "instance"

// SettingsService reads and changes the instance settings (implemented by
// *settings.Service).
type SettingsService interface {
	Get(ctx context.Context) (domain.InstanceSettings, error)
	Update(ctx context.Context, revision int64, p domain.InstanceSettingsPatch) (before, after domain.InstanceSettings, err error)
}

// DeploymentInfo is the manager's deployment configuration shown read-only
// by GET /settings (environment variables, #27).
type DeploymentInfo struct {
	PublicURL        string
	LocalDevelopment bool
	// TrustedProxies is the number of DOCKYARD_TRUSTED_PROXIES ranges.
	TrustedProxies int
	MetricsEnabled bool
}

// DeploymentSettings is the read-only deployment configuration.
type DeploymentSettings struct {
	PublicURL              string `json:"publicUrl" example:"https://docker.example.com" doc:"DOCKYARD_PUBLIC_URL: the one public origin serving the web app, the API and agent sessions."`
	LocalDevelopment       bool   `json:"localDevelopment" example:"false" doc:"True when the public URL is plain HTTP on localhost (development only)."`
	TrustedProxyCount      int    `json:"trustedProxyCount" example:"1" doc:"Number of DOCKYARD_TRUSTED_PROXIES address ranges whose X-Forwarded-* headers are honored (the ranges themselves are in the owner's support bundle)."`
	StreamHeartbeatSeconds int    `json:"streamHeartbeatSeconds" example:"15" doc:"DOCKYARD_STREAM_HEARTBEAT: SSE heartbeat and WebSocket ping interval; keep it below the reverse proxy's idle timeout."`
	FilesMaxUploadBytes    int64  `json:"filesMaxUploadBytes" example:"2147483648" doc:"DOCKYARD_FILES_MAX_UPLOAD_MB: the largest file-manager upload; the reverse proxy's body limit must allow it."`
	MetricsEndpoint        bool   `json:"metricsEndpoint" example:"false" doc:"DOCKYARD_METRICS_ENABLED: GET /api/v1/system/metrics is served."`
}

// InstanceSettings are the instance-wide settings.
type InstanceSettings struct {
	Name       string             `json:"name" example:"Homelab" doc:"Display name of this DockYard (editable)."`
	InstanceID string             `json:"instanceId" example:"01921b4e-7c1a-7cc3-9b1e-4d6f0a2b3c4d" doc:"ID of this manager instance (read-only)."`
	Deployment DeploymentSettings `json:"deployment" doc:"Read-only deployment configuration. It comes from the manager's environment variables: change them in the deployment and restart the manager."`
	Revision   int64              `json:"revision" example:"3"`
	UpdatedAt  time.Time          `json:"updatedAt" doc:"When the editable settings last changed."`
}

type instanceSettingsOutput struct {
	ETagHeader
	Body InstanceSettings
}

type patchInstanceSettingsInput struct {
	IfMatchParam
	Body struct {
		Name *string `json:"name,omitempty" minLength:"1" maxLength:"64" example:"Homelab" doc:"New display name (1-64 characters, no control characters; surrounding white space is removed)."`
	}
}

type settingsAPI struct {
	svc        SettingsService
	authz      authz.Authorizer
	instanceID string
	deployment DeploymentSettings
}

func (h *settingsAPI) check(ctx context.Context, capability Capability) (SettingsService, error) {
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	if !c.Can(string(capability), authz.Instance()).Allowed {
		return nil, Forbidden("requires " + string(capability))
	}
	if h.svc == nil {
		return nil, Unavailable(CodeUnavailable, "instance settings are not available")
	}
	return h.svc, nil
}

func (h *settingsAPI) view(s domain.InstanceSettings) InstanceSettings {
	return InstanceSettings{Name: s.Name, InstanceID: h.instanceID, Deployment: h.deployment, Revision: s.Revision, UpdatedAt: s.UpdatedAt}
}

func registerSettings(a huma.API, deps Deps) {
	heartbeat := deps.SSEHeartbeat
	if heartbeat <= 0 {
		heartbeat = DefaultSSEHeartbeat
	}
	maxUpload := deps.FilesMaxUpload
	if maxUpload <= 0 {
		maxUpload = DefaultMaxUpload
	}
	d := deps.Deployment
	h := &settingsAPI{svc: deps.Settings, authz: deps.Authorizer, instanceID: deps.InstanceID, deployment: DeploymentSettings{
		PublicURL: d.PublicURL, LocalDevelopment: d.LocalDevelopment, TrustedProxyCount: d.TrustedProxies,
		StreamHeartbeatSeconds: int(heartbeat / time.Second), FilesMaxUploadBytes: maxUpload, MetricsEndpoint: d.MetricsEnabled,
	}}

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-settings", Method: http.MethodGet, Path: BasePath + "/settings",
			Summary: "Get the instance settings",
			Description: "The display name of this DockYard and a read-only summary of its deployment configuration (public URL, " +
				"trusted proxies, stream heartbeat, upload limit, metrics endpoint). The sign-in policy is GET /api/v1/settings/security " +
				"(owner only), schedule defaults GET /api/v1/schedule-defaults and maintenance defaults GET /api/v1/maintenance-defaults.",
			Tags: []string{tagSettings}, Errors: []int{http.StatusForbidden},
		},
		Capability: CapSettingsRead, Scope: ScopeInstance,
	}, func(ctx context.Context, _ *struct{}) (*instanceSettingsOutput, error) {
		svc, err := h.check(ctx, CapSettingsRead)
		if err != nil {
			return nil, err
		}
		s, err := svc.Get(ctx)
		if err != nil {
			return nil, Internal(err)
		}
		return &instanceSettingsOutput{ETagHeader: ETagHeader{ETag: RevisionETag(s.Revision)}, Body: h.view(s)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-settings", Method: http.MethodPatch, Path: BasePath + "/settings",
			Summary: "Change the instance settings",
			Description: "Changes the display name. Requires If-Match. The deployment configuration is read-only here: it comes from " +
				"the manager's environment variables.",
			Tags: []string{tagSettings},
			Errors: []int{http.StatusForbidden, http.StatusPreconditionFailed, http.StatusPreconditionRequired,
				http.StatusUnprocessableEntity},
		},
		Capability: CapSettingsManage, Scope: ScopeInstance,
	}, func(ctx context.Context, in *patchInstanceSettingsInput) (*instanceSettingsOutput, error) {
		svc, err := h.check(ctx, CapSettingsManage)
		if err != nil {
			return nil, err
		}
		cur, err := svc.Get(ctx)
		if err != nil {
			return nil, Internal(err)
		}
		if err := in.CheckIfMatch(RevisionETag(cur.Revision)); err != nil {
			return nil, err
		}
		before, after, err := svc.Update(ctx, cur.Revision, domain.InstanceSettingsPatch{Name: in.Body.Name})
		switch {
		case errors.Is(err, domain.ErrRevisionConflict):
			latest, gerr := svc.Get(ctx)
			if gerr != nil {
				return nil, Internal(gerr)
			}
			return nil, CheckIfMatch(RevisionETag(cur.Revision), RevisionETag(latest.Revision), true)
		case errors.Is(err, settings.ErrInvalidName):
			return nil, Invalid("invalid settings", Field("body.name", err.Error()))
		case err != nil:
			return nil, Internal(err)
		}
		// The target also announces the change to live streams (#23:
		// settings.read holders refresh ['settings', 'item', 'instance']).
		audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeSettings, ID: SettingsInstanceID})
		audit.SetDiff(ctx, map[string]any{"name": before.Name}, map[string]any{"name": after.Name})
		return &instanceSettingsOutput{ETagHeader: ETagHeader{ETag: RevisionETag(after.Revision)}, Body: h.view(after)}, nil
	})
}
