package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/manager/api"
	"github.com/neurekadev/dockyard/internal/testutil"
)

const bootScript = `
	import("/_app/immutable/entry/start.abc123.js").then(m => m.start());
`

var testUI = fstest.MapFS{
	"index.html":                           {Data: []byte("<!doctype html><html><head><script>" + bootScript + "</script></head><body>DockYard</body></html>")},
	"robots.txt":                           {Data: []byte("User-agent: *\nDisallow: /\n")},
	"_app/version.json":                    {Data: []byte(`{"version":"1"}`)},
	"_app/immutable/entry/start.abc123.js": {Data: []byte("export const start = () => {};")},
	"_app/immutable/assets/app.def456.css": {Data: []byte("body{}")},
	"service-worker.js":                    {Data: []byte("self.addEventListener('fetch', () => {});")},
	"manifest.webmanifest":                 {Data: []byte(`{"name":"DockYard","start_url":"/","scope":"/","display":"standalone"}`)},
	"icons/pwa-192x192.png":                {Data: []byte("PNG placeholder")},
	"favicon.ico":                          {Data: []byte{0, 0, 1, 0}},
}

func newTestServer(t *testing.T) (*Server, *testutil.LogBuffer) {
	t.Helper()
	logger, logs := testutil.CaptureLogger()
	s, err := New(Options{Logger: logger, Clock: testutil.FakeClock(), UI: testUI})
	if err != nil {
		t.Fatal(err)
	}
	return s, logs
}

