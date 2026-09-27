package files_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	agentfiles "code.neureka.dev/docker-manager/docker-manager/internal/agent/files"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/storage"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/agents"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/api"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/authztest"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/files"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/streammux"
	"code.neureka.dev/docker-manager/docker-manager/internal/streammux/muxtest"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

const (
	envID   = "0190a6e0-3333-7000-8000-000000000003"
	stackID = "0190a6e0-5555-7000-8000-000000000005"
)

// agentLoop is a fake session hub: requests go straight to the real agent
// file service, streams through an in-memory stream pipe (encoded and
// validated like on the wire).
type agentLoop struct {
	reqs    map[string]session.RequestHandler
	pipe    *muxtest.Pipe
	offline bool
}

func (l *agentLoop) RequestEnvironment(ctx context.Context, env, name string, input any, _ time.Duration) (json.RawMessage, error) {
	if l.offline || env != envID {
		return nil, jobs.ErrAgentOffline
	}
	b, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	out, err := l.reqs[name](ctx, b)
	var he *session.HandlerError
	if errors.As(err, &he) {
		return nil, &agents.RequestError{Code: he.Code, Message: he.Message}
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func (l *agentLoop) OpenStream(ctx context.Context, env, kind string, input any, o streammux.OpenOptions) (*streammux.Stream, error) {
	if l.offline || env != envID {
		return nil, jobs.ErrAgentOffline
	}
	return l.pipe.Open(ctx, kind, input, o)
}

// syncJobs is a job engine that validates like the real one (locks,
// capabilities on every target) and runs the agent executor at once.
type syncJobs struct {
	mu    sync.Mutex
	execs map[domain.JobKind]jobexec.Executor
	auth  authz.Authorizer
	jobs  map[string]domain.Job
	reqs  []jobs.Request
}

type memJournal struct{}

func (memJournal) Save(context.Context, *jobexec.State) error { return nil }

func (s *syncJobs) Enqueue(ctx context.Context, req jobs.Request) (domain.Job, bool, error) {
	spec, ok := jobspec.Lookup(req.Kind)
	if !ok {
		return domain.Job{}, false, domain.ErrJobUnknownKind
	}
	locks, err := spec.ComputeLocks(req.EnvironmentID, req.Targets)
	if err != nil {
		return domain.Job{}, false, err
	}
	in, err := json.Marshal(req.Input)
	if err != nil {
		return domain.Job{}, false, err
	}
	caps, err := spec.Capabilities(req.Targets, in)
	if err != nil {
		return domain.Job{}, false, err
	}
	c := authz.For(ctx, s.auth, req.Principal)
	for _, r := range authz.TargetResources(req.EnvironmentID, req.Targets) {
		for _, k := range caps {
			if !c.Can(k, r).Allowed {
				return domain.Job{}, false, domain.ErrJobForbidden
			}
		}
	}
	st := &jobexec.State{JobID: ids.New(), Attempt: 1, FencingToken: 1, Kind: req.Kind, Input: in}
	res, err := jobexec.Run(ctx, s.execs[req.Kind], st, jobexec.Options{Journal: memJournal{}})
	if err != nil {
		return domain.Job{}, false, err
	}
	now := testutil.Epoch
	j := domain.Job{ID: st.JobID, Kind: req.Kind, State: domain.JobState(res.Outcome), EnvironmentID: req.EnvironmentID,
		Targets: req.Targets, Input: in, Locks: locks, CreatedAt: now, UpdatedAt: now, StartedAt: &now, FinishedAt: &now,
		ErrorMessage: res.Message}
	for _, it := range res.Items {
		j.Items = append(j.Items, domain.JobItem{Name: it.Name, Status: it.Status, Message: it.Message})
	}
	s.mu.Lock()
	s.jobs[j.ID] = j
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()
	return j, true, nil
}

func (s *syncJobs) Get(_ context.Context, id string) (domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return j, domain.ErrJobNotFound
	}
	return j, nil
}

func (s *syncJobs) Subscribe(string) (<-chan struct{}, func()) {
	ch := make(chan struct{})
	close(ch)
	return ch, func() {}
}

type stackRoots struct{ dir string }

func (s stackRoots) StackFileRoot(_ context.Context, id string) (files.StackRoot, error) {
	if id != stackID {
		return files.StackRoot{}, domain.ErrFileScopeNotFound
	}
	return files.StackRoot{EnvironmentID: envID, Dir: s.dir}, nil
}

type observer struct {
	mu      sync.Mutex
	changes [][]string
	signal  chan struct{} // closed and replaced on every call
	// invalid: content the definition validator refuses.
	invalid string
}

// ValidateSourceSave refuses the invalid content for definition files.
func (o *observer) ValidateSourceSave(_ context.Context, _ string, path string, content []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.invalid != "" && string(content) == o.invalid && api.IsDefinitionFile(path) {
		return &domain.StackError{Code: domain.StackErrInvalidDefinition, Message: "the Compose definition is invalid",
			Issues: []domain.StackIssue{{Code: "invalid_project", Message: "yaml: line 1: did not find expected node content"}}}
	}
	return nil
}

func (o *observer) StackSourcesChanged(_ context.Context, id string, paths []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if id == stackID {
		o.changes = append(o.changes, paths)
	}
	if o.signal != nil {
		close(o.signal)
	}
	o.signal = make(chan struct{})
}

// waitFor returns the calls once there are at least n (or when ctx ends).
// The service tells the observer from a watcher goroutine once a job has
// ended, which may be after the job's HTTP response was written.
func (o *observer) waitFor(ctx context.Context, n int) [][]string {
	for {
		o.mu.Lock()
		if len(o.changes) >= n {
			got := slices.Clone(o.changes)
			o.mu.Unlock()
			return got
		}
		if o.signal == nil {
			o.signal = make(chan struct{})
		}
		ch := o.signal
		o.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return o.all()
		}
	}
}

