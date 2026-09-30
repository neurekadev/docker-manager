package live

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func newHub(t *testing.T, o Options) (*Hub, *clock.Fake) {
	t.Helper()
	clk := testutil.FakeClock()
	o.Clock, o.Logger = clk, testutil.Logger(t)
	if o.Bus == nil {
		o.Bus = events.New(clk)
	}
	return New(o), clk
}

func container(name, action string) events.Event {
	return events.Event{Type: events.DockerEvent, ResourceType: events.ResourceContainer, ResourceID: name, EnvironmentID: "e1",
		Attributes: map[string]string{"action": action}}
}

func drain(s *Subscriber) []Record {
	var out []Record
	for {
		select {
		case r := <-s.C():
			out = append(out, r)
		default:
			return out
		}
	}
}

// TestSnapshotCursorAndReplay (#23): a fresh subscription gets the cursor
// to fetch snapshots at; reconnecting with a retained cursor replays
// exactly what came after it; a cursor from another manager process, from
// the future or older than the replay log gets a reset instead.
func TestSnapshotCursorAndReplay(t *testing.T) {
	h, clk := newHub(t, Options{ReplaySize: 5})
	fresh, err := h.Subscribe("user:a", "")
	if err != nil || fresh.Resumed || fresh.Reset != "" || fresh.Cursor != h.Epoch()+".0" {
		t.Fatalf("fresh %+v %v", fresh, err)
	}
	defer fresh.Sub.Close()
	for i := range 3 {
		h.Offer(container("c"+strconv.Itoa(i), "start"))
	}
	got := drain(fresh.Sub)
	if len(got) != 3 || got[0].Seq != 1 || got[2].Seq != 3 || h.Cursor(got[1]) != h.Epoch()+".2" {
		t.Fatalf("live records %+v", got)
	}
	resumed, _ := h.Subscribe("user:a", h.Cursor(got[0]))
	defer resumed.Sub.Close()
	if !resumed.Resumed || resumed.Reset != "" || len(resumed.Replay) != 2 || resumed.Replay[0].Seq != 2 || resumed.Cursor != h.Epoch()+".3" {
		t.Fatalf("resumed %+v", resumed)
	}
	upToDate, _ := h.Subscribe("user:a", h.Epoch()+".3")
	defer upToDate.Sub.Close()
	if !upToDate.Resumed || len(upToDate.Replay) != 0 {
		t.Fatalf("up to date %+v", upToDate)
	}
	for _, tc := range []struct{ cursor, reset string }{
		{"deadbeef.1", ResetServerRestart},
		{"garbage", ResetServerRestart},
		{h.Epoch() + ".99", ResetCursorExpired},
	} {
		s, _ := h.Subscribe("user:b", tc.cursor)
		if s.Reset != tc.reset || s.Resumed || len(s.Replay) != 0 {
			t.Errorf("cursor %s: %+v", tc.cursor, s)
		}
		s.Sub.Close()
	}
	// The log keeps the newest ReplaySize records: older cursors expire.
	for i := range 6 {
		h.Offer(container("d"+strconv.Itoa(i), "start"))
	}
	expired, _ := h.Subscribe("user:b", h.Epoch()+".1")
	defer expired.Sub.Close()
	if expired.Reset != ResetCursorExpired {
		t.Fatalf("expired %+v", expired)
	}
	// And the records of the last 15 minutes only (beyond the newest one).
	clk.Advance(DefaultReplayAge + time.Minute)
	h.Offer(container("late", "start"))
	old, _ := h.Subscribe("user:b", h.Epoch()+".8")
	defer old.Sub.Close()
	if old.Reset != ResetCursorExpired {
		t.Fatalf("aged-out cursor %+v", old)
	}
}

