// Package s3probetest is a minimal in-memory S3 endpoint for connection
// test tests: object PUT/GET/DELETE and the bucket's Object Lock
// configuration, with SigV4 credential checks (the access key must match;
// signatures are checked for presence and shape only).
package s3probetest

import (
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Server is the fake endpoint.
type Server struct {
	*httptest.Server
	AccessKey string
	Bucket    string
	// ObjectLock makes the bucket report Object Lock enabled.
	ObjectLock bool
	// DenyDelete refuses deletions (403), like a write-once policy.
	DenyDelete bool

	mu      sync.Mutex
	objects map[string][]byte
	// Requests records "METHOD path?query" of every request.
	Requests []string
}

// New starts a fake with path-style addressing.
func New(t testing.TB, accessKey, bucket string) *Server {
	t.Helper()
	s := &Server{AccessKey: accessKey, Bucket: bucket, objects: map[string][]byte{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.Requests = append(s.Requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	s.mu.Unlock()
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential="+s.AccessKey+"/") || !strings.Contains(auth, "Signature=") ||
		r.Header.Get("X-Amz-Date") == "" || r.Header.Get("X-Amz-Content-Sha256") == "" {
		http.Error(w, "<Error><Code>InvalidAccessKeyId</Code></Error>", http.StatusForbidden)
		return
	}
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if bucket != s.Bucket {
		http.Error(w, "<Error><Code>NoSuchBucket</Code></Error>", http.StatusNotFound)
		return
	}
	if _, ok := r.URL.Query()["object-lock"]; ok {
		if !s.ObjectLock {
			http.Error(w, "<Error><Code>ObjectLockConfigurationNotFoundError</Code></Error>", http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, "<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled></ObjectLockConfiguration>")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if q := r.URL.Query(); r.Method == http.MethodGet && key == "" && q.Get("list-type") == "2" {
		s.list(w, q.Get("prefix"), q.Get("delimiter"))
		return
	}
	switch r.Method {
	case http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		s.objects[key] = b
	case http.MethodGet:
		b, ok := s.objects[key]
		if !ok {
			http.Error(w, "<Error><Code>NoSuchKey</Code></Error>", http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	case http.MethodDelete:
		if s.DenyDelete {
			http.Error(w, "<Error><Code>AccessDenied</Code></Error>", http.StatusForbidden)
			return
		}
		delete(s.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// list answers ListObjectsV2 (common prefixes only with a delimiter).
func (s *Server) list(w http.ResponseWriter, prefix, delim string) {
	seen := map[string]bool{}
	var b strings.Builder
	b.WriteString("<ListBucketResult>")
	keys := make([]string, 0, len(s.objects))
	for k := range s.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		rest, ok := strings.CutPrefix(k, prefix)
		if !ok {
			continue
		}
		if delim != "" {
			if i := strings.Index(rest, delim); i >= 0 {
				cp := prefix + rest[:i+len(delim)]
				if !seen[cp] {
					seen[cp] = true
					b.WriteString("<CommonPrefixes><Prefix>" + html.EscapeString(cp) + "</Prefix></CommonPrefixes>")
				}
				continue
			}
		}
		b.WriteString("<Contents><Key>" + html.EscapeString(k) + "</Key></Contents>")
	}
	b.WriteString("</ListBucketResult>")
	_, _ = io.WriteString(w, b.String())
}

// Put stores an object (tests seed "directories").
func (s *Server) Put(key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = data
}

// Objects returns the number of stored objects.
func (s *Server) Objects() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objects)
}
