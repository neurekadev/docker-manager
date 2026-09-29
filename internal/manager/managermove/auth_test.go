package managermove

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authsep"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// TestRequestSignature: a request signed with the code verifies once
// (a replay is refused), for its method and path only; another code, a
// changed signature and malformed headers are refused alike; a correct
// signature outside ±5 minutes answers clock skew. The header never
// carries the code or its secret.
func TestRequestSignature(t *testing.T) {
	const id = "0190a6e0-0000-7000-8000-000000000035"
	minted, _ := authsep.MintMoveCode(id)
	code := minted.Token
	_, secret, _ := authsep.ParseMoveCode(code)
	now := testutil.Epoch
	header, err := SignRequest(code, http.MethodPost, HandoffPath, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(header, "DMM "+id+":") || strings.Contains(header, secret) || strings.Contains(header, code) {
		t.Fatalf("header %q", header)
	}
	verify := func(h, method, path, withCode string, at time.Time, replay *replayCache) error {
		p, err := parseAuth(h)
		if err != nil {
			return err
		}
		return verifyAuthMAC(p, withCode, MoveAuth{Header: h, Method: method, Path: path}, at, replay)
	}
	replay := newReplayCache()
	if err := verify(header, http.MethodPost, HandoffPath, code, now.Add(time.Minute), replay); err != nil {
		t.Fatalf("valid: %v", err)
	}
	if err := verify(header, http.MethodPost, HandoffPath, code, now.Add(time.Minute), replay); !errors.Is(err, domain.ErrMoveCodeInvalid) {
		t.Fatalf("replay: %v", err)
	}
	other, _ := authsep.MintMoveCode(id)
	for name, err := range map[string]error{
		"other code":  verify(header, http.MethodPost, HandoffPath, other.Token, now, newReplayCache()),
		"other path":  verify(header, http.MethodPost, ConfirmPath, code, now, newReplayCache()),
		"other verb":  verify(header, http.MethodGet, HandoffPath, code, now, newReplayCache()),
		"changed mac": verify(header[:strings.LastIndex(header, ":")+1]+strings.Repeat("A", 43), http.MethodPost, HandoffPath, code, now, newReplayCache()),
		"bearer":      verify("Bearer "+code, http.MethodPost, HandoffPath, code, now, newReplayCache()),
		"short":       verify("DMM "+id+":1:n", http.MethodPost, HandoffPath, code, now, newReplayCache()),
		"bad time":    verify("DMM "+id+":soon:n:"+strings.Repeat("A", 43), http.MethodPost, HandoffPath, code, now, newReplayCache()),
		"empty":       verify("", http.MethodPost, HandoffPath, code, now, newReplayCache()),
	} {
		if !errors.Is(err, domain.ErrMoveCodeInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, d := range []time.Duration{AuthWindow + time.Second, -AuthWindow - time.Second} {
		if err := verify(header, http.MethodPost, HandoffPath, code, now.Add(d), newReplayCache()); !errors.Is(err, domain.ErrMoveClockSkew) {
			t.Errorf("%s off: %v", d, err)
		}
	}
	if err := verify(header, http.MethodPost, HandoffPath, code, now.Add(AuthWindow), newReplayCache()); err != nil {
		t.Errorf("at the window's edge: %v", err)
	}
}

// TestReplayCacheExpires: expired entries make room; a full cache of live
// entries refuses new ones.
func TestReplayCacheExpires(t *testing.T) {
	c := newReplayCache()
	now := testutil.Epoch
	for i := range maxReplayEntries {
		if !c.use("k"+strconv.Itoa(i), now.Add(time.Minute), now) {
			t.Fatalf("entry %d refused", i)
		}
	}
	if c.use("one more", now.Add(time.Minute), now) {
		t.Fatal("a full cache of live entries took another")
	}
	if !c.use("after expiry", now.Add(2*time.Minute), now.Add(time.Minute)) {
		t.Fatal("expired entries did not make room")
	}
}

// TestAuthenticateMoves: the service verifies a signature against the
// move's sealed code: unknown moves and ended moves (their code
// forgotten) are refused like a wrong signature.
func TestAuthenticateMoves(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	m, code := f.create()
	if id, got, err := f.svc.authenticate(f.ctx, f.signed(code, HandoffPath)); err != nil || id != m.ID || got != code {
		t.Fatalf("authenticate: %s %v", id, err)
	}
	unknown, _ := authsep.MintMoveCode("0190a6e0-0000-7000-8000-00000000dead")
	if _, _, err := f.svc.authenticate(f.ctx, f.signed(unknown.Token, HandoffPath)); !errors.Is(err, domain.ErrMoveCodeInvalid) {
		t.Fatalf("unknown move: %v", err)
	}
	if _, err := f.svc.Cancel(f.ctx, CancelRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.authenticate(f.ctx, f.signed(code, HandoffPath)); !errors.Is(err, domain.ErrMoveCodeInvalid) {
		t.Fatalf("cancelled move: %v", err)
	}
}
