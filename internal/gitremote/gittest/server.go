// Package gittest is a scripted HTTP(S) Git server for Docker-free tests
// of ref listing (#33): smart (pkt-line) or dumb ref advertisements,
// basic-auth protected private repositories and request accounting.
package gittest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Repo is one repository's refs.
type Repo struct {
	// Head is HEAD's target ("refs/heads/main").
	Head string
	// Refs maps full ref names to object IDs.
	Refs map[string]string
	// Peeled maps annotated tag refs to their commit.
	Peeled map[string]string
	// Private repositories need the server's credential.
	Private bool
}

// Server is the fake Git server.
type Server struct {
	Server   *httptest.Server
	User     string
	Password string
	// Dumb serves the static (dumb protocol) layout.
	Dumb bool

	mu    sync.Mutex
	repos map[string]*Repo
	auths []string
	hits  int
	// FailStatus, when set, answers every request with that status.
	FailStatus int
}

// New starts a TLS server (tls) or plain HTTP server.
func New(t testing.TB, tls bool, user, password string) *Server {
	t.Helper()
	s := &Server{User: user, Password: password, repos: map[string]*Repo{}}
	if tls {
		s.Server = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	} else {
		s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	}
	t.Cleanup(s.Server.Close)
	return s
}

// Add registers a repository at path ("/org/app.git").
func (s *Server) Add(path string, r *Repo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repos[path] = r
}

// URL returns the clone URL of path.
func (s *Server) URL(path string) string { return s.Server.URL + path }

// Client trusts the server's certificate.
func (s *Server) Client() *http.Client { return s.Server.Client() }

// AuthHeaders returns every Authorization header received.
func (s *Server) AuthHeaders() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.auths...)
}

// Hits counts requests.
func (s *Server) Hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hits++
	if a := r.Header.Get("Authorization"); a != "" {
		s.auths = append(s.auths, a)
	}
	if s.FailStatus != 0 {
		w.WriteHeader(s.FailStatus)
		return
	}
	var repo *Repo
	var rest string
	for p, rp := range s.repos {
		if after, ok := strings.CutPrefix(r.URL.Path, p+"/"); ok {
			repo, rest = rp, after
		}
	}
	if repo == nil {
		http.NotFound(w, r)
		return
	}
	if repo.Private {
		u, p, ok := r.BasicAuth()
		if !ok || u != s.User || p != s.Password {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	switch {
	case rest == "info/refs" && !s.Dumb && r.URL.Query().Get("service") == "git-upload-pack":
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = w.Write([]byte(smart(repo)))
	case rest == "info/refs":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(dumb(repo)))
	case rest == "HEAD" && s.Dumb:
		_, _ = fmt.Fprintf(w, "ref: %s\n", repo.Head)
	default:
		http.NotFound(w, r)
	}
}

func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

func names(r *Repo) []string {
	out := make([]string, 0, len(r.Refs))
	for n := range r.Refs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func smart(r *Repo) string {
	var b strings.Builder
	b.WriteString(pkt("# service=git-upload-pack\n"))
	b.WriteString("0000")
	caps := "multi_ack thin-pack side-band side-band-64k ofs-delta shallow no-progress include-tag"
	if r.Head != "" {
		caps += " symref=HEAD:" + r.Head
	}
	first := true
	emit := func(sha, name string) {
		if first {
			b.WriteString(pkt(sha + " " + name + "\x00" + caps + "\n"))
			first = false
			return
		}
		b.WriteString(pkt(sha + " " + name + "\n"))
	}
	if head, ok := r.Refs[r.Head]; ok {
		emit(head, "HEAD")
	}
	for _, n := range names(r) {
		emit(r.Refs[n], n)
		if c, ok := r.Peeled[n]; ok {
			emit(c, n+"^{}")
		}
	}
	if first {
		emit(strings.Repeat("0", 40), "capabilities^{}")
	}
	b.WriteString("0000")
	return b.String()
}

func dumb(r *Repo) string {
	var b strings.Builder
	for _, n := range names(r) {
		fmt.Fprintf(&b, "%s\t%s\n", r.Refs[n], n)
		if c, ok := r.Peeled[n]; ok {
			fmt.Fprintf(&b, "%s\t%s^{}\n", c, n)
		}
	}
	return b.String()
}
