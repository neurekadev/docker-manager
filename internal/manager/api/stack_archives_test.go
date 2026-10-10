package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/authztest"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/authz/policy"
	"github.com/neurekadev/docker-manager/internal/manager/stackarchives"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// fakeArchives records calls (the real service is tested in
// internal/manager/stackarchives).
type fakeArchives struct {
	mu    sync.Mutex
	calls []string
	// mayData is what MayDownload answered for the stack's data volume.
	mayData []bool
	maxSize int64
	upload  stackarchives.Upload
	archive []byte
}

func (f *fakeArchives) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeArchives) MaxSize() int64 { return f.maxSize }

func (f *fakeArchives) PreviewExport(_ context.Context, st domain.Stack, r stackarchives.ExportRequest) (stackarchives.ExportPlan, error) {
	f.record("preview-export:" + st.ID)
	f.mu.Lock()
	f.mayData = append(f.mayData, r.MayDownload("shop_data"))
	f.mu.Unlock()
	return stackarchives.ExportPlan{StackID: st.ID, Volumes: []stackarchives.ExportVolume{{Key: "data", Name: "shop_data", Included: true, Bytes: 10}},
		Running: []string{"web"}}, nil
}

func (f *fakeArchives) StartExport(_ context.Context, _ authz.Principal, st domain.Stack, r stackarchives.ExportRequest) (domain.Job, error) {
	f.record("start-export:" + st.ID + ":" + strings.Join(r.ExcludeVolumes, ","))
	return domain.Job{ID: "job-exp", Kind: "stack.export", State: domain.JobQueued, Attempt: 1}, nil
}

