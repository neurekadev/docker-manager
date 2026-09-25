package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/authztest"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/manager/authz/policy"
	"github.com/neurekadev/dockyard/internal/manager/events"
	"github.com/neurekadev/dockyard/internal/manager/live"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// livePolicy: an owner, a metrics-only user of container web, a user with
// the files of stack s1, and a restricted user.
func livePolicy() *authztest.Policy {
	p := authztest.New().Owner("own").
		Member("metrics", "g-metrics").Group("g-metrics", "allow container.metrics.read @container:e1/web").
		Member("filer", "g-files").Group("g-files", "allow stack.read @stack:s1", "allow stack.files.read @stack:s1").
		Member("nobody", "g-none").Group("g-none")
	p.Locate(func(ref authz.ResourceRef) policy.Location {
		switch ref.Type {
		case catalog.TypeStack:
			if ref.ID == "s1" || ref.ID == "s2" {
				return policy.Location{Found: true, EnvironmentID: "e1"}
			}
		case catalog.TypeContainer, catalog.TypeVolume:
			return policy.Location{Found: true, EnvironmentID: "e1"}
		}
		return policy.Location{}
	})
	return p
}

type fakeFileWatch struct {
	mu    sync.Mutex
	holds map[string]int
}

func (f *fakeFileWatch) Hold(env, volume string) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.holds[env+"/"+volume]++
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.holds[env+"/"+volume]--
	}
}

func (f *fakeFileWatch) held(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.holds[key]
}

// liveFixture serves the live stream over HTTP with a running hub. Request
// contexts can be ended like the identity layer does (revoke).
type liveFixture struct {
	t     *testing.T
	ctx   context.Context
	clk   *clock.Fake
	bus   *events.Bus
	hub   *live.Hub
	srv   *httptest.Server
	watch *fakeFileWatch

	mu      sync.Mutex
	cancels map[string][]context.CancelCauseFunc
}

func newLiveFixture(t *testing.T, o live.Options) *liveFixture {
	t.Helper()
	f := &liveFixture{t: t, ctx: testutil.Context(t), clk: testutil.FakeClock(), watch: &fakeFileWatch{holds: map[string]int{}},
		cancels: map[string][]context.CancelCauseFunc{}}
	f.bus = events.New(f.clk)
	o.Bus, o.Clock, o.Logger = f.bus, f.clk, testutil.Logger(t)
	f.hub = live.New(o)
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { defer close(done); f.hub.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	mux := http.NewServeMux()
	New(mux, Deps{Authorizer: livePolicy(), Clock: f.clk, Live: f.hub, Events: f.bus, FileWatch: f.watch, Idempotency: &memIdempotency{}})
	revocable := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancelCause(r.Context())
		f.mu.Lock()
		f.cancels[r.Header.Get(authztest.UserHeader)] = append(f.cancels[r.Header.Get(authztest.UserHeader)], cancel)
		f.mu.Unlock()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
	f.srv = httptest.NewServer(authztest.Authenticate(withTestContext(t, revocable, "")))
	t.Cleanup(f.srv.Close)
	return f
}

// revoke ends user's open requests like auth.Service.AccessChanged.
func (f *liveFixture) revoke(user string, cause error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cancels[user] {
		c(cause)
	}
}

// liveStream is an open stream's parsed events.
type liveStream struct {
	t      *testing.T
	events chan sseEvent
	status int
	cancel context.CancelFunc
}

func (f *liveFixture) open(user, query string, header ...string) *liveStream {
	f.t.Helper()
	ctx, cancel := context.WithCancel(f.ctx)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.srv.URL+BasePath+"/live/stream"+query, nil)
	req.Header.Set(authztest.UserHeader, user)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := f.srv.Client().Do(req) //nolint:bodyclose // closed by the reader goroutine
	if err != nil {
		cancel()
		f.t.Fatal(err)
	}
	s := &liveStream{t: f.t, events: make(chan sseEvent, 50_000), status: resp.StatusCode, cancel: cancel}
	f.t.Cleanup(cancel)
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		close(s.events)
		return s
	}
	go func() {
		defer close(s.events)
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		var e sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if e.event != "" {
					s.events <- e
				}
				e = sseEvent{}
			case strings.HasPrefix(line, ":"):
				s.events <- sseEvent{event: ":" + strings.TrimSpace(line[1:])}
			case strings.HasPrefix(line, "id: "):
				e.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				e.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				e.data = line[6:]
			}
		}
	}()
	return s
}

