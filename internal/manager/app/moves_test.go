package app

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/manager/config"
	"github.com/neurekadev/docker-manager/internal/manager/managermove"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
	"github.com/neurekadev/docker-manager/internal/testutil/canary"
)

// envLine reads KEY=value from a rendered .env.
func envLine(env, key string) string {
	for _, line := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	return ""
}

// signedMove signs a move request for path with code on the manager's
// clock.
func (e *env) signedMove(code, path string) reqOpt {
	e.t.Helper()
	h, err := managermove.SignRequest(code, http.MethodPost, path, e.clk.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	return header("Authorization", h)
}

// signedGet signs a move GET (the check-in) for path with code.
func (e *env) signedGet(code, path string) reqOpt {
	e.t.Helper()
	h, err := managermove.SignRequest(code, http.MethodGet, path, e.clk.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	return header("Authorization", h)
}

// agentSessionStatus dials the agent session with a well-formed credential.
func (e *env) agentSessionStatus() (int, string) {
	e.t.Helper()
	req, _ := http.NewRequestWithContext(testutil.Context(e.t), http.MethodGet, e.srv.URL+protocol.SessionPath, nil)
	req.Header.Set("Authorization", "Bearer dya_0190a6e0-0000-7000-8000-000000000001_"+strings.Repeat("A", 43))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Retry-After")
}

// TestManagerMoveLocksTheOldManager runs the old manager's side of a move
// through the real handler: the owner creates a move (the new server's
// compose.yaml and .env with the code and an enrollment token, shown once,
// never logged or audited); the code authenticates nothing and is never
// sent itself: the waiting manager's signed requests get not_ready until
// the move is ready, then the encrypted package; from then on the manager
// is read-only (409 manager_moved, reads and sign-out still work),
// refuses agents with 503 + Retry-After, stays locked after a restart and
// cannot be cancelled once the new manager confirmed.
func TestManagerMoveLocksTheOldManager(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	if lock := sessionMove(t, owner); lock.State != "none" || lock.Address != "" {
		t.Fatalf("session before the move: %+v", lock)
	}
	var created struct {
		Move struct {
			ID                string `json:"id"`
			State             string `json:"state"`
			ThisServerAddress string `json:"thisServerAddress"`
		} `json:"move"`
		ComposeYAML string `json:"composeYaml"`
		Env         string `json:"env"`
		StatusURL   string `json:"statusUrl"`
	}
	body := map[string]any{"thisServerAddress": "192.168.1.10", "newServerAddress": "192.168.1.20"}
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/manager/moves", body, secretOK).json(t, &created)
	code, token := envLine(created.Env, "DOCKER_MANAGER_MOVE_CODE"), envLine(created.Env, "DOCKER_AGENT_ENROLLMENT_TOKEN")
	if created.Move.State != "open" || created.Move.ThisServerAddress != "192.168.1.10:8080" || !strings.HasPrefix(code, "dmm_"+created.Move.ID+"_") ||
		!strings.HasPrefix(token, "dye_") || created.StatusURL != "http://192.168.1.20:8080" ||
		!strings.Contains(created.ComposeYAML, "DOCKER_MANAGER_MOVE_CODE: ${DOCKER_MANAGER_MOVE_CODE:-}") ||
		envLine(created.Env, "DOCKER_MANAGER_MOVE_FROM") != "http://192.168.1.10:8080" ||
		envLine(created.Env, "DOCKER_MANAGER_PUBLIC_URL") != "https://"+publicHost {
		t.Fatalf("created %+v\n%s", created.Move, created.Env)
	}
	e.secrets.Register(canary.APIToken, "move code", code)
	e.secrets.Register(canary.APIToken, "enrollment token", token)
	owner.fail(http.StatusConflict, "manager_move_exists", http.MethodPost, "/api/v1/manager/moves", body)

	// The code is not a session or an API token anywhere, and not accepted
	// as a bearer by the move routes.
	anon := e.client()
	anon.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/manager/move", nil, bearer(code))
	anon.fail(http.StatusUnauthorized, "unauthenticated", http.MethodPost, "/api/v1/manager/move/handoff", nil, bearer(code))
	anon.fail(http.StatusUnauthorized, "move_code_invalid", http.MethodPost, "/api/v1/manager/move/handoff", nil,
		header("Authorization", "DMM "+created.Move.ID+":1:nonce:"+strings.Repeat("A", 43)))
	var checkIn struct {
		State string `json:"state"`
	}
	anon.must(http.StatusOK, http.MethodGet, managermove.CheckInPath, nil, e.signedGet(code, managermove.CheckInPath)).json(t, &checkIn)
	if checkIn.State != "open" {
		t.Fatalf("check-in %+v", checkIn)
	}
	r := anon.fail(http.StatusConflict, "manager_move_not_ready", http.MethodPost, "/api/v1/manager/move/handoff", nil,
		e.signedMove(code, managermove.HandoffPath))
	if r.header.Get(managermove.MoveStateHeader) != "open" || r.header.Get("Retry-After") != "10" {
		t.Fatalf("not ready: %v", r.header)
	}
	var mv struct {
		State     string `json:"state"`
		NewServer *struct {
			ManagerCheckedIn bool `json:"managerCheckedIn"`
		} `json:"newServer"`
		HandoffAddress string `json:"handoffAddress"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/manager/move", nil).json(t, &mv)
	if mv.State != "open" || mv.NewServer == nil || !mv.NewServer.ManagerCheckedIn || mv.HandoffAddress == "" {
		t.Fatalf("after the check-in %+v", mv)
	}
	owner.fail(http.StatusConflict, "manager_move_new_server_missing", http.MethodPost, "/api/v1/manager/move/runs", nil)

	// Move everything is covered by the move service's tests: the move is
	// ready here.
	if _, err := e.m.DB().ExecContext(testutil.Context(t), "UPDATE manager_moves SET state = 'ready' WHERE id = ?", created.Move.ID); err != nil {
		t.Fatal(err)
	}
	r = anon.do(http.MethodPost, "/api/v1/manager/move/handoff", nil, e.signedMove(code, managermove.HandoffPath), secretOK)
	if r.status != http.StatusOK || r.header.Get("Content-Type") != managermove.PackageContentType || r.header.Get(managermove.PackageSizeHeader) == "" ||
		len(r.body) == 0 || strings.Contains(string(r.body), managermove.PartManifest) {
		t.Fatalf("handoff: %d %s", r.status, r.header)
	}

	// Read-only now; agents refused. Every session reads the lock.
	if lock := sessionMove(t, owner); lock.State != "moving" || lock.Address != "https://"+publicHost {
		t.Fatalf("session while moving: %+v", lock)
	}
	owner.fail(http.StatusConflict, "manager_moved", http.MethodPost, "/api/v1/registries", map[string]any{"name": "x", "host": "registry.example.com"})
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/manager/move", nil).json(t, &mv)
	if mv.State != "handed_off" {
		t.Fatalf("move %+v", mv)
	}
	if status, retry := e.agentSessionStatus(); status != http.StatusServiceUnavailable || retry != "60" {
		t.Fatalf("agent session while moved: %d %q", status, retry)
	}

	// A restart stays locked (the restarted server has a new address).
	e.restartManager()
	owner.base, anon.base = e.srv.URL, e.srv.URL
	owner.fail(http.StatusConflict, "manager_moved", http.MethodPost, "/api/v1/registries", map[string]any{"name": "x", "host": "registry.example.com"})

	anon.must(http.StatusOK, http.MethodPost, "/api/v1/manager/move/confirm", nil, e.signedMove(code, managermove.ConfirmPath))
	if lock := sessionMove(t, owner); lock.State != "moved" {
		t.Fatalf("session after the confirmation: %+v", lock)
	}
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

// sessionMove reads the move lock of c's session (GET /auth/session).
func sessionMove(t *testing.T, c *client) struct{ State, Address string } {
	t.Helper()
	var s struct {
		ManagerMove *struct {
			State   string `json:"state"`
			Address string `json:"address"`
		} `json:"managerMove"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/auth/session", nil).json(t, &s)
	if s.ManagerMove == nil {
		t.Fatal("the session carries no managerMove")
	}
	return struct{ State, Address string }{s.ManagerMove.State, s.ManagerMove.Address}
}

// withMove sets DOCKER_MANAGER_MOVE_FROM and DOCKER_MANAGER_MOVE_CODE.
func withMove(code string) func(*Options) {
	return func(o *Options) {
		o.Config.Move = config.MoveConfig{From: &url.URL{Scheme: "http", Host: "192.168.1.10:8080"}, Code: logging.Secret(code)}
	}
}

const waitingCode = "dmm_0190a6e0-0000-7000-8000-000000000035_3q2-7wEXAMPLEEXAMPLEEXAMPLEEXAMPLEEXAMPLE"

// TestWaitingModeServesOnlyTheMoveStatus: a new, empty manager with the
// move variables waits: it serves the move status (public, never the
// code), health and the web app; every other route answers 503
// manager_move_waiting, setup included; agents are refused with 503 and
// Retry-After (never 401).
func TestWaitingModeServesOnlyTheMoveStatus(t *testing.T) {
	e := newEnv(t, withMove(waitingCode))
	e.secrets.Register(canary.APIToken, "move code", waitingCode)
	anon := e.client()
	var st struct {
		Phase      string `json:"phase"`
		OldManager string `json:"oldManager"`
		PublicURL  string `json:"publicUrl"`
	}
	anon.must(http.StatusOK, http.MethodGet, "/api/v1/move/status", nil).json(t, &st)
	if st.Phase != managermove.WaitConnecting || st.OldManager != "http://192.168.1.10:8080" || st.PublicURL != "https://"+publicHost {
		t.Fatalf("status %+v", st)
	}
	anon.must(http.StatusOK, http.MethodGet, "/api/v1/health", nil)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/setup/status"},
		{http.MethodPost, "/api/v1/setup/owner"},
		{http.MethodGet, "/api/v1/auth/session"},
		{http.MethodGet, "/api/v1/environments"},
	} {
		r := anon.fail(http.StatusServiceUnavailable, "manager_move_waiting", c.method, c.path, nil)
		if r.header.Get("Retry-After") == "" {
			t.Errorf("%s %s: no Retry-After", c.method, c.path)
		}
	}
	if status, retry := e.agentSessionStatus(); status != http.StatusServiceUnavailable || retry != "60" {
		t.Fatalf("agent session while waiting: %d %q", status, retry)
	}
	resp, err := http.Get(e.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("web app while waiting: %d", resp.StatusCode)
	}
}

// TestMoveVariablesIgnoredWithAnOwner: a manager that already has an
// owner ignores the move variables (with a warning): setup and sign-in
// work, and the move status is none.
func TestMoveVariablesIgnoredWithAnOwner(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	e.m.opts.Config.Move = withMoveConfig()
	e.restartManager()
	owner.base = e.srv.URL
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/auth/session", nil)
	var st struct {
		Phase string `json:"phase"`
	}
	e.client().must(http.StatusOK, http.MethodGet, "/api/v1/move/status", nil).json(t, &st)
	if st.Phase != managermove.WaitNone {
		t.Fatalf("status %+v", st)
	}
}

func withMoveConfig() config.MoveConfig {
	var o Options
	withMove(waitingCode)(&o)
	return o.Config.Move
}