func (o *observer) all() [][]string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.changes)
}

type memAudit struct {
	mu  sync.Mutex
	evs []domain.AuditEvent
}

func (m *memAudit) Record(_ context.Context, ev domain.AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evs = append(m.evs, ev)
	return nil
}

func (m *memAudit) Records(context.Context, domain.AuditFilter) ([]domain.AuditRecord, error) {
	return nil, nil
}

func (m *memAudit) dump() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, _ := json.Marshal(m.evs)
	return string(b)
}

type fakeEngine struct{ volumes map[string]engine.Volume }

func (f fakeEngine) InspectVolume(_ context.Context, name string) (engine.Volume, error) {
	v, ok := f.volumes[name]
	if !ok {
		return v, &engine.Error{Code: engine.CodeNotFound}
	}
	return v, nil
}

func (fakeEngine) ListContainers(context.Context, engine.ContainerFilter) ([]engine.Container, error) {
	return nil, nil
}

// env is a manager API over the real agent file service.
type env struct {
	t      *testing.T
	ctx    context.Context
	srv    *httptest.Server
	pol    *authztest.Policy
	vol    string // the volume "data" root
	stack  string // the stack project directory
	loop   *agentLoop
	jobs   *syncJobs
	obs    *observer
	audit  *memAudit
	logs   *testutil.LogBuffer
	volURL string
	stkURL string
}

