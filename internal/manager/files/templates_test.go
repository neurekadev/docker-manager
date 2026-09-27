package files_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/fsroot"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/api"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/authztest"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/files"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

const templateID = "0190a6e0-0000-7000-8000-00000000cccc"

// fakeTemplates serves one template's draft from a temporary directory
// with a byte limit.
type fakeTemplates struct {
	dir     string
	fs      *fsroot.Service
	max     int64
	writing int
}

func (f *fakeTemplates) Exists(_ context.Context, id string) error {
	if id != templateID {
		return domain.ErrTemplateNotFound
	}
	return nil
}

func (f *fakeTemplates) Files() *fsroot.Service { return f.fs }

func (f *fakeTemplates) CheckQuota(_ context.Context, _ string, adding int64) error {
	if adding > f.max {
		return &domain.TemplateTooLargeError{Message: "a template holds at most 1 KiB"}
	}
	return nil
}

func (f *fakeTemplates) Writing(string) func() { f.writing++; return func() {} }

var templateKinds = fsroot.Kinds{Delete: jobspec.TemplateFilesDelete, Copy: jobspec.TemplateFilesCopy, Move: jobspec.TemplateFilesMove,
	Archive: jobspec.TemplateFilesArchive, Extract: jobspec.TemplateFilesExtract, Metadata: jobspec.TemplateFilesMetadata}

func newTemplateEnv(t *testing.T) (*env, *fakeTemplates) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ft := &fakeTemplates{dir: dir, max: 1 << 10}
	ft.fs = fsroot.New(fsroot.Options{Kinds: templateKinds, Clock: testutil.FakeClock(),
		Resolve: func(_ context.Context, s protocol.FileScope) (string, error) {
			if s.Kind != protocol.ScopeTemplate {
				return "", fsroot.Fail(protocol.CodeInvalidFrame, "not a template")
			}
			return dir, nil
		}})
	e := &env{t: t, ctx: testutil.Context(t), obs: &observer{}, audit: &memAudit{}}
	e.pol = authztest.New().Owner("owner")
	e.jobs = &syncJobs{execs: map[domain.JobKind]jobexec.Executor{}, auth: e.pol, jobs: map[string]domain.Job{}}
	for _, x := range ft.fs.Executors() {
		e.jobs.execs[x.Kind] = x
	}
	svc := files.New(files.Options{Jobs: e.jobs, Logger: testutil.Logger(t)})
	svc.SetTemplates(ft)
	t.Cleanup(svc.Close)
	mux := http.NewServeMux()
	api.New(mux, api.Deps{Authorizer: e.pol, Files: svc, Audit: e.audit, Clock: testutil.FakeClock()})
	e.srv = httptest.NewServer(authztest.Authenticate(mux))
	t.Cleanup(e.srv.Close)
	return e, ft
}

// TestTemplateDraftsAreServedByTheManager: template roots never reach an
// agent; writes respect the template size limit and file jobs become
// template.files.* jobs without an environment.
func TestTemplateDraftsAreServedByTheManager(t *testing.T) {
	e, ft := newTemplateEnv(t)
	base := "/api/v1/templates/" + templateID + "/files"
	e.write(ft.dir, "compose.yaml", "services: {}\n")

	var listing api.FileListing
	r := e.do("owner", http.MethodGet, base+"?path=.", nil)
	if r.status != http.StatusOK {
		t.Fatalf("list: %d %s", r.status, r.body)
	}
	r.json(t, &listing)
	if len(listing.Items) != 1 || listing.Items[0].Name != "compose.yaml" {
		t.Fatalf("listing %+v", listing.Items)
	}
	if r := e.do("owner", http.MethodGet, "/api/v1/templates/other/files?path=.", nil); r.status != http.StatusNotFound {
		t.Fatalf("unknown template: %d %s", r.status, r.body)
	}

	// Saving within the limit works; beyond it answers template_too_large.
	r = e.do("owner", http.MethodPost, base+"/entries", map[string]any{"path": ".env", "type": "file", "content": "A=1\n"})
	if r.status != http.StatusCreated || e.read(ft.dir, ".env") != "A=1\n" || ft.writing == 0 {
		t.Fatalf("create .env: %d %s", r.status, r.body)
	}
	r = e.do("owner", http.MethodPost, base+"/uploads?path=.&name=big.bin", []byte(strings.Repeat("x", 2<<10)), "If-None-Match", "*")
	if r.status != http.StatusRequestEntityTooLarge || !strings.Contains(string(r.body), api.CodeTemplateTooLarge) {
		t.Fatalf("upload over the limit: %d %s", r.status, r.body)
	}
	if _, err := os.Stat(filepath.Join(ft.dir, "big.bin")); !os.IsNotExist(err) {
		t.Fatalf("a refused upload left a file: %v", err)
	}

	// Downloads stream from the draft.
	r = e.do("owner", http.MethodGet, base+"/downloads?path=compose.yaml", nil)
	if r.status != http.StatusOK || string(r.body) != "services: {}\n" {
		t.Fatalf("download: %d %s", r.status, r.body)
	}
	if r := e.do("owner", http.MethodGet, base+"/downloads?path=missing.txt", nil); r.status != http.StatusNotFound {
		t.Fatalf("download of a missing file: %d %s", r.status, r.body)
	}

	// A copy runs as template.files.copy on the template, no environment.
	r = e.do("owner", http.MethodPost, base+"/copies", map[string]any{"paths": []string{"compose.yaml"}, "destination": ".",
		"conflict": "keep_both"}, "Idempotency-Key", "k1")
	if r.status != http.StatusAccepted {
		t.Fatalf("copy: %d %s", r.status, r.body)
	}
	req := e.jobs.reqs[len(e.jobs.reqs)-1]
	if req.Kind != jobspec.TemplateFilesCopy || req.EnvironmentID != "" || len(req.Targets) != 1 ||
		req.Targets[0] != (domain.JobTarget{Type: domain.TargetTemplate, ID: templateID}) {
		t.Fatalf("copy request %+v", req)
	}
	entries, err := os.ReadDir(ft.dir)
	if err != nil || len(entries) != 3 {
		t.Fatalf("after copy: %v %v", entries, err)
	}
}
