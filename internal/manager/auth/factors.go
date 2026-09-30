package auth

import "github.com/neurekadev/docker-manager/internal/domain"

// factorSet is a set of sign-in factors.
type factorSet uint8

const (
	fPassword factorSet = 1 << iota
	fTOTP
	fPasskey
)

func (f factorSet) has(g factorSet) bool { return f&g == g }

func (f factorSet) list() []domain.Factor {
	out := []domain.Factor{}
	if f.has(fPassword) {
		out = append(out, domain.FactorPassword)
	}
	if f.has(fTOTP) {
		out = append(out, domain.FactorTOTP)
	}
	if f.has(fPasskey) {
		out = append(out, domain.FactorPasskey)
	}
	return out
}

// alternatives are the factor combinations that satisfy the instance
// policy for one account (any one alternative is enough):
//
//	none     password, or a passkey; an account that enrolled TOTP must
//	         add it to its password
//	totp     password + TOTP
//	passkey  a passkey (user verification makes it multi-factor)
//	either   password + TOTP, or a passkey
//	both     password + TOTP + passkey
func alternatives(policy domain.RequiredFactors, totpEnrolled bool) []factorSet {
	switch policy {
	case domain.FactorsTOTP:
		return []factorSet{fPassword | fTOTP}
	case domain.FactorsPasskey:
		return []factorSet{fPasskey}
	case domain.FactorsEither:
		return []factorSet{fPassword | fTOTP, fPasskey}
	case domain.FactorsBoth:
		return []factorSet{fPassword | fTOTP | fPasskey}
	}
	if totpEnrolled {
		return []factorSet{fPassword | fTOTP, fPasskey}
	}
	return []factorSet{fPassword, fPasskey}
}

// evaluation is the outcome of a sign-in step.
type evaluation struct {
	stage domain.SessionStage
	// next: factors that complete a pending sign-in.
	next factorSet
	// missing: factors to enroll (enrollment stage).
	missing factorSet
}

// evaluate decides the session stage from what the account has enrolled
// and what this sign-in proved. A used recovery code stands in for the
// TOTP and passkey factors of the sign-in it completes.
//
//   - Some alternative fully proven: authenticated.
//   - Otherwise, if some alternative can be completed with enrolled
//     factors: a pending sign-in asking for them.
//   - Otherwise, if some alternative lacks only factors the account has
//     not enrolled: a limited enrollment session.
//   - Otherwise: pending, asking for the enrolled factors still missing.
func evaluate(policy domain.RequiredFactors, enrolled, proven factorSet, recovery bool) evaluation {
	alts := alternatives(policy, enrolled.has(fTOTP))
	p := proven
	if recovery {
		p |= fTOTP | fPasskey
	}
	for _, a := range alts {
		if p.has(a) {
			return evaluation{stage: domain.StageAuthenticated}
		}
	}
	var next factorSet
	for _, a := range alts {
		need := a &^ p
		if need&^enrolled == 0 {
			next |= need
		}
	}
	if next != 0 {
		return evaluation{stage: domain.StageSecondFactor, next: next}
	}
	var missing factorSet
	for _, a := range alts {
		need := a &^ p
		if need&enrolled == 0 {
			missing |= need
		}
	}
	if missing != 0 {
		return evaluation{stage: domain.StageEnrollment, missing: missing}
	}
	for _, a := range alts {
		next |= (a &^ p) & enrolled
	}
	return evaluation{stage: domain.StageSecondFactor, next: next}
}

// enrollmentComplete reports whether enrolled factors can satisfy the
// policy on their own (so removing a factor never strands an account).
func enrollmentComplete(policy domain.RequiredFactors, enrolled factorSet) bool {
	for _, a := range alternatives(policy, enrolled.has(fTOTP)) {
		if enrolled.has(a) {
			return true
		}
	}
	return false
}
