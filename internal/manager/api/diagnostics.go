package api

import (
	"context"
	"io"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/logging"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
)

// Diagnostics (#34): Docker Manager's own Prometheus metrics and the owner-only
// support bundle.

// Diagnostics capabilities (#17 catalog).
const (
	// CapSystemMetricsRead reads the internal metrics endpoint; meant for
	// a monitoring API token.
	CapSystemMetricsRead Capability = "system.metrics.read"
	// CapSystemSupportBundle downloads the support bundle (owner only).
	CapSystemSupportBundle Capability = "system.support_bundle"
)

// DiagnosticsService serves the metrics and the support bundle
// (*diagnostics.Service).
type DiagnosticsService interface {
	MetricsEnabled() bool
	WriteMetrics(ctx context.Context, w io.Writer) error
	WriteSupportBundle(ctx context.Context, w io.Writer) error
}

// Content types of the diagnostics downloads.
const (
	metricsContentType = "text/plain; version=0.0.4; charset=utf-8"
	bundleContentType  = "application/zip"
)

func registerDiagnostics(a huma.API, deps Deps) {
	az := authz.OrDenyAll(deps.Authorizer)
	allowed := func(ctx context.Context, capability Capability) error {
		p, ok := authz.PrincipalFrom(ctx)
		if !ok {
			return Unauthenticated("authentication required")
		}
		if !az.Can(ctx, p, string(capability), authz.Instance()).Allowed {
			return Forbidden("not permitted")
		}
		return nil
	}

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-system-metrics", Method: http.MethodGet, Path: BasePath + "/system/metrics",
			Summary: "Docker Manager internal metrics (Prometheus)",
			Description: "Docker Manager's own metrics in the Prometheus text exposition format 0.0.4: job queue depth and unfinished jobs " +
				"by state and kind, connected agent sessions, environments by state, agents by version compatibility, open event " +
				"streams and event bus subscribers, database sizes, the audit chain length and Go runtime basics. Off by default: " +
				"404 unless DOCKER_MANAGER_METRICS_ENABLED=true. Requires system.metrics.read (grant it to a dedicated API token and scrape " +
				"with Authorization: Bearer). Host and container metrics are GET /environments/{environmentId}/metrics.",
			Tags: []string{tagSystem}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable},
			Responses: map[string]*huma.Response{"200": {Description: "Prometheus text exposition",
				Content: map[string]*huma.MediaType{metricsContentType: {Schema: &huma.Schema{Type: "string"}}}}},
		},
		Capability: CapSystemMetricsRead, Scope: ScopeInstance,
	}, func(ctx context.Context, _ *struct{}) (*huma.StreamResponse, error) {
		if err := allowed(ctx, CapSystemMetricsRead); err != nil {
			return nil, err
		}
		if deps.Diagnostics == nil || !deps.Diagnostics.MetricsEnabled() {
			return nil, NotFound("the metrics endpoint is disabled; set DOCKER_MANAGER_METRICS_ENABLED=true to enable it")
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			hctx.SetHeader("Content-Type", metricsContentType)
			hctx.SetStatus(http.StatusOK)
			if err := deps.Diagnostics.WriteMetrics(hctx.Context(), hctx.BodyWriter()); err != nil {
				logging.FromContext(hctx.Context()).Error("write metrics", "error", err)
			}
		}}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-support-bundle", Method: http.MethodGet, Path: BasePath + "/support-bundle",
			Summary: "Download a support bundle",
			Description: "Streams a zip for troubleshooting: versions (manager, API, agent protocol, every agent), the effective " +
				"configuration (redacted), support-matrix checks per environment, environments and agents with their connection " +
				"and version compatibility, the audit chain verification result, a job queue summary, database migration and snapshot " +
				"status, and the manager's recent log lines. It never contains secret values (passwords, tokens, keys, credentials, " +
				"the Recovery Key, TOTP seeds, Compose/.env or file contents, job inputs). Owner only; audited.",
			Tags: []string{tagSystem}, Errors: []int{http.StatusForbidden, http.StatusServiceUnavailable},
			Responses: map[string]*huma.Response{"200": {Description: "Support bundle (zip)",
				Content: map[string]*huma.MediaType{bundleContentType: {Schema: &huma.Schema{Type: "string", Format: "binary"}}}}},
		},
		Capability: CapSystemSupportBundle, Scope: ScopeInstance, Audit: AuditAlways,
	}, func(ctx context.Context, _ *struct{}) (*huma.StreamResponse, error) {
		if err := allowed(ctx, CapSystemSupportBundle); err != nil {
			return nil, err
		}
		if deps.Diagnostics == nil {
			return nil, Unavailable(CodeUnavailable, "support bundles are not available")
		}
		name := "docker-manager-support-" + deps.clock().Now().UTC().Format("20060102T150405Z") + ".zip"
		audit.SetDetail(ctx, "fileName", name)
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			hctx.SetHeader("Content-Type", bundleContentType)
			hctx.SetHeader("Content-Disposition", `attachment; filename="`+name+`"`)
			hctx.SetHeader("X-Content-Type-Options", "nosniff")
			hctx.SetStatus(http.StatusOK)
			if err := deps.Diagnostics.WriteSupportBundle(hctx.Context(), hctx.BodyWriter()); err != nil {
				logging.FromContext(hctx.Context()).Error("write support bundle", "error", err)
			}
		}}, nil
	})
}
