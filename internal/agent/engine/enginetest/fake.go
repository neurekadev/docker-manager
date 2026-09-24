// Package enginetest provides a scripted fake Docker Engine API server for
// Docker-free tests of the Engine and Compose adapters. It serves the
// version-independent endpoints (_ping, version, info) and routes every
// other request, with the /v1.xx prefix stripped, to handlers registered by
// the test. Import it from tests only.
package enginetest

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sync"
	"testing"
)

// Options configures the fake Engine.
type Options struct {
	// APIVersion is the highest API version the fake serves (default "1.56").
	APIVersion string
	// MinAPIVersion defaults to "1.24".
	MinAPIVersion string
	// Version is the Engine version (default "29.8.1").
	Version string
	// SecurityOptions for /info (default seccomp + cgroupns).
	SecurityOptions []string
	// DockerRootDir for /info (default /var/lib/docker).
	DockerRootDir string
	// PlatformName for /version (default "Docker Engine - Community").
	PlatformName    string
	OperatingSystem string
}

// Request is a recorded API request.
type Request struct {
	Method string
	// Path is the path without the /v1.xx prefix, e.g. "/containers/json".
	Path string
	// APIVersion is the version prefix the client used ("" for _ping).
	APIVersion string
	Query      url.Values
	Header     http.Header
	Body       []byte
}

// Engine is a running fake Engine.
type Engine struct {
	// Host is a DOCKER_HOST value (tcp://127.0.0.1:port).
	Host string
	opts Options
	srv  *httptest.Server

	mu       sync.Mutex
	handlers []route
	requests []Request
}

type route struct {
	method string
	re     *regexp.Regexp
	h      http.HandlerFunc
}

var versionPrefix = regexp.MustCompile(`^/v(1\.\d+)(/.*)$`)

// Start starts a fake Engine that is closed when the test ends.
func Start(t testing.TB, opts Options) *Engine {
	t.Helper()
	if opts.APIVersion == "" {
		opts.APIVersion = "1.56"
	}
	if opts.MinAPIVersion == "" {
		opts.MinAPIVersion = "1.24"
	}
	if opts.Version == "" {
		opts.Version = "29.8.1"
	}
	if opts.SecurityOptions == nil {
		opts.SecurityOptions = []string{"name=seccomp,profile=builtin", "name=cgroupns"}
	}
	if opts.DockerRootDir == "" {
		opts.DockerRootDir = "/var/lib/docker"
	}
	if opts.PlatformName == "" {
		opts.PlatformName = "Docker Engine - Community"
	}
	if opts.OperatingSystem == "" {
		opts.OperatingSystem = "Debian GNU/Linux 12 (bookworm)"
	}
	e := &Engine{opts: opts}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e.srv = httptest.NewUnstartedServer(http.HandlerFunc(e.serve))
	e.srv.Listener = ln
	e.srv.Start()
	t.Cleanup(e.srv.Close)
	e.Host = "tcp://" + ln.Addr().String()
	return e
}

// Handle registers h for method and a path regexp (anchored, without the
// version prefix). Later registrations win.
func (e *Engine) Handle(method, pathRE string, h http.HandlerFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers = append([]route{{method: method, re: regexp.MustCompile("^" + pathRE + "$"), h: h}}, e.handlers...)
}

// Requests returns the recorded requests.
func (e *Engine) Requests() []Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Request(nil), e.requests...)
}

// Find returns the recorded requests for method and a path regexp.
func (e *Engine) Find(method, pathRE string) []Request {
	re := regexp.MustCompile("^" + pathRE + "$")
	var out []Request
	for _, r := range e.Requests() {
		if r.Method == method && re.MatchString(r.Path) {
			out = append(out, r)
		}
	}
	return out
}

func (e *Engine) serve(w http.ResponseWriter, r *http.Request) {
	path, version := r.URL.Path, ""
	if m := versionPrefix.FindStringSubmatch(path); m != nil {
		version, path = m[1], m[2]
	}
	var body []byte
	if r.Body != nil && r.Header.Get("Upgrade") == "" {
		body, _ = io.ReadAll(r.Body)
	}
	e.mu.Lock()
	e.requests = append(e.requests, Request{
		Method: r.Method, Path: path, APIVersion: version, Query: r.URL.Query(), Header: r.Header.Clone(), Body: body,
	})
	handlers := append([]route(nil), e.handlers...)
	e.mu.Unlock()

	w.Header().Set("Api-Version", e.opts.APIVersion)
	w.Header().Set("Ostype", "linux")
	w.Header().Set("Builder-Version", "2")
	switch {
	case path == "/_ping":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("OK"))
		return
	case path == "/version" && r.Method == http.MethodGet:
		JSON(w, http.StatusOK, map[string]any{
			"Platform": map[string]string{"Name": e.opts.PlatformName},
			"Version":  e.opts.Version, "ApiVersion": e.opts.APIVersion, "MinAPIVersion": e.opts.MinAPIVersion,
			"Os": "linux", "Arch": "amd64", "KernelVersion": "6.8.0",
		})
		return
	case path == "/info" && r.Method == http.MethodGet:
		JSON(w, http.StatusOK, map[string]any{
			"ID": "FAKE:ENGINE:ID", "Name": "fake-host", "OSType": "linux", "Architecture": "x86_64",
			"OperatingSystem": e.opts.OperatingSystem, "KernelVersion": "6.8.0", "DockerRootDir": e.opts.DockerRootDir,
			"Driver": "overlay2", "CgroupVersion": "2", "NCPU": 4, "MemTotal": 8 << 30,
			"SecurityOptions": e.opts.SecurityOptions, "ServerVersion": e.opts.Version,
		})
		return
	}
	for _, h := range handlers {
		if (h.method == "" || h.method == r.Method) && h.re.MatchString(path) {
			// Handlers see the unversioned path.
			r.URL.Path = path
			h.h(w, r)
			return
		}
	}
	Error(w, http.StatusNotFound, fmt.Sprintf("fake engine: no handler for %s %s", r.Method, path))
}

// JSON writes v as a JSON response.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes an Engine error response.
func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"message": msg})
}

// Multiplexed frames p as the Engine's stdout (1) / stderr (2) stream format.
func Multiplexed(stream byte, p []byte) []byte {
	h := []byte{stream, 0, 0, 0, byte(len(p) >> 24), byte(len(p) >> 16), byte(len(p) >> 8), byte(len(p))} //nolint:gosec // G115: big-endian frame length
	return append(h, p...)
}

// Stream writes newline-delimited JSON messages (pull/build/load streams).
func Stream(w http.ResponseWriter, msgs ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	for _, m := range msgs {
		_ = enc.Encode(m)
	}
}
