package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/protocol"
)

const tagSystem = "System"

// HealthBody is the liveness response.
type HealthBody struct {
	Status  string `json:"status" enum:"ok" doc:"Always ok when the process can serve requests."`
	Version string `json:"version" doc:"Manager build version." example:"0.0.0-edge"`
	Commit  string `json:"commit" doc:"Source commit the manager was built from."`
}

// ReadinessCheck is one readiness check.
type ReadinessCheck struct {
	Name    string `json:"name" example:"database"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

// ReadinessBody is the readiness response when ready.
type ReadinessBody struct {
	Status string           `json:"status" enum:"ready"`
	Checks []ReadinessCheck `json:"checks"`
}

// CapabilitiesBody describes what this manager supports.
type CapabilitiesBody struct {
	ManagerVersion       string   `json:"managerVersion" example:"0.0.0-edge"`
	APIVersion           string   `json:"apiVersion" example:"v1"`
	AgentProtocolVersion string   `json:"agentProtocolVersion" example:"docker-manager.agent/v1"`
	Features             []string `json:"features" doc:"Stable feature-flag keys enabled on this manager."`
}

type healthOutput struct{ Body HealthBody }
type readinessOutput struct{ Body ReadinessBody }
type capabilitiesOutput struct{ Body CapabilitiesBody }

func registerSystem(a huma.API, deps Deps) {
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-health",
			Method:      http.MethodGet,
			Path:        BasePath + "/health",
			Summary:     "Liveness",
			Description: "Reports that the manager process is serving requests. Used by the container HEALTHCHECK.",
			Tags:        []string{tagSystem},
		},
		Capability: CapabilityPublic,
		Scope:      ScopeNone,
	}, func(_ context.Context, _ *struct{}) (*healthOutput, error) {
		return &healthOutput{Body: HealthBody{Status: "ok", Version: deps.Build.Version, Commit: deps.Build.Commit}}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-health-ready",
			Method:      http.MethodGet,
			Path:        BasePath + "/health/ready",
			Summary:     "Readiness",
			Description: "Reports whether the manager can serve traffic: the database is reachable and all migrations are applied. " +
				"Returns 503 not_ready with one detail per failing check otherwise.",
			Tags:   []string{tagSystem},
			Errors: []int{http.StatusServiceUnavailable},
		},
		Capability: CapabilityPublic,
		Scope:      ScopeNone,
	}, func(ctx context.Context, _ *struct{}) (*readinessOutput, error) {
		var checks []Check
		if deps.Readiness != nil {
			checks = deps.Readiness(ctx)
		}
		body := ReadinessBody{Status: "ready", Checks: make([]ReadinessCheck, 0, len(checks))}
		var failed []ErrorDetail
		for _, c := range checks {
			body.Checks = append(body.Checks, ReadinessCheck(c))
			if !c.OK {
				failed = append(failed, Field("check."+c.Name, c.Message))
			}
		}
		if len(failed) > 0 {
			e := Unavailable(CodeNotReady, "manager is not ready")
			e.Details = failed
			return nil, e
		}
		return &readinessOutput{Body: body}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-capabilities",
			Method:      http.MethodGet,
			Path:        BasePath + "/capabilities",
			Summary:     "Manager capabilities",
			Description: "Versions and feature flags clients use to adapt to this manager.",
			Tags:        []string{tagSystem},
		},
		Capability: CapabilityPublic,
		Scope:      ScopeNone,
	}, func(_ context.Context, _ *struct{}) (*capabilitiesOutput, error) {
		features := append([]string{}, deps.Features...)
		return &capabilitiesOutput{Body: CapabilitiesBody{
			ManagerVersion:       deps.Build.Version,
			APIVersion:           Version,
			AgentProtocolVersion: protocol.Version,
			Features:             features,
		}}, nil
	})
}