func (f *fakeArchives) Export(_ context.Context, stackID, jobID string) (stackarchives.ExportFile, error) {
	if stackID != "st-1" || jobID != "job-exp" {
		return stackarchives.ExportFile{}, stackarchives.ErrExportNotFound
	}
	return stackarchives.ExportFile{JobID: jobID, StackID: stackID, FileName: "shop-2026-10-10.tar.gz", Size: int64(len(f.archive)),
		SHA256: "abc", ExpiresAt: time.Now().Add(time.Hour), Volumes: []string{"data"}, VolumeNames: []string{"shop_data"}}, nil
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

func (f *fakeArchives) OpenExport(stackarchives.ExportFile) (io.ReadSeekCloser, error) {
	return nopCloser{bytes.NewReader(f.archive)}, nil
}

func (f *fakeArchives) StoreUpload(_ context.Context, p authz.Principal, size int64, body io.Reader) (stackarchives.Upload, error) {
	b, _ := io.ReadAll(body)
	f.record("upload:" + p.UserID + ":" + string(b))
	u := f.upload
	u.OwnerUserID, u.Size = p.UserID, size
	return u, nil
}

func (f *fakeArchives) Upload(p authz.Principal, id string) (stackarchives.Upload, error) {
	if id != f.upload.ID || p.UserID == "stranger" {
		return stackarchives.Upload{}, stackarchives.ErrUploadNotFound
	}
	return f.upload, nil
}

func (f *fakeArchives) DeleteUpload(p authz.Principal, id string) error {
	f.record("delete:" + id)
	_, err := f.Upload(p, id)
	return err
}

func (f *fakeArchives) PreviewImport(_ context.Context, _ authz.Principal, id string, r stackarchives.ImportRequest) (stackarchives.ImportPlan, error) {
	f.record("preview-import:" + id + ">" + r.EnvironmentID + "/" + r.Name)
	return stackarchives.ImportPlan{ArchiveID: id, EnvironmentID: r.EnvironmentID, Name: r.Name,
		Volumes: []stackarchives.ImportVolume{{Key: "data", Source: "shop_data", Name: r.Name + "_data"}}}, nil
}

func (f *fakeArchives) StartImport(_ context.Context, _ authz.Principal, id string, r stackarchives.ImportRequest) (domain.Stack, domain.Job, error) {
	f.record("start-import:" + id + ">" + r.EnvironmentID + "/" + r.Name)
	return domain.Stack{ID: "st-new", EnvironmentID: r.EnvironmentID, Name: r.Name},
		domain.Job{ID: "job-imp", Kind: "stack.import_archive", State: domain.JobQueued, Attempt: 1,
			Targets: []domain.JobTarget{{Type: domain.TargetStack, ID: "st-new"}}}, nil
}

var _ StackArchiveService = (*fakeArchives)(nil)

func archiveAPIFor(t *testing.T, pol *authztest.Policy) (http.Handler, *fakeArchives) {
	t.Helper()
	svc := newFakeStacks()
	arch := &fakeArchives{maxSize: 1 << 20, archive: []byte("0123456789"),
		upload: stackarchives.Upload{ID: "a1", Manifest: stackarchives.Manifest{Stack: stackarchives.ManifestStack{Name: "shop"},
			Volumes: []stackarchives.ManifestVolume{{Key: "data", Name: "shop_data", FollowsProject: true}}}}}
	pol.Locate(func(ref authz.ResourceRef) policy.Location {
		if st, ok := svc.stacks[ref.ID]; ok && ref.Type == catalog.TypeStack {
			return policy.Location{Found: true, EnvironmentID: st.EnvironmentID}
		}
		return policy.Location{}
	})
	mux := http.NewServeMux()
	New(mux, Deps{Stacks: svc, StackArchives: arch, Agents: newFakeAgents(), Authorizer: pol, Clock: testutil.FakeClock(), Idempotency: &memIdempotency{}})
	return authztest.Authenticate(withTestContext(t, mux, "")), arch
}

// TestStackExportNeedsFilesAndDefinition (#313): an export needs
// stack.export and the right to read the stack's files and Compose
// definition; volumes are offered by volume.files.download.
func TestStackExportNeedsFilesAndDefinition(t *testing.T) {
	pol := authztest.New().
		User("exporter", "allow stack.export @stack:st-1", "allow stack.files.download @stack:st-1", "allow stack.definition.read @stack:st-1",
			"allow volume.files.download @env:env-1").
		User("novolumes", "allow stack.export @stack:st-1", "allow stack.files.download @stack:st-1", "allow stack.definition.read @stack:st-1").
		User("nodefinition", "allow stack.export @stack:st-1", "allow stack.files.download @stack:st-1").
		User("reader", "allow stack.read @stack:st-1")
	h, arch := archiveAPIFor(t, pol)
	preview := authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/export-previews", Body: map[string]any{}}
	start := authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/exports", Body: map[string]any{"excludeVolumes": []string{"cache"}},
		Headers: map[string]string{"Idempotency-Key": "k1"}}
	download := authztest.Call{Method: http.MethodGet, Path: "/api/v1/stacks/st-1/exports/job-exp"}

	r := authztest.Do(t, h, "exporter", preview)
	var p StackExportPreview
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &p) != nil || len(p.Volumes) != 1 || !p.Volumes[0].Included || p.Running[0] != "web" {
		t.Fatalf("preview %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "novolumes", preview); r.Status != http.StatusOK {
		t.Fatalf("novolumes preview %d", r.Status)
	}
	if got := arch.mayData; len(got) != 2 || !got[0] || got[1] {
		t.Errorf("MayDownload answers %v, want [true false]", got)
	}
	for _, u := range []string{"nodefinition", "reader"} {
		for _, c := range []authztest.Call{preview, start, download} {
			if r := authztest.Do(t, h, u, c); r.Status != http.StatusForbidden {
				t.Errorf("%s %s: %d, want 403", u, c, r.Status)
			}
		}
	}
	if r := authztest.Do(t, h, "stranger", preview); r.Status != http.StatusNotFound {
		t.Errorf("stranger: %d, want 404", r.Status)
	}

	r = authztest.Do(t, h, "exporter", start)
	if r.Status != http.StatusAccepted || !strings.Contains(string(r.Body), "job-exp") {
		t.Fatalf("start %d %s", r.Status, r.Body)
	}
	// The archive holds the volume's data: downloading it needs the volume's
	// files too, whoever exported it.
	if r := authztest.Do(t, h, "novolumes", download); r.Status != http.StatusForbidden {
		t.Errorf("novolumes download: %d, want 403", r.Status)
	}
	r = authztest.Do(t, h, "exporter", download)
	if r.Status != http.StatusOK || string(r.Body) != "0123456789" ||
		!strings.Contains(r.Header.Get("Content-Disposition"), "shop-2026-10-10.tar.gz") || r.Header.Get("Content-Type") != "application/gzip" {
		t.Fatalf("download %d %v %q", r.Status, r.Header, r.Body)
	}
	ranged := download
	ranged.Headers = map[string]string{"Range": "bytes=2-4"}
	if r := authztest.Do(t, h, "exporter", ranged); r.Status != http.StatusPartialContent || string(r.Body) != "234" ||
		r.Header.Get("Content-Range") != "bytes 2-4/10" {
		t.Fatalf("range %d %q %v", r.Status, r.Body, r.Header)
	}
	ranged.Headers = map[string]string{"Range": "bytes=20-"}
	if r := authztest.Do(t, h, "exporter", ranged); r.Status != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("bad range %d", r.Status)
	}
	if r := authztest.Do(t, h, "exporter", authztest.Call{Method: http.MethodGet, Path: "/api/v1/stacks/st-1/exports/other"}); r.Status != http.StatusNotFound {
		t.Fatalf("unknown export %d", r.Status)
	}
}