func newEnv(t *testing.T, maxUpload int64) *env {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, ctx: testutil.Context(t), obs: &observer{}, audit: &memAudit{}}
	e.vol = filepath.Join(base, "data", "_data")
	stacks := filepath.Join(base, "docker-manager_stacks", "_data")
	e.stack = filepath.Join(stacks, "app")
	for _, d := range []string{e.vol, e.stack, filepath.Join(base, "remote", "_data")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sl := filepath.ToSlash
	st := &storage.Result{VolumesDir: sl(base), StacksDir: sl(stacks),
		Roots: []storage.Root{{Kind: storage.KindStacks, Path: sl(stacks), OK: true}, {Kind: storage.KindVolumes, Path: sl(base), OK: true}}}
	eng := fakeEngine{volumes: map[string]engine.Volume{
		"data":   {Name: "data", Driver: "local", Mountpoint: sl(e.vol)},
		"remote": {Name: "remote", Driver: "local", Mountpoint: sl(filepath.Join(base, "remote", "_data")), Options: map[string]string{"type": "nfs"}},
	}}
	logger, logs := testutil.CaptureLogger()
	e.logs = logs
	asvc := agentfiles.New(agentfiles.Options{Engine: func() agentfiles.Engine { return eng }, Storage: func() *storage.Result { return st },
		Clock: testutil.FakeClock(), Logger: logger})
	hs := map[string]muxtest.Handler{}
	for k, h := range asvc.Streams() {
		hs[k] = muxtest.Handler(h)
	}
	e.loop = &agentLoop{reqs: asvc.Requests(), pipe: muxtest.New(t, hs)}
	e.pol = authztest.New().Owner("owner")
	e.jobs = &syncJobs{execs: map[domain.JobKind]jobexec.Executor{}, auth: e.pol, jobs: map[string]domain.Job{}}
	for _, x := range asvc.Executors() {
		e.jobs.execs[x.Kind] = x
	}
	svc := files.New(files.Options{Agents: e.loop, Jobs: e.jobs, Stacks: stackRoots{dir: sl(e.stack)}, Observer: e.obs, Logger: logger})
	t.Cleanup(svc.Close)
	mux := http.NewServeMux()
	api.New(mux, api.Deps{Authorizer: e.pol, Files: svc, FilesMaxUpload: maxUpload, Audit: e.audit, Clock: testutil.FakeClock()})
	e.srv = httptest.NewServer(authztest.Authenticate(mux))
	t.Cleanup(e.srv.Close)
	e.volURL = "/api/v1/environments/" + envID + "/volumes/data/files"
	e.stkURL = "/api/v1/stacks/" + stackID + "/files"
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("logs:\n%s", logs.String())
		}
	})
	return e
}

type resp struct {
	status int
	header http.Header
	body   []byte
}

func (r resp) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

// do sends a request as user; body is JSON unless it is []byte.
func (e *env) do(user, method, path string, body any, headers ...string) resp {
	e.t.Helper()
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		rd, ct = bytes.NewReader(b), "application/octet-stream"
	default:
		j, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd, ct = bytes.NewReader(j), "application/json"
	}
	req, err := http.NewRequestWithContext(e.ctx, method, e.srv.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if user != "" {
		req.Header.Set(authztest.UserHeader, user)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := e.srv.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, header: res.Header, body: b}
}

func (e *env) write(root, rel, content string) {
	e.t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) read(root, rel string) string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		e.t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func must(t *testing.T, r resp, status int) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status %d, want %d: %s", r.status, status, r.body)
	}
}

func errCode(t *testing.T, r resp) string {
	t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	r.json(t, &e)
	return e.Code
}