// TestCoalescingAndDedupe: repeated events of one resource within the
// window are one immediate record plus one merged record when the window
// ends (file paths united); other resources are independent; metrics
// coalesce per environment over 1 s.
func TestCoalescingAndDedupe(t *testing.T) {
	h, clk := newHub(t, Options{})
	s, _ := h.Subscribe("user:a", "")
	defer s.Sub.Close()
	for range 10 {
		h.Offer(container("web", "health_status"))
	}
	h.Offer(container("db", "start"))
	files := func(paths ...string) events.Event {
		return events.Event{Type: events.FilesInvalidated, ResourceType: events.ResourceFileScope, ResourceID: "stack:s1", EnvironmentID: "e1",
			Paths: paths, Attributes: map[string]string{"scopeKind": "stack", "scopeId": "s1"}}
	}
	h.Offer(files("a.txt"))
	h.Offer(files("b.txt"))
	h.Offer(files("a.txt", "c.txt"))
	got := drain(s.Sub)
	if len(got) != 3 || got[0].Event.ResourceID != "web" || got[1].Event.ResourceID != "db" || !slices.Equal(got[2].Event.Paths, []string{"a.txt"}) {
		t.Fatalf("immediate records %+v", got)
	}
	if d := h.Flush(); d != DefaultCoalesce {
		t.Fatalf("next flush in %v", d)
	}
	clk.Advance(DefaultCoalesce)
	h.Flush()
	got = drain(s.Sub)
	if len(got) != 2 {
		t.Fatalf("merged records %+v", got)
	}
	for _, r := range got {
		switch r.Event.ResourceID {
		case "web":
		case "stack:s1":
			if !slices.Equal(r.Event.Paths, []string{"a.txt", "b.txt", "c.txt"}) {
				t.Errorf("merged paths %v", r.Event.Paths)
			}
		default:
			t.Errorf("unexpected %+v", r)
		}
	}
	// Past the window the next event is immediate again.
	clk.Advance(DefaultCoalesce)
	h.Flush()
	h.Offer(container("web", "die"))
	if got := drain(s.Sub); len(got) != 1 || got[0].Event.Attributes["action"] != "die" {
		t.Fatalf("after the window %+v", got)
	}
	// Too many merged paths become an overflow of the scope.
	clk.Advance(time.Minute)
	h.Flush()
	h.Offer(files("first"))
	for i := range protocol.MaxPaths + 1 {
		h.Offer(files("p" + strconv.Itoa(i)))
	}
	clk.Advance(DefaultCoalesce)
	h.Flush()
	got = drain(s.Sub)
	if len(got) != 2 || !got[1].Event.Overflow || len(got[1].Event.Paths) != 0 {
		t.Fatalf("overflowing merge %+v", got)
	}
	// Metrics: at most one record per environment every second.
	if DefaultMetricsCoalesce != time.Second {
		t.Fatalf("metrics coalesce %v", DefaultMetricsCoalesce)
	}
	metric := func(host bool, members ...string) events.Event {
		return events.Event{Type: events.MetricsSampled, EnvironmentID: "e1", Members: members, Attributes: map[string]string{"host": strconv.FormatBool(host)}}
	}
	h.Offer(metric(true, "web"))
	h.Offer(metric(false, "db"))
	h.Offer(metric(false, "cache"))
	clk.Advance(DefaultCoalesce)
	h.Flush()
	if got := drain(s.Sub); len(got) != 1 {
		t.Fatalf("metrics within 1 s %+v", got)
	}
	clk.Advance(DefaultMetricsCoalesce)
	h.Flush()
	got = drain(s.Sub)
	if len(got) != 1 || !slices.Equal(got[0].Event.Members, []string{"cache", "db"}) {
		t.Fatalf("merged metrics %+v", got)
	}
	// Live metrics too, merging the host flag.
	clk.Advance(time.Minute)
	h.Flush()
	live := func(host bool, members ...string) events.Event {
		e := metric(host, members...)
		e.Type = events.MetricsLive
		return e
	}
	h.Offer(live(false, "web"))
	h.Offer(live(true, "db"))
	h.Offer(live(false, "cache"))
	if got := drain(s.Sub); len(got) != 1 {
		t.Fatalf("live metrics within 1 s %+v", got)
	}
	clk.Advance(DefaultMetricsCoalesce)
	h.Flush()
	got = drain(s.Sub)
	if len(got) != 1 || got[0].Event.Type != events.MetricsLive || got[0].Event.Attributes["host"] != "true" ||
		!slices.Equal(got[0].Event.Members, []string{"cache", "db"}) {
		t.Fatalf("merged live metrics %+v", got)
	}
}