func request(t *testing.T, h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDeepLinkServesIndex(t *testing.T) {
	s, _ := newTestServer(t)
	for _, p := range []string{"/", "/index.html", "/stacks/abc", "/environments/e1/containers/c1/logs", "/stacks/abc/"} {
		rec := request(t, s.Handler, http.MethodGet, p, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", p, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("%s: content type %q", p, ct)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != CacheRevalidate {
			t.Errorf("%s: cache-control %q, want %q", p, cc, CacheRevalidate)
		}
		if !strings.Contains(rec.Body.String(), "DockYard") {
			t.Errorf("%s: body is not the app shell", p)
		}
	}
	if rec := request(t, s.Handler, http.MethodPost, "/stacks/abc", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST to SPA route: %d", rec.Code)
	}
}

func TestAssetCaching(t *testing.T) {
	s, _ := newTestServer(t)
	rec := request(t, s.Handler, http.MethodGet, "/_app/immutable/entry/start.abc123.js", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != CacheImmutable {
		t.Fatalf("immutable asset: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/javascript; charset=utf-8" {
		t.Errorf("js content type %q", ct)
	}
	if ct := request(t, s.Handler, http.MethodGet, "/_app/immutable/assets/app.def456.css", nil).Header().Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Errorf("css content type %q", ct)
	}
	rec = request(t, s.Handler, http.MethodGet, "/_app/version.json", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != CacheRevalidate {
		t.Fatalf("version.json: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	rec = request(t, s.Handler, http.MethodGet, "/robots.txt", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != CacheRevalidate {
		t.Fatalf("robots.txt: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}

	// Missing build assets are hard 404s, never the HTML shell.
	for _, p := range []string{"/_app/immutable/entry/missing.js", "/_app/nope", "/_app"} {
		rec = request(t, s.Handler, http.MethodGet, p, nil)
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Body.String())
		}
	}

	// Conditional requests revalidate via ETag.
	rec = request(t, s.Handler, http.MethodGet, "/", nil)
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on index.html")
	}
	rec = request(t, s.Handler, http.MethodGet, "/some/route", map[string]string{"If-None-Match": etag})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match: %d", rec.Code)
	}
}

func TestSecurityHeadersAndCSP(t *testing.T) {
	s, _ := newTestServer(t)
	rec := request(t, s.Handler, http.MethodGet, "/", nil)
	h := rec.Header()
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "no-referrer" || h.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("missing security headers: %v", h)
	}
	csp := h.Get("Content-Security-Policy")
	sum := sha256.Sum256([]byte(bootScript))
	wantHash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	for _, want := range []string{"frame-ancestors 'none'", "default-src 'self'", "object-src 'none'", "script-src 'self' " + wantHash} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Error("CSP allows inline scripts")
	}
}

func TestAPIResponsesAreNoStore(t *testing.T) {
	s, _ := newTestServer(t)
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/api/v1/health", http.StatusOK},
		{http.MethodGet, "/api/v1/capabilities", http.StatusOK},
		{http.MethodGet, "/api/v1/openapi.json", http.StatusOK},
		{http.MethodGet, "/api/v1/openapi.yaml", http.StatusOK},
		{http.MethodGet, "/api/v1/jobs", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/jobs/x/events/stream", http.StatusUnauthorized},
		{http.MethodPost, "/api/v1/jobs/x/cancellations", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/does-not-exist", http.StatusNotFound},
		{http.MethodPost, "/api/v1/health", http.StatusMethodNotAllowed},
		{http.MethodGet, "/agent/v1/session", http.StatusNotFound},
		{http.MethodPost, "/agent/v1/enroll", http.StatusNotFound},
	} {
		rec := request(t, s.Handler, tc.method, tc.path, nil)
		if rec.Code != tc.status {
			t.Errorf("%s %s: status %d, want %d", tc.method, tc.path, rec.Code, tc.status)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s %s: Cache-Control %q, want no-store", tc.method, tc.path, cc)
		}
		if rec.Header().Get(RequestIDHeader) == "" {
			t.Errorf("%s %s: no request ID", tc.method, tc.path)
		}
	}
}

func TestAPIFallbackErrorShape(t *testing.T) {
	s, _ := newTestServer(t)
	rec := request(t, s.Handler, http.MethodGet, "/api/v1/no-such-collection/nope", map[string]string{RequestIDHeader: "client-req-1"})
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != api.ErrorContentType {
		t.Fatalf("status %d content-type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var e api.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	// The client is not a trusted proxy, so its X-Request-ID is replaced.
	if id := rec.Header().Get(RequestIDHeader); e.Code != api.CodeNotFound || e.RequestID != id || id == "client-req-1" || len(id) != 32 {
		t.Fatalf("error = %+v, header %q", e, rec.Header().Get(RequestIDHeader))
	}

	rec = request(t, s.Handler, http.MethodDelete, "/api/v1/health", nil)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("405: %d Allow=%q", rec.Code, rec.Header().Values("Allow"))
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if e.Code != api.CodeMethodNotAllowed {
		t.Fatalf("405 code %q", e.Code)
	}
}

func TestRequestIDSanitized(t *testing.T) {
	s, _ := newTestServer(t)
	rec := request(t, s.Handler, http.MethodGet, "/api/v1/health", map[string]string{RequestIDHeader: "bad id\nwith newline"})
	id := rec.Header().Get(RequestIDHeader)
	if id == "" || strings.ContainsAny(id, " \n") || len(id) != 32 {
		t.Fatalf("request id %q", id)
	}
}

func TestPanicRecovery(t *testing.T) {
	s, logs := newTestServer(t)
	api.Register(s.API, api.Operation{
		Operation:  huma.Operation{OperationID: "test-panic", Method: http.MethodGet, Path: api.BasePath + "/test/panic", Summary: "panic"},
		Capability: api.CapabilityAuthenticated, Scope: api.ScopeNone,
	}, func(context.Context, *struct{}) (*struct{}, error) {
		panic("kaboom")
	})
	rec := request(t, s.Handler, http.MethodGet, api.BasePath+"/test/panic", nil)
	if rec.Code != http.StatusInternalServerError || rec.Header().Get("Content-Type") != api.ErrorContentType {
		t.Fatalf("panic: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var e api.Error
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if e.Code != api.CodeInternal || strings.Contains(rec.Body.String(), "kaboom") {
		t.Fatalf("panic body %s", rec.Body)
	}
	if !strings.Contains(logs.String(), "kaboom") || !strings.Contains(logs.String(), `"status":500`) {
		t.Fatalf("panic not logged: %s", logs.String())
	}
}

func TestAccessLogOmitsQuery(t *testing.T) {
	s, logs := newTestServer(t)
	request(t, s.Handler, http.MethodGet, "/stacks?token=sekrit", nil)
	if strings.Contains(logs.String(), "sekrit") {
		t.Fatalf("query string logged: %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"path":"/stacks"`) {
		t.Fatalf("access log missing: %s", logs.String())
	}
}

func TestNewRequiresIndex(t *testing.T) {
	logger, _ := testutil.CaptureLogger()
	if _, err := New(Options{Logger: logger, UI: fstest.MapFS{"a.js": {Data: []byte("x")}}}); err == nil {
		t.Fatal("expected error without index.html")
	}
}

func TestPWAAssets(t *testing.T) {
	s, _ := newTestServer(t)
	for _, tc := range []struct{ path, contentType string }{
		{"/service-worker.js", "text/javascript; charset=utf-8"},
		{"/manifest.webmanifest", "application/manifest+json"},
		{"/icons/pwa-192x192.png", "image/png"},
		{"/favicon.ico", "image/x-icon"},
	} {
		rec := request(t, s.Handler, http.MethodGet, tc.path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", tc.path, rec.Code)
		}
		h := rec.Header()
		if ct := h.Get("Content-Type"); ct != tc.contentType {
			t.Errorf("%s: content type %q, want %q", tc.path, ct, tc.contentType)
		}
		// Unhashed: always revalidated, so a new build is found on the next check.
		if cc := h.Get("Cache-Control"); cc != CacheRevalidate {
			t.Errorf("%s: cache-control %q, want %q", tc.path, cc, CacheRevalidate)
		}
		if h.Get("ETag") == "" {
			t.Errorf("%s: no ETag", tc.path)
		}
		if h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: no nosniff", tc.path)
		}
	}

	// Scope: the worker is served from the root, so its maximum scope is the
	// whole origin; the server never widens it with Service-Worker-Allowed.
	rec := request(t, s.Handler, http.MethodGet, "/service-worker.js", map[string]string{"Service-Worker": "script"})
	if v := rec.Header().Values("Service-Worker-Allowed"); len(v) != 0 {
		t.Errorf("Service-Worker-Allowed = %q, want none", v)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"worker-src 'self'", "manifest-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
}

func TestPWABuildOnlyPathsNeverFallBackToHTML(t *testing.T) {
	logger, _ := testutil.CaptureLogger()
	// A build without PWA files (e.g. the placeholder page).
	s, err := New(Options{Logger: logger, Clock: testutil.FakeClock(), UI: fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><title>DockYard</title>")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/service-worker.js", "/manifest.webmanifest", "/favicon.ico", "/icons/pwa-512x512.png", "/icons"} {
		rec := request(t, s.Handler, http.MethodGet, p, nil)
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "<title>") {
			t.Errorf("%s: %d %q, want a plain 404", p, rec.Code, rec.Body.String())
		}
		if cc := rec.Header().Get("Cache-Control"); cc != CacheRevalidate {
			t.Errorf("%s: cache-control %q", p, cc)
		}
	}
	// Look-alike client routes still reach the SPA.
	for _, p := range []string{"/iconsets", "/stacks/service-worker.js", "/environments/e1"} {
		if rec := request(t, s.Handler, http.MethodGet, p, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>") {
			t.Errorf("%s: %d, want the app shell", p, rec.Code)
		}
	}
}
