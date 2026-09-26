package agents

import (
	"net/http"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authsep"
)

// TestAgentEventsAreAudited: /agent/v1 events land in the audit trail
// (#30) with the agent as actor, stable error codes for refusals and no
// secret material; the chain verifies.
func TestAgentEventsAreAudited(t *testing.T) {
	f := newFixture(t)
	a := f.newAgent("ENG-A", "host-a")
	tok := f.createEnrollment(domain.EnrollmentSpec{})
	r := a.enroll(tok.Token)
	// Refusals: reused token, duplicate Engine, invalid credential.
	if code, _ := f.enrollHTTP(tok.Token, a.enrollRequest()); code != http.StatusUnauthorized {
		t.Fatalf("reuse: %d", code)
	}
	dup := f.newAgent("ENG-A", "host-a")
	if code, _ := f.enrollHTTP(f.createEnrollment(domain.EnrollmentSpec{}).Token, dup.enrollRequest()); code != http.StatusConflict {
		t.Fatalf("duplicate: %d", code)
	}
	if code := f.upgradeStatus(wrongSecret(t, r.Credential)); code != http.StatusUnauthorized {
		t.Fatalf("wrong credential: %d", code)
	}
	// Rotation completed by the agent over its session.
	a.start()
	f.waitOnline(r.EnvironmentID)
	if rot, err := f.svc.RotateCredential(f.ctx, r.AgentID); err != nil || rot.State != domain.RotationCompleted {
		t.Fatalf("rotation %+v %v", rot, err)
	}

	recs, err := f.audit.Records(f.ctx, domain.AuditFilter{Ascending: true})
	if err != nil {
		t.Fatal(err)
	}
	type key struct{ action, outcome, class, actor string }
	got := map[key]domain.AuditRecord{}
	for _, rec := range recs {
		got[key{rec.Action, string(rec.Outcome), rec.ErrorClass, string(rec.Actor.Kind)}] = rec
	}
	ok := got[key{AuditEnroll, "success", "", "agent"}]
	if ok.Actor.AgentID != r.AgentID || ok.EnvironmentID != r.EnvironmentID || ok.UserAgent == "" || ok.ClientIP == "" ||
		!strings.Contains(string(ok.Details), a.install) {
		t.Errorf("enroll success record %+v (%s)", ok, ok.Details)
	}
	for _, k := range []key{
		{AuditEnroll, "denied", "unauthenticated", "anonymous"},
		{AuditEnroll, "failure", domain.ConflictEngineAlreadyEnrolled, "anonymous"},
		{AuditSessionDenied, "denied", "unauthenticated", "anonymous"},
		{AuditRotation, "success", "", "agent"},
	} {
		if _, found := got[k]; !found {
			t.Errorf("no audit record %+v; have %v", k, keys(got))
		}
	}
	_, tokSecret, _ := authsep.ParseEnrollmentToken(tok.Token)
	cred, _ := a.store.Credential()
	_, credSecret, _ := authsep.ParseAgentCredential(cred.Credential)
	_, oldSecret, _ := authsep.ParseAgentCredential(r.Credential)
	for _, rec := range recs {
		all := string(rec.Details) + rec.UserAgent + rec.ErrorClass
		for _, tg := range rec.Targets {
			all += tg.ID
		}
		for _, secret := range []string{tokSecret, credSecret, oldSecret} {
			if strings.Contains(all, secret) {
				t.Fatalf("audit record %s contains a secret", rec.Action)
			}
		}
	}
	if rep, err := f.audit.Verify(f.ctx); err != nil || rep.Err() != nil {
		t.Fatalf("chain: %+v %v", rep, err)
	}
}

func keys[K comparable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