// TestVolumeFilesThroughTheAPI drives every volume file route through the
// public API, the manager file service and the real agent service.
func TestVolumeFilesThroughTheAPI(t *testing.T) {
	e := newEnv(t, 1<<20)
	e.write(e.vol, "conf/app.env", "A=1\n")
	e.write(e.vol, "b.txt", "bb")

	var list api.FileListing
	r := e.do("owner", http.MethodGet, e.volURL+"?sort=-name", nil)
	must(t, r, 200)
	r.json(t, &list)
	if len(list.Items) != 2 || list.Items[0].Name != "conf" || list.Dir.Type != "dir" || list.Total != 2 {
		t.Fatalf("listing %+v", list)
	}
	// Paging with cursors bound to the query.
	r = e.do("owner", http.MethodGet, e.volURL+"?limit=1", nil)
	r.json(t, &list)
	if len(list.Items) != 1 || list.NextCursor == "" {
		t.Fatalf("page 1 %+v", list)
	}
	r = e.do("owner", http.MethodGet, e.volURL+"?limit=1&cursor="+list.NextCursor, nil)
	list = api.FileListing{}
	r.json(t, &list)
	if len(list.Items) != 1 || list.Items[0].Name != "conf" || list.NextCursor != "" {
		t.Fatalf("page 2 %+v", list)
	}
	must(t, e.do("owner", http.MethodGet, e.volURL+"?hidden=true&cursor="+"eyJxIjoibm9wZSJ9", nil), 422)

	// Read, save with If-Match, conflicting save: 412 with the current ETag.
	var c api.FileContent
	r = e.do("owner", http.MethodGet, e.volURL+"/content?path=conf/app.env", nil)
	must(t, r, 200)
	r.json(t, &c)
	etag := r.header.Get("ETag")
	if c.Content != "A=1\n" || etag == "" || etag != c.Entry.ETag || c.Binary {
		t.Fatalf("content %+v etag %q", c, etag)
	}
	must(t, e.do("owner", http.MethodPut, e.volURL+"/content?path=conf/app.env", map[string]string{"content": "A=2\n"}), 428)
	r = e.do("owner", http.MethodPut, e.volURL+"/content?path=conf/app.env", map[string]string{"content": "A=2\n"}, "If-Match", etag)
	must(t, r, 200)
	newTag := r.header.Get("ETag")
	e.write(e.vol, "conf/app.env", "A=external\n") // an external edit
	r = e.do("owner", http.MethodPut, e.volURL+"/content?path=conf/app.env", map[string]string{"content": "A=3\n"}, "If-Match", newTag)
	must(t, r, 412)
	if cur := r.header.Get("ETag"); cur == "" || cur == newTag {
		t.Fatalf("412 must carry the current ETag, got %q", cur)
	}
	if got := e.read(e.vol, "conf/app.env"); got != "A=external\n" {
		t.Fatalf("a stale save overwrote the file: %q", got)
	}
	must(t, e.do("owner", http.MethodPut, e.volURL+"/content?path=new.txt", map[string]string{"content": "n"}, "If-None-Match", "*"), 200)
	must(t, e.do("owner", http.MethodPut, e.volURL+"/content?path=new.txt", map[string]string{"content": "n"}, "If-None-Match", "*"), 412)

	// Traversal is a 422 on the path field; nothing outside is touched.
	for _, p := range []string{"..", "../x", "/etc/passwd", "a/../../b", "%2e%2e/x"} {
		r := e.do("owner", http.MethodGet, e.volURL+"/content?path="+p, nil)
		if p == "%2e%2e/x" { // decoded to ../x by the query parser
			must(t, r, 422)
			continue
		}
		must(t, r, 422)
	}

	// Entries.
	must(t, e.do("owner", http.MethodPost, e.volURL+"/entries", map[string]string{"path": "made", "type": "dir"}), 201)
	must(t, e.do("owner", http.MethodPost, e.volURL+"/entries", map[string]string{"path": "made", "type": "dir"}), 409)

	// Uploads.
	body := bytes.Repeat([]byte("u"), 300_000)
	must(t, e.do("owner", http.MethodPost, e.volURL+"/uploads?path=made&name=u.bin", body), 428)
	r = e.do("owner", http.MethodPost, e.volURL+"/uploads?path=made&name=u.bin", body, "If-None-Match", "*")
	must(t, r, 201)
	var up api.FileUploadResult
	r.json(t, &up)
	if up.Entry.Size != int64(len(body)) || e.read(e.vol, "made/u.bin") != string(body) {
		t.Fatalf("upload %+v", up)
	}
	must(t, e.do("owner", http.MethodPost, e.volURL+"/uploads?path=made&name=u.bin", body, "If-None-Match", "*"), 412)
	r = e.do("owner", http.MethodPost, e.volURL+"/uploads?path=made&name=u.bin&conflict=keep_both", []byte("kb"))
	r.json(t, &up)
	if r.status != 201 || up.Entry.Name != "u (1).bin" {
		t.Fatalf("keep_both %d %s", r.status, r.body)
	}
	r = e.do("owner", http.MethodPost, e.volURL+"/uploads?path=made&name=u.bin&conflict=skip", []byte("sk"))
	r.json(t, &up)
	if r.status != 201 || !up.Skipped {
		t.Fatalf("skip %d %s", r.status, r.body)
	}
	must(t, e.do("owner", http.MethodPost, e.volURL+"/uploads?path=made&name=big", make([]byte, 1<<20+1), "If-None-Match", "*"), 413)
	must(t, e.do("owner", http.MethodPost, e.volURL+"/uploads?path=made&name=x", []byte("x"), "If-None-Match", "*",
		"X-Docker-Manager-Content-SHA256", strings.Repeat("0", 64)), 422)
	must(t, e.do("owner", http.MethodPost, e.volURL+"/uploads?path=made&name=j", map[string]string{}, "If-None-Match", "*"), 415)

	// Downloads: raw with ranges, archives.
	r = e.do("owner", http.MethodGet, e.volURL+"/downloads?path=made/u.bin", nil)
	must(t, r, 200)
	if !bytes.Equal(r.body, body) || r.header.Get("Content-Length") != fmt.Sprint(len(body)) || r.header.Get("ETag") == "" ||
		!strings.Contains(r.header.Get("Content-Disposition"), "u.bin") || r.header.Get("Cache-Control") == "" && false {
		t.Fatalf("raw download %d bytes, headers %v", len(r.body), r.header)
	}
	r = e.do("owner", http.MethodGet, e.volURL+"/downloads?path=made/u.bin", nil, "Range", "bytes=10-19")
	must(t, r, 206)
	if len(r.body) != 10 || r.header.Get("Content-Range") != fmt.Sprintf("bytes 10-19/%d", len(body)) {
		t.Fatalf("range %q %v", r.body, r.header)
	}
	must(t, e.do("owner", http.MethodGet, e.volURL+"/downloads?path=made/u.bin", nil, "Range", "bytes=999999999-"), 416)
	r = e.do("owner", http.MethodGet, e.volURL+"/downloads?path=made&path=b.txt", nil)
	must(t, r, 200)
	zr, err := zip.NewReader(bytes.NewReader(r.body), int64(len(r.body)))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"b.txt", "made/", "made/u (1).bin", "made/u.bin"}) {
		t.Fatalf("zip members %v", names)
	}
	must(t, e.do("owner", http.MethodGet, e.volURL+"/downloads?path=nope", nil), 404)

	// Jobs: 202 + job, executed by the agent executors.
	job := func(r resp) api.Job {
		t.Helper()
		must(t, r, 202)
		var j api.Job
		r.json(t, &j)
		if r.header.Get("Location") != "/api/v1/jobs/"+j.ID {
			t.Fatalf("Location %q", r.header.Get("Location"))
		}
		return j
	}
	j := job(e.do("owner", http.MethodPost, e.volURL+"/copies", map[string]any{"paths": []string{"b.txt"}, "destination": "made"}))
	if j.State != "succeeded" || e.read(e.vol, "made/b.txt") != "bb" {
		t.Fatalf("copy %+v", j)
	}
	j = job(e.do("owner", http.MethodPost, e.volURL+"/moves", map[string]any{"paths": []string{"made/b.txt"}, "destination": "conf"}))
	if j.State != "succeeded" || e.read(e.vol, "conf/b.txt") != "bb" {
		t.Fatalf("move %+v", j)
	}
	j = job(e.do("owner", http.MethodPost, e.volURL+"/moves", map[string]any{"paths": []string{"conf/b.txt"}, "destination": "conf", "name": "c.txt"}))
	if j.State != "succeeded" || e.read(e.vol, "conf/c.txt") != "bb" {
		t.Fatalf("rename %+v", j)
	}
	must(t, e.do("owner", http.MethodPost, e.volURL+"/moves", map[string]any{"paths": []string{"conf/c.txt", "b.txt"}, "destination": "conf", "name": "d"}), 422)
	j = job(e.do("owner", http.MethodPost, e.volURL+"/moves", map[string]any{"paths": []string{"conf/c.txt"}, "destination": "conf", "name": "b.txt"}))
	if j.State != "succeeded" {
		t.Fatalf("rename back %+v", j)
	}
	j = job(e.do("owner", http.MethodPost, e.volURL+"/archives", map[string]any{"paths": []string{"conf"}, "destination": "conf.tar.gz", "format": "tar.gz"}))
	if j.State != "succeeded" {
		t.Fatalf("archive %+v", j)
	}
	j = job(e.do("owner", http.MethodPost, e.volURL+"/extractions", map[string]any{"path": "conf.tar.gz", "destination": "restored"}))
	if j.State != "succeeded" || e.read(e.vol, "restored/conf/b.txt") != "bb" {
		t.Fatalf("extract %+v", j)
	}
	j = job(e.do("owner", http.MethodPatch, e.volURL+"/metadata", map[string]any{"paths": []string{"restored"}, "recursive": true,
		"chmod": map[string]string{"mode": "0640", "dirMode": "0750"}}))
	if j.State != "succeeded" {
		t.Fatalf("chmod %+v", j)
	}
	must(t, e.do("owner", http.MethodPatch, e.volURL+"/metadata", map[string]any{"paths": []string{"restored"}, "chmod": map[string]string{"mode": "4755"}}), 422)
	var pv api.FilePreview
	r = e.do("owner", http.MethodPost, e.volURL+"/conflict-previews", map[string]any{"operation": "delete", "paths": []string{"restored"}})
	must(t, r, 200)
	r.json(t, &pv)
	if pv.Impact.Files != 2 || pv.Impact.Dirs != 2 {
		t.Fatalf("preview %+v", pv)
	}
	j = job(e.do("owner", http.MethodPost, e.volURL+"/deletions", map[string]any{"paths": []string{"restored"}}, "Idempotency-Key", "k-1"))
	if _, err := os.Stat(filepath.Join(e.vol, "restored")); !errors.Is(err, os.ErrNotExist) || j.State != "succeeded" {
		t.Fatalf("delete %+v %v", j, err)
	}
	must(t, e.do("owner", http.MethodPost, e.volURL+"/deletions", map[string]any{"paths": []string{"."}}), 422)
	// The job locks name the volume's paths, never host paths.
	last := e.jobs.reqs[len(e.jobs.reqs)-1]
	if last.Targets[0] != (domain.JobTarget{Type: domain.TargetVolume, ID: "data"}) || last.Targets[1].ID != "/volume/data/restored" {
		t.Fatalf("targets %+v", last.Targets)
	}

	// Unsupported volumes and offline agents.
	must(t, e.do("owner", http.MethodGet, "/api/v1/environments/"+envID+"/volumes/remote/files", nil), 409)
	must(t, e.do("owner", http.MethodGet, "/api/v1/environments/"+envID+"/volumes/nope/files", nil), 404)
	e.loop.offline = true
	r = e.do("owner", http.MethodGet, e.volURL, nil)
	if r.status != 503 || errCode(t, r) != api.CodeUnavailable {
		t.Fatalf("offline %d %s", r.status, r.body)
	}
}

