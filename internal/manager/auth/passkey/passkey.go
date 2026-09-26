// Package passkey configures go-webauthn for Docker Manager (#16, #18).
//
// The library owns the WebAuthn ceremonies: challenge generation, client
// data (type, challenge, origin) checks, RP ID hash, user presence and
// verification flags, signature verification, the sign counter and the
// backup eligibility/state consistency rules. Docker Manager owns the credential
// records, the ceremony state between begin and finish (in the server-side
// session), naming/listing/revocation, policy and recovery.
//
// The relying party is derived from DOCKER_MANAGER_PUBLIC_URL (#27) only: the RP
// ID is its host name and the only accepted origin is that exact origin.
// Requests reaching the manager on another address (the internal
// http://docker-manager:8080, a different proxy host name) can therefore
// never complete a ceremony, and credentials stay bound to the public host
// name: changing DOCKER_MANAGER_PUBLIC_URL's host invalidates existing passkeys
// (docs/deployment.md).
package passkey

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// CeremonyTimeout bounds how long a begun registration or sign-in stays
// valid (sent to the browser and enforced by the server).
const CeremonyTimeout = 5 * time.Minute

// Errors. Callers answer all of them with one generic failure.
var (
	// ErrVerification: the response failed WebAuthn verification (wrong
	// origin/RP ID, challenge, signature, missing user verification, ...).
	ErrVerification = errors.New("passkey: verification failed")
	// ErrCloneWarning: the sign counter did not increase; the credential
	// may have been cloned. The sign-in is refused.
	ErrCloneWarning = errors.New("passkey: signature counter did not increase (possible cloned authenticator)")
	// ErrUnknownCredential: no account owns the presented credential.
	ErrUnknownCredential = errors.New("passkey: unknown credential")
)

// User is a Docker Manager account as seen by WebAuthn.
type User struct {
	// Handle is the random, stable WebAuthn user handle (not the user ID,
	// so authenticators never learn Docker Manager identifiers).
	Handle      []byte
	Name        string
	DisplayName string
	Credentials []webauthn.Credential
}

// WebAuthnID implements webauthn.User.
func (u *User) WebAuthnID() []byte { return u.Handle }

// WebAuthnName implements webauthn.User.
func (u *User) WebAuthnName() string { return u.Name }

// WebAuthnDisplayName implements webauthn.User.
func (u *User) WebAuthnDisplayName() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Name
}

// WebAuthnCredentials implements webauthn.User.
func (u *User) WebAuthnCredentials() []webauthn.Credential { return u.Credentials }

// RelyingParty is Docker Manager's WebAuthn relying party.
type RelyingParty struct {
	id     string
	origin string
	w      *webauthn.WebAuthn
}

// RPFor derives the RP ID and origin from the public URL.
func RPFor(publicURL *url.URL) (id, origin string, err error) {
	if publicURL == nil || publicURL.Hostname() == "" {
		return "", "", errors.New("passkey: DOCKER_MANAGER_PUBLIC_URL is required")
	}
	return publicURL.Hostname(), publicURL.Scheme + "://" + publicURL.Host, nil
}

// New returns the relying party for publicURL. Every ceremony requires a
// discoverable credential (passkey) and user verification.
func New(publicURL *url.URL) (*RelyingParty, error) {
	id, origin, err := RPFor(publicURL)
	if err != nil {
		return nil, err
	}
	w, err := webauthn.New(&webauthn.Config{
		RPID:                  id,
		RPDisplayName:         "Docker Manager",
		RPOrigins:             []string{origin},
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: CeremonyTimeout, TimeoutUVD: CeremonyTimeout},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: CeremonyTimeout, TimeoutUVD: CeremonyTimeout},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("passkey: relying party for %s: %w", origin, err)
	}
	return &RelyingParty{id: id, origin: origin, w: w}, nil
}

// ID is the RP ID (the public host name).
func (rp *RelyingParty) ID() string { return rp.id }

// Origin is the only accepted origin.
func (rp *RelyingParty) Origin() string { return rp.origin }

// BeginRegistration starts adding a passkey to u. The returned options go
// to navigator.credentials.create(); the session data must be kept
// server-side until FinishRegistration and used once.
func (rp *RelyingParty) BeginRegistration(u *User) (*protocol.CredentialCreation, *webauthn.SessionData, error) {
	return rp.w.BeginRegistration(u,
		webauthn.WithExclusions(webauthn.Credentials(u.Credentials).CredentialDescriptors()),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
	)
}

// FinishRegistration verifies the browser's attestation response (JSON).
func (rp *RelyingParty) FinishRegistration(u *User, session webauthn.SessionData, response []byte) (*webauthn.Credential, error) {
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrVerification, err)
	}
	cred, err := rp.w.CreateCredential(u, session, parsed)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrVerification, err)
	}
	if !cred.Flags.UserVerified {
		return nil, fmt.Errorf("%w: user verification missing", ErrVerification)
	}
	return cred, nil
}

// BeginLogin starts a sign-in with any discoverable passkey (username-less:
// the options never reveal which accounts or credentials exist).
func (rp *RelyingParty) BeginLogin() (*protocol.CredentialAssertion, *webauthn.SessionData, error) {
	return rp.w.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
}

// BeginUserLogin starts an assertion restricted to u's passkeys (second
// factor after a password, step-up).
func (rp *RelyingParty) BeginUserLogin(u *User) (*protocol.CredentialAssertion, *webauthn.SessionData, error) {
	return rp.w.BeginLogin(u, webauthn.WithUserVerification(protocol.VerificationRequired))
}

// Lookup resolves the account owning a credential from the assertion's
// credential ID and user handle. It returns ErrUnknownCredential when
// there is none.
type Lookup func(credentialID, userHandle []byte) (*User, error)

// FinishLogin verifies an assertion (JSON). For a discoverable ceremony
// (BeginLogin) lookup finds the account; for BeginUserLogin pass the
// expected user as want (lookup may be nil). It returns the account and
// the credential with its updated counter and flags, which the caller must
// store.
func (rp *RelyingParty) FinishLogin(session webauthn.SessionData, response []byte, want *User, lookup Lookup) (*User, *webauthn.Credential, error) {
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrVerification, err)
	}
	var (
		user *User
		cred *webauthn.Credential
	)
	if want != nil {
		user = want
		cred, err = rp.w.ValidateLogin(want, session, parsed)
	} else {
		if lookup == nil {
			return nil, nil, errors.New("passkey: lookup is required for a discoverable sign-in")
		}
		var u webauthn.User
		u, cred, err = rp.w.ValidatePasskeyLogin(func(rawID, handle []byte) (webauthn.User, error) {
			found, err := lookup(rawID, handle)
			if err != nil {
				return nil, err
			}
			return found, nil
		}, session, parsed)
		if err == nil {
			user, _ = u.(*User)
		}
	}
	if err != nil {
		if errors.Is(err, ErrUnknownCredential) {
			return nil, nil, ErrUnknownCredential
		}
		return nil, nil, fmt.Errorf("%w: %w", ErrVerification, err)
	}
	if user == nil || !bytes.Equal(user.Handle, parsed.Response.UserHandle) && len(parsed.Response.UserHandle) > 0 {
		return nil, nil, ErrVerification
	}
	if cred.Authenticator.CloneWarning {
		return nil, nil, ErrCloneWarning
	}
	if !cred.Flags.UserVerified {
		return nil, nil, fmt.Errorf("%w: user verification missing", ErrVerification)
	}
	return user, cred, nil
}
