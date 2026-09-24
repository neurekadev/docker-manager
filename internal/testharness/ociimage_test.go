package testharness

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeRegistry implements the push subset of the distribution API with
// basic auth and digest verification.
type fakeRegistry struct {
	mu        sync.Mutex
	blobs     map[string][]byte
	manifests map[string][]byte
}

func (f *fakeRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if u, p, ok := r.BasicAuth(); !ok || u != "u" || p != "p" {
		w.Header().Set("WWW-Authenticate", `Basic realm="fake"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/blobs/uploads/"):
		w.Header().Set("Location", r.URL.Path+"session-1?_state=abc")
		w.WriteHeader(http.StatusAccepted)
	case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/blobs/uploads/"):
		d := r.URL.Query().Get("digest")
		if r.URL.Query().Get("_state") != "abc" || d != "sha256:"+PayloadHash(body) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.blobs[d] = body
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/"):
		if r.Header.Get("Content-Type") != OCIManifestType {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var m struct {
			Config struct{ Digest string }   `json:"config"`
			Layers []struct{ Digest string } `json:"layers"`
		}
		if json.Unmarshal(body, &m) != nil || f.blobs[m.Config.Digest] == nil || len(m.Layers) != 1 || f.blobs[m.Layers[0].Digest] == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.manifests[r.URL.Path] = body
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestPushOCIImage(t *testing.T) {
	fr := &fakeRegistry{blobs: map[string][]byte{}, manifests: map[string][]byte{}}
	srv := httptest.NewServer(fr)
	t.Cleanup(srv.Close)

	img, err := NewTestImage("arm64", "content")
	if err != nil {
		t.Fatal(err)
	}
	if err := PushOCIImage(t.Context(), srv.Client(), srv.URL, "u", "p", "dockyard/hello", "v1", img); err != nil {
		t.Fatal(err)
	}
	if got := fr.manifests["/v2/dockyard/hello/manifests/v1"]; !bytes.Equal(got, img.Manifest) {
		t.Errorf("stored manifest = %s", got)
	}
	if err := PushOCIImage(t.Context(), srv.Client(), srv.URL, "u", "bad", "dockyard/hello", "v1", img); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("bad credentials: err = %v", err)
	}
}

func TestNewTestImageIsReproducibleAndValid(t *testing.T) {
	a, err := NewTestImage("amd64", "x")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewTestImage("amd64", "x")
	c, _ := NewTestImage("arm64", "x")
	if a.Digest != b.Digest {
		t.Error("same input, different digests")
	}
	if a.Digest == c.Digest || a.LayerDigest != c.LayerDigest {
		t.Error("architecture must change the config (and manifest) but not the layer")
	}
	var cfg struct {
		Architecture string
		RootFS       struct {
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}
	if err := json.Unmarshal(a.Config, &cfg); err != nil || cfg.Architecture != "amd64" || len(cfg.RootFS.DiffIDs) != 1 || cfg.RootFS.DiffIDs[0] != a.DiffID {
		t.Errorf("config = %s (%v)", a.Config, err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(a.Layer))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	h, err := tr.Next()
	if err != nil || h.Name != "dockyard-fixture.txt" {
		t.Fatalf("layer entry = %+v, %v", h, err)
	}
	if data, _ := io.ReadAll(tr); string(data) != "x" {
		t.Errorf("layer content = %q", data)
	}
}

func TestS3ClientSignsRequests(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AK/") || r.Header.Get("X-Amz-Content-Sha256") != PayloadHash(body) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPut && strings.Count(r.URL.Path, "/") == 1:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut:
			objects[r.URL.Path] = body
		case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
			for k := range objects {
				if strings.HasPrefix(k, "/b/"+r.URL.Query().Get("prefix")) {
					_, _ = io.WriteString(w, "<Contents><Key>"+strings.TrimPrefix(k, "/b/")+"</Key></Contents>")
				}
			}
		case r.Method == http.MethodGet:
			if b, ok := objects[r.URL.Path]; ok {
				_, _ = w.Write(b)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c := &S3Client{Endpoint: srv.URL, AccessKey: "AK", SecretKey: "SK"}
	ctx := t.Context()
	if err := c.CreateBucket(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if err := c.PutObject(ctx, "b", "dir/k.txt", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if got, err := c.GetObject(ctx, "b", "dir/k.txt"); err != nil || string(got) != "v" {
		t.Errorf("get = %q, %v", got, err)
	}
	if keys, err := c.ListKeys(ctx, "b", "dir/"); err != nil || len(keys) != 1 || keys[0] != "dir/k.txt" {
		t.Errorf("list = %v, %v", keys, err)
	}
	if _, err := c.GetObject(ctx, "b", "missing"); err == nil {
		t.Error("missing object: no error")
	}
}
