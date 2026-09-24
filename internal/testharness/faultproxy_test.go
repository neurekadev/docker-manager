package testharness

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type hostLog struct {
	mu    sync.Mutex
	hosts []string
}

func (l *hostLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.hosts...)
}

func newProxyPair(t *testing.T) (*FaultProxy, *httptest.Server, *hostLog) {
	t.Helper()
	seen := &hostLog{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.mu.Lock()
		seen.hosts = append(seen.hosts, r.Host)
		seen.mu.Unlock()
		w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
		_, _ = io.WriteString(w, "upstream "+r.URL.Path)
	}))
	t.Cleanup(upstream.Close)
	p, err := NewFaultProxy(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return p, srv, seen
}

type result struct {
	StatusCode int
	Header     http.Header
}

func get(t *testing.T, url string) (result, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return result{resp.StatusCode, resp.Header}, string(b)
}

func TestFaultProxyPassesThroughAndPreservesHost(t *testing.T) {
	p, srv, hosts := newProxyPair(t)
	resp, body := get(t, srv.URL+"/v2/")
	if resp.StatusCode != http.StatusOK || body != "upstream /v2/" {
		t.Fatalf("pass-through = %d %q", resp.StatusCode, body)
	}
	if want, got := strings.TrimPrefix(srv.URL, "http://"), hosts.get(); len(got) != 1 || got[0] != want {
		t.Errorf("upstream saw Host %v, want the proxy's %s", got, want)
	}
	if h := p.Hits(); len(h) != 1 || h[0].Injected || h[0].Status != http.StatusOK {
		t.Errorf("hits = %+v", h)
	}
}

func TestFaultProxyInjectsPerPath(t *testing.T) {
	p, srv, _ := newProxyPair(t)
	p.Inject(FaultRule{Path: RegistryManifestPath, Status: http.StatusTooManyRequests, RetryAfter: "7"})
	p.Inject(FaultRule{Path: RepositoryPath("private/app"), Status: http.StatusForbidden})
	p.Inject(FaultRule{Method: http.MethodGet, Path: RegistryBasePath, Status: http.StatusUnauthorized,
		Header: http.Header{"Www-Authenticate": {`Basic realm="test"`}}})

	resp, body := get(t, srv.URL+"/v2/library/hello/manifests/latest")
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "7" {
		t.Fatalf("manifest = %d Retry-After=%q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	var env struct {
		Errors []struct{ Code, Message string } `json:"errors"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil || len(env.Errors) != 1 || env.Errors[0].Code != "TOOMANYREQUESTS" {
		t.Errorf("429 body = %q (%v)", body, err)
	}

	// First matching rule wins: the manifest rule precedes the repository rule.
	resp, _ = get(t, srv.URL+"/v2/private/app/manifests/1.0")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("private manifest = %d, want 429 (first rule)", resp.StatusCode)
	}
	resp, body = get(t, srv.URL+"/v2/private/app/blobs/sha256:"+strings.Repeat("a", 64))
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "DENIED") {
		t.Errorf("private blob = %d %q", resp.StatusCode, body)
	}
	resp, _ = get(t, srv.URL+"/v2/")
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != `Basic realm="test"` {
		t.Errorf("base = %d %v", resp.StatusCode, resp.Header)
	}
	resp, body = get(t, srv.URL+"/v2/library/hello/blobs/sha256:"+strings.Repeat("b", 64))
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(body, "upstream") {
		t.Errorf("unmatched blob = %d %q", resp.StatusCode, body)
	}
	if got := p.InjectedCount(); got != 4 {
		t.Errorf("InjectedCount = %d, want 4", got)
	}
}

func TestFaultProxyTimesAndRemove(t *testing.T) {
	p, srv, _ := newProxyPair(t)
	p.Inject(FaultRule{Path: RegistryBasePath, Status: http.StatusTooManyRequests, Times: 2})
	for i, want := range []int{429, 429, 200, 200} {
		if resp, _ := get(t, srv.URL+"/v2/"); resp.StatusCode != want {
			t.Errorf("request %d = %d, want %d", i, resp.StatusCode, want)
		}
	}
	remove := p.Inject(FaultRule{Status: http.StatusForbidden, Body: "nope"})
	if resp, body := get(t, srv.URL+"/anything"); resp.StatusCode != http.StatusForbidden || body != "nope" {
		t.Errorf("catch-all = %d %q", resp.StatusCode, body)
	}
	remove()
	if resp, _ := get(t, srv.URL+"/anything"); resp.StatusCode != http.StatusOK {
		t.Errorf("after remove = %d", resp.StatusCode)
	}
	p.Reset()
	if len(p.Hits()) != 0 {
		t.Error("Reset kept hits")
	}
}

func TestNewFaultProxyValidatesUpstream(t *testing.T) {
	for _, u := range []string{"", "localhost:5000", "://bad"} {
		if _, err := NewFaultProxy(u); err == nil {
			t.Errorf("NewFaultProxy(%q) succeeded", u)
		}
	}
}

// TestFaultProxyRecordsHitBeforeResponding pins the hit-log ordering: a hit
// is recorded before the first response byte reaches the client, so a
// caller that has read a response always finds its hit. The upstream holds
// the response open after sending headers, so the proxy's ServeHTTP cannot
// have returned when the client checks the log — deterministically, with
// no sleeps.
func TestFaultProxyRecordsHitBeforeResponding(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "partial")
		http.NewResponseController(w).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(upstream.Close)
	p, err := NewFaultProxy(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock) // runs first: never leave the upstream handler blocked

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/v2/held", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// The upstream is still blocked: only the headers have been proxied.
	if h := p.Hits(); len(h) != 1 || h[0].Path != "/v2/held" || h[0].Status != http.StatusAccepted || h[0].Injected {
		t.Errorf("hits while response in flight = %+v", h)
	}
	unblock()
	if b, err := io.ReadAll(resp.Body); err != nil || string(b) != "partial" {
		t.Errorf("body = %q (%v)", b, err)
	}

	// Injected faults are recorded before they are written, too.
	p.Inject(FaultRule{Path: RegistryBasePath, Status: http.StatusTooManyRequests})
	if r, _ := get(t, srv.URL+"/v2/"); r.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("injected = %d", r.StatusCode)
	}
	if h := p.Hits(); len(h) != 2 || !h[1].Injected || h[1].Status != http.StatusTooManyRequests {
		t.Errorf("hits = %+v", h)
	}
}

func TestFaultProxyRecordsUpstreamErrors(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	p, err := NewFaultProxy(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	upstream.Close() // connection refused from now on
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	if r, _ := get(t, srv.URL+"/v2/"); r.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", r.StatusCode)
	}
	if h := p.Hits(); len(h) != 1 || h[0].Status != http.StatusBadGateway || h[0].Injected || h[0].Path != "/v2/" {
		t.Errorf("hits = %+v", h)
	}
}
