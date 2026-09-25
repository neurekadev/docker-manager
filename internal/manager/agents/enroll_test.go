package agents

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/authsep"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// TestEnrollmentTokenStoredAsVerifierOnly: the token is returned once and
// only the SHA-256 verifier of its secret is stored.
func TestEnrollmentTokenStoredAsVerifierOnly(t *testing.T) {
	f := newFixture(t)
	c := f.createEnrollment(domain.EnrollmentSpec{EnvironmentName: "NAS"})
	id, secret, ok := authsep.ParseEnrollmentToken(c.Token)
	if !ok || id != c.Enrollment.ID {
		t.Fatalf("token %q does not embed enrollment %s", c.Token, c.Enrollment.ID)
	}
	var stored []string
	if err := f.db.NewRaw(`SELECT verifier || '|' || target_id || '|' || environment_name FROM agent_enrollments`).Scan(f.ctx, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || strings.Contains(stored[0], secret) || !strings.HasPrefix(stored[0], authsep.Verifier(secret)+"|") {
		t.Fatalf("stored row %v", stored)
	}
	if c.Enrollment.ExpiresAt.Sub(c.Enrollment.CreatedAt) != DefaultEnrollmentTTL {
		t.Fatalf("default lifetime %v", c.Enrollment.ExpiresAt.Sub(c.Enrollment.CreatedAt))
	}
	// Install commands carry the manager URL and the token, never in a URL.
	if len(c.Install) != 3 {
		t.Fatalf("install commands %+v", c.Install)
	}
	for _, cmd := range c.Install {
		if !strings.Contains(cmd.Command, c.Token) {
			t.Errorf("%s lacks the token", cmd.Variant)
		}
		if strings.Contains(cmd.Command, "?"+c.Token) || strings.Contains(cmd.Command, "/"+c.Token) || strings.Contains(cmd.Command, "="+c.Token+"&") {
			t.Errorf("%s puts the token in a URL: %s", cmd.Variant, cmd.Command)
		}
	}
	for _, v := range []string{InstallRemote, InstallRemoteCompose} {
		for _, cmd := range c.Install {
			if cmd.Variant == v && !strings.Contains(cmd.Command, "https://docker.example.com") {
				t.Errorf("%s lacks DOCKYARD_PUBLIC_URL: %s", v, cmd.Command)
			}
		}
	}
	if strings.Contains(f.logs.String(), secret) {
		t.Fatal("enrollment secret logged")
	}
}

func TestCreateEnrollmentValidation(t *testing.T) {
	f := newFixture(t)
	a := f.newAgent("ENG-A", "host-a")
	resp := a.enroll(f.createEnrollment(domain.EnrollmentSpec{}).Token)
	cases := map[string]domain.EnrollmentSpec{
		"ttl too short":            {TTL: time.Second},
		"ttl too long":             {TTL: 25 * time.Hour},
		"bad name":                 {EnvironmentName: " padded"},
		"replace unknown":          {Intent: domain.IntentReplace, TargetID: "0190a6e0-0000-7000-8000-000000000001"},
		"reattach active":          {Intent: domain.IntentReattach, TargetID: resp.EnvironmentID},
		"duplicate needs new":      {Intent: domain.IntentReplace, TargetID: resp.AgentID, AllowDuplicateEngineID: true},
		"new with target":          {Intent: domain.IntentNew, TargetID: resp.AgentID},
		"unknown intent":           {Intent: "adopt", TargetID: resp.AgentID},
		"reattach unknown":         {Intent: domain.IntentReattach, TargetID: "0190a6e0-0000-7000-8000-000000000002"},
		"replace an environment":   {Intent: domain.IntentReplace, TargetID: resp.EnvironmentID},
		"reattach to an agent ID":  {Intent: domain.IntentReattach, TargetID: resp.AgentID},
		"name longer than allowed": {EnvironmentName: strings.Repeat("n", 64)},
	}
	for name, spec := range cases {
		var in *domain.InputError
		if _, err := f.svc.CreateEnrollment(f.ctx, spec); !errors.As(err, &in) {
			t.Errorf("%s: %v, want an input error", name, err)
		}
	}
	for in, want := range map[string]string{"": "new", "new": "new", "replace:" + resp.AgentID: "replace", "reattach:" + resp.EnvironmentID: "reattach"} {
		kind, _, err := ParseIntent(in)
		if err != nil || string(kind) != want {
			t.Errorf("ParseIntent(%q) = %q %v", in, kind, err)
		}
	}
	for _, bad := range []string{"replace", "replace:", "reattach:x", "new:abc", "adopt:" + resp.AgentID} {
		if _, _, err := ParseIntent(bad); err == nil {
			t.Errorf("ParseIntent(%q) accepted", bad)
		}
	}
}

// TestEnrollmentTokenCannotBeMisused: stolen (wrong secret), expired,
// revoked and reused tokens all fail with the same generic 401, and none of
// them creates an agent.
func TestEnrollmentTokenCannotBeMisused(t *testing.T) {
	f := newFixture(t)
	a := f.newAgent("ENG-A", "host-a")
	req := a.enrollRequest()

	used := f.createEnrollment(domain.EnrollmentSpec{})
	a.enroll(used.Token)

	expired := f.createEnrollment(domain.EnrollmentSpec{TTL: time.Minute})
	revoked := f.createEnrollment(domain.EnrollmentSpec{})
	if _, err := f.svc.RevokeEnrollment(f.ctx, revoked.Enrollment.ID); err != nil {
		t.Fatal(err)
	}
	valid := f.createEnrollment(domain.EnrollmentSpec{})
	id, _, _ := authsep.ParseEnrollmentToken(valid.Token)
	forged, _ := authsep.MintEnrollmentToken(id) // right ID, wrong secret
	unknown, _ := authsep.MintEnrollmentToken("0190a6e0-0000-7000-8000-00000000dead")
	f.clk.Advance(time.Minute)

	b := f.newAgent("ENG-B", "host-b").enrollRequest()
	for name, tok := range map[string]string{
		"reused": used.Token, "expired": expired.Token, "revoked": revoked.Token, "wrong secret": forged.Token,
		"unknown": unknown.Token, "credential instead of token": "dya_" + strings.TrimPrefix(valid.Token, "dye_"),
		"garbage": "dye_not-a-token", "missing": "",
	} {
		code, body := f.enrollHTTP(tok, b)
		e := decodeErr(t, body)
		if code != http.StatusUnauthorized || e.Code != "unauthenticated" || e.Message != "agent authentication failed" {
			t.Errorf("%s: %d %+v", name, code, e)
		}
	}
	// A browser (Origin header) cannot use a valid token either.
	if code, body := f.enrollHTTP(valid.Token, b, "Origin", "https://evil.example"); code != http.StatusForbidden {
		t.Errorf("browser request: %d %s", code, body)
	}
	agents, _ := f.svc.ListAgents(f.ctx, domain.AgentFilter{})
	if len(agents) != 1 {
		t.Fatalf("%d agents after misuse, want 1", len(agents))
	}
	// The valid token still works once, and only once.
	f.newAgent("ENG-B", "host-b").enroll(valid.Token)
	if code, _ := f.enrollHTTP(valid.Token, b); code != http.StatusUnauthorized {
		t.Fatalf("second use: %d", code)
	}
	_ = req
	if st := mustEnrollment(t, f, valid.Enrollment.ID).State(f.clk.Now()); st != domain.EnrollmentUsed {
		t.Fatalf("state %s", st)
	}
	if st := mustEnrollment(t, f, expired.Enrollment.ID).State(f.clk.Now()); st != domain.EnrollmentExpired {
		t.Fatalf("expired state %s", st)
	}
	// Revoking a used enrollment changes nothing.
	if e, err := f.svc.RevokeEnrollment(f.ctx, valid.Enrollment.ID); err != nil || e.RevokedAt != nil {
		t.Fatalf("revoke used: %+v %v", e, err)
	}
}

func mustEnrollment(t *testing.T, f *fixture, id string) domain.Enrollment {
	t.Helper()
	e, err := f.svc.GetEnrollment(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestEnrollRequestValidationAndVersion(t *testing.T) {
	f := newFixture(t)
	tok := f.createEnrollment(domain.EnrollmentSpec{}).Token
	a := f.newAgent("ENG-A", "host-a")
	good, _ := json.Marshal(a.enrollRequest())
	cases := []struct {
		name string
		body []byte
		code int
		err  string
	}{
		{"unknown field", []byte(strings.Replace(string(good), `{`, `{"shell":"sh",`, 1)), 422, "validation_failed"},
		{"trailing data", append(append([]byte{}, good...), []byte(` {}`)...), 422, "validation_failed"},
		{"not json", []byte(`nope`), 422, "validation_failed"},
		{"missing install id", mutate(t, good, func(r *protocol.EnrollRequest) { r.InstallID = "" }), 422, "validation_failed"},
		{"bad engine id", mutate(t, good, func(r *protocol.EnrollRequest) { r.Engine.ID = "a b" }), 422, "validation_failed"},
		{"control characters", mutate(t, good, func(r *protocol.EnrollRequest) { r.EnvironmentName = "a\x07" }), 422, "validation_failed"},
		{"wrong protocol", mutate(t, good, func(r *protocol.EnrollRequest) { r.Protocol = "dockyard.agent/v2" }), 426, "version_unsupported"},
		{"agent too old", mutate(t, good, func(r *protocol.EnrollRequest) { r.AgentVersion = "1.2.9" }), 426, "version_unsupported"},
		{"agent newer than manager", mutate(t, good, func(r *protocol.EnrollRequest) { r.AgentVersion = "1.5.0" }), 426, "version_unsupported"},
		{"too large", []byte(`{"protocol":"` + strings.Repeat("x", 70<<10) + `"}`), 413, "payload_too_large"},
	}
	for _, c := range cases {
		code, body := f.enrollHTTP(tok, c.body)
		if e := decodeErr(t, body); code != c.code || e.Code != c.err {
			t.Errorf("%s: %d %+v, want %d %s", c.name, code, e, c.code, c.err)
		}
	}
	// Refusals do not consume the token; an N-1 agent enrolls (outdated).
	a.version = "1.3.7"
	resp := a.enroll(tok)
	ag, err := f.svc.GetAgent(f.ctx, resp.AgentID)
	if err != nil || ag.VersionStatus != protocol.VersionOutdated {
		t.Fatalf("N-1 agent: %+v %v", ag, err)
	}
}

func mutate(t *testing.T, good []byte, fn func(*protocol.EnrollRequest)) []byte {
	t.Helper()
	var r protocol.EnrollRequest
	if err := json.Unmarshal(good, &r); err != nil {
		t.Fatal(err)
	}
	fn(&r)
	b, _ := json.Marshal(r)
	return b
}

// TestOneAgentPerEngine: a second agent for an enrolled Engine is refused
// with a clear message (and the refusal is recorded for the owner) unless
// the enrollment's intent replaces the old agent, which revokes it.
func TestOneAgentPerEngine(t *testing.T) {
	f := newFixture(t)
	first := f.newAgent("ENG-A", "host-a")
	r1 := first.enroll(f.createEnrollment(domain.EnrollmentSpec{EnvironmentName: "NAS"}).Token)
	first.start()
	f.waitOnline(r1.EnvironmentID)

	// Same host, new agent installation (state volume recreated): refused.
	second := f.newAgent("ENG-A", "host-a")
	tok := f.createEnrollment(domain.EnrollmentSpec{})
	code, body := f.enrollHTTP(tok.Token, second.enrollRequest())
	e := decodeErr(t, body)
	if code != http.StatusConflict || e.Code != domain.ConflictEngineAlreadyEnrolled || !strings.Contains(e.Message, "replace:"+r1.AgentID) {
		t.Fatalf("duplicate: %d %+v", code, e)
	}
	rec := mustEnrollment(t, f, tok.Enrollment.ID)
	if rec.Rejection == nil || rec.Rejection.ConflictAgentID != r1.AgentID || rec.State(f.clk.Now()) != domain.EnrollmentPending {
		t.Fatalf("rejection not recorded or token consumed: %+v", rec)
	}

	// Replace: the new agent takes over the environment, the old credential
	// dies and its live session is closed with 4403.
	repl := f.createEnrollment(domain.EnrollmentSpec{Intent: domain.IntentReplace, TargetID: r1.AgentID})
	r2 := second.enroll(repl.Token)
	if r2.EnvironmentID != r1.EnvironmentID || r2.EnvironmentName != "NAS" || r2.AgentID == r1.AgentID {
		t.Fatalf("replace: %+v vs %+v", r2, r1)
	}
	var stop interface{ Error() string }
	select {
	case err := <-first.done:
		stop = err
		first.cancel = nil
	case <-f.ctx.Done():
		t.Fatal("old agent kept its session")
	}
	if !strings.Contains(stop.Error(), "4403") {
		t.Fatalf("old agent stopped with %v", stop)
	}
	old, _ := f.svc.GetAgent(f.ctx, r1.AgentID)
	if old.Status != domain.AgentRevoked || !strings.Contains(old.RevokedReason, r2.AgentID) {
		t.Fatalf("old agent %+v", old)
	}
	oldCred, _ := first.store.Credential()
	if _, err := f.svc.Authenticate(f.ctx, oldCred.Credential); !errors.Is(err, domain.ErrCredentialInvalid) {
		t.Fatalf("old credential still authenticates: %v", err)
	}
	second.start()
	f.waitOnline(r1.EnvironmentID)
	env, _ := f.svc.GetEnvironment(f.ctx, r1.EnvironmentID)
	if env.AgentID != r2.AgentID || env.InstallID != second.install || !env.Online {
		t.Fatalf("environment after replace %+v", env)
	}

	// Replace for a different Engine is refused.
	other := f.newAgent("ENG-OTHER", "host-x")
	code, body = f.enrollHTTP(f.createEnrollment(domain.EnrollmentSpec{Intent: domain.IntentReplace, TargetID: r2.AgentID}).Token, other.enrollRequest())
	if e := decodeErr(t, body); code != http.StatusConflict || e.Code != domain.ConflictEngineMismatch {
		t.Fatalf("replace other engine: %d %+v", code, e)
	}
	// A replace enrollment whose target is gone.
	stale := f.createEnrollment(domain.EnrollmentSpec{Intent: domain.IntentReplace, TargetID: r2.AgentID})
	if _, err := f.svc.RemoveAgent(f.ctx, r2.AgentID, mustAgent(t, f, r2.AgentID).Revision); err != nil {
		t.Fatal(err)
	}
	code, body = f.enrollHTTP(stale.Token, f.newAgent("ENG-A", "host-a").enrollRequest())
	if e := decodeErr(t, body); code != http.StatusConflict || e.Code != domain.ConflictTargetUnavailable {
		t.Fatalf("stale replace: %d %+v", code, e)
	}
}

func mustAgent(t *testing.T, f *fixture, id string) domain.Agent {
	t.Helper()
	a, err := f.svc.GetAgent(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestClonedEngineIDCollision: an Engine ID enrolled from another host is
// surfaced as engine_identity_conflict; the owner may allow the duplicate
// explicitly, which creates a separate environment.
func TestClonedEngineIDCollision(t *testing.T) {
	f := newFixture(t)
	r1 := f.newAgent("CLONED", "vm-1").enroll(f.createEnrollment(domain.EnrollmentSpec{}).Token)
	clone := f.newAgent("CLONED", "vm-2")
	tok := f.createEnrollment(domain.EnrollmentSpec{})
	code, body := f.enrollHTTP(tok.Token, clone.enrollRequest())
	e := decodeErr(t, body)
	if code != http.StatusConflict || e.Code != domain.ConflictEngineIdentity || !strings.Contains(e.Message, "vm-1") || !strings.Contains(e.Message, "vm-2") {
		t.Fatalf("collision: %d %+v", code, e)
	}
	rec := mustEnrollment(t, f, tok.Enrollment.ID)
	if rec.Rejection == nil || rec.Rejection.Code != domain.ConflictEngineIdentity || rec.Rejection.InstallID != clone.install ||
		rec.Rejection.ConflictEnvironmentID != r1.EnvironmentID {
		t.Fatalf("rejection %+v", rec.Rejection)
	}
	r2 := clone.enroll(f.createEnrollment(domain.EnrollmentSpec{AllowDuplicateEngineID: true}).Token)
	if r2.EnvironmentID == r1.EnvironmentID {
		t.Fatal("duplicate Engine ID joined the existing environment")
	}
	env, _ := f.svc.GetEnvironment(f.ctx, r2.EnvironmentID)
	if !env.AllowDuplicateEngineID || env.Name != "vm-2" {
		t.Fatalf("environment %+v", env)
	}
	// The allowance does not let the same installation enroll twice.
	code, body = f.enrollHTTP(f.createEnrollment(domain.EnrollmentSpec{AllowDuplicateEngineID: true}).Token, clone.enrollRequest())
	if e := decodeErr(t, body); code != http.StatusConflict || e.Code != domain.ConflictEngineAlreadyEnrolled {
		t.Fatalf("same install twice: %d %+v", code, e)
	}
}

// TestArchiveAndReattach: archiving revokes the agent and keeps the
// records; re-enrolling the Engine needs a reattach enrollment and brings
// the same environment back.
func TestArchiveAndReattach(t *testing.T) {
	f := newFixture(t)
	a := f.newAgent("ENG-A", "host-a")
	r1 := a.enroll(f.createEnrollment(domain.EnrollmentSpec{EnvironmentName: "Prod"}).Token)
	a.start()
	f.waitOnline(r1.EnvironmentID)
	env, _ := f.svc.GetEnvironment(f.ctx, r1.EnvironmentID)
	if _, err := f.svc.ArchiveEnvironment(f.ctx, env.ID, env.Revision+1); !errors.Is(err, domain.ErrRevisionMismatch) {
		t.Fatalf("stale archive: %v", err)
	}
	if _, err := f.svc.ArchiveEnvironment(f.ctx, env.ID, env.Revision); err != nil {
		t.Fatal(err)
	}
	f.waitEvent("environment.archived", env.ID)
	select {
	case err := <-a.done:
		a.cancel = nil
		if err == nil || !strings.Contains(err.Error(), "4403") {
			t.Fatalf("agent stopped with %v", err)
		}
	case <-f.ctx.Done():
		t.Fatal("session of the archived environment stayed open")
	}
	env, _ = f.svc.GetEnvironment(f.ctx, env.ID)
	if env.Status != domain.EnvironmentArchived || env.Online || env.AgentID != "" {
		t.Fatalf("archived environment %+v", env)
	}
	if list, _ := f.svc.ListEnvironments(f.ctx, domain.EnvironmentFilter{Statuses: []domain.EnvironmentStatus{domain.EnvironmentActive}}); len(list) != 0 {
		t.Fatalf("archived environment listed as active: %+v", list)
	}
	name := "x"
	if _, err := f.svc.UpdateEnvironment(f.ctx, env.ID, env.Revision, domain.EnvironmentPatch{Name: &name}); !errors.Is(err, domain.ErrEnvironmentArchived) {
		t.Fatalf("edit archived: %v", err)
	}

	// A plain new enrollment for the Engine is refused with guidance.
	b := f.newAgent("ENG-A", "host-a")
	code, body := f.enrollHTTP(f.createEnrollment(domain.EnrollmentSpec{}).Token, b.enrollRequest())
	if e := decodeErr(t, body); code != http.StatusConflict || e.Code != domain.ConflictEnvironmentArchived || !strings.Contains(e.Message, "reattach:"+env.ID) {
		t.Fatalf("new enrollment for archived engine: %d %+v", code, e)
	}
	// A reattach enrollment for another Engine is refused.
	code, body = f.enrollHTTP(f.createEnrollment(domain.EnrollmentSpec{Intent: domain.IntentReattach, TargetID: env.ID}).Token, f.newAgent("ENG-Z", "z").enrollRequest())
	if e := decodeErr(t, body); code != http.StatusConflict || e.Code != domain.ConflictEngineMismatch {
		t.Fatalf("reattach other engine: %d %+v", code, e)
	}
	reattach := f.createEnrollment(domain.EnrollmentSpec{Intent: domain.IntentReattach, TargetID: env.ID})
	if reattach.Enrollment.EnvironmentName != "Prod" {
		t.Fatalf("reattach enrollment name %q", reattach.Enrollment.EnvironmentName)
	}
	r2 := b.enroll(reattach.Token)
	if r2.EnvironmentID != env.ID || !r2.Reattached || r2.EnvironmentName != "Prod" {
		t.Fatalf("reattach: %+v", r2)
	}
	f.waitEvent("environment.reattached", env.ID)
	b.start()
	f.waitOnline(env.ID)
	env, _ = f.svc.GetEnvironment(f.ctx, env.ID)
	if env.Status != domain.EnvironmentActive || env.ArchivedAt != nil || env.AgentID != r2.AgentID {
		t.Fatalf("re-attached environment %+v", env)
	}
}

// TestRemoveAgentDetachesEnvironment: removing an agent revokes its
// credential and closes its session; the environment stays detached until
// a reattach enrollment.
func TestRemoveAgentDetachesEnvironment(t *testing.T) {
	f := newFixture(t)
	a := f.newAgent("ENG-A", "host-a")
	r := a.enroll(f.createEnrollment(domain.EnrollmentSpec{}).Token)
	a.start()
	f.waitOnline(r.EnvironmentID)
	ag := mustAgent(t, f, r.AgentID)
	if _, err := f.svc.RemoveAgent(f.ctx, r.AgentID, ag.Revision); err != nil {
		t.Fatal(err)
	}
	if err := <-a.done; err == nil || !strings.Contains(err.Error(), "4403") {
		t.Fatalf("removed agent stopped with %v", err)
	}
	a.cancel = nil
	env, _ := f.svc.GetEnvironment(f.ctx, r.EnvironmentID)
	if env.AgentID != "" || env.Online || env.Status != domain.EnvironmentActive {
		t.Fatalf("environment %+v", env)
	}
	cred, _ := a.store.Credential()
	if code := f.upgradeStatus(cred.Credential); code != http.StatusUnauthorized {
		t.Fatalf("revoked credential upgrade: %d", code)
	}
	b := f.newAgent("ENG-A", "host-a")
	code, body := f.enrollHTTP(f.createEnrollment(domain.EnrollmentSpec{}).Token, b.enrollRequest())
	if e := decodeErr(t, body); code != http.StatusConflict || e.Code != domain.ConflictEnvironmentDetached {
		t.Fatalf("new enrollment for a detached environment: %d %+v", code, e)
	}
	r2 := b.enroll(f.createEnrollment(domain.EnrollmentSpec{Intent: domain.IntentReattach, TargetID: r.EnvironmentID}).Token)
	if r2.EnvironmentID != r.EnvironmentID || r2.Reattached {
		t.Fatalf("attach to detached: %+v", r2)
	}
}

func TestResetOnlineAndRetention(t *testing.T) {
	f := newFixture(t)
	a := f.newAgent("ENG-A", "host-a")
	r := a.enroll(f.createEnrollment(domain.EnrollmentSpec{}).Token)
	a.start()
	f.waitOnline(r.EnvironmentID)
	a.stop()
	f.waitEvent("environment.offline", r.EnvironmentID)
	// Simulate a manager crash that left the environment marked online.
	if _, err := f.db.NewRaw(`UPDATE environments SET online = 1`).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ResetOnline(f.ctx); err != nil {
		t.Fatal(err)
	}
	if env, _ := f.svc.GetEnvironment(f.ctx, r.EnvironmentID); env.Online {
		t.Fatal("still online after ResetOnline")
	}
	// Old enrollments are deleted a week after they expired.
	f.clk.Advance(DefaultEnrollmentTTL + enrollmentRetention + time.Minute)
	f.createEnrollment(domain.EnrollmentSpec{})
	list, _ := store.ListEnrollments(f.ctx, f.db, "", 0)
	if len(list) != 1 {
		t.Fatalf("%d enrollments retained", len(list))
	}
}
