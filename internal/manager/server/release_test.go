//go:build !e2e

package server

import (
	"net/http"
	"testing"
)

// Release builds (no e2e tag) serve none of the test-only routes.
func TestNoTestRoutesInReleaseBuild(t *testing.T) {
	tp := newTopo(t, AgentLimits{}, nil)
	if rec := tp.do(http.MethodGet, "/api/v1/__e2e/request-info", "203.0.113.5:1"); rec.Code != http.StatusNotFound {
		t.Fatalf("api test route: %d", rec.Code)
	}
	tp.do(http.MethodGet, "/agent/v1/__e2e/echo", "203.0.113.5:1")
	if calls, _, _, _ := tp.agent.get(); calls != 1 {
		t.Fatal("agent test route served by something other than Options.Agent")
	}
}