// next returns the next event (failing after 10 s).
func (s *liveStream) next() sseEvent {
	s.t.Helper()
	select {
	case e, ok := <-s.events:
		if !ok {
			s.t.Fatal("stream ended")
		}
		return e
	case <-time.After(10 * time.Second):
		s.t.Fatal("stream stalled")
	}
	return sseEvent{}
}

// until returns the events up to and including the first named one.
func (s *liveStream) until(name string) []sseEvent {
	s.t.Helper()
	var out []sseEvent
	for {
		e := s.next()
		out = append(out, e)
		if e.event == name {
			return out
		}
	}
}

func (s *liveStream) hello() LiveHello {
	s.t.Helper()
	e := s.next()
	var h LiveHello
	if e.event != "hello" || e.id != "" || json.Unmarshal([]byte(e.data), &h) != nil || h.Version != live.Version {
		s.t.Fatalf("hello %+v", e)
	}
	return h
}

func liveDocker(name, action string) events.Event {
	return events.Event{Type: events.DockerEvent, ResourceType: events.ResourceContainer, ResourceID: name, EnvironmentID: "e1",
		Attributes: map[string]string{"action": action, "image": "secret/image:tag", "name": name}}
}

func invalidation(t *testing.T, e sseEvent) LiveInvalidate {
	t.Helper()
	var inv LiveInvalidate
	if e.event != "invalidate" || json.Unmarshal([]byte(e.data), &inv) != nil {
		t.Fatalf("not an invalidation: %+v", e)
	}
	return inv
}

// TestLiveStreamSnapshotResumeAndReset (#23): hello fixes the snapshot
// cursor; events follow with increasing ids; a reconnect with the last id
// (header, or the cursor parameter) resumes with exactly the missed
// events; an unknown cursor starts with a reset; repeated events of one
// resource are coalesced; unknown topics are refused.
func TestLiveStreamSnapshotResumeAndReset(t *testing.T) {
	f := newLiveFixture(t, live.Options{})
	s := f.open("own", "")
	if s.status != http.StatusOK {
		t.Fatalf("status %d", s.status)
	}
	h := s.hello()
	if h.Resumed || !slices.Contains(h.Topics, live.TopicFiles) || h.Cursor != f.hub.Epoch()+".0" {
		t.Fatalf("hello %+v", h)
	}
	for _, n := range []string{"a", "b", "c"} {
		f.bus.Publish(liveDocker(n, "start"))
	}
	var ids []string
	for range 3 {
		e := s.next()
		inv := invalidation(t, e)
		if inv.Topic != live.TopicContainers || inv.Kind != "container" || inv.Action != live.ActionUpdated || inv.EnvironmentID != "e1" {
			t.Fatalf("invalidation %+v", inv)
		}
		if strings.Contains(e.data, "secret/image") {
			t.Fatalf("attributes leaked: %s", e.data)
		}
		ids = append(ids, e.id)
	}
	if ids[0] != f.hub.Epoch()+".1" || ids[2] != f.hub.Epoch()+".3" {
		t.Fatalf("ids %v", ids)
	}
	s.cancel()
	// Missed while disconnected.
	f.bus.Publish(liveDocker("d", "die"))
	f.bus.Publish(liveDocker("e", "destroy"))
	for _, resume := range []*liveStream{f.open("own", "", "Last-Event-ID", ids[2]), f.open("own", "?cursor="+ids[2])} {
		if h := resume.hello(); !h.Resumed {
			t.Fatalf("not resumed %+v", h)
		}
		d, e := invalidation(t, resume.next()), invalidation(t, resume.next())
		if d.ResourceID != "d" || e.ResourceID != "e" || e.Action != live.ActionDeleted {
			t.Fatalf("replay %+v %+v", d, e)
		}
		resume.cancel()
	}
	stale := f.open("own", "", "Last-Event-ID", "0000dead.4")
	if h := stale.hello(); h.Resumed {
		t.Fatal("stale cursor resumed")
	}
	var reset LiveReset
	if e := stale.next(); e.event != "reset" || json.Unmarshal([]byte(e.data), &reset) != nil || reset.Reason != live.ResetServerRestart {
		t.Fatalf("reset %+v", e)
	}
	stale.cancel()

	// Coalescing: ten health events of one container are one invalidation
	// now and one when the 250 ms window ends.
	c := f.open("own", "?topics=containers")
	c.hello()
	for range 10 {
		f.bus.Publish(liveDocker("busy", "health_status"))
	}
	f.bus.Publish(liveDocker("marker", "start"))
	first := invalidation(t, c.next())
	if first.ResourceID != "busy" {
		t.Fatalf("first %+v", first)
	}
	if m := invalidation(t, c.next()); m.ResourceID != "marker" {
		t.Fatalf("expected the marker right after one busy invalidation, got %+v", m)
	}
	if err := f.clk.BlockUntilWaiters(f.ctx, 1); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(live.DefaultCoalesce)
	for {
		e := c.next()
		if e.event == "invalidate" {
			if inv := invalidation(t, e); inv.ResourceID != "busy" {
				t.Fatalf("merged %+v", inv)
			}
			break
		}
	}

	bad := f.open("own", "?topics=containers,secrets")
	if bad.status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown topic: %d", bad.status)
	}
	if bad := f.open("own", "?volume=../x"); bad.status != http.StatusUnprocessableEntity {
		t.Fatalf("bad volume filter: %d", bad.status)
	}
}