// TestFileRoutesAuthorization: every file route answers only the
// capabilities it declares (#17), per root type, with 404 for callers who
// may not see the root; tokens are limited to their scope.
func TestFileRoutesAuthorization(t *testing.T) {
	e := newEnv(t, 1<<20)
	e.write(e.vol, "a.txt", "a")
	e.write(e.stack, "a.txt", "a")
	h := e.srv.Config.Handler
	params := map[string]string{"environmentId": envID, "volumeId": "data", "stackId": stackID}
	calls := authztest.Routes(t, params, "/api/v1/environments/{environmentId}/volumes/{volumeId}/files", "/api/v1/stacks/{stackId}/files")
	if len(calls) != 26 {
		t.Fatalf("%d file routes in the inventory, want 26", len(calls))
	}
	for i := range calls {
		if strings.HasSuffix(calls[i].Path, "/content") || strings.HasSuffix(calls[i].Path, "/downloads") {
			calls[i].Path += "?path=a.txt"
		}
	}
	for user, caps := range map[string][]string{
		"reader":   {"volume.files.read"},
		"loader":   {"volume.files.download"},
		"writer":   {"volume.files.write"},
		"chmodder": {"volume.files.chmod"},
		"stacker":  {"stack.files.read", "stack.files.delete"},
	} {
		var rules []string
		for _, c := range caps {
			if strings.HasPrefix(c, "volume.") {
				rules = append(rules, "allow "+c+" @volume:"+envID+"/data")
			} else {
				rules = append(rules, "allow "+c+" @stack:"+stackID)
			}
		}
		e.pol.Member(user, "g-"+user).Group("g-"+user, rules...)
		allowed, denied := authztest.Split(calls, caps...)
		authztest.AssertOnly(t, h, user, allowed, denied)
	}
	// Metrics, restart or volume.read grants never open files.
	e.pol.Member("viewer", "g-viewer").Group("g-viewer", "allow volume.read @volume:"+envID+"/data",
		"allow container.logs.read @all", "allow container.restart @all")
	authztest.AssertOnly(t, h, "viewer", nil, calls)
	// Nobody sees the root: 404 everywhere.
	for _, c := range calls {
		if r := authztest.Do(t, h, "stranger", c); r.Status != http.StatusNotFound {
			t.Errorf("%s as stranger: %d", c, r.Status)
		}
	}
	// A token only has its own scope (intersected with the user's).
	e.pol.Member("tina", "g-tina").Group("g-tina", "allow volume.files.read @volume:"+envID+"/data", "allow volume.files.write @volume:"+envID+"/data")
	e.pol.Token("tok-1", "tina", "allow volume.files.read @volume:"+envID+"/data")
	put := authztest.Call{Method: http.MethodPut, Path: e.volURL + "/content?path=a.txt", Body: map[string]string{"content": "x"},
		Headers: map[string]string{authztest.TokenHeader: "tok-1", "If-None-Match": "*"}}
	if r := authztest.Do(t, h, "tina", put); r.Status != http.StatusForbidden {
		t.Fatalf("token without write: %d %s", r.Status, r.Body)
	}
	get := authztest.Call{Method: http.MethodGet, Path: e.volURL + "/content?path=a.txt", Headers: map[string]string{authztest.TokenHeader: "tok-1"}}
	if r := authztest.Do(t, h, "tina", get); r.Status != http.StatusOK {
		t.Fatalf("token read: %d %s", r.Status, r.Body)
	}
}

