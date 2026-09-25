package auth

import (
	"testing"

	"github.com/neurekadev/dockyard/internal/domain"
)

func TestEvaluatePolicyMatrix(t *testing.T) {
	const (
		pw = fPassword
		tp = fTOTP
		pk = fPasskey
	)
	type want struct {
		stage   domain.SessionStage
		next    factorSet
		missing factorSet
	}
	auth := want{stage: domain.StageAuthenticated}
	cases := []struct {
		name     string
		policy   domain.RequiredFactors
		enrolled factorSet
		proven   factorSet
		recovery bool
		want     want
	}{
		{"none: password", domain.FactorsNone, pw, pw, false, auth},
		{"none: passkey", domain.FactorsNone, pk, pk, false, auth},
		{"none: user enabled totp", domain.FactorsNone, pw | tp, pw, false, want{stage: domain.StageSecondFactor, next: tp}},
		{"none: password+totp", domain.FactorsNone, pw | tp, pw | tp, false, auth},
		{"none: passkey skips user totp", domain.FactorsNone, pw | tp | pk, pk, false, auth},
		{"totp: password only enrolled", domain.FactorsTOTP, pw, pw, false, want{stage: domain.StageEnrollment, missing: tp}},
		{"totp: pending", domain.FactorsTOTP, pw | tp, pw, false, want{stage: domain.StageSecondFactor, next: tp}},
		{"totp: recovery code", domain.FactorsTOTP, pw | tp, pw, true, auth},
		{"totp: passkey needs password+totp", domain.FactorsTOTP, pw | tp | pk, pk, false, want{stage: domain.StageSecondFactor, next: pw | tp}},
		{"passkey: none enrolled", domain.FactorsPasskey, pw, pw, false, want{stage: domain.StageEnrollment, missing: pk}},
		{"passkey: invite without password", domain.FactorsPasskey, 0, 0, false, want{stage: domain.StageEnrollment, missing: pk}},
		{"passkey: password then passkey", domain.FactorsPasskey, pw | pk, pw, false, want{stage: domain.StageSecondFactor, next: pk}},
		{"passkey: passkey", domain.FactorsPasskey, pk, pk, false, auth},
		{"either: nothing enrolled", domain.FactorsEither, pw, pw, false, want{stage: domain.StageEnrollment, missing: tp | pk}},
		{"either: totp enrolled", domain.FactorsEither, pw | tp, pw, false, want{stage: domain.StageSecondFactor, next: tp}},
		{"either: both enrolled", domain.FactorsEither, pw | tp | pk, pw, false, want{stage: domain.StageSecondFactor, next: tp | pk}},
		{"either: passkey", domain.FactorsEither, pw | pk, pk, false, auth},
		{"both: missing passkey after totp", domain.FactorsBoth, pw | tp, pw | tp, false, want{stage: domain.StageEnrollment, missing: pk}},
		{"both: totp enrolled but not proven", domain.FactorsBoth, pw | tp, pw, false, want{stage: domain.StageSecondFactor, next: tp}},
		{"both: pending both", domain.FactorsBoth, pw | tp | pk, pw, false, want{stage: domain.StageSecondFactor, next: tp | pk}},
		{"both: all", domain.FactorsBoth, pw | tp | pk, pw | tp | pk, false, auth},
		{"both: recovery replaces lost factors", domain.FactorsBoth, pw | tp | pk, pw, true, auth},
		{"both: passkey alone", domain.FactorsBoth, pw | tp | pk, pk, false, want{stage: domain.StageSecondFactor, next: pw | tp}},
	}
	for _, c := range cases {
		got := evaluate(c.policy, c.enrolled, c.proven, c.recovery)
		if got.stage != c.want.stage || got.next != c.want.next || got.missing != c.want.missing {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestEnrollmentComplete(t *testing.T) {
	cases := []struct {
		policy   domain.RequiredFactors
		enrolled factorSet
		ok       bool
	}{
		{domain.FactorsNone, fPassword, true},
		{domain.FactorsNone, fPasskey, true},
		{domain.FactorsNone, 0, false},
		{domain.FactorsTOTP, fPassword, false},
		{domain.FactorsTOTP, fPassword | fTOTP, true},
		{domain.FactorsPasskey, fPassword | fTOTP, false},
		{domain.FactorsEither, fPasskey, true},
		{domain.FactorsBoth, fPassword | fTOTP, false},
		{domain.FactorsBoth, fPassword | fTOTP | fPasskey, true},
	}
	for _, c := range cases {
		if got := enrollmentComplete(c.policy, c.enrolled); got != c.ok {
			t.Errorf("%s with %03b: %v, want %v", c.policy, c.enrolled, got, c.ok)
		}
	}
	if got := (fPassword | fPasskey).list(); len(got) != 2 || got[0] != domain.FactorPassword || got[1] != domain.FactorPasskey {
		t.Fatalf("list %v", got)
	}
}

func TestEnrollmentAllowlist(t *testing.T) {
	for _, p := range []string{"/api/v1/me", "/api/v1/me/password", "/api/v1/me/recovery-codes", "/api/v1/me/passkeys", "/api/v1/me/passkeys/x",
		"/api/v1/auth/session", "/api/v1/auth/totp/enrollments", "/api/v1/auth/passkeys/registration-options", "/api/v1/health", "/api/v1/setup/status"} {
		if !enrollmentAllowed(p) {
			t.Errorf("%s refused to enrollment sessions", p)
		}
	}
	for _, p := range []string{"/api/v1/jobs", "/api/v1/users", "/api/v1/invitations", "/api/v1/settings/security", "/api/v1/me/api-tokens", "/api/v1/mex", "/api/v1/environments"} {
		if enrollmentAllowed(p) {
			t.Errorf("%s open to enrollment sessions", p)
		}
	}
}

func TestCodes(t *testing.T) {
	a, b := newLinkCode(InvitationCodePrefix), newLinkCode(InvitationCodePrefix)
	if a == b || len(a) != len(InvitationCodePrefix)+43 || verifier(a) == verifier(b) || verifier(a) != verifier(" "+a+" ") {
		t.Fatalf("link codes %q %q", a, b)
	}
	rc := newRecoveryCode()
	if len(rc) != 19 || rc[4] != '-' || recoveryVerifier("u1", rc) != recoveryVerifier("u1", " "+rc[:4]+rc[5:]) ||
		recoveryVerifier("u1", rc) == recoveryVerifier("u2", rc) {
		t.Fatalf("recovery code %q", rc)
	}
}
