package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Cache policies for embedded UI assets.
const (
	// CacheImmutable is used for content-hashed files under /_app/immutable/.
	CacheImmutable = "public, max-age=31536000, immutable"
	// CacheRevalidate is used for index.html, the service worker and every
	// other unhashed file: browsers must revalidate (ETag) before reuse.
	CacheRevalidate = "no-cache"
)

type asset struct {
	data        []byte
	etag        string
	contentType string
}

// spa serves the embedded SvelteKit build: real files where they exist,
// index.html for client-side routes (deep links), and a hard 404 for
// missing /_app/* assets so a stale page never receives HTML as JavaScript.
type spa struct {
	assets        map[string]asset // key: clean path without leading slash
	scriptHashes  []string         // CSP hashes of inline scripts in index.html
	indexFallback asset
}

func newSPA(ui fs.FS) (*spa, error) {
	s := &spa{assets: map[string]asset{}}
	err := fs.WalkDir(ui, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(ui, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		s.assets[p] = asset{
			data:        b,
			etag:        `"` + base64.RawURLEncoding.EncodeToString(sum[:18]) + `"`,
			contentType: contentTypeFor(p),
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("server: load UI assets: %w", err)
	}
	idx, ok := s.assets["index.html"]
	if !ok {
		return nil, errors.New("server: UI assets lack index.html")
	}
	s.indexFallback = idx
	s.scriptHashes = inlineScriptHashes(idx.data)
	return s, nil
}

func (s *spa) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	clean := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if clean == "" {
		clean = "index.html"
	}

	if a, ok := s.assets[clean]; ok {
		cache := CacheRevalidate
		if strings.HasPrefix(clean, "_app/immutable/") {
			cache = CacheImmutable
		}
		s.serve(w, r, a, cache)
		return
	}
	if clean == "_app" || strings.HasPrefix(clean, "_app/") {
		w.Header().Set("Cache-Control", CacheRevalidate)
		http.NotFound(w, r)
		return
	}
	// Client-side route: serve the app shell.
	s.serve(w, r, s.indexFallback, CacheRevalidate)
}

func (s *spa) serve(w http.ResponseWriter, r *http.Request, a asset, cache string) {
	h := w.Header()
	h.Set("Content-Type", a.contentType)
	h.Set("Cache-Control", cache)
	h.Set("ETag", a.etag)
	// ServeContent handles If-None-Match/304, HEAD and Range. A zero modtime
	// disables Last-Modified (embedded files have none).
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(a.data))
}

// contentTypes avoids mime.TypeByExtension, which consults the host OS
// registry and differs between machines (notably Windows).
var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json",
	".webmanifest": "application/manifest+json",
	".map":         "application/json",
	".txt":         "text/plain; charset=utf-8",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".webp":        "image/webp",
	".avif":        "image/avif",
	".gif":         "image/gif",
	".ico":         "image/x-icon",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".wasm":        "application/wasm",
	".xml":         "application/xml",
}

func contentTypeFor(p string) string {
	if ct, ok := contentTypes[strings.ToLower(path.Ext(p))]; ok {
		return ct
	}
	return "application/octet-stream"
}

var inlineScriptRE = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)

// inlineScriptHashes returns CSP source expressions ('sha256-…') for every
// inline <script> in html. SvelteKit's SPA shell boots from one inline
// script; hashing it lets the CSP forbid 'unsafe-inline' scripts.
func inlineScriptHashes(html []byte) []string {
	var out []string
	for _, m := range inlineScriptRE.FindAllSubmatch(html, -1) {
		attrs := strings.ToLower(string(m[1]))
		if strings.Contains(attrs, "src=") || len(m[2]) == 0 {
			continue
		}
		sum := sha256.Sum256(m[2])
		out = append(out, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	sort.Strings(out)
	return out
}

// contentSecurityPolicy builds the baseline CSP. Scripts are limited to
// same-origin files plus the hashed inline bootstrap; styles allow
// 'unsafe-inline' because Svelte transitions and component libraries set
// inline styles. frame-ancestors 'none' forbids embedding (clickjacking).
func contentSecurityPolicy(scriptHashes []string) string {
	script := "script-src 'self'"
	if len(scriptHashes) > 0 {
		script += " " + strings.Join(scriptHashes, " ")
	}
	return strings.Join([]string{
		"default-src 'self'",
		script,
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"connect-src 'self'",
		"worker-src 'self'",
		"manifest-src 'self'",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}
