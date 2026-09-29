package app

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/managermove"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil/canary"
)

// TestManagerMoveLocksTheOldManager runs the old manager's side of a move
// through the real handler: the owner creates a move code (shown once,
// never logged or audited); the code authenticates nothing but the move
// routes; the handoff streams the package; from then on the manager is
// read-only (409 manager_moved, reads and sign-out still work), refuses
// agents with 503 + Retry-After, stays locked after a restart and cannot
// be cancelled once the new manager confirmed.
func TestManagerMoveLocksTheOldManager(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	var created struct {
		Move struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"move"`
		Code string `json:"code"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/manager/moves", nil, secretOK).json(t, &created)
	code := created.Code
	if created.Move.State != "open" || !strings.HasPrefix(code, "dmm_"+created.Move.ID+"_") {
		t.Fatalf("created %+v", created.Move)
	}
	e.secrets.Register(canary.APIToken, "move code", code)
	owner.fail(http.StatusConflict, "manager_move_exists", http.MethodPost, "/api/v1/manager/moves", nil)

	// The code is not a session or an API token anywhere else.
	anon := e.client()
	anon.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/manager/move", nil, bearer(code))
	anon.fail(http.StatusUnauthorized, "move_code_invalid", http.MethodPost, "/api/v1/manager/move/handoff", nil, bearer(code+"x"))

	r := anon.do(http.MethodPost, "/api/v1/manager/move/handoff", nil, bearer(code), secretOK)
	if r.status != http.StatusOK || r.header.Get("Content-Type") != managermove.PackageContentType {
		t.Fatalf("handoff: %d %s", r.status, r.header)
	}
	var names []string
	tr := tar.NewReader(bytes.NewReader(r.body))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	if len(names) == 0 || names[len(names)-1] != managermove.PartManifest || !slices.Contains(names, managermove.PartDatabase) ||
		!slices.Contains(names, managermove.PartSealedKey) {
		t.Fatalf("package parts %v", names)
	}

	// Read-only now; agents refused.
	owner.fail(http.StatusConflict, "manager_moved", http.MethodPost, "/api/v1/registries", map[string]any{"name": "x", "host": "registry.example.com"})
	var mv struct {
		State          string `json:"state"`
		HandoffAddress string `json:"handoffAddress"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/manager/move", nil).json(t, &mv)
	if mv.State != "handed_off" || mv.HandoffAddress == "" {
		t.Fatalf("move %+v", mv)
	}
	req, _ := http.NewRequestWithContext(testutil.Context(t), http.MethodGet, e.srv.URL+protocol.SessionPath, nil)
	req.Header.Set("Authorization", "Bearer dya_0190a6e0-0000-7000-8000-000000000001_"+strings.Repeat("A", 43))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "60" {
		t.Fatalf("agent session while moved: %d %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}

	// A restart stays locked.
	e.restartManager()
	owner.fail(http.StatusConflict, "manager_moved", http.MethodPost, "/api/v1/registries", map[string]any{"name": "x", "host": "registry.example.com"})

	anon.must(http.StatusOK, http.MethodPost, "/api/v1/manager/move/confirm", nil, bearer(code))
	owner.fail(http.StatusConflict, "manager_move_state", http.MethodPost, "/api/v1/manager/move/cancellations",
		map[string]any{"resumeHere": true, "instanceName": "Docker Manager"})
	owner.must(http.StatusNoContent, http.MethodDelete, "/api/v1/auth/session", nil)

	var actions []string
	for _, row := range e.auditRows() {
		actions = append(actions, row.Action)
	}
	for _, want := range []string{"manager.move.create", "manager.move.handoff", "manager.move.confirm", "manager.move.cancel"} {
		if !slices.Contains(actions, want) {
			t.Errorf("audit lacks %s: %v", want, actions)
		}
	}
	e.secrets.AssertClean(t, "audit trail", e.tableDump("audit_events", "manager_moves"))
}
