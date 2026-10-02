// Package regtest is a scripted OCI Distribution registry (httptest, TLS)
// for Docker-free tests of registry checks (#19, #20): Bearer token or
// Basic auth, private and denied repositories, scripted failures (429 with
// Retry-After, 5xx), multi-platform indexes, image config blobs and request
// accounting, and response headers such as rate limits.
package regtest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Auth modes.
const (
	AuthBearer = "bearer"
	AuthBasic  = "basic"
	AuthNone   = ""
)

// ManifestBody is a minimal OCI image manifest.
const ManifestBody = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:c"},"layers":[]}`

// Registry is the fake registry. Fields may be changed between requests
// (under Lock/Unlock when requests may be in flight).
type Registry struct {
	Server *httptest.Server

	mu sync.Mutex
	// Auth is AuthBearer, AuthBasic or AuthNone.
	Auth     string
	User     string
	Password string
	// Private repositories need a valid credential; others accept
	// anonymous tokens.
	Private map[string]bool
	// Denied repositories answer 403 even with a valid credential.
	Denied map[string]bool
	// NoDigest omits Docker-Content-Digest on HEAD.
	NoDigest bool
	// Realm overrides the token realm (default <server>/token).
	Realm string
	// Headers are set on every authorized manifest response (rate-limit
	// headers, for example).
	Headers map[string]string
	// Block, when set, holds manifest requests until closed; Entered is
	// signaled (non-blocking) when a manifest request arrives.
	Block   chan struct{}
	Entered chan struct{}

	manifests map[string]manifest
	blobs     map[string][]byte
	fail      []failure
	tokens    map[string]string
	// AuthHeaders records every Authorization header received (manifest
	// and token requests), for secrecy assertions.
	authHeaders []string

	ManifestHits, HeadHits, GetHits, TokenHits int
	// BlobHits counts blob requests (not in ManifestHits).
	BlobHits int
	// AnonHits counts manifest requests without Authorization.
	AnonHits int
}

type manifest struct {
	mediaType string
	body      []byte
}

type failure struct {
	status  int
	headers map[string]string
}

// New starts a registry with the given auth mode; it stops with the test.
func New(t testing.TB, auth, user, password string) *Registry {
	t.Helper()
	r := &Registry{Auth: auth, User: user, Password: password, Private: map[string]bool{}, Denied: map[string]bool{},
		manifests: map[string]manifest{}, blobs: map[string][]byte{}, tokens: map[string]string{}}
	r.Server = httptest.NewTLSServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.Server.Close)
	return r
}

// Lock guards field changes while requests may be in flight.
func (r *Registry) Lock() { r.mu.Lock() }

// Unlock releases Lock.
func (r *Registry) Unlock() { r.mu.Unlock() }

// Host is host:port of the registry.
func (r *Registry) Host() string { return strings.TrimPrefix(r.Server.URL, "https://") }

// Client is an HTTP client trusting the registry's certificate.
func (r *Registry) Client() *http.Client { return r.Server.Client() }

// Digest returns the sha256 digest of b.
func Digest(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// Put stores a manifest under repo:tag and repo@digest and returns the digest.
func (r *Registry) Put(repo, tag, mediaType string, body []byte) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := Digest(body)
	r.manifests[repo+":"+tag] = manifest{mediaType, body}
	r.manifests[repo+":"+d] = manifest{mediaType, body}
	return d
}

// PutBlob stores a blob of repo and returns its digest.
func (r *Registry) PutBlob(repo string, body []byte) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := Digest(body)
	r.blobs[repo+"@"+d] = body
	return d
}

// PutImage stores an image config created at created (zero: no "created"
// field) and a single-platform manifest naming it under repo:tag; salt
// makes otherwise equal images differ. It returns the manifest digest.
func (r *Registry) PutImage(repo, tag string, created time.Time, salt string) string {
	cfg := map[string]any{"architecture": "amd64", "os": "linux", "config": map[string]any{"Labels": map[string]string{"salt": salt}}}
	if !created.IsZero() {
		cfg["created"] = created.UTC().Format(time.RFC3339Nano)
	}
	body, _ := json.Marshal(cfg)
	cd := r.PutBlob(repo, body)
	m, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": cd, "size": len(body)},
		"layers": []any{}})
	return r.Put(repo, tag, "application/vnd.oci.image.manifest.v1+json", m)
}

// BlobCount returns the number of blob requests.
func (r *Registry) BlobCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.BlobHits
}

// FailNext makes the next n manifest requests answer status with headers.
func (r *Registry) FailNext(status int, headers map[string]string, n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for range n {
		r.fail = append(r.fail, failure{status, headers})
	}
}