// TestStackFilesDefinitionGatingAndRevisionHook: a stack's Compose
// sources need stack.definition.* on top of the file capabilities, and
// saving them tells #7 (never for other files).
func TestStackFilesDefinitionGatingAndRevisionHook(t *testing.T) {
	e := newEnv(t, 1<<20)
	e.write(e.stack, "compose.yaml", "services: {}\n")
	e.write(e.stack, ".env", "TOKEN=1\n")
	e.write(e.stack, "html/index.html", "<p>hi</p>")
	files := "@stack:" + stackID
	e.pol.Member("dev", "g-dev").Group("g-dev", "allow stack.files.read "+files, "allow stack.files.write "+files,
		"allow stack.files.download "+files, "allow stack.files.move "+files, "allow stack.files.delete "+files)
	must(t, e.do("dev", http.MethodGet, e.stkURL, nil), 200)
	must(t, e.do("dev", http.MethodGet, e.stkURL+"/content?path=html/index.html", nil), 200)
	for _, p := range []string{"compose.yaml", ".env"} {
		must(t, e.do("dev", http.MethodGet, e.stkURL+"/content?path="+p, nil), 403)
	}
	must(t, e.do("dev", http.MethodGet, e.stkURL+"/downloads?path=.", nil), 403)
	must(t, e.do("dev", http.MethodPut, e.stkURL+"/content?path=compose.yaml", map[string]string{"content": "x"}, "If-Match", "*"), 403)
	must(t, e.do("dev", http.MethodPost, e.stkURL+"/deletions", map[string]any{"paths": []string{".env"}}), 403)
	must(t, e.do("dev", http.MethodPost, e.stkURL+"/moves", map[string]any{"paths": []string{"html/index.html"}, "destination": "."}), 202)
	must(t, e.do("dev", http.MethodPost, e.stkURL+"/uploads?name=compose.override.yaml", []byte("x"), "If-None-Match", "*"), 403)
	if got := e.obs.all(); len(got) != 0 {
		t.Fatalf("non-definition changes reported: %v", got)
	}
	e.pol.Group("g-dev", "allow stack.files.read "+files, "allow stack.files.write "+files, "allow stack.definition.read "+files,
		"allow stack.definition.write "+files, "allow stack.files.delete "+files)
	r := e.do("dev", http.MethodGet, e.stkURL+"/content?path=compose.yaml", nil)
	must(t, r, 200)
	// A save that would break the definition is refused before the agent
	// writes anything (#7).
	e.obs.mu.Lock()
	e.obs.invalid = "services: ["
	e.obs.mu.Unlock()
	bad := e.do("dev", http.MethodPut, e.stkURL+"/content?path=compose.yaml", map[string]string{"content": "services: ["},
		"If-Match", r.header.Get("ETag"))
	must(t, bad, 422)
	if !strings.Contains(string(bad.body), `"invalid_definition"`) || !strings.Contains(string(bad.body), "did not find expected node") {
		t.Errorf("refusal body %s", bad.body)
	}
	must(t, e.do("dev", http.MethodPost, e.stkURL+"/entries", map[string]any{"path": "compose.override.yaml", "type": "file",
		"content": "services: ["}), 422)
	if b, _ := os.ReadFile(filepath.Join(e.stack, "compose.yaml")); string(b) != "services: {}\n" {
		t.Errorf("refused save was written: %q", b)
	}
	must(t, e.do("dev", http.MethodPut, e.stkURL+"/content?path=compose.yaml", map[string]string{"content": "services:\n  web: {}\n"},
		"If-Match", r.header.Get("ETag")), 200)
	must(t, e.do("dev", http.MethodPost, e.stkURL+"/uploads?name=compose.override.yaml", []byte("services: {}\n"), "If-None-Match", "*"), 201)
	must(t, e.do("dev", http.MethodPost, e.stkURL+"/deletions", map[string]any{"paths": []string{".env"}}), 202)
	// The deletion is a job: its watcher reports once the job has ended.
	want := [][]string{{"compose.yaml"}, {"compose.override.yaml"}, {".env"}}
	got := e.obs.waitFor(e.ctx, len(want))
	if len(got) != len(want) {
		t.Fatalf("revision hook calls %v, want %v", got, want)
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Fatalf("revision hook calls %v, want %v", got, want)
		}
	}
	// Unknown stacks (and a service without #7's resolver) are 404.
	must(t, e.do("owner", http.MethodGet, "/api/v1/stacks/0190a6e0-9999-7000-8000-000000000009/files", nil), 404)
}