// TestLiveMetricsAreNotReplayed: live metric records reach subscribers
// but stay out of the replay log, so their volume never shortens the
// replay window, and a cursor pointing at one still resumes.
func TestLiveMetricsAreNotReplayed(t *testing.T) {
	h, clk := newHub(t, Options{ReplaySize: 3})
	s, _ := h.Subscribe("user:a", "")
	defer s.Sub.Close()
	h.Offer(container("web", "start"))
	for i := range 10 {
		clk.Advance(DefaultMetricsCoalesce)
		h.Offer(events.Event{Type: events.MetricsLive, EnvironmentID: "e" + strconv.Itoa(i), Attributes: map[string]string{"host": "true"}})
	}
	got := drain(s.Sub)
	if len(got) != 11 {
		t.Fatalf("records %d", len(got))
	}
	resumed, _ := h.Subscribe("user:a", h.Cursor(got[0]))
	defer resumed.Sub.Close()
	if !resumed.Resumed || len(resumed.Replay) != 0 {
		t.Fatalf("resumed after the container event %+v", resumed)
	}
	atLive, _ := h.Subscribe("user:a", h.Cursor(got[5]))
	defer atLive.Sub.Close()
	if !atLive.Resumed || len(atLive.Replay) != 0 {
		t.Fatalf("cursor at a live record %+v", atLive)
	}
}

// TestSlowSubscriberOverflowReset: a subscriber that does not keep up
// loses its queue and resumes from a fresh cursor (reset overflow); the
// producer never blocks and other subscribers are unaffected.
func TestSlowSubscriberOverflowReset(t *testing.T) {
	h, _ := newHub(t, Options{Queue: 4})
	slow, _ := h.Subscribe("user:slow", "")
	defer slow.Sub.Close()
	fast, _ := h.Subscribe("user:fast", "")
	defer fast.Sub.Close()
	for i := range 10 {
		h.Offer(container("c"+strconv.Itoa(i), "start"))
		drain(fast.Sub)
	}
	cursor, lost := slow.Sub.Overflowed()
	if !lost || cursor != h.Epoch()+".10" || len(drain(slow.Sub)) != 0 {
		t.Fatalf("overflow %v %s", lost, cursor)
	}
	if _, again := slow.Sub.Overflowed(); again {
		t.Fatal("overflow reported twice")
	}
	if _, lost := fast.Sub.Overflowed(); lost {
		t.Fatal("a fast subscriber overflowed")
	}
}

// TestLossesBecomeResets: the Hub falling behind the bus resets every
// stream; an environment resync (agent reconnect, event gap) is a reset
// scoped to that environment.
func TestLossesBecomeResets(t *testing.T) {
	h, clk := newHub(t, Options{})
	bus := events.New(clk)
	sub := bus.Subscribe(2, nil)
	for i := range 5 {
		bus.Publish(container("c"+strconv.Itoa(i), "start"))
	}
	bus.Publish(events.Event{Type: events.EnvironmentResync, ResourceType: events.ResourceEnvironment, ResourceID: "e1", EnvironmentID: "e1",
		Attributes: map[string]string{"reason": "reconnect"}})
	s, _ := h.Subscribe("user:a", "")
	defer s.Sub.Close()
	ctx, cancel := context.WithCancel(testutil.Context(t))
	done := make(chan struct{})
	go func() { defer close(done); h.consume(ctx, sub) }()
	var got []Record
	for len(got) < 3 {
		got = append(got, <-s.Sub.C())
	}
	cancel()
	<-done
	if got[0].Reset != ResetGap || got[0].Event.Type != "" {
		t.Fatalf("bus loss %+v", got[0])
	}
	if got[1].Event.ResourceID != "c0" {
		t.Fatalf("after the loss %+v", got[1])
	}
	// The resync was dropped by the tiny buffer here; offer it directly.
	h.Offer(events.Event{Type: events.EnvironmentResync, ResourceType: events.ResourceEnvironment, ResourceID: "e1", EnvironmentID: "e1"})
	r := <-s.Sub.C()
	if r.Reset != ResetGap || r.Event.EnvironmentID != "e1" {
		t.Fatalf("environment reset %+v", r)
	}
}

