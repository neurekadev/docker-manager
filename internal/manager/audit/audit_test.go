package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/db/migrations"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/requestinfo"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

type fixture struct {
	t   *testing.T
	ctx context.Context
	db  *bun.DB
	clk *clock.Fake
	log *audit.Log
}

func openDB(t *testing.T) *bun.DB {
	t.Helper()
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "docker-manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: filepath.Join(dir, "snap"),
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	return db
}

func newFixture(t *testing.T, mod ...func(*audit.Options)) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: testutil.Context(t), db: openDB(t), clk: testutil.FakeClock()}
	o := audit.Options{DB: f.db, Clock: f.clk, Logger: testutil.Logger(t)}
	for _, m := range mod {
		m(&o)
	}
	l, err := audit.New(o)
	if err != nil {
		t.Fatal(err)
	}
	f.log = l
	return f
}

func (f *fixture) record(ev domain.AuditEvent) {
	f.t.Helper()
	if err := f.log.Record(f.ctx, ev); err != nil {
		f.t.Fatalf("record %s: %v", ev.Action, err)
	}
}

func (f *fixture) all() []domain.AuditRecord {
	f.t.Helper()
	recs, err := f.log.Records(f.ctx, domain.AuditFilter{Ascending: true, Limit: 10000})
	if err != nil {
		f.t.Fatal(err)
	}
	return recs
}

