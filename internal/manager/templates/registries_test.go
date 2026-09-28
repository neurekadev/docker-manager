package templates

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

const svgIcon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"/>`

// fakeRegistry is another instance's public registry.
type fakeRegistry struct {
	mu       sync.Mutex
	srv      *httptest.Server
	index    RegistryIndex
	archive  []byte
	icon     []byte
	requests []string
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func newFakeRegistry(t *testing.T) *fakeRegistry {
	t.Helper()
	f := &fakeRegistry{archive: []byte("archive-bytes"), icon: []byte(svgIcon)}
	f.index = RegistryIndex{Format: RegistryFormat, InstanceID: "remote-1", Name: "Friend", Templates: []RegistryEntry{{
		ID: "tpl-a", Name: "Cloud", Description: "files", Tags: []string{"Cloud", "files"}, UpdatedAt: testutil.Epoch,
		Icon: &RegistryIcon{MediaType: IconSVG, SHA256: sum(f.icon), Size: int64(len(f.icon)), URL: registryRoutes + "tpl-a/icon?v=" + sum(f.icon)},
		Versions: []RegistryVersion{{Number: 2, Label: "1.1.0", PublishedAt: testutil.Epoch,
			Archive: RegistryArchive{SHA256: sum(f.archive), Size: int64(len(f.archive)), URL: registryRoutes + "tpl-a/versions/2/archive"}}},
	}, {
		// Dropped by validation: no version.
		ID: "tpl-empty", Name: "Empty",
	}}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.URL.Path)
		switch {
		case r.URL.Path == registryIndexPath:
			b, _ := json.Marshal(f.index)
			etag := `"` + sum(b)[:16] + `"`
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag)
			_, _ = w.Write(b)
		case strings.HasSuffix(r.URL.Path, "/icon"):
			_, _ = w.Write(f.icon)
		case strings.HasSuffix(r.URL.Path, "/archive"):
			_, _ = w.Write(f.archive)
		case r.URL.Path == "/elsewhere":
			http.Redirect(w, r, "https://example.invalid/steal", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRegistry) set(fn func(*fakeRegistry)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func TestNormalizeRegistryURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://Docker.Example.com":                               "https://docker.example.com",
		"docker.example.com":                                       "https://docker.example.com",
		"https://docker.example.com/registry":                      "https://docker.example.com",
		"https://docker.example.com:8443/api/v1/template-registry": "https://docker.example.com:8443",
		"http://localhost:8080":                                    "http://localhost:8080",
		"http://127.0.0.1:8080/":                                   "http://127.0.0.1:8080",
	} {
		if got, err := NormalizeRegistryURL(in); err != nil || got != want {
			t.Errorf("NormalizeRegistryURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for in, class := range map[string]string{
		"http://docker.example.com":          RegistryInsecure,
		"http://192.168.1.5":                 RegistryInsecure,
		"ftp://docker.example.com":           RegistryInvalid,
		"https://docker.example.com/other":   RegistryInvalid,
		"https://user:pw@docker.example.com": RegistryInvalid,
		"":                                   RegistryInvalid,
	} {
		var re *RegistryError
		if _, err := NormalizeRegistryURL(in); !errors.As(err, &re) || re.Class != class {
			t.Errorf("NormalizeRegistryURL(%q) error %v, want class %s", in, err, class)
		}
	}
}

func TestRegistryClientChecksEverything(t *testing.T) {
	f := newFakeRegistry(t)
	c := NewRegistryClient(f.srv.Client())
	ctx := testutil.Context(t)
	idx, etag, notModified, err := c.FetchIndex(ctx, f.srv.URL, "")
	if err != nil || notModified || etag == "" || len(idx.Templates) != 1 || idx.Templates[0].ID != "tpl-a" {
		t.Fatalf("index = %+v, %q, %v, %v", idx, etag, notModified, err)
	}
	if got := strings.Join(idx.Templates[0].Tags, ","); got != "cloud,files" {
		t.Errorf("tags = %s, want normalized", got)
	}
	if _, _, notModified, err := c.FetchIndex(ctx, f.srv.URL, etag); err != nil || !notModified {
		t.Fatalf("revalidation: %v %v", notModified, err)
	}
	a := idx.Templates[0].Versions[0].Archive
	if b, err := c.FetchArchive(ctx, f.srv.URL, a, 1<<20); err != nil || string(b) != "archive-bytes" {
		t.Fatalf("archive = %q, %v", b, err)
	}
	bad := a
	bad.SHA256 = strings.Repeat("0", 64)
	if _, err := c.FetchArchive(ctx, f.srv.URL, bad, 1<<20); err == nil {
		t.Fatal("a digest mismatch was accepted")
	}
	if _, err := c.FetchArchive(ctx, f.srv.URL, a, 4); err == nil {
		t.Fatal("an archive above the limit was accepted")
	}
	for _, ref := range []string{"https://evil.example/x", "/api/v1/other", registryRoutes + "../../../admin", "//evil.example" + registryRoutes} {
		if _, err := resolve(f.srv.URL, ref); err == nil {
			t.Errorf("resolve(%q) accepted a path outside the registry", ref)
		}
	}
	if _, _, _, err := c.get(ctx, f.srv.URL+"/elsewhere", "", 100, time.Second); err == nil {
		t.Fatal("a redirect to another origin was followed")
	}
	f.set(func(f *fakeRegistry) { f.index.Format = "something/v9" })
	var re *RegistryError
	if _, _, _, err := c.FetchIndex(ctx, f.srv.URL, ""); !errors.As(err, &re) || re.Class != RegistryInvalid {
		t.Fatalf("unknown format: %v", err)
	}
}

func TestRegistriesAddSyncAndRemove(t *testing.T) {
	remote := newFakeRegistry(t)
	f := newFixture(t, func(o *Options) {
		o.InstanceID, o.HTTPClient, o.SyncInterval = "self-1", remote.srv.Client(), time.Hour
	})
	r, err := f.svc.AddRegistry(f.ctx, remote.srv.URL, "owner")
	if err != nil || r.InstanceID != "remote-1" || r.Name != "Friend" || r.Templates != 1 || r.Status != domain.TemplateRegistryOK {
		t.Fatalf("add = %+v, %v", r, err)
	}
	if _, err := f.svc.AddRegistry(f.ctx, remote.srv.URL, "owner"); !errors.Is(err, domain.ErrTemplateRegistryExists) {
		t.Fatalf("adding it twice: %v", err)
	}
	icon, data, err := f.svc.RegistryIcon(f.ctx, "remote-1", "tpl-a")
	if err != nil || string(data) != svgIcon || icon.MediaType != IconSVG {
		t.Fatalf("cached icon %+v, %v", icon, err)
	}
	rt, v, archive, err := f.svc.RegistryArchive(f.ctx, "remote-1", "tpl-a", 0)
	if err != nil || rt.Name != "Cloud" || v.Label != "1.1.0" || string(archive) != "archive-bytes" {
		t.Fatalf("archive %+v %+v %q %v", rt, v, archive, err)
	}

	// The icon changes and a template disappears on the next sync.
	newIcon := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 2 2"/>`)
	remote.set(func(r *fakeRegistry) {
		r.icon = newIcon
		r.index.Templates[0].Icon = &RegistryIcon{MediaType: IconSVG, SHA256: sum(newIcon), Size: int64(len(newIcon)), URL: registryRoutes + "tpl-a/icon?v=new"}
	})
	if _, err := f.svc.SyncRegistry(f.ctx, "remote-1"); err != nil {
		t.Fatal(err)
	}
	if _, data, _ := f.svc.RegistryIcon(f.ctx, "remote-1", "tpl-a"); string(data) != string(newIcon) {
		t.Fatalf("icon after sync = %q", data)
	}
	remote.set(func(r *fakeRegistry) { r.index.Templates = nil })
	if r, _ := f.svc.SyncRegistry(f.ctx, "remote-1"); r.Templates != 0 {
		t.Fatalf("templates after the remote removed them: %d", r.Templates)
	}

	// A failing sync is recorded and backs off.
	remote.srv.Close()
	r, err = f.svc.SyncRegistry(f.ctx, "remote-1")
	if err != nil || r.Status != domain.TemplateRegistryError || r.ErrorClass != RegistryUnreachable || r.Failures != 1 {
		t.Fatalf("failed sync = %+v, %v", r, err)
	}
	if f.svc.due(r, r.AttemptedAt.Add(time.Hour)) || !f.svc.due(r, r.AttemptedAt.Add(2*time.Hour)) {
		t.Fatal("the backoff after one failure is not twice the interval")
	}

	if err := f.svc.RemoveRegistry(f.ctx, "remote-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.RegistryIcon(f.ctx, "remote-1", "tpl-a"); !errors.Is(err, domain.ErrTemplateIconNotFound) {
		t.Fatalf("icon after removal: %v", err)
	}
	if _, err := f.svc.Registry(f.ctx, "remote-1"); !errors.Is(err, domain.ErrTemplateRegistryNotFound) {
		t.Fatalf("registry after removal: %v", err)
	}
}

func TestAddingThisInstanceIsRefused(t *testing.T) {
	remote := newFakeRegistry(t)
	f := newFixture(t, func(o *Options) { o.InstanceID, o.HTTPClient = "remote-1", remote.srv.Client() })
	if _, err := f.svc.AddRegistry(f.ctx, remote.srv.URL, ""); !errors.Is(err, domain.ErrTemplateRegistryIsSelf) {
		t.Fatalf("adding this instance: %v", err)
	}
}

// Re-adding a registry under a new address keeps its identity: stacks of
// its templates find its icons again.
func TestReaddingARegistryRestoresItsTemplates(t *testing.T) {
	remote := newFakeRegistry(t)
	f := newFixture(t, func(o *Options) { o.InstanceID, o.HTTPClient = "self-1", remote.srv.Client() })
	if _, err := f.svc.AddRegistry(f.ctx, remote.srv.URL, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RemoveRegistry(f.ctx, "remote-1"); err != nil {
		t.Fatal(err)
	}
	r, err := f.svc.AddRegistry(f.ctx, remote.srv.URL+"/registry", "")
	if err != nil || r.InstanceID != "remote-1" {
		t.Fatalf("re-add = %+v, %v", r, err)
	}
	if _, _, err := f.svc.RegistryIcon(f.ctx, "remote-1", "tpl-a"); err != nil {
		t.Fatalf("icon after re-adding: %v", err)
	}
}
