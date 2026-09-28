package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/db/migrations"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/logging"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs/jobstest"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// testAuthz grants job.read/job.cancel except where denied; jobs targeting
// the stack "secret" are invisible to user "eve". Only "alice" holds the
// owner-only probe (she reads every job); bobDenied is a capability user
// "bob" lacks.
type testAuthz struct {
	noCancel  bool
	bobDenied string
}

func (a testAuthz) Can(_ context.Context, p authz.Principal, capability string, r authz.Resource) authz.Decision {
	if capability == string(CapJobCancel) && a.noCancel {
		return authz.Deny("no cancel")
	}
	if capability == ownerProbe && p.UserID != "alice" {
		return authz.Deny("owner only")
	}
	if p.UserID == "bob" && capability == a.bobDenied {
		return authz.Deny("not granted")
	}
	if r.ID == "secret" && p.UserID == "eve" {
		return authz.Deny("no access")
	}
	for _, t := range r.Targets {
		if p.UserID == "eve" && t.ID == "secret" {
			return authz.Deny("no access")
		}
	}
	return authz.Allow("test")
}

type jobsFixture struct {
	t    *testing.T
	ctx  context.Context
	clk  *clock.Fake
	eng  *jobs.Engine
	disp *jobstest.Dispatcher
	h    http.Handler
}

// newJobsFixture serves the job routes with az; the engine allows every
// enqueue unless engineAz is given.
func newJobsFixture(t *testing.T, az authz.Authorizer, engineAz ...authz.Authorizer) *jobsFixture {
	t.Helper()
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: dir, Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	clk := testutil.FakeClock()
	disp := jobstest.New()
	var engAz authz.Authorizer = authz.Func(func(context.Context, authz.Principal, string, authz.Resource) authz.Decision { return authz.Allow("t") })
	if len(engineAz) > 0 {
		engAz = engineAz[0]
	}
	eng, err := jobs.New(jobs.Options{DB: db, Clock: clk, Logger: testutil.Logger(t), Dispatcher: disp, Authorizer: engAz})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	mux := http.NewServeMux()
	New(mux, Deps{Jobs: eng, Authorizer: az, Clock: clk})
	logger := testutil.Logger(t)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := logging.WithRequestID(r.Context(), "req-123")
		ctx = logging.IntoContext(ctx, logger)
		if u := r.Header.Get("X-Test-User"); u != "" { // test-only principal injection
			ctx, _ = authz.WithPrincipal(ctx, authz.Principal{Kind: authz.KindUser, UserID: u})
		}
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
	return &jobsFixture{t: t, ctx: ctx, clk: clk, eng: eng, disp: disp, h: h}
}

func (f *jobsFixture) enqueue(kind domain.JobKind, env string, targets ...domain.JobTarget) domain.Job {
	f.t.Helper()
	j, _, err := f.eng.Enqueue(f.ctx, jobs.Request{Kind: kind, Principal: authz.Principal{Kind: authz.KindUser, UserID: "alice"},
		EnvironmentID: env, Targets: targets})
	if err != nil {
		f.t.Fatal(err)
	}
	return j
}

