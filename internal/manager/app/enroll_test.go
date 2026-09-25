package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/neurekadev/dockyard/internal/buildinfo"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestHeadlessEnrollmentAgainstRunningManager: `dockyard-manager
// enrollment create` (CreateEnrollment on the data directory) works next to
// a running manager, which accepts the token on /agent/v1/enroll and the
// credential on /agent/v1/session; the public agent routes fail closed
// until #16/#17 provide principals.
func TestHeadlessEnrollmentAgainstRunningManager(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	if _, err := CreateEnrollment(testutil.Context(t), testConfig(dataDir), testutil.Logger(t), domain.EnrollmentSpec{}); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("uninitialized: %v", err)
	}
	m := runManager(t, dataDir, nil)
	created, err := CreateEnrollment(testutil.Context(t), testConfig(dataDir), testutil.Logger(t), domain.EnrollmentSpec{EnvironmentName: "Local"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Token, protocol.EnrollmentTokenPrefix) || created.ManagerURL != "http://localhost:8080" {
		t.Fatalf("created %+v", created)
	}
	body, _ := json.Marshal(protocol.EnrollRequest{Protocol: protocol.Version, AgentVersion: buildinfo.Get().Version,
		InstallID: "0190a6e0-1111-7000-8000-000000000001", Engine: protocol.EngineInfo{ID: "ENG", Version: "28.5.2", APIVersion: "1.51"}, Hostname: "h"})
	req, _ := http.NewRequestWithContext(testutil.Context(t), http.MethodPost, m.base+protocol.EnrollPath, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+created.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var er protocol.EnrollResponse
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(b, &er) != nil || er.EnvironmentName != "Local" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("enroll: %d %s %v", resp.StatusCode, b, resp.Header)
	}
	h := http.Header{"Authorization": {"Bearer " + er.Credential}}
	c, _, err := websocket.Dial(testutil.Context(t), "ws"+strings.TrimPrefix(m.base, "http")+protocol.SessionPath, //nolint:bodyclose // the library owns the handshake response
		&websocket.DialOptions{HTTPHeader: h, Subprotocols: []string{protocol.Version}})
	if err != nil {
		t.Fatalf("session upgrade: %v", err)
	}
	if c.Subprotocol() != protocol.Version {
		t.Fatalf("subprotocol %q", c.Subprotocol())
	}
	// Anonymous public API calls fail closed.
	for _, p := range []string{"/api/v1/agents", "/api/v1/environments", "/api/v1/agent-enrollments"} {
		if code, _ := get(t, m.base+p); code != http.StatusUnauthorized {
			t.Errorf("%s: %d", p, code)
		}
	}
	// Shutdown closes the hijacked session with 1001 (going away).
	stopped := make(chan error, 1)
	go func() { stopped <- m.stop() }()
	_, _, rerr := c.Read(testutil.Context(t))
	if websocket.CloseStatus(rerr) != protocol.CloseGoingAway {
		t.Fatalf("session end on shutdown: %v", rerr)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
}