// TestLiveEventFiltering (#17, #23): every record is filtered and shaped
// per subscriber. A metrics-only user gets only status and metric
// invalidations of its container; file names reach only holders of the
// scope's files-read capability; jobs need job.read; nobody learns about
// resources they may not see.
func TestLiveEventFiltering(t *testing.T) {
	ctx := context.Background()
	pol := livePolicy()
	checker := func(user string) authz.Checker {
		return authz.For(ctx, pol, authz.Principal{Kind: authz.KindUser, UserID: user})
	}
	all, err := parseLiveFilter(&streamLiveEventsInput{})
	if err != nil {
		t.Fatal(err)
	}
	job := domain.Job{ID: "j1", Kind: "container.restart", EnvironmentID: "e1", State: domain.JobRunning,
		Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}}}
	recs := map[string]events.Event{
		"web":          liveDocker("web", "die"),
		"db":           liveDocker("db", "die"),
		"files-s1":     {Type: events.FilesInvalidated, ResourceType: events.ResourceFileScope, ResourceID: "stack:s1", EnvironmentID: "e1", Paths: []string{"secret.env"}, Attributes: map[string]string{"scopeKind": "stack", "scopeId": "s1"}},
		"files-s2":     {Type: events.FilesInvalidated, ResourceType: events.ResourceFileScope, ResourceID: "stack:s2", EnvironmentID: "e1", Paths: []string{"other.txt"}, Attributes: map[string]string{"scopeKind": "stack", "scopeId": "s2"}},
		"files-gap":    {Type: events.FilesInvalidated, ResourceType: events.ResourceFileScope, ResourceID: "*", EnvironmentID: "e1", Overflow: true},
		"metrics":      {Type: events.MetricsSampled, EnvironmentID: "e1", Members: []string{"web", "db"}, Attributes: map[string]string{"host": "true"}},
		"job":          live.JobEvent(job),
		"policy":       {Type: events.ResourceChanged, ResourceType: "backup_policy", ResourceID: "p1", Attributes: map[string]string{"op": "update"}},
		"stack-s1":     {Type: events.StackUpdated, ResourceType: events.ResourceStack, ResourceID: "s1", EnvironmentID: "e1", Revision: 4},
		"online":       {Type: events.EnvironmentOnline, ResourceType: events.ResourceEnvironment, ResourceID: "e1", EnvironmentID: "e1"},
		"resync":       {Type: events.EnvironmentResync, ResourceType: events.ResourceEnvironment, ResourceID: "e1", EnvironmentID: "e1"},
		"group":        {Type: events.ResourceChanged, ResourceType: "group", ResourceID: "g-files"},
		"enrollment":   {Type: events.EnrollmentCreated, ResourceType: events.ResourceEnrollment, ResourceID: "en1"},
		"other-volume": {Type: events.DockerEvent, ResourceType: events.ResourceVolume, ResourceID: "pgdata", EnvironmentID: "e1", Attributes: map[string]string{"action": "create"}},
	}
	visible := func(user string) []string {
		c := checker(user)
		var out []string
		for name, e := range recs {
			r := live.Record{Seq: 1, Event: e}
			if e.Type == events.EnvironmentResync {
				r.Reset = live.ResetGap
			}
			evName, data, ok := liveEvent(c, all, r, "x.1")
			if !ok {
				continue
			}
			b, _ := json.Marshal(data)
			if name == "files-s1" && user != "own" && user != "filer" {
				t.Errorf("%s received file names: %s", user, b)
			}
			if strings.Contains(string(b), "secret/image") {
				t.Errorf("%s received attributes: %s", user, b)
			}
			out = append(out, name+"="+evName)
		}
		slices.Sort(out)
		return out
	}
	for user, want := range map[string][]string{
		"own": {"db=invalidate", "enrollment=invalidate", "files-gap=files.changed", "files-s1=files.changed", "files-s2=files.changed",
			"group=invalidate", "job=job", "metrics=invalidate", "online=agent", "other-volume=invalidate", "policy=invalidate",
			"resync=reset", "stack-s1=invalidate", "web=invalidate"},
		// Metrics-only: its container's status and metrics, the
		// environment it sees minimally; no files, jobs or policies.
		"metrics": {"files-gap=files.changed", "metrics=invalidate", "online=agent", "resync=reset", "web=invalidate"},
		"filer":   {"files-gap=files.changed", "files-s1=files.changed", "online=agent", "resync=reset", "stack-s1=invalidate"},
		"nobody":  nil,
	} {
		if got := visible(user); !slices.Equal(got, want) {
			t.Errorf("%s sees %v, want %v", user, got, want)
		}
	}
	// Narrowing: environment, topics and open file views.
	narrow, err := parseLiveFilter(&streamLiveEventsInput{Topics: "files,jobs", EnvironmentID: "e2", StackID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := liveEvent(checker("own"), narrow, live.Record{Event: recs["files-s1"]}, "x.1"); ok {
		t.Error("environment filter ignored")
	}
	narrow.env = ""
	for name, want := range map[string]bool{"files-s1": true, "files-s2": false, "files-gap": true, "job": true, "web": false} {
		if _, _, ok := liveEvent(checker("own"), narrow, live.Record{Event: recs[name]}, "x.1"); ok != want {
			t.Errorf("narrowed %s: %v", name, ok)
		}
	}
}

// TestLiveStreamRevocation (#17, #23): a permission change ends the
// stream with permissions.changed and close (the client clears its cache);
// an ended session closes with session_expired; a volume filter keeps the
// volume watched only while the stream is open and only with
// volume.files.read.
func TestLiveStreamRevocation(t *testing.T) {
	f := newLiveFixture(t, live.Options{})
	s := f.open("filer", "?volume=e1/pgdata")
	s.hello()
	if f.watch.held("e1/pgdata") != 0 {
		t.Fatal("a volume was watched without volume.files.read")
	}
	f.revoke("filer", authz.ErrPermissionsChanged)
	got := s.until("close")
	if len(got) != 2 || got[0].event != "permissions.changed" || got[1].data != `{"reason":"permissions_changed"}` {
		t.Fatalf("revocation %+v", got)
	}
	o := f.open("own", "?volume=e1/pgdata")
	o.hello()
	if f.watch.held("e1/pgdata") != 1 {
		t.Fatal("the open volume view is not watched")
	}
	f.revoke("own", authz.ErrSessionEnded)
	got = o.until("close")
	if len(got) != 1 || got[0].data != `{"reason":"session_expired"}` {
		t.Fatalf("session end %+v", got)
	}
	for f.watch.held("e1/pgdata") != 0 {
		// The stream handler releases the hold as it returns.
		if _, ok := <-o.events; !ok {
			break
		}
	}
	// Eight streams per principal.
	for range 8 {
		f.open("nobody", "").hello()
	}
	if extra := f.open("nobody", ""); extra.status != http.StatusTooManyRequests {
		t.Fatalf("ninth stream: %d", extra.status)
	}
}

// TestTwoSessionsConverge (#23): two sessions see the same records in the
// same order; one that disconnects and resumes from its cursor ends up
// with exactly the same set; a session that falls behind gets a reset
// (refetch) instead of silently missing records.
func TestTwoSessionsConverge(t *testing.T) {
	f := newLiveFixture(t, live.Options{})
	a := f.open("own", "")
	a.hello()
	b := f.open("own", "")
	b.hello()
	var seenA, seenB []string
	for i := range 20 {
		f.bus.Publish(liveDocker("c"+strconv.Itoa(i), "start"))
		if i == 9 {
			for range 10 {
				seenB = append(seenB, b.next().id)
			}
			b.cancel()
		}
	}
	for range 20 {
		seenA = append(seenA, a.next().id)
	}
	b2 := f.open("own", "", "Last-Event-ID", seenB[len(seenB)-1])
	if h := b2.hello(); !h.Resumed {
		t.Fatalf("resume %+v", h)
	}
	for len(seenB) < 20 {
		seenB = append(seenB, b2.next().id)
	}
	if !slices.Equal(seenA, seenB) {
		t.Fatalf("sessions diverged:\n%v\n%v", seenA, seenB)
	}
}

// TestLiveHighEventVolume (#23): 20 sessions (owner and metrics-only
// users) under 10 000 Docker events: every session receives the records it
// may see in order, and for everything it lost (bus or queue overflow) a
// reset, never a silent gap; the run stays within the budget and the
// replay log bounded.
func TestLiveHighEventVolume(t *testing.T) {
	const sessions, total = 20, 10_000
	f := newLiveFixture(t, live.Options{MaxPerPrincipal: sessions})
	streams := make([]*liveStream, sessions)
	owner := func(i int) bool { return i%2 == 0 }
	for i := range sessions {
		user := "metrics"
		if owner(i) {
			user = "own"
		}
		streams[i] = f.open(user, "?topics=containers,environments")
		streams[i].hello()
	}
	start := time.Now()
	for i := range total {
		e := liveDocker("c"+strconv.Itoa(i), "start") // distinct: nothing to coalesce
		if i%10 == 0 {
			e = liveDocker("web", "start") // what the metrics-only users see
		}
		f.bus.Publish(e)
	}
	results := make([]string, sessions)
	var wg sync.WaitGroup
	for i, s := range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var last uint64
			resets, received := 0, 0
			for {
				var e sseEvent
				select {
				case ev, ok := <-s.events:
					if !ok {
						results[i] = "ended"
						return
					}
					e = ev
				case <-time.After(30 * time.Second):
					results[i] = fmt.Sprintf("stalled after %d records", received)
					return
				}
				switch e.event {
				case "agent": // the final marker
					results[i] = fmt.Sprintf("ok received=%d resets=%d", received, resets)
					return
				case "reset":
					resets++
					var r LiveReset
					_ = json.Unmarshal([]byte(e.data), &r)
					_, seq, _ := strings.Cut(r.Cursor, ".")
					last, _ = strconv.ParseUint(seq, 10, 64)
					continue
				case "invalidate":
				default:
					continue
				}
				_, seqText, _ := strings.Cut(e.id, ".")
				seq, _ := strconv.ParseUint(seqText, 10, 64)
				// Owners see every record: consecutive ids unless a reset
				// said what was lost.
				if seq <= last || (owner(i) && last != 0 && seq != last+1) {
					results[i] = fmt.Sprintf("silent gap: id %d after %d", seq, last)
					return
				}
				last = seq
				received++
			}
		}()
	}
	// The final marker, repeated (a later window each time) until every
	// session saw it: a session that overflowed at the very end still
	// gets one after its reset.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	online := events.Event{Type: events.EnvironmentOnline, ResourceType: events.ResourceEnvironment, ResourceID: "e1", EnvironmentID: "e1"}
	for finished := false; !finished; {
		f.clk.Advance(time.Second)
		f.bus.Publish(online)
		select {
		case <-done:
			finished = true
		case <-time.After(100 * time.Millisecond):
		}
	}
	elapsed := time.Since(start)
	for i, r := range results {
		if !strings.HasPrefix(r, "ok") {
			t.Errorf("session %d: %s", i, r)
		}
	}
	t.Logf("%d sessions, %d events: %v (owner %s, metrics-only %s)", sessions, total, elapsed, results[0], results[1])
	if elapsed > 60*time.Second {
		t.Errorf("high event volume took %v (budget 60 s)", elapsed)
	}
	if replay, _ := f.hub.Subscribe("probe", f.hub.Epoch()+".0"); replay.Reset == "" && len(replay.Replay) > live.DefaultReplaySize {
		t.Errorf("replay log unbounded: %d", len(replay.Replay))
	}
}