func (f *jobsFixture) do(method, target, user string, hdr ...string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

// finish runs a job to success through fake agent frames.
func (f *jobsFixture) finish(env string) { f.t.Helper(); f.end(env, "succeeded") }

// fail runs the queued jobs of env to failure through fake agent frames.
func (f *jobsFixture) fail(env string) { f.t.Helper(); f.end(env, "failed") }

func (f *jobsFixture) end(env, outcome string) {
	f.t.Helper()
	f.disp.Connect(env)
	if err := f.eng.DispatchPending(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	for _, cmd := range f.disp.Drain(env) {
		for _, fr := range []struct {
			typ protocol.Type
			p   any
		}{
			{protocol.TypeAck, protocol.AckPayload{Accepted: true}},
			{protocol.TypeProgress, protocol.ProgressPayload{Step: "start", Percent: 50, Message: "halfway"}},
			{protocol.TypeResult, protocol.ResultPayload{Outcome: outcome}},
		} {
			frame, err := protocol.NewFrame(fr.typ, string(fr.typ)+"-"+cmd.ID, cmd.ID, cmd.Ref(), fr.p)
			if err != nil {
				f.t.Fatal(err)
			}
			if _, err := f.eng.HandleAgentFrame(f.ctx, env, frame); err != nil {
				f.t.Fatal(err)
			}
		}
	}
}

func stackT(id string) domain.JobTarget { return domain.JobTarget{Type: domain.TargetStack, ID: id} }

func TestJobRoutesFailClosed(t *testing.T) {
	f := newJobsFixture(t, testAuthz{})
	j := f.enqueue(jobspec.StackStart, "e1", stackT("web"))
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, BasePath + "/jobs"},
		{http.MethodGet, BasePath + "/jobs/" + j.ID},
		{http.MethodPost, BasePath + "/jobs/" + j.ID + "/cancellations"},
		{http.MethodGet, BasePath + "/jobs/" + j.ID + "/events/stream"},
	} {
		decodeError(t, f.do(c.method, c.path, ""), http.StatusUnauthorized, CodeUnauthenticated)
	}
	// The default authorizer (nil -> DenyAll) hides everything.
	g := newJobsFixture(t, nil)
	k := g.enqueue(jobspec.StackStart, "e1", stackT("web"))
	rec := g.do(http.MethodGet, BasePath+"/jobs", "alice")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("list under DenyAll: %d %s", rec.Code, rec.Body)
	}
	decodeError(t, g.do(http.MethodGet, BasePath+"/jobs/"+k.ID, "alice"), http.StatusNotFound, CodeNotFound)
	decodeError(t, g.do(http.MethodPost, BasePath+"/jobs/"+k.ID+"/cancellations", "alice"), http.StatusNotFound, CodeNotFound)
	if got, _ := g.eng.Get(g.ctx, k.ID); got.State != domain.JobQueued {
		t.Fatal("job cancelled without permission")
	}
	// No job engine configured: 503.
	mux := http.NewServeMux()
	New(mux, Deps{Authorizer: testAuthz{}})
	req := httptest.NewRequest(http.MethodGet, BasePath+"/jobs", nil)
	ctx, _ := authz.WithPrincipal(logging.IntoContext(req.Context(), testutil.Logger(t)), authz.Principal{Kind: authz.KindUser, UserID: "a"})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no engine: %d", rec.Code)
	}
}