func (f *fixture) verify() audit.VerifyReport {
	f.t.Helper()
	rep, err := f.log.Verify(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	return rep
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.ExecContext(f.ctx, q, args...); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
}

// seed records n events, one minute apart.
func (f *fixture) seed(n int) {
	f.t.Helper()
	for i := range n {
		f.record(domain.AuditEvent{Action: "stack.deploy", Actor: audit.ServiceActor(),
			Targets: []domain.AuditTarget{{Type: "stack", ID: fmt.Sprintf("s-%d", i)}}, Details: map[string]any{"n": i}})
		f.clk.Advance(time.Minute)
	}
}

func TestRecordFillsContextAndChains(t *testing.T) {
	f := newFixture(t)
	ctx, err := authz.WithPrincipal(f.ctx, authz.Principal{Kind: authz.KindAPIToken, UserID: "u-1", TokenID: "t-1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx = requestinfo.With(logging.WithRequestID(ctx, "req-42"), requestinfo.Info{ClientIP: netip.MustParseAddr("::ffff:203.0.113.9")})
	if err := f.log.Record(ctx, domain.AuditEvent{Action: "container.restart", OperationID: "restart-container",
		EnvironmentID: "env-1", Targets: []domain.AuditTarget{{Type: "container", ID: "web", EnvironmentID: "env-1"}, {Type: "container", ID: "web", EnvironmentID: "env-1"}},
		UserAgent: "curl/8.9\x00\n", JobID: "job-1"}); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(time.Second)
	f.record(domain.AuditEvent{Action: "invitation.create", Category: domain.AuditIdentity, Outcome: domain.AuditDenied, ErrorClass: "Forbidden!"})

	recs := f.all()
	if len(recs) != 2 {
		t.Fatalf("records %d", len(recs))
	}
	r := recs[0]
	want := domain.AuditActor{Kind: domain.AuditActorAPIToken, UserID: "u-1", TokenID: "t-1"}
	if r.Seq != 1 || r.Actor != want || r.ClientIP != "203.0.113.9" || r.RequestID != "req-42" || r.UserAgent != "curl/8.9" ||
		r.Category != domain.AuditOperations || r.Outcome != domain.AuditSuccess || len(r.Targets) != 1 || r.JobID != "job-1" ||
		!r.At.Equal(testutil.Epoch) || r.PrevHash != "" || len(r.Hash) != 64 || string(r.Details) != "{}" {
		t.Fatalf("record %+v", r)
	}
	r2 := recs[1]
	if r2.Seq != 2 || r2.PrevHash != r.Hash || r2.Actor.Kind != domain.AuditActorAnonymous || r2.ErrorClass != "unknown" ||
		r2.ClientIP != "" || r2.RequestID != "" {
		t.Fatalf("second record %+v", r2)
	}
	if got := audit.ChainHash(r2.PrevHash, audit.Canonical(&r2)); got != r2.Hash {
		t.Fatalf("hash %s != %s", got, r2.Hash)
	}
	if rep := f.verify(); !rep.OK || rep.Checked != 2 || rep.HeadSeq != 2 {
		t.Fatalf("verify %+v", rep)
	}
}

func TestRecordRejectsMalformedEvents(t *testing.T) {
	f := newFixture(t)
	for name, ev := range map[string]domain.AuditEvent{
		"no action":          {},
		"bad action":         {Action: "Stack Deploy"},
		"selector action":    {Action: "stack.{action}"},
		"bad category":       {Action: "a.b", Category: "misc"},
		"bad outcome":        {Action: "a.b", Outcome: "maybe"},
		"bad actor kind":     {Action: "a.b", Actor: domain.AuditActor{Kind: "robot"}},
		"user without id":    {Action: "a.b", Actor: domain.AuditActor{Kind: domain.AuditActorUser}},
		"token without user": {Action: "a.b", Actor: domain.AuditActor{Kind: domain.AuditActorAPIToken, TokenID: "t"}},
		"agent without id":   {Action: "a.b", Actor: domain.AuditActor{Kind: domain.AuditActorAgent}},
	} {
		if err := f.log.Record(f.ctx, ev); !errors.Is(err, audit.ErrInvalidEvent) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if n := len(f.all()); n != 0 {
		t.Fatalf("%d records stored", n)
	}
	// Service and anonymous actors never carry IDs.
	f.record(domain.AuditEvent{Action: "a.b", Actor: domain.AuditActor{Kind: domain.AuditActorService, UserID: "x", AgentID: "y"}})
	if a := f.all()[0].Actor; a != audit.ServiceActor() {
		t.Fatalf("actor %+v", a)
	}
	if err := audit.Record(f.ctx, domain.AuditEvent{Action: "a.b"}); !errors.Is(err, audit.ErrNoRecorder) {
		t.Fatalf("Record without recorder: %v", err)
	}
	if err := audit.Record(audit.WithRecorder(f.ctx, f.log), domain.AuditEvent{Action: "agent.enroll", Actor: audit.AgentActor("ag-1")}); err != nil {
		t.Fatal(err)
	}
	if r := f.all()[1]; r.Actor.AgentID != "ag-1" || r.Category != domain.AuditCredentials {
		t.Fatalf("agent record %+v", r)
	}
}

func TestAppendOnlyAtTheDatabase(t *testing.T) {
	f := newFixture(t)
	f.seed(3)
	if _, err := f.db.ExecContext(f.ctx, `UPDATE audit_events SET action = 'x.y' WHERE seq = 2`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("update allowed: %v", err)
	}
	if _, err := f.db.ExecContext(f.ctx, `DELETE FROM audit_events WHERE seq = 2`); err == nil || !strings.Contains(err.Error(), "retention purge") {
		t.Fatalf("delete allowed: %v", err)
	}
	if rep := f.verify(); !rep.OK {
		t.Fatalf("verify %+v", rep)
	}
}

// tamper drops the append-only triggers, as an attacker with raw database
// access would.
func (f *fixture) tamper(stmts ...string) {
	f.t.Helper()
	f.exec(`DROP TRIGGER audit_events_no_update`)
	f.exec(`DROP TRIGGER audit_events_purge_only`)
	for _, s := range stmts {
		f.exec(s)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	cases := map[string]struct {
		stmts []string
		kind  string
		seq   int64
	}{
		"modified details":  {[]string{`UPDATE audit_events SET details = '{"n":99}' WHERE seq = 3`}, "hash", 3},
		"modified actor":    {[]string{`UPDATE audit_events SET actor_kind = 'user', actor_user_id = 'mallory' WHERE seq = 2`}, "hash", 2},
		"modified time":     {[]string{`UPDATE audit_events SET at = '2020-01-01T00:00:00.000000Z' WHERE seq = 4`}, "hash", 4},
		"deleted middle":    {[]string{`DELETE FROM audit_events WHERE seq = 3`}, "link", 4},
		"deleted first":     {[]string{`DELETE FROM audit_events WHERE seq = 1`}, "link", 2},
		"truncated newest":  {[]string{`DELETE FROM audit_events WHERE seq = 5`}, "head", 5},
		"reordered records": {[]string{`UPDATE audit_events SET seq = 100 WHERE seq = 2`, `UPDATE audit_events SET seq = 2 WHERE seq = 3`, `UPDATE audit_events SET seq = 3 WHERE seq = 100`}, "link", 2},
		"rehashed record": {[]string{
			// Recomputing one record's hash still breaks its successor's link.
			`UPDATE audit_events SET details = '{"n":7}', hash = 'ffff' WHERE seq = 2`}, "link", 3},
		"moved anchor":  {[]string{`UPDATE audit_chain SET anchor_hash = 'abc'`}, "link", 1},
		"forged insert": {[]string{`INSERT INTO audit_events SELECT 6, 'forged', at, category, action, operation_id, actor_kind, actor_user_id, actor_token_id, actor_agent_id, client_ip, user_agent, environment_id, targets, outcome, error_class, job_id, request_id, details, size, hash, hash FROM audit_events WHERE seq = 5`, `UPDATE audit_chain SET head_seq = 6, record_count = 6`}, "hash", 6},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.seed(5)
			if rep := f.verify(); !rep.OK {
				t.Fatalf("clean chain: %+v", rep)
			}
			f.tamper(tc.stmts...)
			rep := f.verify()
			if rep.OK || rep.Err() == nil || !errors.Is(rep.Err(), domain.ErrAuditChainBroken) {
				t.Fatalf("tampering not detected: %+v", rep)
			}
			found := false
			for _, p := range rep.Problems {
				if p.Kind == tc.kind && p.Seq == tc.seq {
					found = true
				}
			}
			if !found {
				t.Fatalf("want a %s problem at %d, got %+v", tc.kind, tc.seq, rep.Problems)
			}
		})
	}
}

func TestRetentionPurgeKeepsChainVerifiable(t *testing.T) {
	f := newFixture(t, func(o *audit.Options) { o.Retention = 24 * time.Hour; o.PurgeBatch = 4 })
	f.seed(10) // t0 .. t0+9m; the clock is at t0+10m
	f.clk.Advance(24*time.Hour - 5*time.Minute)
	// now = t0+24h+5m: the records at t0..t0+4m (seq 1-5) are older than 24h.
	res, err := f.log.Purge(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 5 || res.Batches != 2 {
		t.Fatalf("purge %+v", res)
	}
	recs := f.all()
	// seq 6-10 kept + 2 purge records (batches of at most 4 deletions).
	if len(recs) != 7 || recs[0].Seq != 6 {
		t.Fatalf("after purge: %d records, first %d", len(recs), recs[0].Seq)
	}
	purges := 0
	for _, r := range recs {
		if r.Action != audit.ActionAuditPurge {
			continue
		}
		purges++
		var d map[string]any
		if err := json.Unmarshal(r.Details, &d); err != nil {
			t.Fatal(err)
		}
		if r.Actor != audit.ServiceActor() || r.Category != domain.AuditSystem || d["reason"] != "retention" || d["retentionDays"] != float64(1) {
			t.Fatalf("purge record %+v %v", r, d)
		}
	}
	if purges != 2 {
		t.Fatalf("purge records %d", purges)
	}
	rep := f.verify()
	if !rep.OK || rep.AnchorSeq != 5 || rep.Checked != 7 {
		t.Fatalf("verify after purge %+v", rep)
	}
	// New records chain onto the purge records. Two minutes later the
	// records of t0+5m and t0+6m (seq 6, 7) are too old as well.
	f.seed(2)
	if res, err := f.log.Purge(f.ctx); err != nil || res.Deleted != 2 || res.Batches != 1 {
		t.Fatalf("second purge %+v %v", res, err)
	}
	if res, err := f.log.Purge(f.ctx); err != nil || res.Deleted != 0 || res.Batches != 0 {
		t.Fatalf("idle purge %+v %v", res, err)
	}
	if rep := f.verify(); !rep.OK || rep.Checked != 8 || rep.AnchorSeq != 7 {
		t.Fatalf("verify %+v", rep)
	}
	// Tampering after a purge is still detected.
	f.tamper(`UPDATE audit_events SET action = 'stack.remove' WHERE seq = 8`)
	if rep := f.verify(); rep.OK {
		t.Fatal("tampering after purge not detected")
	}
}

func TestSizeCapPurge(t *testing.T) {
	f := newFixture(t, func(o *audit.Options) { o.MaxBytes = 10000 })
	f.seed(40)
	chain, err := store.GetAuditChain(f.ctx, f.db)
	if err != nil {
		t.Fatal(err)
	}
	if chain.TotalBytes <= 10000 || chain.RecordCount != 40 {
		t.Fatalf("chain %+v", chain)
	}
	res, err := f.log.Purge(f.ctx)
	if err != nil || res.Deleted == 0 {
		t.Fatalf("purge %+v %v", res, err)
	}
	chain, _ = store.GetAuditChain(f.ctx, f.db)
	if chain.TotalBytes > 10000 {
		t.Fatalf("size after purge %d", chain.TotalBytes)
	}
	recs := f.all()
	last := recs[len(recs)-1]
	if last.Action != audit.ActionAuditPurge || !strings.Contains(string(last.Details), `"reason":"size_cap"`) {
		t.Fatalf("last record %+v", last)
	}
	if rep := f.verify(); !rep.OK {
		t.Fatalf("verify %+v", rep)
	}
}

func TestRunPurgesOnSchedule(t *testing.T) {
	f := newFixture(t, func(o *audit.Options) { o.Retention = time.Hour; o.PurgeInterval = time.Hour })
	f.seed(1)
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = f.log.Run(ctx)
	}()
	if err := f.clk.BlockUntilWaiters(f.ctx, 1); err != nil { // first purge ran (nothing old yet)
		t.Fatal(err)
	}
	f.clk.Advance(2 * time.Hour)                              // fires the timer; the record is now too old
	if err := f.clk.BlockUntilWaiters(f.ctx, 1); err != nil { // second purge ran
		t.Fatal(err)
	}
	cancel()
	<-done
	recs := f.all()
	if len(recs) != 1 || recs[0].Action != audit.ActionAuditPurge {
		t.Fatalf("records %+v", recs)
	}
}

func TestMirrorWritesRedactedRecords(t *testing.T) {
	logger, buf := testutil.CaptureLogger()
	f := newFixture(t, func(o *audit.Options) { o.Mirror = logger })
	f.record(domain.AuditEvent{Action: "settings.manage", Actor: domain.AuditActor{Kind: domain.AuditActorUser, UserID: "u-1"},
		Details: map[string]any{"password": "hunter2hunter2", "field": "smtp"}})
	var line map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &line); err != nil {
		t.Fatalf("mirror output %q: %v", buf.String(), err)
	}
	if line["msg"] != "audit" || line["action"] != "settings.manage" || line["actor_user_id"] != "u-1" ||
		line["audit_seq"] != float64(1) || !strings.Contains(line["details"].(string), audit.Redacted) ||
		strings.Contains(buf.String(), "hunter2") {
		t.Fatalf("mirror line %v", line)
	}
	// Off by default.
	quiet, qbuf := testutil.CaptureLogger()
	g := newFixture(t, func(o *audit.Options) { o.Logger = quiet })
	g.record(domain.AuditEvent{Action: "a.b"})
	if qbuf.String() != "" {
		t.Fatalf("mirrored without opt-in: %s", qbuf)
	}
}

func TestDraftHelpers(t *testing.T) {
	ctx := context.Background()
	// No draft: helpers are no-ops.
	audit.AddTarget(ctx, domain.AuditTarget{Type: "stack", ID: "s"})
	if audit.SetAction(ctx, "stack.stop") {
		t.Fatal("SetAction without draft")
	}
	d := audit.NewDraft("stack_operation.create", []string{"stack.start", "stack.stop"})
	ctx = audit.WithDraft(ctx, d)
	if audit.SetAction(ctx, "container.exec") || !audit.SetAction(ctx, "stack.stop") {
		t.Fatal("SetAction must accept only the operation's values")
	}
	audit.SetPrincipal(ctx, authz.Principal{Kind: authz.KindUser, UserID: "u"})
	audit.SetEnvironment(ctx, "env")
	audit.AddTarget(ctx, domain.AuditTarget{Type: "container", ID: "c"})
	audit.SetDiff(ctx, map[string]any{"effect": "allow"}, map[string]any{"effect": "deny"})
	audit.SetDetail(ctx, "count", 2)
	audit.SetJob(ctx, "j")
	audit.SetOutcome(ctx, domain.AuditPartial)
	if d.HasErrorClass() {
		t.Fatal("error class set")
	}
	audit.SetErrorClass(ctx, "conflict")
	action, actor, ev := d.Snapshot()
	if action != "stack.stop" || actor == nil || actor.UserID != "u" || ev.EnvironmentID != "env" || len(ev.Targets) != 1 ||
		ev.JobID != "j" || ev.Outcome != domain.AuditPartial || ev.ErrorClass != "conflict" || ev.Details["count"] != 2 || ev.Details["diff"] == nil {
		t.Fatalf("snapshot %s %+v %+v", action, actor, ev)
	}
}

func TestActionAndCategory(t *testing.T) {
	for opID, want := range map[string]string{
		"create-invitation":              "invitation.create",
		"delete-my-passkey":              "my_passkey.delete",
		"create-user-session-revocation": "user_session_revocation.create",
		"search":                         "api.search",
	} {
		if got := audit.ActionForOperation(opID); got != want {
			t.Errorf("ActionForOperation(%s) = %s, want %s", opID, got, want)
		}
	}
	cases := []struct {
		action, op string
		want       domain.AuditCategory
	}{
		{"auth_session.create", "create-auth-session", domain.AuditIdentity},
		{"setup_owner.create", "create-setup-owner", domain.AuditIdentity},
		{"user_factor_reset.create", "create-user-factor-reset", domain.AuditIdentity},
		{"invitation_redemption.create", "create-invitation-redemption", domain.AuditIdentity},
		{"group_permissions.replace", "replace-group-permissions", domain.AuditAuthorization},
		{"user_permissions.replace", "replace-user-permissions", domain.AuditAuthorization},
		{"api_tokens.create", "create-my-api-token", domain.AuditCredentials},
		{"my_api_token.delete", "delete-my-api-token", domain.AuditCredentials},
		{"agent.enroll", "create-agent-enrollment", domain.AuditCredentials},
		{"registry.create", "create-registry", domain.AuditCredentials},
		{"git_credential.delete", "delete-git-credential", domain.AuditCredentials},
		{"backup_repository.manage", "create-backup-repository-key-rotation", domain.AuditCredentials},
		{"container.exec", "create-container-exec-session", domain.AuditOperations},
		{"stack.deploy", "create-stack-deployment", domain.AuditOperations},
		{"settings.manage", "update-settings", domain.AuditOperations},
		{"job.cancel", "create-job-cancellation", domain.AuditOperations},
		{audit.ActionJobFinished, "", domain.AuditOperations},
		{audit.ActionAuditPurge, "", domain.AuditSystem},
		{"audit.export", "export-audit-events", domain.AuditSystem},
	}
	for _, c := range cases {
		if got := audit.CategoryFor(c.action, c.op); got != c.want {
			t.Errorf("CategoryFor(%s, %s) = %s, want %s", c.action, c.op, got, c.want)
		}
	}
	caps := audit.Capabilities()
	if len(caps) != 2 || caps[0].Key != "audit.read" || !caps[0].HighRisk || !caps[0].OwnerOnlyByDefault || caps[0].Scope != "instance" {
		t.Fatalf("capabilities %+v", caps)
	}
}

func TestSafeCSVCell(t *testing.T) {
	for in, want := range map[string]string{
		"":                    "",
		"stack.deploy":        "stack.deploy",
		"=HYPERLINK(\"x\")":   "'=HYPERLINK(\"x\")",
		"+1":                  "'+1",
		"-2+3":                "'-2+3",
		"@SUM(A1)":            "'@SUM(A1)",
		"\tx":                 "'\tx",
		"\rx":                 "'\rx",
		"  =cmd|' /C calc'!A": "'  =cmd|' /C calc'!A",
		"a=b":                 "a=b",
		"{\"a\":1}":           "{\"a\":1}",
	} {
		if got := audit.SafeCSVCell(in); got != want {
			t.Errorf("SafeCSVCell(%q) = %q, want %q", in, got, want)
		}
	}
}