// rawUpload posts body as the archive upload.
func rawUpload(t *testing.T, h http.Handler, user, ctype string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stack-archives", bytes.NewReader(body))
	req.Header.Set(authztest.UserHeader, user)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Length", strconv.Itoa(len(body)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestStackArchiveUpload: an upload needs stack.create somewhere, streams
// the raw body, and is bounded.
func TestStackArchiveUpload(t *testing.T) {
	pol := authztest.New().
		User("creator", "allow stack.create @env:env-1").
		User("reader", "allow stack.read @env:env-1")
	h, arch := archiveAPIFor(t, pol)
	rec := rawUpload(t, h, "creator", "application/gzip", []byte("archive-bytes"))
	var out StackArchive
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.ID != "a1" || out.Name != "shop" || len(out.Volumes) != 1 {
		t.Fatalf("upload %d %s", rec.Code, rec.Body)
	}
	if len(arch.calls) != 1 || arch.calls[0] != "upload:creator:archive-bytes" {
		t.Fatalf("calls %v", arch.calls)
	}
	if rec := rawUpload(t, h, "reader", "", []byte("x")); rec.Code != http.StatusForbidden {
		t.Errorf("reader: %d", rec.Code)
	}
	if rec := rawUpload(t, h, "creator", "text/plain", []byte("x")); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: %d", rec.Code)
	}
	arch.maxSize = 4
	if rec := rawUpload(t, h, "creator", "", []byte("too large")); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("too large: %d", rec.Code)
	}
	if len(arch.calls) != 1 {
		t.Errorf("refused uploads reached the service: %v", arch.calls)
	}

	get := authztest.Call{Method: http.MethodGet, Path: "/api/v1/stack-archives/a1"}
	if r := authztest.Do(t, h, "creator", get); r.Status != http.StatusOK {
		t.Errorf("get %d", r.Status)
	}
	if r := authztest.Do(t, h, "stranger", get); r.Status != http.StatusNotFound {
		t.Errorf("another user's upload: %d", r.Status)
	}
	if r := authztest.Do(t, h, "creator", authztest.Call{Method: http.MethodDelete, Path: "/api/v1/stack-archives/a1"}); r.Status != http.StatusNoContent {
		t.Errorf("delete %d", r.Status)
	}
}

// TestStackArchiveImportNeedsDestination: creating the stack needs
// stack.create in the environment, volume.create for the archive's volumes
// and stack.deploy to deploy it.
func TestStackArchiveImportNeedsDestination(t *testing.T) {
	pol := authztest.New().
		User("creator", "allow stack.create @env:env-1", "allow volume.create @env:env-1", "allow stack.deploy @env:env-1").
		User("novolumes", "allow stack.create @env:env-1", "allow stack.deploy @env:env-1").
		User("nodeploy", "allow stack.create @env:env-1", "allow volume.create @env:env-1")
	h, arch := archiveAPIFor(t, pol)
	body := func(env string, deploy bool) map[string]any {
		return map[string]any{"environmentId": env, "name": "boutique", "deploy": deploy}
	}
	preview := func(env string, deploy bool) authztest.Call {
		return authztest.Call{Method: http.MethodPost, Path: "/api/v1/stack-archives/a1/import-previews", Body: body(env, deploy)}
	}
	start := authztest.Call{Method: http.MethodPost, Path: "/api/v1/stack-archives/a1/imports", Body: body("env-1", true),
		Headers: map[string]string{"Idempotency-Key": "k1"}}

	r := authztest.Do(t, h, "creator", preview("env-1", true))
	var p StackArchiveImportPreview
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &p) != nil || p.Volumes[0].Name != "boutique_data" {
		t.Fatalf("preview %d %s", r.Status, r.Body)
	}
	for _, c := range []struct {
		user   string
		call   authztest.Call
		status int
	}{
		{"novolumes", preview("env-1", false), http.StatusForbidden},
		{"nodeploy", preview("env-1", true), http.StatusForbidden},
		{"nodeploy", preview("env-1", false), http.StatusOK},
		{"creator", preview("env-secret", false), http.StatusNotFound},
		{"stranger", preview("env-1", false), http.StatusNotFound},
	} {
		if r := authztest.Do(t, h, c.user, c.call); r.Status != c.status {
			t.Errorf("%s %v: %d, want %d", c.user, c.call.Body, r.Status, c.status)
		}
	}

	r = authztest.Do(t, h, "creator", start)
	if r.Status != http.StatusAccepted || !strings.Contains(string(r.Body), "st-new") {
		t.Fatalf("start %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "creator", start); r.Status != http.StatusAccepted {
		t.Fatalf("replay %d", r.Status)
	}
	n := 0
	for _, c := range arch.calls {
		if strings.HasPrefix(c, "start-import:") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the replayed start ran %d times", n)
	}
}
