package passkey

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"testing"

	"github.com/descope/virtualwebauthn"
	"github.com/go-webauthn/webauthn/webauthn"
)

// The public origin behind the reverse proxy (#27) and the manager's
// internal address, which must never complete a ceremony.
const (
	publicURL   = "https://docker.example.com"
	internalURL = "http://docker-manager:8080"
)

func newRP(t *testing.T, raw string) *RelyingParty {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	rp, err := New(u)
	if err != nil {
		t.Fatal(err)
	}
	return rp
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// device is a software authenticator holding one passkey for user.
type device struct {
	rp   virtualwebauthn.RelyingParty
	auth virtualwebauthn.Authenticator
	cred virtualwebauthn.Credential
}

func newDevice(rpID, origin string, user *User, opts virtualwebauthn.AuthenticatorOptions) *device {
	opts.UserHandle = user.Handle
	return &device{
		rp:   virtualwebauthn.RelyingParty{ID: rpID, Name: "Docker Manager", Origin: origin},
		auth: virtualwebauthn.NewAuthenticatorWithOptions(opts),
		cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
	}
}

func (d *device) register(t *testing.T, rp *RelyingParty, u *User) (*webauthn.Credential, error) {
	t.Helper()
	creation, session, err := rp.BeginRegistration(u)
	if err != nil {
		t.Fatal(err)
	}
	opts, err := virtualwebauthn.ParseAttestationOptions(toJSON(t, creation))
	if err != nil {
		t.Fatal(err)
	}
	resp := virtualwebauthn.CreateAttestationResponse(d.rp, d.auth, d.cred, *opts)
	return rp.FinishRegistration(u, *session, []byte(resp))
}

// assert answers a discoverable sign-in; it increments the device counter
// like a real authenticator.
func (d *device) assert(t *testing.T, rp *RelyingParty, lookup Lookup) (*User, *webauthn.Credential, error) {
	t.Helper()
	assertion, session, err := rp.BeginLogin()
	if err != nil {
		t.Fatal(err)
	}
	if len(assertion.Response.AllowedCredentials) != 0 {
		t.Fatal("discoverable sign-in options list credentials (account enumeration)")
	}
	opts, err := virtualwebauthn.ParseAssertionOptions(toJSON(t, assertion))
	if err != nil {
		t.Fatal(err)
	}
	d.cred.Counter++
	resp := virtualwebauthn.CreateAssertionResponse(d.rp, d.auth, d.cred, *opts)
	return rp.FinishLogin(*session, []byte(resp), nil, lookup)
}

func alice() *User {
	return &User{Handle: bytes.Repeat([]byte{7}, 32), Name: "alice", DisplayName: "Alice"}
}

func lookupFor(users ...*User) Lookup {
	return func(credID, handle []byte) (*User, error) {
		for _, u := range users {
			if !bytes.Equal(u.Handle, handle) {
				continue
			}
			for _, c := range u.Credentials {
				if bytes.Equal(c.ID, credID) {
					return u, nil
				}
			}
		}
		return nil, ErrUnknownCredential
	}
}

func TestRPFromPublicURL(t *testing.T) {
	rp := newRP(t, "https://docker.example.com:8443")
	if rp.ID() != "docker.example.com" || rp.Origin() != "https://docker.example.com:8443" {
		t.Fatalf("rp %s %s", rp.ID(), rp.Origin())
	}
	if _, err := New(nil); err == nil {
		t.Fatal("nil public URL accepted")
	}
}

// TestRegistrationAndSignIn: a software authenticator registers a passkey
// and signs in with it; counter and backup flags are recorded.
func TestRegistrationAndSignIn(t *testing.T) {
	rp := newRP(t, publicURL)
	u := alice()
	d := newDevice("docker.example.com", publicURL, u, virtualwebauthn.AuthenticatorOptions{BackupEligible: true, BackupState: true})
	cred, err := d.register(t, rp, u)
	if err != nil {
		t.Fatal(err)
	}
	if !cred.Flags.UserVerified || !cred.Flags.BackupEligible || !cred.Flags.BackupState || !bytes.Equal(cred.ID, d.cred.ID) {
		t.Fatalf("credential flags %+v", cred.Flags)
	}
	u.Credentials = append(u.Credentials, *cred)

	got, signed, err := d.assert(t, rp, lookupFor(u))
	if err != nil {
		t.Fatal(err)
	}
	if got != u || signed.Authenticator.SignCount != 1 {
		t.Fatalf("signed in as %v, counter %d", got, signed.Authenticator.SignCount)
	}
	u.Credentials[0] = *signed

	// Second sign-in bound to the known user (second factor / step-up).
	assertion, session, err := rp.BeginUserLogin(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(assertion.Response.AllowedCredentials) != 1 {
		t.Fatalf("allow list %v", assertion.Response.AllowedCredentials)
	}
	opts, _ := virtualwebauthn.ParseAssertionOptions(toJSON(t, assertion))
	d.cred.Counter++
	resp := virtualwebauthn.CreateAssertionResponse(d.rp, d.auth, d.cred, *opts)
	if _, c2, err := rp.FinishLogin(*session, []byte(resp), u, nil); err != nil || c2.Authenticator.SignCount != 2 {
		t.Fatalf("user sign-in: %v %+v", err, c2)
	}

	// Registering the same authenticator again is excluded.
	creation, _, err := rp.BeginRegistration(u)
	if err != nil || len(creation.Response.CredentialExcludeList) != 1 {
		t.Fatalf("exclude list %v %v", creation.Response.CredentialExcludeList, err)
	}
}

// TestReverseProxyOriginAndRPIDMismatch: ceremonies performed against the
// manager's internal address or a different RP ID never verify, in either
// direction (registration and sign-in).
func TestReverseProxyOriginAndRPIDMismatch(t *testing.T) {
	rp := newRP(t, publicURL)
	cases := map[string]struct{ rpID, origin string }{
		"internal origin, public RP ID":  {"docker.example.com", internalURL},
		"internal RP ID and origin":      {"docker-manager", internalURL},
		"public origin, other RP ID":     {"example.com", publicURL},
		"http scheme of the public host": {"docker.example.com", "http://docker.example.com"},
		"other port of the public host":  {"docker.example.com", "https://docker.example.com:8443"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			u := alice()
			bad := newDevice(c.rpID, c.origin, u, virtualwebauthn.AuthenticatorOptions{})
			if _, err := bad.register(t, rp, u); !errors.Is(err, ErrVerification) {
				t.Fatalf("registration accepted: %v", err)
			}
			// A legitimately registered passkey used through the wrong
			// origin/RP ID is rejected too.
			good := newDevice("docker.example.com", publicURL, u, virtualwebauthn.AuthenticatorOptions{})
			cred, err := good.register(t, rp, u)
			if err != nil {
				t.Fatal(err)
			}
			u.Credentials = []webauthn.Credential{*cred}
			good.rp = virtualwebauthn.RelyingParty{ID: c.rpID, Name: "Docker Manager", Origin: c.origin}
			if _, _, err := good.assert(t, rp, lookupFor(u)); !errors.Is(err, ErrVerification) {
				t.Fatalf("sign-in accepted: %v", err)
			}
		})
	}
}

func TestUserVerificationRequired(t *testing.T) {
	rp := newRP(t, publicURL)
	u := alice()
	noUV := newDevice("docker.example.com", publicURL, u, virtualwebauthn.AuthenticatorOptions{UserNotVerified: true})
	if _, err := noUV.register(t, rp, u); !errors.Is(err, ErrVerification) {
		t.Fatalf("registration without user verification: %v", err)
	}
	d := newDevice("docker.example.com", publicURL, u, virtualwebauthn.AuthenticatorOptions{})
	cred, err := d.register(t, rp, u)
	if err != nil {
		t.Fatal(err)
	}
	u.Credentials = []webauthn.Credential{*cred}
	d.auth.Options.UserNotVerified = true
	if _, _, err := d.assert(t, rp, lookupFor(u)); !errors.Is(err, ErrVerification) {
		t.Fatalf("sign-in without user verification: %v", err)
	}
}

// TestCounterAndChallengeReplay: a stale counter (cloned authenticator) is
// refused, and a captured assertion does not verify against a new challenge.
func TestCounterAndChallengeReplay(t *testing.T) {
	rp := newRP(t, publicURL)
	u := alice()
	d := newDevice("docker.example.com", publicURL, u, virtualwebauthn.AuthenticatorOptions{})
	cred, err := d.register(t, rp, u)
	if err != nil {
		t.Fatal(err)
	}
	u.Credentials = []webauthn.Credential{*cred}
	_, signed, err := d.assert(t, rp, lookupFor(u))
	if err != nil {
		t.Fatal(err)
	}
	u.Credentials[0] = *signed

	// A clone replays the same counter value.
	d.cred.Counter--
	if _, _, err := d.assert(t, rp, lookupFor(u)); !errors.Is(err, ErrCloneWarning) {
		t.Fatalf("stale counter: %v", err)
	}

	// Capture a valid assertion for one challenge, replay it on another.
	a1, s1, _ := rp.BeginLogin()
	o1, _ := virtualwebauthn.ParseAssertionOptions(toJSON(t, a1))
	d.cred.Counter += 5
	captured := virtualwebauthn.CreateAssertionResponse(d.rp, d.auth, d.cred, *o1)
	_, s2, _ := rp.BeginLogin()
	if _, _, err := rp.FinishLogin(*s2, []byte(captured), nil, lookupFor(u)); !errors.Is(err, ErrVerification) {
		t.Fatalf("assertion replayed on a new challenge: %v", err)
	}
	if _, _, err := rp.FinishLogin(*s1, []byte(captured), nil, lookupFor(u)); err != nil {
		t.Fatalf("original ceremony: %v", err)
	}
}

func TestUnknownCredential(t *testing.T) {
	rp := newRP(t, publicURL)
	u := alice()
	d := newDevice("docker.example.com", publicURL, u, virtualwebauthn.AuthenticatorOptions{})
	if _, err := d.register(t, rp, u); err != nil {
		t.Fatal(err)
	}
	// The account never stored the credential (e.g. it was revoked).
	if _, _, err := d.assert(t, rp, lookupFor(u)); err == nil {
		t.Fatal("revoked credential signed in")
	}
	if _, _, err := rp.FinishLogin(webauthn.SessionData{}, []byte("{"), nil, lookupFor(u)); !errors.Is(err, ErrVerification) {
		t.Fatalf("garbage: %v", err)
	}
}