func listIDs(t *testing.T, rec *httptest.ResponseRecorder) ([]string, string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var page struct {
		Items      []Job  `json:"items"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, j := range page.Items {
		ids = append(ids, j.ID)
	}
	return ids, page.NextCursor
}

func TestListJobsPaginationFiltersAndVisibility(t *testing.T) {
	f := newJobsFixture(t, testAuthz{})
	var all []string
	for _, s := range []string{"a", "secret", "b", "secret", "c"} {
		all = append([]string{f.enqueue(jobspec.StackStart, "e1", stackT(s)).ID}, all...) // newest first
	}
	other := f.enqueue(jobspec.PruneRun, "e2")
	all = append([]string{other.ID}, all...)

	// alice sees everything, two per page.
	var got []string
	cursor := ""
	for i := 0; i < 10; i++ {
		ids, next := listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?limit=2&cursor="+cursor, "alice"))
		got = append(got, ids...)
		if next == "" {
			break
		}
		cursor = next
	}
	if strings.Join(got, ",") != strings.Join(all, ",") {
		t.Fatalf("paged %v, want %v", got, all)
	}
	// eve does not see jobs targeting the secret stack (filtered per item).
	got, cursor = nil, ""
	for i := 0; i < 10; i++ {
		ids, next := listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?limit=2&cursor="+cursor, "eve"))
		got = append(got, ids...)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(got) != 4 {
		t.Fatalf("eve sees %v", got)
	}
	decodeError(t, f.do(http.MethodGet, BasePath+"/jobs/"+all[2], "eve"), http.StatusNotFound, CodeNotFound)

	ids, _ := listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?environmentId=e2", "alice"))
	if len(ids) != 1 || ids[0] != other.ID {
		t.Fatalf("env filter %v", ids)
	}
	ids, _ = listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?kind=prune.run&state=queued", "alice"))
	if len(ids) != 1 || ids[0] != other.ID {
		t.Fatalf("kind filter %v", ids)
	}
	ids, _ = listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?target=stack:secret", "alice"))
	if len(ids) != 2 {
		t.Fatalf("target filter %v", ids)
	}
	ids, _ = listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?state=succeeded", "alice"))
	if len(ids) != 0 {
		t.Fatalf("state filter %v", ids)
	}
	decodeError(t, f.do(http.MethodGet, BasePath+"/jobs?target=nocolon", "alice"), http.StatusUnprocessableEntity, CodeValidationFailed)
	decodeError(t, f.do(http.MethodGet, BasePath+"/jobs?target=planet:x", "alice"), http.StatusUnprocessableEntity, CodeValidationFailed)
	decodeError(t, f.do(http.MethodGet, BasePath+"/jobs?cursor=!!", "alice"), http.StatusUnprocessableEntity, CodeValidationFailed)
	// A cursor is bound to the filters it was issued for.
	_, next := listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?limit=1&environmentId=e1", "alice"))
	if next == "" {
		t.Fatal("expected a next cursor")
	}
	if ids, _ := listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?limit=1&environmentId=e1&cursor="+next, "alice")); len(ids) != 1 {
		t.Fatalf("second page %v", ids)
	}
	decodeError(t, f.do(http.MethodGet, BasePath+"/jobs?limit=1&environmentId=e2&cursor="+next, "alice"), http.StatusUnprocessableEntity, CodeValidationFailed)
	decodeError(t, f.do(http.MethodGet, BasePath+"/jobs?state=done", "alice"), http.StatusUnprocessableEntity, CodeValidationFailed)
}

// Views restore their progress bars from the running jobs (#22): the kind
// filter takes a comma-separated list, and targetEnvironmentId narrows the
// target filter to one environment (container names repeat across
// environments; a target's environment is the job's unless it names one).
func TestListJobsKindListAndTargetEnvironment(t *testing.T) {
	f := newJobsFixture(t, testAuthz{})
	start := f.enqueue(jobspec.StackStart, "e1", stackT("a"))
	web1 := f.enqueue(jobspec.ContainerRestart, "e1", domain.JobTarget{Type: domain.TargetContainer, ID: "web"})
	web2 := f.enqueue(jobspec.ContainerRestart, "e2", domain.JobTarget{Type: domain.TargetContainer, ID: "web"})
	prune := f.enqueue(jobspec.PruneRun, "e1")

	ids, _ := listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?kind=prune.run,stack.start", "alice"))
	if strings.Join(ids, ",") != prune.ID+","+start.ID {
		t.Fatalf("kind list %v", ids)
	}
	ids, _ = listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?kind=prune.run,%20prune.run", "alice"))
	if len(ids) != 1 || ids[0] != prune.ID {
		t.Fatalf("repeated kind %v", ids)
	}
	ids, _ = listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?target=container:web", "alice"))
	if strings.Join(ids, ",") != web2.ID+","+web1.ID {
		t.Fatalf("target in every environment %v", ids)
	}
	ids, _ = listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?target=container:web&targetEnvironmentId=e2", "alice"))
	if len(ids) != 1 || ids[0] != web2.ID {
		t.Fatalf("target in e2 %v", ids)
	}
	if ids, _ := listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?target=container:web&targetEnvironmentId=e3", "alice")); len(ids) != 0 {
		t.Fatalf("target in e3 %v", ids)
	}
	// A cursor is bound to the target's environment.
	_, next := listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?limit=1&target=container:web", "alice"))
	if next == "" {
		t.Fatal("expected a next cursor")
	}
	decodeError(t, f.do(http.MethodGet, BasePath+"/jobs?limit=1&target=container:web&targetEnvironmentId=e1&cursor="+next, "alice"),
		http.StatusUnprocessableEntity, CodeValidationFailed)
	decodeError(t, f.do(http.MethodGet, BasePath+"/jobs?targetEnvironmentId=e1", "alice"), http.StatusUnprocessableEntity, CodeValidationFailed)
	decodeError(t, f.do(http.MethodGet, BasePath+"/jobs?kind=prune.run,,stack.start", "alice"), http.StatusUnprocessableEntity, CodeValidationFailed)
	kinds := make([]string, maxKindFilters+1)
	for i := range kinds {
		kinds[i] = "k" + itoa(int64(i)) + ".y"
	}
	decodeError(t, f.do(http.MethodGet, BasePath+"/jobs?kind="+strings.Join(kinds, ","), "alice"), http.StatusUnprocessableEntity, CodeValidationFailed)
}

// The jobs view (#22) filters by origin: manual, scheduled or API token.
func TestListJobsOriginFilter(t *testing.T) {
	f := newJobsFixture(t, testAuthz{})
	manual := f.enqueue(jobspec.StackStart, "e1", stackT("a"))
	scheduled, _, err := f.eng.Enqueue(f.ctx, jobs.Request{Kind: jobspec.PruneRun, Principal: authz.Service(), EnvironmentID: "e1"})
	if err != nil {
		t.Fatal(err)
	}
	if scheduled.Origin != domain.OriginScheduled {
		t.Fatalf("origin %s", scheduled.Origin)
	}
	ids, _ := listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?origin=scheduled", "alice"))
	if len(ids) != 1 || ids[0] != scheduled.ID {
		t.Fatalf("scheduled filter %v", ids)
	}
	ids, _ = listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?origin=manual", "alice"))
	if len(ids) != 1 || ids[0] != manual.ID {
		t.Fatalf("manual filter %v", ids)
	}
	ids, _ = listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?origin=manual&origin=scheduled", "alice"))
	if len(ids) != 2 {
		t.Fatalf("both origins %v", ids)
	}
	if ids, _ := listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?origin=api_token", "alice")); len(ids) != 0 {
		t.Fatalf("api_token filter %v", ids)
	}
	// A cursor is bound to its origin filter.
	_, next := listIDs(t, f.do(http.MethodGet, BasePath+"/jobs?limit=1&origin=manual&origin=scheduled", "alice"))
	if next == "" {
		t.Fatal("expected a next cursor")
	}
	decodeError(t, f.do(http.MethodGet, BasePath+"/jobs?limit=1&origin=manual&cursor="+next, "alice"), http.StatusUnprocessableEntity, CodeValidationFailed)
	decodeError(t, f.do(http.MethodGet, BasePath+"/jobs?origin=robot", "alice"), http.StatusUnprocessableEntity, CodeValidationFailed)
}

func TestGetAndCancelJob(t *testing.T) {
	f := newJobsFixture(t, testAuthz{})
	f.disp.Connect("e1")
	running := f.enqueue(jobspec.StackDeploy, "e1", stackT("web"))
	blocked := f.enqueue(jobspec.StackDeploy, "e1", stackT("web"))
	if err := f.eng.DispatchPending(f.ctx); err != nil {
		t.Fatal(err)
	}
	rec := f.do(http.MethodGet, BasePath+"/jobs/"+blocked.ID, "alice")
	var j Job
	if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if j.State != "blocked" || j.BlockedBy == nil || j.BlockedBy.JobID != running.ID || j.BlockedBy.Reason != "lock" ||
		j.Origin != "manual" || j.Attempt != 1 || len(j.Locks) != 2 || j.LocksHeld || j.InitiatorUserID != "alice" || !j.Cancellable {
		t.Fatalf("job %+v", j)
	}
	rec = f.do(http.MethodGet, BasePath+"/jobs/"+running.ID, "alice")
	j = Job{}
	_ = json.Unmarshal(rec.Body.Bytes(), &j)
	if j.State != "dispatched" || !j.LocksHeld || j.BlockedBy != nil || j.DispatchedAt == nil {
		t.Fatalf("running job %+v", j)
	}
	decodeError(t, f.do(http.MethodGet, BasePath+"/jobs/nope", "alice"), http.StatusNotFound, CodeNotFound)

	rec = f.do(http.MethodPost, BasePath+"/jobs/"+blocked.ID+"/cancellations", "alice")
	if rec.Code != http.StatusAccepted || rec.Header().Get("Location") != BasePath+"/jobs/"+blocked.ID {
		t.Fatalf("cancel %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	j = Job{}
	_ = json.Unmarshal(rec.Body.Bytes(), &j)
	if j.State != "cancelled" || j.Error == nil || j.Error.Class != domain.ErrorCancelled || j.Error.Recovery == "" || j.Cancellable {
		t.Fatalf("cancelled job %+v", j)
	}
	decodeError(t, f.do(http.MethodPost, BasePath+"/jobs/"+blocked.ID+"/cancellations", "alice"), http.StatusConflict, "job_finished")

	g := newJobsFixture(t, testAuthz{noCancel: true})
	k := g.enqueue(jobspec.StackStart, "e1", stackT("web"))
	decodeError(t, g.do(http.MethodPost, BasePath+"/jobs/"+k.ID+"/cancellations", "alice"), http.StatusForbidden, CodeForbidden)
}

type sseEvent struct {
	id, event, data string
}

func parseSSE(t *testing.T, body string) (events []sseEvent, comments int) {
	t.Helper()
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		var e sseEvent
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, ":"):
				comments++
			case strings.HasPrefix(line, "id: "):
				e.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				e.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				e.data = strings.TrimPrefix(line, "data: ")
			}
		}
		if e.event != "" {
			events = append(events, e)
		}
	}
	return events, comments
}

func TestJobEventStreamReplayAndClose(t *testing.T) {
	f := newJobsFixture(t, testAuthz{})
	j := f.enqueue(jobspec.StackStart, "e1", stackT("web"))
	f.finish("e1")
	rec := f.do(http.MethodGet, BasePath+"/jobs/"+j.ID+"/events/stream", "alice")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream" || rec.Header().Get("X-Accel-Buffering") != "no" ||
		rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d headers %v", rec.Code, rec.Header())
	}
	events, _ := parseSSE(t, rec.Body.String())
	if len(events) < 5 || events[0].event != "job" || events[0].id != "" {
		t.Fatalf("events %+v", events)
	}
	var last JobEvent
	for i, e := range events[1:] {
		var je JobEvent
		if err := json.Unmarshal([]byte(e.data), &je); err != nil {
			t.Fatal(err)
		}
		if e.id != itoa(int64(i+1)) || je.Seq != int64(i+1) || e.event != je.Type {
			t.Fatalf("event %d: %+v %+v", i, e, je)
		}
		last = je
	}
	if last.Type != "state" || last.State != "succeeded" {
		t.Fatalf("last event %+v", last)
	}
	// Resume after event 2.
	rec = f.do(http.MethodGet, BasePath+"/jobs/"+j.ID+"/events/stream", "alice", "Last-Event-ID", "2")
	resumed, _ := parseSSE(t, rec.Body.String())
	if len(resumed) != len(events)-2 || resumed[1].id != "3" {
		t.Fatalf("resumed %+v", resumed)
	}
	decodeError(t, f.do(http.MethodGet, BasePath+"/jobs/"+j.ID+"/events/stream", "alice", "Last-Event-ID", "x"),
		http.StatusUnprocessableEntity, CodeValidationFailed)
	hidden := f.enqueue(jobspec.StackStart, "e1", stackT("secret"))
	decodeError(t, f.do(http.MethodGet, BasePath+"/jobs/"+hidden.ID+"/events/stream", "eve"), http.StatusNotFound, CodeNotFound)
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// TestJobEventStreamLiveHeartbeat streams a running job over a real
// connection: heartbeats follow the fake clock, live events arrive as the
// job progresses and the stream closes after the terminal state.
func TestJobEventStreamLiveHeartbeat(t *testing.T) {
	f := newJobsFixture(t, testAuthz{})
	j := f.enqueue(jobspec.StackStart, "e1", stackT("web"))
	srv := httptest.NewServer(f.h)
	defer srv.Close()
	req, _ := http.NewRequestWithContext(f.ctx, http.MethodGet, srv.URL+BasePath+"/jobs/"+j.ID+"/events/stream", nil)
	req.Header.Set("X-Test-User", "alice")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	lines := make(chan string, 100)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	waitFor := func(prefix string) string {
		t.Helper()
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("stream closed before %q", prefix)
				}
				if strings.HasPrefix(l, prefix) {
					return l
				}
			case <-f.ctx.Done():
				t.Fatalf("timed out waiting for %q", prefix)
			}
		}
	}
	waitFor("event: job")
	waitFor("id: 1") // queued
	if err := f.clk.BlockUntilWaiters(f.ctx, 1); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(DefaultSSEHeartbeat)
	waitFor(": heartbeat")
	f.finish("e1")
	if l := waitFor("data: {\"seq\""); !strings.Contains(l, `"type"`) {
		t.Fatalf("live event %q", l)
	}
	for range lines { // drains until the server closes the stream
	}
	got, _ := f.eng.Get(f.ctx, j.ID)
	if got.State != domain.JobSucceeded {
		t.Fatalf("state %s", got.State)
	}
}

type jobsPage struct {
	Items      []Job  `json:"items"`
	NextCursor string `json:"nextCursor"`
	Total      *int64 `json:"total"`
}

func listPage(t *testing.T, rec *httptest.ResponseRecorder) jobsPage {
	t.Helper()
	var page jobsPage
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &page) != nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	return page
}

// total counts the visible matching jobs across pages: a COUNT for the
// owner, a per-job check for others; policyId filters by the job's policy.
func TestListJobsTotalAndPolicyFilter(t *testing.T) {
	f := newJobsFixture(t, testAuthz{})
	f.enqueue(jobspec.StackStart, "e1", stackT("a"))
	f.enqueue(jobspec.StackStart, "e1", stackT("secret"))
	run, _, err := f.eng.Enqueue(f.ctx, jobs.Request{Kind: jobspec.PruneRun, Principal: authz.Service(), EnvironmentID: "e2", PolicyID: "pol-1"})
	if err != nil {
		t.Fatal(err)
	}
	manual, _, err := f.eng.Enqueue(f.ctx, jobs.Request{Kind: jobspec.PruneRun, Principal: authz.Principal{Kind: authz.KindUser, UserID: "alice"},
		EnvironmentID: "e1", PolicyID: "pol-2"})
	if err != nil {
		t.Fatal(err)
	}
	const want = 4
	if p := listPage(t, f.do(http.MethodGet, BasePath+"/jobs?limit=1", "alice")); len(p.Items) != 1 || p.Total == nil || *p.Total != want ||
		p.NextCursor == "" {
		t.Fatalf("alice: %d items, total %v", len(p.Items), p.Total)
	}
	// eve may not see the secret stack's job: it is neither listed nor counted.
	if p := listPage(t, f.do(http.MethodGet, BasePath+"/jobs?limit=1", "eve")); p.Total == nil || *p.Total != want-1 {
		t.Fatalf("eve: total %v", p.Total)
	}
	if p := listPage(t, f.do(http.MethodGet, BasePath+"/jobs?target=stack:secret", "eve")); len(p.Items) != 0 || p.Total == nil || *p.Total != 0 {
		t.Fatalf("eve on secret: %d items, total %v", len(p.Items), p.Total)
	}
	p := listPage(t, f.do(http.MethodGet, BasePath+"/jobs?policyId=pol-1", "eve"))
	if len(p.Items) != 1 || p.Items[0].ID != run.ID || p.Items[0].PolicyID != "pol-1" || p.Total == nil || *p.Total != 1 {
		t.Fatalf("policy filter: %+v total %v", p.Items, p.Total)
	}
	if p := listPage(t, f.do(http.MethodGet, BasePath+"/jobs?policyId=pol-2&kind=prune.run", "alice")); len(p.Items) != 1 || p.Items[0].ID != manual.ID {
		t.Fatalf("manual policy run: %+v", p.Items)
	}
	if p := listPage(t, f.do(http.MethodGet, BasePath+"/jobs?policyId=nope", "alice")); len(p.Items) != 0 || p.Total == nil || *p.Total != 0 {
		t.Fatalf("unknown policy: %+v", p)
	}
	// A cursor is bound to its policy filter.
	first := listPage(t, f.do(http.MethodGet, BasePath+"/jobs?limit=1", "alice"))
	decodeError(t, f.do(http.MethodGet, BasePath+"/jobs?limit=1&policyId=pol-1&cursor="+first.NextCursor, "alice"),
		http.StatusUnprocessableEntity, CodeValidationFailed)
}

// A failed image pull is retried as a new job linked to it; the job shows
// retryable only to callers who may run it again.
func TestRetryJob(t *testing.T) {
	az := testAuthz{bobDenied: "image.pull"}
	f := newJobsFixture(t, az, az)
	f.disp.Connect("e1")
	orig, _, err := f.eng.Enqueue(f.ctx, jobs.Request{Kind: jobspec.ImagePull, Principal: authz.Principal{Kind: authz.KindUser, UserID: "alice"},
		EnvironmentID: "e1", Targets: []domain.JobTarget{{Type: domain.TargetImage, ID: "nginx:1"}}, Input: map[string]any{"reference": "nginx:1"}})
	if err != nil {
		t.Fatal(err)
	}
	f.fail("e1")
	getJob := func(id, user string) Job {
		t.Helper()
		rec := f.do(http.MethodGet, BasePath+"/jobs/"+id, user)
		var j Job
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &j) != nil {
			t.Fatalf("get %s: %d %s", id, rec.Code, rec.Body)
		}
		return j
	}
	if j := getJob(orig.ID, "alice"); j.State != "failed" || !j.Retryable {
		t.Fatalf("alice sees %s retryable=%v", j.State, j.Retryable)
	}
	if p := listPage(t, f.do(http.MethodGet, BasePath+"/jobs", "alice")); len(p.Items) != 1 || !p.Items[0].Retryable {
		t.Fatalf("list: %+v", p.Items)
	}
	// bob may read the job but not pull images: no retry.
	if getJob(orig.ID, "bob").Retryable {
		t.Fatal("bob sees a retry he may not start")
	}
	decodeError(t, f.do(http.MethodPost, BasePath+"/jobs/"+orig.ID+"/retries", "bob"), http.StatusForbidden, CodeForbidden)

	rec := f.do(http.MethodPost, BasePath+"/jobs/"+orig.ID+"/retries", "alice", "Idempotency-Key", "retry-1")
	var nj Job
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &nj) != nil {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}
	if nj.ID == orig.ID || nj.RetryOf != orig.ID || nj.Kind != "image.pull" || nj.State != "queued" || nj.Retryable ||
		rec.Header().Get("Location") != BasePath+"/jobs/"+nj.ID {
		t.Fatalf("new job %+v (Location %s)", nj, rec.Header().Get("Location"))
	}
	if getJob(nj.ID, "alice").RetryOf != orig.ID {
		t.Fatal("retryOf not returned")
	}
	// Repeating the request returns the same retry.
	rec = f.do(http.MethodPost, BasePath+"/jobs/"+orig.ID+"/retries", "alice", "Idempotency-Key", "retry-1")
	var again Job
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &again) != nil || again.ID != nj.ID {
		t.Fatalf("repeated retry: %d %s", rec.Code, rec.Body)
	}
	// The retry is still queued: not retryable.
	decodeError(t, f.do(http.MethodPost, BasePath+"/jobs/"+nj.ID+"/retries", "alice"), http.StatusConflict, CodeJobNotRetryable)
	// A kind that is not retryable.
	stop := f.enqueue(jobspec.StackStop, "e2", stackT("web"))
	if _, err := f.eng.Cancel(f.ctx, stop.ID); err != nil {
		t.Fatal(err)
	}
	if getJob(stop.ID, "alice").Retryable {
		t.Fatal("stack.stop is retryable")
	}
	decodeError(t, f.do(http.MethodPost, BasePath+"/jobs/"+stop.ID+"/retries", "alice"), http.StatusConflict, CodeJobNotRetryable)
	// Invisible and unknown jobs are 404; unauthenticated calls 401.
	hidden := f.enqueue(jobspec.StackStop, "e2", stackT("secret"))
	decodeError(t, f.do(http.MethodPost, BasePath+"/jobs/"+hidden.ID+"/retries", "eve"), http.StatusNotFound, CodeNotFound)
	decodeError(t, f.do(http.MethodPost, BasePath+"/jobs/nope/retries", "alice"), http.StatusNotFound, CodeNotFound)
	decodeError(t, f.do(http.MethodPost, BasePath+"/jobs/"+orig.ID+"/retries", ""), http.StatusUnauthorized, CodeUnauthenticated)
}

func TestJobErrorFor(t *testing.T) {
	cases := map[error]struct {
		status int
		code   string
	}{
		domain.ErrJobNotFound:            {404, CodeNotFound},
		domain.ErrJobFinished:            {409, "job_finished"},
		domain.ErrJobIdempotencyConflict: {409, "idempotency_key_reused"},
		domain.ErrJobForbidden:           {403, CodeForbidden},
		domain.ErrJobInvalid:             {422, CodeValidationFailed},
		domain.ErrJobUnknownKind:         {422, CodeValidationFailed},
		domain.ErrJobKindUnavailable:     {501, "job_kind_unavailable"},
		domain.ErrJobNotRetryable:        {409, CodeJobNotRetryable},
		&domain.JobNotRetryableError{Reason: domain.RetryRefusedKind, Message: "no"}: {409, CodeJobNotRetryable},
		errors.New("disk"): {500, CodeInternal},
	}
	for in, want := range cases {
		var e *Error
		if !errors.As(JobErrorFor(in), &e) || e.GetStatus() != want.status || e.Code != want.code {
			t.Errorf("%v -> %+v", in, e)
		}
	}
	if JobErrorFor(nil) != nil {
		t.Fatal("nil error mapped")
	}
	acc := Accepted(domain.Job{ID: "j1", State: domain.JobQueued, Progress: domain.JobProgress{Percent: -1}})
	if acc.Location != BasePath+"/jobs/j1" || acc.Body.Progress.Percent != nil || acc.Body.Targets == nil {
		t.Fatalf("accepted %+v", acc)
	}
}
