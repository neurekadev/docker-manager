package regclient

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/imageref"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/regclient/regtest"
	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

const manifestBody = regtest.ManifestBody

func newClient(t *testing.T, f *regtest.Registry, clk clock.Clock) *Client {
	t.Helper()
	return New(Options{HTTP: f.Client(), Clock: clk, Logger: testutil.Logger(t), Jitter: func() float64 { return 0 }})
}

func ref(t *testing.T, f *regtest.Registry, repoTag string) imageref.Ref {
	t.Helper()
	r, err := imageref.Parse(f.Host() + "/" + repoTag)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func cred(f *regtest.Registry) *Credential {
	return &Credential{Username: f.User, Secret: logging.Secret(f.Password)}
}

func wantClass(t *testing.T, err error, class string) Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Class != class {
		t.Fatalf("error = %v, want class %s", err, class)
	}
	return *e
}

func TestBearerAuthPrivateAndPublic(t *testing.T) {
	f := regtest.New(t, regtest.AuthBearer, "robot", "correct-horse-battery-canary")
	f.Private["team/private"] = true
	d := f.Put("team/private", "1.0", MediaOCIManifest, []byte(manifestBody))
	f.Put("team/public", "1.0", MediaOCIManifest, []byte(manifestBody))
	c := newClient(t, f, testutil.FakeClock())
	ctx := testutil.Context(t)

	// Public image anonymously: anonymous token, then the digest.
	res, err := c.Resolve(ctx, Request{Ref: ref(t, f, "team/public:1.0")})
	if err != nil || res.Digest != d || res.PlatformDigest != d || res.MediaType != MediaOCIManifest {
		t.Fatalf("public: %+v, %v", res, err)
	}

	// Private image without a credential: unauthorized.
	_, err = c.Resolve(ctx, Request{Ref: ref(t, f, "team/private:1.0")})
	e := wantClass(t, err, ClassUnauthorized)
	if !strings.Contains(e.Message, "no registry connection matches") {
		t.Fatalf("message %q", e.Message)
	}

	// With the credential.
	res, err = c.Resolve(ctx, Request{Ref: ref(t, f, "team/private:1.0"), Credential: cred(f), CredentialKey: "conn1/1"})
	if err != nil || res.Digest != d {
		t.Fatalf("private: %+v, %v", res, err)
	}
	_, tokens, _ := f.Counts()

	// The token is reused for a fresh check with the same credential.
	if _, err := c.Resolve(ctx, Request{Ref: ref(t, f, "team/private:1.0"), Credential: cred(f), CredentialKey: "conn1/1", Fresh: true}); err != nil {
		t.Fatal(err)
	}
	if _, again, _ := f.Counts(); again != tokens {
		t.Fatalf("token fetched again: %d -> %d", tokens, again)
	}
}

func TestWrongCredentialNeverFallsBackToAnonymous(t *testing.T) {
	secrets := canary.New()
	f := regtest.New(t, regtest.AuthBearer, "robot", "correct-horse-battery-canary")
	f.Put("team/public", "1.0", MediaOCIManifest, []byte(manifestBody))
	c := New(Options{HTTP: f.Client(), Clock: testutil.FakeClock(), Logger: secrets.CaptureLogger(t)})
	wrong := secrets.New(canary.RegistryCredential, "wrong registry token")
	_, _, anonBefore := f.Counts()
	_, err := c.Resolve(testutil.Context(t), Request{Ref: ref(t, f, "team/public:1.0"),
		Credential: &Credential{Username: "robot", Secret: logging.Secret(wrong)}, CredentialKey: "c/1"})
	e := wantClass(t, err, ClassUnauthorized)
	if !strings.Contains(e.Message, "rejected the credential") {
		t.Fatalf("message %q", e.Message)
	}
	// Exactly one unauthenticated probe (the challenge), no anonymous
	// retry although the repository is public.
	if _, _, anon := f.Counts(); anon-anonBefore != 1 {
		t.Fatalf("anonymous manifest requests = %d, want 1 (the challenge probe)", anon-anonBefore)
	}
	secrets.AssertClean(t, "error", err.Error())
}

func TestBasicAuth(t *testing.T) {
	f := regtest.New(t, regtest.AuthBasic, "robot", "correct-horse-battery-canary")
	d := f.Put("app", "v1", MediaDockerV2, []byte(manifestBody))
	c := newClient(t, f, testutil.FakeClock())
	ctx := testutil.Context(t)
	if _, err := c.Resolve(ctx, Request{Ref: ref(t, f, "app:v1")}); ClassOf(err) != ClassUnauthorized {
		t.Fatalf("anonymous: %v", err)
	}
	res, err := c.Resolve(ctx, Request{Ref: ref(t, f, "app:v1"), Credential: cred(f), CredentialKey: "k"})
	if err != nil || res.Digest != d {
		t.Fatalf("basic: %+v, %v", res, err)
	}
}