// TestLiveSustainedVolumeIsLossless (#23): at a sustained high rate that
// stays within the bounded queues (waves of 400 events), 20 sessions
// receive every record they may see, in order, without a single reset;
// the throughput is logged against the budget.
func TestLiveSustainedVolumeIsLossless(t *testing.T) {
	const sessions, waves, perWave = 20, 25, 400
	f := newLiveFixture(t, live.Options{MaxPerPrincipal: sessions})
	type tally struct {
		received, resets int
		marks            chan struct{}
	}
	tallies := make([]*tally, sessions)
	var wg sync.WaitGroup
	for i := range sessions {
		user := "metrics"
		if i%2 == 0 {
			user = "own"
		}
		s := f.open(user, "?topics=containers")
		s.hello()
		tl := &tally{marks: make(chan struct{}, waves)}
		tallies[i] = tl
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range s.events {
				switch e.event {
				case "reset":
					tl.resets++
				case "invalidate":
					tl.received++
					if strings.Contains(e.data, `"resourceId":"web"`) {
						tl.marks <- struct{}{}
					}
				}
			}
		}()
	}
	start := time.Now()
	for w := range waves {
		for i := range perWave {
			f.bus.Publish(liveDocker("c"+strconv.Itoa(w*perWave+i), "start"))
		}
		f.clk.Advance(time.Second) // the marker opens a new coalescing window
		f.bus.Publish(liveDocker("web", "start"))
		for i, tl := range tallies {
			select {
			case <-tl.marks:
			case <-time.After(10 * time.Second):
				t.Fatalf("session %d missed wave %d", i, w)
			}
		}
	}
	elapsed := time.Since(start)
	f.srv.CloseClientConnections()
	wg.Wait()
	for i, tl := range tallies {
		want := waves * (perWave + 1)
		if i%2 == 1 {
			want = waves // metrics-only: container web
		}
		if tl.resets != 0 || tl.received != want {
			t.Errorf("session %d: received %d (want %d), resets %d", i, tl.received, want, tl.resets)
		}
	}
	t.Logf("%d sessions, %d events: %v (%.0f deliveries/s)", sessions, waves*(perWave+1), elapsed,
		float64(sessions/2*waves*(perWave+1)+sessions/2*waves)/elapsed.Seconds())
	if elapsed > 30*time.Second {
		t.Errorf("sustained volume took %v (budget 30 s)", elapsed)
	}
}