// Counts returns manifest, token and anonymous manifest request counts.
func (r *Registry) Counts() (manifests, tokens, anon int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ManifestHits, r.TokenHits, r.AnonHits
}

// AuthHeaders returns every Authorization header received.
func (r *Registry) AuthHeaders() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.authHeaders...)
}

// BasicCredential returns the base64 user:password as sent by a client.
func (r *Registry) BasicCredential() string {
	return base64.StdEncoding.EncodeToString([]byte(r.User + ":" + r.Password))
}

func (r *Registry) serve(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/token" {
		r.serveToken(w, req)
		return
	}
	rest, ok := strings.CutPrefix(req.URL.Path, "/v2/")
	if !ok {
		http.NotFound(w, req)
		return
	}
	if repo, digest, ok := strings.Cut(rest, "/blobs/"); ok {
		r.serveBlob(w, req, repo, digest)
		return
	}
	repo, ref, ok := strings.Cut(rest, "/manifests/")
	if !ok {
		http.NotFound(w, req)
		return
	}
	r.mu.Lock()
	block, entered := r.Block, r.Entered
	r.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if block != nil {
		<-block
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ManifestHits++
	if req.Method == http.MethodHead {
		r.HeadHits++
	} else {
		r.GetHits++
	}
	authz := req.Header.Get("Authorization")
	if authz == "" {
		r.AnonHits++
	} else {
		r.authHeaders = append(r.authHeaders, authz)
	}
	if len(r.fail) > 0 {
		fl := r.fail[0]
		r.fail = r.fail[1:]
		for k, v := range fl.headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(fl.status)
		return
	}
	if !r.authorizedLocked(w, req, repo, authz) {
		return
	}
	for k, v := range r.Headers {
		w.Header().Set(k, v)
	}
	m, ok := r.manifests[repo+":"+ref]
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}`))
		return
	}
	w.Header().Set("Content-Type", m.mediaType)
	if !r.NoDigest || req.Method == http.MethodGet {
		w.Header().Set("Docker-Content-Digest", Digest(m.body))
	}
	if req.Method == http.MethodGet {
		_, _ = w.Write(m.body)
	}
}

// authorizedLocked answers the auth challenge or 403 and reports whether
// the request may proceed (r.mu held).
func (r *Registry) authorizedLocked(w http.ResponseWriter, req *http.Request, repo, authz string) bool {
	switch r.Auth {
	case AuthBearer:
		tok := strings.TrimPrefix(authz, "Bearer ")
		who, valid := r.tokens[tok]
		if !strings.HasPrefix(authz, "Bearer ") || !valid {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+r.realmURL()+`",service="fake",scope="repository:`+repo+`:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		if r.Private[repo] && who == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+r.realmURL()+`",service="fake",error="insufficient_scope"`)
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
	case AuthBasic:
		u, p, ok := req.BasicAuth()
		if !ok || u != r.User || p != r.Password {
			w.Header().Set("WWW-Authenticate", `Basic realm="fake"`)
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
	}
	if r.Denied[repo] {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"code":"DENIED","message":"requested access to the resource is denied"}]}`))
		return false
	}
	return true
}

// serveBlob serves a stored blob (the same auth as manifests; scripted
// failures apply to manifests only).
func (r *Registry) serveBlob(w http.ResponseWriter, req *http.Request, repo, digest string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.BlobHits++
	authz := req.Header.Get("Authorization")
	if authz != "" {
		r.authHeaders = append(r.authHeaders, authz)
	}
	if !r.authorizedLocked(w, req, repo, authz) {
		return
	}
	b, ok := r.blobs[repo+"@"+digest]
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"code":"BLOB_UNKNOWN","message":"blob unknown"}]}`))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", digest)
	if req.Method == http.MethodGet {
		_, _ = w.Write(b)
	}
}

func (r *Registry) realmURL() string {
	if r.Realm != "" {
		return r.Realm
	}
	return r.Server.URL + "/token"
}

func (r *Registry) serveToken(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.TokenHits++
	if a := req.Header.Get("Authorization"); a != "" {
		r.authHeaders = append(r.authHeaders, a)
	}
	who := ""
	if u, p, ok := req.BasicAuth(); ok {
		if u != r.User || p != r.Password {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"details":"incorrect username or password"}`))
			return
		}
		who = u
	}
	tok := "tok-" + base64.RawURLEncoding.EncodeToString([]byte(who+"|"+req.URL.Query().Get("scope")+"|"+strconv.Itoa(r.TokenHits)))
	r.tokens[tok] = who
	_ = json.NewEncoder(w).Encode(map[string]any{"token": tok, "expires_in": 300})
}
