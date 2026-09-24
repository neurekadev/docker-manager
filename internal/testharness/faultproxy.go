package testharness

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"sync"
)

// FaultRule makes the FaultProxy answer matching requests itself instead of
// forwarding them, e.g. to simulate registry 401/403/429 responses (#19).
type FaultRule struct {
	// Method matches the request method; empty matches any.
	Method string
	// Path matches the request path; nil matches any.
	Path *regexp.Regexp
	// Status is the injected status code (e.g. 401, 403, 429).
	Status int
	// RetryAfter, when set, is sent as the Retry-After header (seconds or
	// an HTTP date).
	RetryAfter string
	// Header adds response headers (e.g. WWW-Authenticate).
	Header http.Header
	// Body overrides the default registry-style JSON error body.
	Body string
	// Times limits how many requests the rule answers; 0 means unlimited.
	// Afterwards requests pass through again (e.g. "first pull hits 429").
	Times int
}

// FaultHit records one request seen by the proxy.
type FaultHit struct {
	Method   string
	Path     string
	Status   int
	Injected bool
}

// FaultProxy is a reverse proxy that forwards to an upstream (the registry
// fixture) and injects faults per path. It is an http.Handler; serve it
// with httptest.NewServer or any listener. Safe for concurrent use.
type FaultProxy struct {
	proxy *httputil.ReverseProxy

	mu    sync.Mutex
	rules []*faultRule
	hits  []FaultHit
}

type faultRule struct {
	FaultRule
	used int
}

// NewFaultProxy returns a proxy forwarding to upstream (scheme://host:port).
// The incoming Host header is preserved so the registry builds upload
// Location URLs that point back at the proxy.
func NewFaultProxy(upstream string) (*FaultProxy, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("fault proxy upstream: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, errors.New("fault proxy upstream must be scheme://host[:port]")
	}
	p := &FaultProxy{}
	p.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.Out.Host = pr.In.Host
		},
		// Stream blob uploads/downloads and chunked responses unbuffered.
		FlushInterval: -1,
	}
	return p, nil
}

// Inject adds a rule and returns a function that removes it. Rules are
// evaluated in insertion order; the first match wins.
func (p *FaultProxy) Inject(r FaultRule) (remove func()) {
	if r.Status == 0 {
		panic("testharness: FaultRule.Status is required")
	}
	fr := &faultRule{FaultRule: r}
	p.mu.Lock()
	p.rules = append(p.rules, fr)
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		for i, x := range p.rules {
			if x == fr {
				p.rules = append(p.rules[:i], p.rules[i+1:]...)
				return
			}
		}
	}
}

// Reset removes all rules and clears the hit log.
func (p *FaultProxy) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rules = nil
	p.hits = nil
}

// Hits returns a copy of the requests seen so far.
func (p *FaultProxy) Hits() []FaultHit {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]FaultHit(nil), p.hits...)
}

// InjectedCount returns how many requests received an injected fault.
func (p *FaultProxy) InjectedCount() int {
	n := 0
	for _, h := range p.Hits() {
		if h.Injected {
			n++
		}
	}
	return n
}

func (p *FaultProxy) match(r *http.Request) *FaultRule {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, fr := range p.rules {
		if fr.Method != "" && fr.Method != r.Method {
			continue
		}
		if fr.Path != nil && !fr.Path.MatchString(r.URL.Path) {
			continue
		}
		if fr.Times > 0 && fr.used >= fr.Times {
			continue
		}
		fr.used++
		rule := fr.FaultRule
		return &rule
	}
	return nil
}

func (p *FaultProxy) record(h FaultHit) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hits = append(p.hits, h)
}

// ServeHTTP implements http.Handler.
func (p *FaultProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if rule := p.match(r); rule != nil {
		for k, vs := range rule.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		if rule.RetryAfter != "" {
			w.Header().Set("Retry-After", rule.RetryAfter)
		}
		body := rule.Body
		if body == "" {
			body = registryErrorBody(rule.Status)
			w.Header().Set("Content-Type", "application/json")
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(rule.Status)
		_, _ = w.Write([]byte(body))
		p.record(FaultHit{Method: r.Method, Path: r.URL.Path, Status: rule.Status, Injected: true})
		return
	}
	sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	p.proxy.ServeHTTP(sw, r)
	p.record(FaultHit{Method: r.Method, Path: r.URL.Path, Status: sw.status})
}

// registryErrorBody renders the distribution error envelope for status.
func registryErrorBody(status int) string {
	code := map[int]string{
		http.StatusUnauthorized:    "UNAUTHORIZED",
		http.StatusForbidden:       "DENIED",
		http.StatusNotFound:        "NAME_UNKNOWN",
		http.StatusTooManyRequests: "TOOMANYREQUESTS",
	}[status]
	if code == "" {
		code = "UNKNOWN"
	}
	b, _ := json.Marshal(map[string]any{"errors": []map[string]string{{
		"code":    code,
		"message": "injected by the DockYard test fault proxy: " + http.StatusText(status),
	}}})
	return string(b)
}

// Common registry paths for FaultRule.Path.
var (
	// RegistryManifestPath matches manifest requests: /v2/<name>/manifests/<ref>.
	RegistryManifestPath = regexp.MustCompile(`^/v2/.+/manifests/[^/]+$`)
	// RegistryBlobPath matches blob downloads: /v2/<name>/blobs/<digest>.
	RegistryBlobPath = regexp.MustCompile(`^/v2/.+/blobs/sha256:[0-9a-f]{64}$`)
	// RegistryBasePath matches the API version check /v2/.
	RegistryBasePath = regexp.MustCompile(`^/v2/?$`)
)

// RepositoryPath matches every registry API path of one repository.
func RepositoryPath(name string) *regexp.Regexp {
	return regexp.MustCompile(`^/v2/` + regexp.QuoteMeta(name) + `/`)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach Flush on the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush forwards to the underlying writer (streamed blob downloads).
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
