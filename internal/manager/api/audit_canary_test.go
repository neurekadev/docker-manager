package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

type rotateInput struct {
	RegistryID string `path:"registryId"`
	Body       struct {
		Username    string            `json:"username"`
		Password    string            `json:"password"`
		Token       string            `json:"token"`
		S3Access    string            `json:"s3AccessKey"`
		S3Secret    string            `json:"s3SecretKey"`
		TOTPSeed    string            `json:"totpSeed"`
		Env         map[string]string `json:"env"`
		ComposeFile string            `json:"composeFile"`
	}
}

// TestSecretCanariesNeverReachTheAuditTrail seeds every #29 canary kind
// through audited requests (bodies, Authorization and Cookie headers, query
// strings), careless handler enrichment (sensitive details, diffs, whole
// request DTOs), job inputs and agent job output, then asserts that no
// canary appears in the stored rows, the list API, the NDJSON and CSV
// exports or the mirrored structured log (#30 Done-when).
func TestSecretCanariesNeverReachTheAuditTrail(t *testing.T) {
	c := canary.New()
	v := map[canary.Kind]string{}
	for _, k := range canary.Kinds {
		v[k] = c.New(k, string(k))
	}
	f := newAuditFixture(t, auditAuthz{})
	registerAuditTestOps(f.api)
	Register(f.api, Operation{
		Operation: huma.Operation{OperationID: "create-test-credential-rotation", Method: http.MethodPost,
			Path: BasePath + "/test/registries/{registryId}/credential-rotations", Summary: "rotate"},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *rotateInput) (*JobAccepted, error) {
		// A careless handler: every one of these must be redacted.
		audit.SetDetail(ctx, "username", in.Body.Username)
		audit.SetDetail(ctx, "password", in.Body.Password)
		audit.SetDetail(ctx, "request", in.Body)
		audit.SetDetail(ctx, "authorization", "Bearer "+in.Body.Token)
		audit.SetDetail(ctx, "note", "rotated with token="+in.Body.Token)
		audit.SetDiff(ctx, map[string]any{"credential": map[string]any{"secret": in.Body.S3Secret}},
			map[string]any{"env": in.Body.Env, "values": []string{in.Body.TOTPSeed}})
		p, _ := authz.PrincipalFrom(ctx)
		j, _, err := f.eng.Enqueue(ctx, jobs.Request{Kind: jobspec.StackDeploy, Principal: p, EnvironmentID: "env-1",
			Targets: []domain.JobTarget{{Type: domain.TargetStack, ID: "st-1"}},
			Input:   map[string]any{"password": in.Body.Password, "env": in.Body.Env, "compose": in.Body.ComposeFile}})
		if err != nil {
			return nil, JobErrorFor(err)
		}
		return Accepted(j), nil
	})

	body, _ := json.Marshal(map[string]any{
		"username": "robot", "password": v[canary.Password], "token": v[canary.APIToken],
		"s3AccessKey": v[canary.S3AccessKey], "s3SecretKey": v[canary.S3SecretKey], "totpSeed": v[canary.TOTPSeed],
		"env":         map[string]string{"DB_PASSWORD": v[canary.EnvValue], "REGISTRY_AUTH": v[canary.RegistryCredential]},
		"composeFile": "services:\n  db:\n    environment:\n      PASSWORD: " + v[canary.EnvValue] + "\n",
	})
	rec := f.do(http.MethodPost, BasePath+"/test/registries/reg-1/credential-rotations", string(body),
		"X-Test-User", "owner", "Authorization", "Bearer "+v[canary.APIToken], "Cookie", "__Host-dockyard_session="+v[canary.Password])
	if rec.Code != http.StatusAccepted {
		t.Fatalf("rotate: %d %s", rec.Code, rec.Body)
	}
	// Secrets in query strings and failed sign-ins.
	f.do(http.MethodPost, BasePath+"/test/registries/reg-1/credential-rotations?token="+v[canary.APIToken], string(body), "X-Test-User", "owner")
	f.do(http.MethodPost, BasePath+"/test/session", `{"user":"bob","password":"`+v[canary.Password]+`"}`)

	// The job runs; the agent's output carries secrets in its messages.
	f.disp.Connect("env-1")
	if err := f.eng.DispatchPending(f.ctx); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range f.disp.Drain("env-1") {
		for _, fr := range []struct {
			typ protocol.Type
			p   any
		}{
			{protocol.TypeAck, protocol.AckPayload{Accepted: true}},
			{protocol.TypeResult, protocol.ResultPayload{Outcome: "partial", Message: "env " + v[canary.EnvValue],
				Items: []protocol.ItemPayload{{Name: "db", Status: domain.ItemFailed, Message: "auth " + v[canary.RegistryCredential]}}}},
		} {
			frame, err := protocol.NewFrame(fr.typ, string(fr.typ)+"-"+cmd.ID, cmd.ID, cmd.Ref(), fr.p)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.eng.HandleAgentFrame(f.ctx, "env-1", frame); err != nil {
				t.Fatal(err)
			}
		}
	}

	recs := f.records()
	var sawRotate, sawFinished, sawSignIn bool
	for _, r := range recs {
		switch r.Action {
		case "test_credential_rotation.create":
			sawRotate = sawRotate || strings.Contains(string(r.Details), `"username":"robot"`)
		case audit.ActionJobFinished:
			sawFinished = r.Outcome == domain.AuditPartial
		case "test_session.create":
			sawSignIn = r.Outcome == domain.AuditDenied
		}
	}
	if !sawRotate || !sawFinished || !sawSignIn {
		t.Fatalf("expected records missing (rotate %v, finished %v, sign-in %v): %+v", sawRotate, sawFinished, sawSignIn, recs)
	}

	c.AssertClean(t, "audit_events rows", dumpAuditTable(t, f))
	c.AssertClean(t, "audit records", recs)
	list := f.do(http.MethodGet, BasePath+"/audit?limit=200", "", "X-Test-User", "owner")
	if list.Code != http.StatusOK {
		t.Fatalf("list %d", list.Code)
	}
	c.CheckRecorder(t, list)
	for _, format := range []string{"ndjson", "csv"} {
		exp := f.do(http.MethodGet, BasePath+"/audit/exports?format="+format, "", "X-Test-User", "owner")
		if exp.Code != http.StatusOK || !strings.Contains(exp.Body.String(), "test_credential_rotation.create") {
			t.Fatalf("%s export %d", format, exp.Code)
		}
		c.CheckRecorder(t, exp)
	}
	if !strings.Contains(f.mirror.String(), `"msg":"audit"`) {
		t.Fatalf("mirror empty: %s", f.mirror)
	}
	c.AssertClean(t, "mirrored audit log", f.mirror.String())
	if rep, err := f.log.Verify(f.ctx); err != nil || !rep.OK {
		t.Fatalf("verify %+v %v", rep, err)
	}
}

func dumpAuditTable(t *testing.T, f *auditFixture) string {
	t.Helper()
	rows, err := f.db.QueryContext(f.ctx, `SELECT * FROM audit_events ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	cols, _ := rows.Columns()
	var sb strings.Builder
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(vals)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return sb.String()
}
