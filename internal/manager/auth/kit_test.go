package auth

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/auth/password"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/auth/totp"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store/storetest"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

var cheapParams = &password.Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

func noSessionError(http.ResponseWriter, *http.Request, error) {}

func TestNewKitValidates(t *testing.T) {
	db := storetest.Migrated(t)
	pub, _ := url.Parse("https://docker.example.com")
	if _, err := NewKit(KitOptions{PublicURL: pub, SessionError: noSessionError}); err == nil {
		t.Error("missing database accepted")
	}
	if _, err := NewKit(KitOptions{DB: db, SessionError: noSessionError, PasswordParams: cheapParams}); err == nil {
		t.Error("missing public URL accepted (no WebAuthn relying party)")
	}
	if _, err := NewKit(KitOptions{DB: db, PublicURL: pub, PasswordParams: cheapParams}); err == nil {
		t.Error("missing session error handler accepted")
	}
	k, err := NewKit(KitOptions{DB: db, PublicURL: pub, SessionError: noSessionError, PasswordParams: cheapParams})
	if err != nil {
		t.Fatal(err)
	}
	if k.RP.ID() != "docker.example.com" || k.Sessions.Cookie.Name != "__Host-docker_manager_session" || k.CSRF == nil || k.IPLimit == nil || k.AccountLimit == nil {
		t.Fatalf("kit %+v", k)
	}
	if k.IdleTimeout != time.Hour || k.Lifetime != 24*time.Hour || k.Sessions.IdleTimeout != 0 || k.Sessions.Lifetime != 24*time.Hour {
		t.Fatalf("session limits: kit %v/%v, scs %v/%v", k.IdleTimeout, k.Lifetime, k.Sessions.IdleTimeout, k.Sessions.Lifetime)
	}
	if _, err := NewKit(KitOptions{DB: db, PublicURL: pub, SessionError: noSessionError, PasswordParams: cheapParams, IdleTimeout: 2 * time.Hour, Lifetime: time.Hour}); err == nil {
		t.Error("idle timeout above lifetime accepted")
	}
}

func TestHousekeepingSweepsExpiredSessions(t *testing.T) {
	db := storetest.Migrated(t)
	clk := testutil.FakeClock()
	pub, _ := url.Parse("https://docker.example.com")
	k, err := NewKit(KitOptions{DB: db, Clock: clk, PublicURL: pub, SessionError: noSessionError, PasswordParams: cheapParams})
	if err != nil {
		t.Fatal(err)
	}
	ctx := testutil.Context(t)
	// Session rows expire on the wall clock (SCS's clock), not the injected one.
	if err := k.SessionStore.CommitCtx(ctx, "old", []byte("x"), time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := k.SessionStore.CommitCtx(ctx, "live", []byte("y"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	k.sweep(ctx)
	var n int
	if err := db.NewRaw("SELECT count(*) FROM sessions").Scan(ctx, &n); err != nil || n != 1 {
		t.Fatalf("sessions after sweep: %d %v", n, err)
	}

	// The loop ticks on the injected clock and stops with its context.
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		k.RunHousekeeping(runCtx)
	}()
	if err := clk.BlockUntilWaiters(ctx, 1); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
}

func TestKitTOTPUsesInjectedClock(t *testing.T) {
	clk := testutil.FakeClock()
	pub, _ := url.Parse("https://docker.example.com")
	k, err := NewKit(KitOptions{DB: storetest.Migrated(t), Clock: clk, PublicURL: pub, SessionError: noSessionError, PasswordParams: cheapParams})
	if err != nil {
		t.Fatal(err)
	}
	key, err := k.NewTOTP("alice")
	if err != nil {
		t.Fatal(err)
	}
	code, _ := totp.Code(key.Secret, clk.Now())
	step, ok, err := k.VerifyTOTP(key.Secret, code, 0)
	if !ok || err != nil || step != totp.Step(clk.Now()) {
		t.Fatalf("verify: %d %v %v", step, ok, err)
	}
	if _, ok, _ := k.VerifyTOTP(key.Secret, code, step); ok {
		t.Fatal("replay accepted")
	}
	clk.Advance(5 * totp.Period)
	if _, ok, _ := k.VerifyTOTP(key.Secret, code, 0); ok {
		t.Fatal("stale code accepted after the clock moved")
	}
}