func TestForbiddenAndNotFound(t *testing.T) {
	f := regtest.New(t, regtest.AuthBearer, "robot", "correct-horse-battery-canary")
	f.Denied["team/secret"] = true
	f.Put("team/secret", "1", MediaOCIManifest, []byte(manifestBody))
	c := newClient(t, f, testutil.FakeClock())
	ctx := testutil.Context(t)
	_, err := c.Resolve(ctx, Request{Ref: ref(t, f, "team/secret:1"), Credential: cred(f), CredentialKey: "k"})
	e := wantClass(t, err, ClassForbidden)
	if e.Status != http.StatusForbidden {
		t.Fatalf("%+v", e)
	}
	_, err = c.Resolve(ctx, Request{Ref: ref(t, f, "team/missing:1"), Credential: cred(f), CredentialKey: "k"})
	wantClass(t, err, ClassNotFound)
}

func TestRateLimitHonorsRetryAfter(t *testing.T) {
	f := regtest.New(t, regtest.AuthNone, "robot", "correct-horse-battery-canary")
	d := f.Put("app", "1", MediaOCIManifest, []byte(manifestBody))
	clk := testutil.FakeClock()
	c := newClient(t, f, clk)
	ctx := testutil.Context(t)
	f.FailNext(http.StatusTooManyRequests, map[string]string{"Retry-After": "7"}, 1)

	type out struct {
		res Result
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := c.Resolve(ctx, Request{Ref: ref(t, f, "app:1")})
		done <- out{res, err}
	}()
	if err := clk.BlockUntilWaiters(ctx, 1); err != nil {
		t.Fatal(err)
	}
	select {
	case o := <-done:
		t.Fatalf("returned before Retry-After elapsed: %+v", o)
	default:
	}
	clk.Advance(6 * time.Second)
	select {
	case o := <-done:
		t.Fatalf("returned before Retry-After elapsed: %+v", o)
	default:
	}
	clk.Advance(time.Second)
	o := <-done
	if o.err != nil || o.res.Digest != d {
		t.Fatalf("%+v", o)
	}
	if n, _, _ := f.Counts(); n != 2 {
		t.Fatalf("manifest requests = %d, want 2", n)
	}
}

func TestLongRetryAfterFailsFastAndCoolsDown(t *testing.T) {
	f := regtest.New(t, regtest.AuthNone, "robot", "correct-horse-battery-canary")
	f.Put("app", "1", MediaOCIManifest, []byte(manifestBody))
	clk := testutil.FakeClock()
	c := newClient(t, f, clk)
	ctx := testutil.Context(t)
	f.FailNext(http.StatusTooManyRequests, map[string]string{"Retry-After": "3600"}, 1)

	_, err := c.Resolve(ctx, Request{Ref: ref(t, f, "app:1")})
	e := wantClass(t, err, ClassRateLimited)
	if e.RetryAfter != time.Hour || !strings.Contains(e.Message, "never removes them") {
		t.Fatalf("%+v", e)
	}
	// During the cooldown no request reaches the registry, for any
	// repository on the host (no retry storm).
	for _, r := range []string{"app:1", "other:2"} {
		_, err = c.Resolve(ctx, Request{Ref: ref(t, f, r), Fresh: true})
		wantClass(t, err, ClassRateLimited)
	}
	if n, _, _ := f.Counts(); n != 1 {
		t.Fatalf("manifest requests during cooldown = %d, want 1", n)
	}
	clk.Advance(time.Hour)
	if _, err := c.Resolve(ctx, Request{Ref: ref(t, f, "app:1")}); err != nil {
		t.Fatal(err)
	}
}

func TestOutageBacksOffThenGivesUp(t *testing.T) {
	f := regtest.New(t, regtest.AuthNone, "robot", "correct-horse-battery-canary")
	f.Put("app", "1", MediaOCIManifest, []byte(manifestBody))
	clk := testutil.FakeClock()
	c := newClient(t, f, clk)
	ctx := testutil.Context(t)
	f.FailNext(http.StatusServiceUnavailable, nil, 3)

	done := make(chan error, 1)
	go func() {
		_, err := c.Resolve(ctx, Request{Ref: ref(t, f, "app:1")})
		done <- err
	}()
	// Jitter 0: waits are a tenth of 1 s and 2 s.
	for _, step := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond} {
		if err := clk.BlockUntilWaiters(ctx, 1); err != nil {
			t.Fatal(err)
		}
		clk.Advance(step)
	}
	err := <-done
	e := wantClass(t, err, ClassUnavailable)
	if e.Status != http.StatusServiceUnavailable {
		t.Fatalf("%+v", e)
	}
	if n, _, _ := f.Counts(); n != 3 {
		t.Fatalf("attempts = %d, want 3", n)
	}
}

