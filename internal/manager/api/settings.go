package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/settings"
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
	// TrustedProxies is the number of DOCKER_MANAGER_TRUSTED_PROXIES ranges.
	TrustedProxies int
	MetricsEnabled bool
}

// DeploymentSettings is the read-only deployment configuration.
type DeploymentSettings struct {
	PublicURL              string `json:"publicUrl" example:"https://docker.example.com" doc:"DOCKER_MANAGER_PUBLIC_URL: the one public origin serving the web app, the API and agent sessions."`
	LocalDevelopment       bool   `json:"localDevelopment" example:"false" doc:"True when the public URL is plain HTTP on localhost (development only)."`
	TrustedProxyCount      int    `json:"trustedProxyCount" example:"1" doc:"Number of DOCKER_MANAGER_TRUSTED_PROXIES address ranges whose X-Forwarded-* headers are honored (the ranges themselves are in the owner's support bundle)."`
	StreamHeartbeatSeconds int    `json:"streamHeartbeatSeconds" example:"15" doc:"DOCKER_MANAGER_STREAM_HEARTBEAT: SSE heartbeat and WebSocket ping interval; keep it below the reverse proxy's idle timeout."`
	FilesMaxUploadBytes    int64  `json:"filesMaxUploadBytes" example:"2147483648" doc:"DOCKER_MANAGER_FILES_MAX_UPLOAD_MB: the largest file-manager upload; the reverse proxy's body limit must allow it."`
	FilesMaxEditBytes      int64  `json:"filesMaxEditBytes" example:"524288" doc:"DOCKER_MANAGER_FILES_MAX_EDIT_KB: larger files open read-only in the file manager's editor."`
	FilesMaxDownloadBytes  int64  `json:"filesMaxDownloadBytes" example:"10737418240" doc:"DOCKER_MANAGER_FILES_MAX_DOWNLOAD_MB: the largest file-manager download or created archive."`
	FilesMaxExtractBytes   int64  `json:"filesMaxExtractBytes" example:"10737418240" doc:"DOCKER_MANAGER_FILES_MAX_EXTRACT_MB: the most one extraction writes."`
	FilesMaxExtractRatio   int64  `json:"filesMaxExtractRatio" example:"100" doc:"DOCKER_MANAGER_FILES_MAX_EXTRACT_RATIO: an extraction writes at most this many times the archive's size."`
	FilesMaxArchiveEntries int    `json:"filesMaxArchiveEntries" example:"100000" doc:"DOCKER_MANAGER_FILES_MAX_ARCHIVE_ENTRIES: the most entries of an archive extracted or created."`
	MetricsEndpoint        bool   `json:"metricsEndpoint" example:"false" doc:"DOCKER_MANAGER_METRICS_ENABLED: GET /api/v1/system/metrics is served."`
}

// InstanceSettings are the instance-wide settings.
type InstanceSettings struct {
	Name       string             `json:"name" example:"Homelab" doc:"Display name of this Docker Manager (editable)."`
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
	fl := fileLimitsOrDefault(deps.FileLimits)
	d := deps.Deployment
	h := &settingsAPI{svc: deps.Settings, authz: deps.Authorizer, instanceID: deps.InstanceID, deployment: DeploymentSettings{
		PublicURL: d.PublicURL, LocalDevelopment: d.LocalDevelopment, TrustedProxyCount: d.TrustedProxies,
		StreamHeartbeatSeconds: int(heartbeat / time.Second), FilesMaxUploadBytes: fl.Upload, FilesMaxEditBytes: fl.Edit,
		FilesMaxDownloadBytes: fl.Download, FilesMaxExtractBytes: fl.ExtractBytes, FilesMaxExtractRatio: fl.ExtractRatio,
		FilesMaxArchiveEntries: fl.ArchiveEntries, MetricsEndpoint: d.MetricsEnabled,
	}}

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-settings", Method: http.MethodGet, Path: BasePath + "/settings",
			Summary: "Get the instance settings",
			Description: "The display name of this Docker Manager and a read-only summary of its deployment configuration (public URL, " +
				"trusted proxies, stream heartbeat, file manager limits, metrics endpoint). The sign-in policy is GET /api/v1/settings/security " +
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
