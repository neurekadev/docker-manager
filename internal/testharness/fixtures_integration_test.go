//go:build integration

package testharness

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
)

// Self-tests of every fixture (#29). Naming: TestEngine* run in the
// engine-matrix job for each DOCKYARD_TEST_ENGINE; TestRegistry*/TestGit*
// in compose-fixtures; TestMinIO*/TestRestic* in storage; TestTLSProxy* in
// e2e. See docs/testing/harness.md.

func testCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), d)
	t.Cleanup(cancel)
	return ctx
}

func TestEngineServesMatrixVersion(t *testing.T) {
	want, err := SelectEngine()
	if err != nil {
		t.Fatal(err)
	}
	e := StartEngine(t, EngineOptions{Version: want})
	ctx := testCtx(t, 2*time.Minute)
	cli := e.Client(t)

	v, err := cli.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		t.Fatalf("version via DOCKER_HOST=%s: %v", e.Host, err)
	}
	t.Logf("Engine %s: API %s (min %s), %s/%s", v.Version, v.APIVersion, v.MinAPIVersion, v.Os, v.Arch)
	if v.Version != want.Version {
		t.Errorf("Engine version = %s, want %s", v.Version, want.Version)
	}
	if v.APIVersion != want.APIVersion {
		t.Errorf("Engine API version = %s, matrix says %s (update %s)", v.APIVersion, want.APIVersion, MatrixPath)
	}
	if v.Arch != runtime.GOARCH {
		t.Errorf("Engine arch = %s, want the runner's %s", v.Arch, runtime.GOARCH)
	}
	// Negotiation downgrades the client to the Engine's API version when
	// the Engine is older than the client (#21).
	if got, wantNeg := cli.ClientVersion(), MinVersion(client.MaxAPIVersion, v.APIVersion); got != wantNeg {
		t.Errorf("negotiated API version = %s, want %s", got, wantNeg)
	}
	info, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Info.ID == "" || info.Info.OSType != "linux" {
		t.Errorf("info = ID %q OSType %q", info.Info.ID, info.Info.OSType)
	}
	// The DOCKER_HOST value also works through the standard environment.
	t.Setenv("DOCKER_HOST", e.Host)
	envCli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer envCli.Close()
	if _, err := envCli.Ping(ctx, client.PingOptions{}); err != nil {
		t.Errorf("ping via DOCKER_HOST env: %v", err)
	}
}

func TestEngineMultiHost(t *testing.T) {
	engines := StartEngines(t, 2, EngineOptions{})
	ctx := testCtx(t, 2*time.Minute)
	ids := map[string]bool{}
	for _, e := range engines {
		info, err := e.Client(t).Info(ctx, client.InfoOptions{})
		if err != nil {
			t.Fatalf("%s: %v", e.Alias, err)
		}
		ids[info.Info.ID] = true
	}
	if len(ids) != 2 {
		t.Fatalf("two Engines report %d distinct IDs", len(ids))
	}
	if engines[0].Network != engines[1].Network {
		t.Fatal("engines are on different networks")
	}
	// Each Engine reaches the other's API by alias over the shared network
	// (as two agents on two hosts reach one manager).
	code, out, err := engines[0].Exec(ctx, "wget", "-q", "-O-", "http://"+engines[1].Alias+":2375/_ping")
	if err != nil || code != 0 || strings.TrimSpace(out) != "OK" {
		t.Errorf("engine-1 -> engine-2 ping: code %d, %q, %v", code, out, err)
	}
}