func TestBackoffIsJitteredAndBounded(t *testing.T) {
	c := New(Options{Jitter: func() float64 { return 0.999 }, BaseBackoff: time.Second, MaxBackoff: 8 * time.Second})
	prev := time.Duration(0)
	for attempt := 1; attempt <= 10; attempt++ {
		d := c.backoff(attempt)
		if d > 8*time.Second || d < prev && d < 8*time.Second*9/10 {
			t.Fatalf("attempt %d: %v (prev %v)", attempt, d, prev)
		}
		prev = d
	}
	low := New(Options{Jitter: func() float64 { return 0 }, BaseBackoff: time.Second})
	if d := low.backoff(1); d != 100*time.Millisecond {
		t.Fatalf("minimum backoff %v", d)
	}
}

func TestPlatformSelection(t *testing.T) {
	f := regtest.New(t, regtest.AuthNone, "robot", "correct-horse-battery-canary")
	amd := f.Put("app", "amd", MediaOCIManifest, []byte(manifestBody+" "))
	arm := f.Put("app", "arm", MediaOCIManifest, []byte(manifestBody+"  "))
	armv7 := f.Put("app", "armv7", MediaOCIManifest, []byte(manifestBody+"   "))
	index := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[` +
		`{"digest":"` + amd + `","platform":{"os":"linux","architecture":"amd64"}},` +
		`{"digest":"` + arm + `","platform":{"os":"linux","architecture":"arm64","variant":"v8"}},` +
		`{"digest":"` + armv7 + `","platform":{"os":"linux","architecture":"arm","variant":"v7"}},` +
		`{"digest":"sha256:att","platform":{"os":"unknown","architecture":"unknown"}}]}`
	idx := f.Put("app", "multi", MediaOCIIndex, []byte(index))
	c := newClient(t, f, testutil.FakeClock())
	ctx := testutil.Context(t)
	for platform, want := range map[string]string{"linux/amd64": amd, "linux/arm64": arm, "linux/arm64/v8": arm, "linux/arm/v7": armv7} {
		res, err := c.Resolve(ctx, Request{Ref: ref(t, f, "app:multi"), Platform: platform})
		if err != nil || res.Digest != idx || res.PlatformDigest != want {
			t.Errorf("%s: %+v, %v", platform, res, err)
		}
	}
	_, err := c.Resolve(ctx, Request{Ref: ref(t, f, "app:multi"), Platform: "linux/s390x"})
	wantClass(t, err, ClassPlatformNotFound)
	// No platform: the index digest.
	res, err := c.Resolve(ctx, Request{Ref: ref(t, f, "app:multi")})
	if err != nil || res.PlatformDigest != idx {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestMissingDigestHeaderFallsBackToGET(t *testing.T) {
	f := regtest.New(t, regtest.AuthNone, "robot", "correct-horse-battery-canary")
	f.NoDigest = true
	d := f.Put("app", "1", MediaOCIManifest, []byte(manifestBody))
	c := newClient(t, f, testutil.FakeClock())
	res, err := c.Resolve(testutil.Context(t), Request{Ref: ref(t, f, "app:1")})
	if err != nil || res.Digest != d {
		t.Fatalf("%+v, %v", res, err)
	}
	if f.HeadHits != 1 || f.GetHits != 1 {
		t.Fatalf("HEAD %d GET %d", f.HeadHits, f.GetHits)
	}
}

func TestCacheAndDeduplication(t *testing.T) {
	f := regtest.New(t, regtest.AuthNone, "robot", "correct-horse-battery-canary")
	f.Put("app", "1", MediaOCIManifest, []byte(manifestBody))
	clk := testutil.FakeClock()
	c := newClient(t, f, clk)
	ctx := testutil.Context(t)

	// Concurrent identical checks share one request.
	f.Block, f.Entered = make(chan struct{}), make(chan struct{}, 1)
	joined := make(chan struct{}, 8)
	c.onJoin = func() { joined <- struct{}{} }
	var wg sync.WaitGroup
	results := make([]Result, 5)
	check := func(i int) {
		res, err := c.Resolve(ctx, Request{Ref: ref(t, f, "app:1"), Platform: "linux/amd64"})
		if err != nil {
			t.Error(err)
		}
		results[i] = res
	}
	wg.Go(func() { check(0) })
	<-f.Entered
	for i := 1; i < len(results); i++ {
		wg.Go(func() { check(i) })
	}
	for i := 1; i < len(results); i++ {
		<-joined
	}
	close(f.Block)
	wg.Wait()
	f.Block = nil
	for i, r := range results {
		if r.Digest == "" || r.Cached != (i > 0) {
			t.Fatalf("result %d: %+v", i, r)
		}
	}
	if n, _, _ := f.Counts(); n != 1 {
		t.Fatalf("manifest requests = %d, want 1", n)
	}
	// Cached within the TTL, per registry/repository/tag/platform/credential.
	res, err := c.Resolve(ctx, Request{Ref: ref(t, f, "app:1"), Platform: "linux/amd64"})
	if err != nil || !res.Cached {
		t.Fatalf("%+v, %v", res, err)
	}
	for _, r := range []Request{
		{Ref: ref(t, f, "app:1"), Platform: "linux/arm64"},
		{Ref: ref(t, f, "app:1"), Platform: "linux/amd64", CredentialKey: "conn/2"},
	} {
		if res, err := c.Resolve(ctx, r); err != nil || res.Cached {
			t.Fatalf("%+v: %+v, %v", r, res, err)
		}
	}
	if n, _, _ := f.Counts(); n != 3 {
		t.Fatalf("manifest requests = %d, want 3", n)
	}
	clk.Advance(DefaultCacheTTL)
	if res, err := c.Resolve(ctx, Request{Ref: ref(t, f, "app:1"), Platform: "linux/amd64"}); err != nil || res.Cached {
		t.Fatalf("after TTL: %+v, %v", res, err)
	}
	// Errors are not cached.
	f.FailNext(http.StatusNotFound, nil, 1)
	if _, err := c.Resolve(ctx, Request{Ref: ref(t, f, "app:2")}); ClassOf(err) != ClassNotFound {
		t.Fatal(err)
	}
}

func TestNoCredentialsToPlainHTTPRealm(t *testing.T) {
	f := regtest.New(t, regtest.AuthBearer, "robot", "correct-horse-battery-canary")
	f.Realm = "http://" + f.Host() + "/token"
	f.Put("app", "1", MediaOCIManifest, []byte(manifestBody))
	c := newClient(t, f, testutil.FakeClock())
	_, err := c.Resolve(testutil.Context(t), Request{Ref: ref(t, f, "app:1"), Credential: cred(f), CredentialKey: "k"})
	wantClass(t, err, ClassInvalidResponse)
	if _, tokens, _ := f.Counts(); tokens != 0 {
		t.Fatalf("token requests = %d", tokens)
	}
}

func TestNoRedirectToPlainHTTP(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the plain-HTTP target was contacted")
	}))
	defer plain.Close()
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer tlsSrv.Close()
	c := New(Options{HTTP: tlsSrv.Client(), Clock: testutil.FakeClock(), MaxAttempts: 1})
	r, err := imageref.Parse(strings.TrimPrefix(tlsSrv.URL, "https://") + "/app:1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Resolve(testutil.Context(t), Request{Ref: r, Credential: &Credential{Username: "u", Secret: "s"}, CredentialKey: "k"})
	if ClassOf(err) != ClassUnavailable {
		t.Fatalf("%v", err)
	}
}

func TestParseChallenge(t *testing.T) {
	scheme, p := parseChallenge(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/nginx:pull"`)
	if scheme != "bearer" || p["realm"] != "https://auth.docker.io/token" || p["service"] != "registry.docker.io" ||
		p["scope"] != "repository:library/nginx:pull" {
		t.Fatalf("%s %v", scheme, p)
	}
	scheme, p = parseChallenge(`Basic realm="Registry Realm"`)
	if scheme != "basic" || p["realm"] != "Registry Realm" {
		t.Fatalf("%s %v", scheme, p)
	}
}

func TestRetryAfterHeaders(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		h    http.Header
		want time.Duration
	}{
		{http.Header{"Retry-After": {"30"}}, 30 * time.Second},
		{http.Header{"Retry-After": {now.Add(time.Minute).Format(http.TimeFormat)}}, time.Minute},
		{http.Header{"Ratelimit-Reset": {"12"}}, 12 * time.Second},
		{http.Header{"X-Ratelimit-Reset": {"1790337660"}}, time.Unix(1790337660, 0).Sub(now)},
		{http.Header{}, 0},
	}
	for _, c := range cases {
		if got := retryAfter(c.h, now); got != c.want {
			t.Errorf("%v: %v, want %v", c.h, got, c.want)
		}
	}
}

func FuzzParseChallenge(f *testing.F) {
	for _, s := range []string{`Bearer realm="x",service="y"`, `Basic realm="a\"b"`, `Bearer realm=unquoted,scope=x`, `Bearer realm="`} {
		f.Add(s)
	}
	f.Fuzz(func(_ *testing.T, s string) { parseChallenge(s) })
}
