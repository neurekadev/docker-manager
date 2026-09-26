package app

import (
	"net/http"
	"strings"
	"testing"
)

// TestAPITokenTerminalNeedsContainerExec (#31 follow-up with #8): a token
// scoped to container.restart is refused the logs and a terminal of the
// container it may restart, although its owner holds both; a token whose
// own grants include container.exec gets a terminal session (created on
// the agent's Engine) and the session start is audited with the token ID.
// Metrics- or restart-only users get neither.
func TestAPITokenTerminalNeedsContainerExec(t *testing.T) {
	e, nas, _ := twoHosts(t)
	owner, _ := e.setupOwner()
	scope := "@container:" + nas.env + "/web"
	rita, _ := e.opsUser(owner, "allow api_tokens.create @all", "allow container.restart @env:"+nas.env,
		"allow container.exec @env:"+nas.env, "allow container.logs.read @env:"+nas.env,
		"allow container.details.read @env:"+nas.env)
	base := "/api/v1/environments/" + nas.env + "/containers/web"
	shell := map[string]any{"command": []string{"/bin/sh"}}

	_, restartSecret := rita.createToken("ci", "allow container.restart "+scope)
	restartOnly := e.bot(restartSecret)
	restartOnly.fail(http.StatusForbidden, "forbidden", http.MethodPost, base+"/exec-sessions", shell)
	restartOnly.fail(http.StatusForbidden, "forbidden", http.MethodGet, base+"/logs", nil)
	restartOnly.fail(http.StatusForbidden, "forbidden", http.MethodGet, base+"/logs/stream", nil)
	if n := len(nas.engine.Execs()); n != 0 {
		t.Fatalf("refused token created %d exec instances", n)
	}

	execTok, execSecret := rita.createToken("shell", "allow container.exec "+scope)
	withExec := e.bot(execSecret)
	var sess struct {
		ID          string `json:"id"`
		StreamURL   string `json:"streamUrl"`
		Subprotocol string `json:"subprotocol"`
		Ticket      string `json:"ticket"`
	}
	withExec.must(http.StatusCreated, http.MethodPost, base+"/exec-sessions", shell).json(t, &sess)
	if sess.ID == "" || sess.Ticket == "" || sess.Subprotocol != "docker-manager.exec.v1" ||
		!strings.HasSuffix(sess.StreamURL, "/containers/web/exec-sessions/"+sess.ID+"/stream") {
		t.Fatalf("exec session %+v", sess)
	}
	xs := nas.engine.Execs()
	if len(xs) != 1 || len(xs[0].Spec.Cmd) != 1 || xs[0].Spec.Cmd[0] != "/bin/sh" || !xs[0].Spec.Tty {
		t.Fatalf("engine exec instances %+v", xs)
	}
	// The exec token still cannot read logs or restart.
	withExec.fail(http.StatusForbidden, "forbidden", http.MethodGet, base+"/logs", nil)
	withExec.fail(http.StatusForbidden, "forbidden", http.MethodPost, base+"/restart", nil)
	// The session can be ended by its creator.
	withExec.must(http.StatusNoContent, http.MethodDelete, base+"/exec-sessions/"+sess.ID, nil)

	found := false
	for _, row := range e.auditRows() {
		if row.Action == "container.exec" && row.ActorToken == execTok.ID && row.ActorKind == "api_token" &&
			row.Outcome == "success" {
			found = true
		}
		if strings.Contains(row.Details, sess.Ticket) {
			t.Fatal("audit row carries the attach ticket")
		}
	}
	if !found {
		t.Fatal("token terminal session not audited with the token ID")
	}

	// Restart- and metrics-only users: no logs, no terminal.
	for i, rule := range []string{"allow container.restart " + scope, "allow container.metrics.read " + scope} {
		name := []string{"restarter", "watcher"}[i]
		g := owner.createGroup(name)
		owner.putRules("/api/v1/groups/"+g.ID+"/permissions", rule)
		u, _, us := e.newUser(owner, name)
		if r := owner.moveUser(us.User.ID, g.ID); r.status != http.StatusOK {
			t.Fatalf("move %s: %d %s", name, r.status, r.body)
		}
		u.fail(http.StatusForbidden, "forbidden", http.MethodPost, base+"/exec-sessions", shell)
		u.fail(http.StatusForbidden, "forbidden", http.MethodGet, base+"/logs", nil)
	}
	e.assertNoTokenValues()
}