func TestRegistryFixtureAuthAndFaults(t *testing.T) {
	reg := StartRegistry(t, RegistryOptions{})
	ctx := testCtx(t, 5*time.Minute)

	img, err := NewTestImage(runtime.GOARCH, "registry fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := PushOCIImage(ctx, http.DefaultClient, reg.Direct, reg.User, reg.Password, "dockyard/hello", "v1", img); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Anonymous access is refused, authenticated access works (host side).
	status := func(url, user, pw string) (int, http.Header) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		req.Header.Set("Accept", OCIManifestType)
		if user != "" {
			req.SetBasicAuth(user, pw)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, resp.Header
	}
	manifest := reg.ProxyURL + "/v2/dockyard/hello/manifests/v1"
	if s, _ := status(manifest, "", ""); s != http.StatusUnauthorized {
		t.Errorf("anonymous manifest = %d, want 401", s)
	}
	if s, _ := status(manifest, reg.User, "wrong"); s != http.StatusUnauthorized {
		t.Errorf("wrong password = %d, want 401", s)
	}
	if s, h := status(manifest, reg.User, reg.Password); s != http.StatusOK || h.Get("Docker-Content-Digest") != img.Digest {
		t.Errorf("authenticated manifest = %d digest %q, want 200 %s", s, h.Get("Docker-Content-Digest"), img.Digest)
	}

	// Injected faults (host side).
	remove := reg.Proxy.Inject(FaultRule{Path: RegistryManifestPath, Status: http.StatusTooManyRequests, RetryAfter: "3"})
	if s, h := status(manifest, reg.User, reg.Password); s != http.StatusTooManyRequests || h.Get("Retry-After") != "3" {
		t.Errorf("429 injection = %d Retry-After %q", s, h.Get("Retry-After"))
	}
	remove()
	remove = reg.Proxy.Inject(FaultRule{Path: RepositoryPath("dockyard/hello"), Status: http.StatusForbidden})
	if s, _ := status(manifest, reg.User, reg.Password); s != http.StatusForbidden {
		t.Errorf("403 injection = %d", s)
	}
	remove()

	// An Engine pulls through the proxy with credentials.
	opts := reg.EngineOptions()
	e := StartEngine(t, opts)
	cli := e.Client(t)
	ref := reg.EngineAddress + "/dockyard/hello:v1"
	pull := func(auth string) error {
		resp, err := cli.ImagePull(ctx, ref, client.ImagePullOptions{RegistryAuth: auth})
		if err != nil {
			return err
		}
		defer resp.Close()
		return resp.Wait(ctx)
	}
	if err := pull(""); err == nil {
		t.Error("anonymous pull through the proxy succeeded")
	}
	reg.Proxy.Reset()
	remove = reg.Proxy.Inject(FaultRule{Path: RegistryManifestPath, Status: http.StatusTooManyRequests, RetryAfter: "1"})
	err = pull(reg.Auth())
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "toomanyrequests") && !strings.Contains(err.Error(), "429") {
		t.Errorf("pull under injected 429: err = %v", err)
	}
	if reg.Proxy.InjectedCount() == 0 {
		t.Error("the Engine's pull never reached the fault proxy")
	}
	remove()
	if err := pull(reg.Auth()); err != nil {
		t.Fatalf("authenticated pull: %v", err)
	}
	ins, err := cli.ImageInspect(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range ins.RepoDigests {
		found = found || strings.HasSuffix(d, "@"+img.Digest)
	}
	if !found {
		t.Errorf("pulled image digests %v, want %s", ins.RepoDigests, img.Digest)
	}
}

func TestGitServerFixture(t *testing.T) {
	nw := NewNetwork(t)
	g := StartGitServer(t, GitServerOptions{Network: nw})
	ctx := testCtx(t, 5*time.Minute)

	get := func(url, user, pw string) int {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if user != "" {
			req.SetBasicAuth(user, pw)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if s := get(g.URL+"/public.git/info/refs", "", ""); s != http.StatusOK {
		t.Errorf("public info/refs = %d", s)
	}
	if s := get(g.URL+"/private.git/info/refs", "", ""); s != http.StatusUnauthorized {
		t.Errorf("anonymous private info/refs = %d, want 401", s)
	}
	if s := get(g.URL+"/private.git/info/refs", g.User, g.Password); s != http.StatusOK {
		t.Errorf("authenticated private info/refs = %d", s)
	}

	// Clone from the test process.
	dir := t.TempDir()
	if out, err := exec.CommandContext(ctx, "git", "clone", "--quiet", g.RepoURL(g.URL, "private", true), filepath.Join(dir, "private")).CombinedOutput(); err != nil {
		t.Fatalf("clone private: %v: %s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "private", "hello.txt")); err != nil || !strings.Contains(string(b), "private fixture") {
		t.Errorf("cloned hello.txt = %q, %v", b, err)
	}

	// Reachable from inside a DinD Engine on the same network, by alias
	// (dockerd's resolver, used for Git build contexts) and by IP.
	e := StartEngine(t, EngineOptions{Network: nw})
	for _, tc := range []struct {
		url  string
		want int
	}{
		{g.RepoURL(g.InternalURL, "public", false), 0},
		{g.RepoURL("http://"+g.IP, "public", false), 0},
		{g.RepoURL(g.InternalURL, "private", true), 0},
		{g.RepoURL(g.InternalURL, "private", false), 128},
	} {
		code, out, err := e.Exec(ctx, "env", "GIT_TERMINAL_PROMPT=0", "git", "ls-remote", tc.url)
		if err != nil {
			t.Fatal(err)
		}
		if code != tc.want {
			t.Errorf("git ls-remote %s in Engine: exit %d, want %d: %s", strings.Replace(tc.url, g.Password, "***", 1), code, tc.want, out)
		}
		if tc.want == 0 && !strings.Contains(out, "refs/heads/"+GitDefaultBranch) {
			t.Errorf("ls-remote output %q lacks refs/heads/%s", out, GitDefaultBranch)
		}
	}
}

func TestMinIOFixture(t *testing.T) {
	m := StartMinIO(t, MinIOOptions{Buckets: []string{"dockyard-backups"}})
	ctx := testCtx(t, 2*time.Minute)
	if err := m.S3.PutObject(ctx, "dockyard-backups", "probe/hello.txt", []byte("hello s3")); err != nil {
		t.Fatal(err)
	}
	got, err := m.S3.GetObject(ctx, "dockyard-backups", "probe/hello.txt")
	if err != nil || string(got) != "hello s3" {
		t.Fatalf("get = %q, %v", got, err)
	}
	keys, err := m.S3.ListKeys(ctx, "dockyard-backups", "probe/")
	if err != nil || len(keys) != 1 || keys[0] != "probe/hello.txt" {
		t.Errorf("list = %v, %v", keys, err)
	}
	// Wrong credentials are rejected.
	bad := *m.S3
	bad.SecretKey = "wrong-secret-key-0000000000000000000000"
	if s, _, err := bad.Do(ctx, http.MethodGet, "/dockyard-backups/probe/hello.txt", nil); err != nil || s != http.StatusForbidden {
		t.Errorf("wrong secret: status %d, %v (want 403)", s, err)
	}
}

func restic(ctx context.Context, t *testing.T, bin string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(append(os.Environ(), env...), "RESTIC_PASSWORD=dockyard-test-repo-password", "RESTIC_CACHE_DIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("restic %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func resticRoundTrip(ctx context.Context, t *testing.T, bin, repo string, env []string) {
	t.Helper()
	src := t.TempDir()
	payload := "backup payload " + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.WriteFile(filepath.Join(src, "data.txt"), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	restic(ctx, t, bin, env, "-r", repo, "init")
	restic(ctx, t, bin, env, "-r", repo, "backup", "--host", "dockyard-test", src)
	snaps := restic(ctx, t, bin, env, "-r", repo, "snapshots", "--json")
	if !strings.Contains(snaps, `"hostname":"dockyard-test"`) {
		t.Errorf("snapshots = %s", snaps)
	}
	dst := t.TempDir()
	restic(ctx, t, bin, env, "-r", repo, "restore", "latest", "--target", dst)
	got, err := os.ReadFile(filepath.Join(dst, src, "data.txt"))
	if err != nil || string(got) != payload {
		t.Errorf("restored %q, %v; want %q", got, err, payload)
	}
	restic(ctx, t, bin, env, "-r", repo, "check")
}

func TestResticPinnedBinary(t *testing.T) {
	ctx := testCtx(t, 5*time.Minute)
	bin, err := FetchRestic(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := restic(ctx, t, bin, nil, "version")
	if !strings.HasPrefix(out, "restic "+ResticVersion+" ") {
		t.Errorf("restic version = %q, want %s", out, ResticVersion)
	}
	t.Run("local repository", func(t *testing.T) {
		resticRoundTrip(ctx, t, bin, filepath.Join(t.TempDir(), "repo"), nil)
	})
	t.Run("MinIO repository", func(t *testing.T) {
		m := StartMinIO(t, MinIOOptions{Buckets: []string{"restic"}})
		resticRoundTrip(ctx, t, bin, m.ResticRepository("restic", "dockyard"), m.ResticEnv())
		keys, err := m.S3.ListKeys(ctx, "restic", "dockyard/")
		if err != nil || len(keys) == 0 {
			t.Errorf("repository objects in MinIO = %v, %v", keys, err)
		}
	})
}

func TestTLSProxyFixture(t *testing.T) {
	// Upstream in the test process: plain HTTP, SSE and WebSocket echo.
	sseRelease := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello proto=%s host=%s", r.Header.Get("X-Forwarded-Proto"), r.Host)
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: first\ndata: 1\n\n")
		http.NewResponseController(w).Flush()
		// The client must see the first event while the stream is open.
		select {
		case <-sseRelease:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "event: second\ndata: 2\n\n")
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		typ, msg, err := c.Read(r.Context())
		if err != nil {
			return
		}
		_ = c.Write(r.Context(), typ, append([]byte("echo:"), msg...))
		_ = c.Close(websocket.StatusNormalClosure, "")
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	up := httptest.NewUnstartedServer(mux)
	up.Listener = ln
	up.Start()
	t.Cleanup(up.Close)
	port := ln.Addr().(*net.TCPAddr).Port

	p := StartTLSProxy(t, TLSProxyOptions{
		Upstream:        testcontainers.HostInternal + ":" + strconv.Itoa(port),
		HostAccessPorts: []int{port},
	})
	ctx := testCtx(t, 2*time.Minute)

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.URL+"/hello", nil)
	resp, err := p.Client.Do(req)
	if err != nil {
		t.Fatalf("GET through TLS proxy (verified against Caddy's root): %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(string(body), "hello proto=https host=localhost:") {
		t.Errorf("GET = %d %q", resp.StatusCode, body)
	}
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 || resp.TLS.PeerCertificates[0].VerifyHostname("localhost") != nil {
		t.Error("proxy certificate is not valid for localhost")
	}

	// SSE: the first event arrives before the upstream finishes.
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, p.URL+"/events", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err = p.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	if !sc.Scan() || sc.Text() != "event: first" {
		t.Fatalf("first SSE line = %q (%v): the proxy buffers streams", sc.Text(), sc.Err())
	}
	close(sseRelease)
	var rest []string
	for sc.Scan() {
		rest = append(rest, sc.Text())
	}
	if !strings.Contains(strings.Join(rest, "\n"), "event: second") {
		t.Errorf("rest of stream = %q", rest)
	}

	// WebSocket upgrade through the proxy.
	wsURL := "wss" + strings.TrimPrefix(p.URL, "https") + "/ws"
	c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: p.Client}) //nolint:bodyclose // the library owns the handshake response
	if err != nil {
		t.Fatalf("websocket through proxy: %v", err)
	}
	defer func() { _ = c.CloseNow() }()
	if err := c.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	_, msg, err := c.Read(ctx)
	if err != nil || string(msg) != "echo:ping" {
		t.Errorf("websocket echo = %q, %v", msg, err)
	}
}