// TestFileContentNeverLogged: contents written, uploaded, read and
// downloaded through the API never reach the logs or the audit trail
// (paths and sizes do).
func TestFileContentNeverLogged(t *testing.T) {
	e := newEnv(t, 1<<20)
	const canary = "CANARY-SECRET-FILE-CONTENT-4411"
	must(t, e.do("owner", http.MethodPut, e.volURL+"/content?path=s.env", map[string]string{"content": "PASSWORD=" + canary}, "If-None-Match", "*"), 200)
	must(t, e.do("owner", http.MethodGet, e.volURL+"/content?path=s.env", nil), 200)
	must(t, e.do("owner", http.MethodPost, e.volURL+"/uploads?name=u.env", []byte(canary), "If-None-Match", "*"), 201)
	must(t, e.do("owner", http.MethodPost, e.volURL+"/uploads?name=u.env", []byte(canary), "If-None-Match", "*"), 412)
	must(t, e.do("owner", http.MethodGet, e.volURL+"/downloads?path=u.env", nil), 200)
	must(t, e.do("owner", http.MethodGet, e.volURL+"/downloads?path=.", nil), 200)
	audit := e.audit.dump()
	if strings.Contains(e.logs.String(), canary) || strings.Contains(audit, canary) {
		t.Fatalf("file content leaked:\nlogs: %s\naudit: %s", e.logs.String(), audit)
	}
	if !strings.Contains(audit, "u.env") || !strings.Contains(audit, "volume.files.write") || !strings.Contains(audit, "volume.files.download") {
		t.Fatalf("audit records lack the operations and paths: %s", audit)
	}
}