// TestStreamsPerPrincipal: at most MaxStreamsPerPrincipal open streams per
// principal; closing one frees its slot.
func TestStreamsPerPrincipal(t *testing.T) {
	h, _ := newHub(t, Options{MaxPerPrincipal: 2})
	a, _ := h.Subscribe("user:a", "")
	b, _ := h.Subscribe("user:a", "")
	if _, err := h.Subscribe("user:a", ""); !errors.Is(err, ErrTooManyStreams) {
		t.Fatalf("third stream: %v", err)
	}
	other, err := h.Subscribe("user:b", "")
	if err != nil {
		t.Fatal(err)
	}
	a.Sub.Close()
	a.Sub.Close() // idempotent
	c, err := h.Subscribe("user:a", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Subscriber{b.Sub, other.Sub, c.Sub} {
		s.Close()
	}
	if h.Subscribers() != 0 {
		t.Fatal("subscribers leaked")
	}
}

// TestJobSourceBatches: changes are batched per window and each job is
// read once; deleted jobs are skipped.
func TestJobSourceBatches(t *testing.T) {
	clk := testutil.FakeClock()
	bus := events.New(clk)
	sub := bus.Subscribe(16, nil)
	reads := map[string]int{}
	get := func(_ context.Context, id string) (domain.Job, error) {
		reads[id]++
		if id == "gone" {
			return domain.Job{}, domain.ErrJobNotFound
		}
		return domain.Job{ID: id, Kind: "stack.deploy", EnvironmentID: "e1", State: domain.JobRunning, LastEventSeq: 7,
			Progress: domain.JobProgress{Percent: 40}}, nil
	}
	js := NewJobSource(get, bus, clk, testutil.Logger(t))
	ctx, cancel := context.WithCancel(testutil.Context(t))
	done := make(chan struct{})
	go func() { defer close(done); js.Run(ctx) }()
	js.Changed([]string{"j1", "gone"})
	js.Changed([]string{"j1"})
	if err := clk.BlockUntilWaiters(ctx, 1); err != nil {
		t.Fatal(err)
	}
	js.Changed([]string{"j1"})
	clk.Advance(DefaultCoalesce)
	e := <-sub.C()
	cancel()
	<-done
	if e.Type != events.JobUpdated || e.ResourceID != "j1" || e.Revision != 7 || e.Attributes["percent"] != "40" ||
		e.Attributes["state"] != "running" || e.Job == nil || e.EnvironmentID != "e1" {
		t.Fatalf("job event %+v", e)
	}
	if reads["j1"] != 1 || reads["gone"] != 1 {
		t.Fatalf("reads %v", reads)
	}
	select {
	case extra := <-sub.C():
		t.Fatalf("extra event %+v", extra)
	default:
	}
}

// TestClassify: every bus event type maps to a topic (or is a reset), and
// actions follow the event.
func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		e             events.Event
		topic, action string
	}{
		{container("web", "create"), TopicContainers, ActionCreated},
		{container("web", "destroy"), TopicContainers, ActionDeleted},
		{container("web", "die"), TopicContainers, ActionUpdated},
		{events.Event{Type: events.StackRemoved}, TopicStacks, ActionDeleted},
		{events.Event{Type: events.JobUpdated}, TopicJobs, ActionUpdated},
		{events.Event{Type: events.ResourceChanged, ResourceType: "backup_policy", Attributes: map[string]string{"op": "delete"}}, TopicPolicies, ActionDeleted},
		{events.Event{Type: events.ResourceChanged, ResourceType: "group", Attributes: map[string]string{"op": "create"}}, TopicPermissions, ActionCreated},
		{events.Event{Type: events.ResourceChanged, ResourceType: "registry"}, TopicRegistries, ActionUpdated},
		{events.Event{Type: events.FilesInvalidated}, TopicFiles, ActionUpdated},
		{events.Event{Type: events.MetricsSampled}, TopicMetrics, ActionUpdated},
		{events.Event{Type: events.MetricsLive}, TopicMetrics, ActionUpdated},
		{events.Event{Type: events.InventoryUpdated}, TopicEnvironments, ActionUpdated},
		{events.Event{Type: events.EnrollmentCreated}, TopicAgents, ActionCreated},
		{events.Event{Type: events.ManagerMoveUpdated, ResourceType: events.ResourceManagerMove}, TopicManager, ActionUpdated},
		{events.Event{Type: events.ManagerMoveLockChanged, ResourceType: events.ResourceManagerMoveLock}, TopicManager, ActionUpdated},
		{events.Event{Type: events.ResourceChanged, ResourceType: "manager_move"}, TopicManager, ActionUpdated},
	} {
		topic, _ := Classify(tc.e)
		if topic != tc.topic || ActionOf(tc.e) != tc.action {
			t.Errorf("%s %s: %s %s", tc.e.Type, tc.e.ResourceType, topic, ActionOf(tc.e))
		}
	}
}
