package jobs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

type captureSender struct {
	mu     sync.Mutex
	frames []*protocol.Frame
}

func (c *captureSender) Send(_ context.Context, f *protocol.Frame) error {
	b, err := protocol.Encode(f) // everything sent must be a valid frame
	if err != nil {
		return err
	}
	g, err := protocol.Decode(b)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frames = append(c.frames, g)
	return nil
}

func (c *captureSender) of(typ protocol.Type) []*protocol.Frame {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*protocol.Frame
	for _, f := range c.frames {
		if f.Type == typ {
			out = append(out, f)
		}
	}
	return out
}

func acks(t *testing.T, c *captureSender) []protocol.AckPayload {
	t.Helper()
	var out []protocol.AckPayload
	for _, f := range c.of(protocol.TypeAck) {
		p, err := protocol.DecodePayload[protocol.AckPayload](f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// effects records simulated side effects.
type effects struct {
	mu  sync.Mutex
	log []string
}

func (e *effects) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, s)
}

func (e *effects) list() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.log)
}

// simExecutor implements every step of kind by recording "<step>"; block,
// when non-nil, gates the named step.
func simExecutor(kind domain.JobKind, fx *effects, blockStep string, block chan struct{}) jobexec.Executor {
	spec, _ := jobspec.Lookup(kind)
	steps := map[string]jobexec.StepFunc{}
	for _, s := range spec.Steps {
		name := s.Name
		steps[name] = func(ctx context.Context, sc *jobexec.StepContext) error {
			if name == "stop_containers" {
				if err := sc.AddCompensation(ctx, jobspec.CompStartContainers, []string{"web"}); err != nil {
					return err
				}
			}
			if name == "start_containers" {
				if err := sc.ReleaseCompensation(ctx, jobspec.CompStartContainers); err != nil {
					return err
				}
			}
			if name == blockStep && block != nil {
				select {
				case <-block:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			fx.add(name)
			return nil
		}
	}
	comps := map[string]jobexec.CompensationFunc{}
	for _, c := range spec.Compensations {
		name := c.Name
		comps[name] = func(context.Context, json.RawMessage) error { fx.add("compensate:" + name); return nil }
	}
	return jobexec.Executor{Kind: kind, Steps: steps, Compensations: comps}
}

func newRunner(t *testing.T, ctx context.Context, dir string, execs ...jobexec.Executor) (*Runner, *captureSender) {
	t.Helper()
	s := &captureSender{}
	r, err := New(ctx, Options{StateDir: dir, Clock: testutil.FakeClock(), Logger: testutil.Logger(t), Sender: s, Executors: execs})
	if err != nil {
		t.Fatal(err)
	}
	return r, s
}

func command(t *testing.T, jobID string, attempt uint32, token uint64, kind domain.JobKind, completed ...string) *protocol.Frame {
	t.Helper()
	f, err := protocol.NewCommandFrame(protocol.JobRef{JobID: jobID, Attempt: attempt, FencingToken: token},
		testutil.Epoch.Add(time.Hour), protocol.CommandPayload{Kind: string(kind), Input: json.RawMessage(`{}`), CompletedSteps: completed})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestCommandRunsJournalsAndReports(t *testing.T) {
	ctx := testutil.Context(t)
	dir := t.TempDir()
	fx := &effects{}
	r, s := newRunner(t, ctx, dir, simExecutor(jobspec.BackupRun, fx, "", nil))
	if err := r.HandleFrame(ctx, command(t, "job-1", 1, 5, jobspec.BackupRun)); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	if a := acks(t, s); len(a) != 1 || !a[0].Accepted {
		t.Fatalf("acks %+v", a)
	}
	if !slices.Equal(fx.list(), []string{"prepare", "stop_containers", "snapshot", "start_containers", "record"}) {
		t.Fatalf("effects %v", fx.list())
	}
	res := s.of(protocol.TypeResult)
	if len(res) != 1 || res[0].CorrelationID != protocol.CommandFrameID("job-1", 1) {
		t.Fatalf("results %+v", res)
	}
	if len(s.of(protocol.TypeProgress)) == 0 {
		t.Fatal("no progress frames")
	}
	// The entry stays journaled (and in the report) until the manager acks.
	rep := r.Report()
	if rep.HighWater != 5 || len(rep.Jobs) != 1 || rep.Jobs[0].Status != protocol.ReportFinished || rep.Jobs[0].Result.Outcome != "succeeded" {
		t.Fatalf("report %+v", rep)
	}
	ack, _ := protocol.NewFrame(protocol.TypeAck, "m-1", res[0].ID, protocol.JobRef{}, protocol.AckPayload{Accepted: true, Forget: []string{"job-1"}})
	if err := r.HandleFrame(ctx, ack); err != nil {
		t.Fatal(err)
	}
	if rep := r.Report(); len(rep.Jobs) != 0 || rep.HighWater != 5 {
		t.Fatalf("report after forget %+v", rep)
	}
	// The high-water mark survives a restart.
	r2, _ := newRunner(t, ctx, dir, simExecutor(jobspec.BackupRun, fx, "", nil))
	if r2.Journal().HighWater() != 5 {
		t.Fatalf("high water after restart = %d", r2.Journal().HighWater())
	}
}

// TestResumedAttemptContinuesFromEarlierOutput (#26): a resumed attempt
// skips the completed steps and its remaining steps read the output those
// steps recorded (the command carries it); a first attempt starts empty.
func TestResumedAttemptContinuesFromEarlierOutput(t *testing.T) {
	ctx := testutil.Context(t)
	seen := map[uint32]string{}
	var mu sync.Mutex
	exec := jobexec.Executor{Kind: jobspec.PruneRun, Steps: map[string]jobexec.StepFunc{
		"collect_candidates": func(ctx context.Context, sc *jobexec.StepContext) error {
			return sc.SetOutput(ctx, map[string]any{"items": []string{"collected-" + string(rune('0'+sc.Attempt))}})
		},
		"delete_candidates": func(_ context.Context, sc *jobexec.StepContext) error {
			mu.Lock()
			defer mu.Unlock()
			seen[sc.Attempt] = string(sc.Output())
			return nil
		},
	}}
	r, s := newRunner(t, ctx, t.TempDir(), exec)
	f, err := protocol.NewCommandFrame(protocol.JobRef{JobID: "job-1", Attempt: 2, FencingToken: 7}, testutil.Epoch.Add(time.Hour),
		protocol.CommandPayload{Kind: string(jobspec.PruneRun), Input: json.RawMessage(`{}`), CompletedSteps: []string{"collect_candidates"},
			Output: json.RawMessage(`{"items":["collected-1"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.HandleFrame(ctx, f); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	if err := r.HandleFrame(ctx, command(t, "job-2", 1, 8, jobspec.PruneRun)); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	mu.Lock()
	defer mu.Unlock()
	if seen[2] != `{"items":["collected-1"]}` {
		t.Fatalf("the resumed attempt saw %q, want the earlier attempt's output", seen[2])
	}
	if seen[1] != `{"items":["collected-1"]}` { // job-2 attempt 1 collected its own
		t.Fatalf("a first attempt saw %q", seen[1])
	}
	if res := s.of(protocol.TypeResult); len(res) != 2 {
		t.Fatalf("results %d", len(res))
	}
}

// TestFencingRejectsStaleAndReplayedCommands: a command replayed after a
// reconnect (older token, superseded attempt) is rejected by its token, also
// after an agent restart; an exact duplicate is acknowledged but not re-run.
func TestFencingRejectsStaleAndReplayedCommands(t *testing.T) {
	ctx := testutil.Context(t)
	dir := t.TempDir()
	fx := &effects{}
	exec := simExecutor(jobspec.StackStart, fx, "", nil)
	r, s := newRunner(t, ctx, dir, exec)

	// Attempt 2 (token 8) supersedes attempt 1 (token 7), which never arrived.
	if err := r.HandleFrame(ctx, command(t, "job-a", 2, 8, jobspec.StackStart)); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	// The delayed attempt-1 frame is replayed: stale.
	if err := r.HandleFrame(ctx, command(t, "job-a", 1, 7, jobspec.StackStart)); err != nil {
		t.Fatal(err)
	}
	// Exact duplicate of attempt 2: acknowledged, not re-run, result resent.
	if err := r.HandleFrame(ctx, command(t, "job-a", 2, 8, jobspec.StackStart)); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	a := acks(t, s)
	if len(a) != 3 || !a[0].Accepted || a[1].Accepted || a[1].Code != protocol.AckStaleToken || a[1].HighWater != 8 ||
		!a[2].Accepted || a[2].Code != protocol.AckDuplicate {
		t.Fatalf("acks %+v", a)
	}
	if got := fx.list(); !slices.Equal(got, []string{"start"}) {
		t.Fatalf("executed %v, want exactly once", got)
	}
	if len(s.of(protocol.TypeResult)) != 2 {
		t.Fatal("duplicate did not resend the result")
	}

	// Another job with an old token (issued before the reconnect) is stale too.
	if err := r.HandleFrame(ctx, command(t, "job-b", 1, 6, jobspec.StackStart)); err != nil {
		t.Fatal(err)
	}
	// After an agent restart the persisted high-water mark still fences.
	r2, s2 := newRunner(t, ctx, dir, exec)
	if err := r2.HandleFrame(ctx, command(t, "job-c", 1, 8, jobspec.StackStart)); err != nil {
		t.Fatal(err)
	}
	if err := r2.HandleFrame(ctx, command(t, "job-c", 1, 9, jobspec.StackStart)); err != nil {
		t.Fatal(err)
	}
	r2.Wait()
	a2 := acks(t, s2)
	if len(a2) != 2 || a2[0].Code != protocol.AckStaleToken || !a2[1].Accepted {
		t.Fatalf("acks after restart %+v", a2)
	}
	if a := acks(t, s); a[len(a)-1].Code != protocol.AckStaleToken {
		t.Fatalf("job-b ack %+v", a[len(a)-1])
	}
}

func TestRejectsUnsupportedBusyAndLateCommands(t *testing.T) {
	ctx := testutil.Context(t)
	fx := &effects{}
	block := make(chan struct{})
	r, s := newRunner(t, ctx, t.TempDir(), simExecutor(jobspec.StackDeploy, fx, "apply", block))

	if err := r.HandleFrame(ctx, command(t, "j1", 1, 1, jobspec.PruneRun)); err != nil {
		t.Fatal(err)
	}
	if err := r.HandleFrame(ctx, command(t, "j2", 1, 2, jobspec.StackDeploy)); err != nil {
		t.Fatal(err)
	}
	// A new attempt of j2 while attempt 1 still runs.
	if err := r.HandleFrame(ctx, command(t, "j2", 2, 3, jobspec.StackDeploy)); err != nil {
		t.Fatal(err)
	}
	late, _ := protocol.NewCommandFrame(protocol.JobRef{JobID: "j3", Attempt: 1, FencingToken: 4}, testutil.Epoch.Add(-time.Second),
		protocol.CommandPayload{Kind: string(jobspec.StackDeploy)})
	if err := r.HandleFrame(ctx, late); err != nil {
		t.Fatal(err)
	}
	bad := command(t, "j4", 1, 5, jobspec.StackDeploy)
	bad.Payload = json.RawMessage(`{"kind":1}`)
	if err := r.HandleFrame(ctx, bad); err != nil {
		t.Fatal(err)
	}
	close(block)
	r.Wait()
	codes := []string{}
	for _, a := range acks(t, s) {
		codes = append(codes, a.Code)
	}
	want := []string{protocol.AckUnsupportedKind, "", protocol.AckBusy, protocol.AckDeadlineExceeded, protocol.AckInvalid}
	if !slices.Equal(codes, want) {
		t.Fatalf("ack codes %v, want %v", codes, want)
	}
	if err := r.HandleFrame(ctx, &protocol.Frame{Type: protocol.TypeHeartbeat, ID: "h"}); err == nil {
		t.Fatal("unexpected frame type accepted")
	}
}

func TestCancelHonoredAtSafePoint(t *testing.T) {
	ctx := testutil.Context(t)
	fx := &effects{}
	block, entered := make(chan struct{}), make(chan struct{})
	exec := simExecutor(jobspec.BackupRun, fx, "stop_containers", block)
	inner := exec.Steps["stop_containers"]
	exec.Steps["stop_containers"] = func(ctx context.Context, sc *jobexec.StepContext) error {
		close(entered)
		return inner(ctx, sc)
	}
	r, s := newRunner(t, ctx, t.TempDir(), exec)
	if err := r.HandleFrame(ctx, command(t, "j", 1, 1, jobspec.BackupRun)); err != nil {
		t.Fatal(err)
	}
	<-entered
	cancel, _ := protocol.NewFrame(protocol.TypeCancel, "c1", protocol.CommandFrameID("j", 1), protocol.JobRef{JobID: "j", Attempt: 1, FencingToken: 1}, nil)
	if err := r.HandleFrame(ctx, cancel); err != nil {
		t.Fatal(err)
	}
	close(block)
	r.Wait()
	if got := fx.list(); !slices.Equal(got, []string{"prepare", "stop_containers", "compensate:start_containers"}) {
		t.Fatalf("effects %v", got)
	}
	res, _ := protocol.DecodePayload[protocol.ResultPayload](s.of(protocol.TypeResult)[0])
	if res.Outcome != "cancelled" {
		t.Fatalf("result %+v", res)
	}
}

// TestRestartRecoversInFlightAttempt simulates an agent crash in the middle
// of a backup snapshot (non-idempotent) after containers were stopped: the
// next start runs the compensation and reports interrupted, never re-running
// the snapshot.
func TestRestartRecoversInFlightAttempt(t *testing.T) {
	ctx := testutil.Context(t)
	dir := t.TempDir()
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	st := jobexec.State{JobID: "j", Attempt: 1, FencingToken: 3, Kind: jobspec.BackupRun,
		Completed: []string{"prepare", "stop_containers"}, CurrentStep: "snapshot", StepInFlight: true,
		Compensations: []jobexec.Compensation{{Name: jobspec.CompStartContainers, Args: json.RawMessage(`["web"]`)}}}
	if err := j.Accept(&st); err != nil {
		t.Fatal(err)
	}
	fx := &effects{}
	r, s := newRunner(t, ctx, dir, simExecutor(jobspec.BackupRun, fx, "", nil))
	if got := fx.list(); !slices.Equal(got, []string{"compensate:start_containers"}) {
		t.Fatalf("recovery effects %v", got)
	}
	if err := r.SendReport(ctx); err != nil {
		t.Fatal(err)
	}
	rep, _ := protocol.DecodePayload[protocol.JobReportPayload](s.of(protocol.TypeJobReport)[0])
	if rep.HighWater != 3 || len(rep.Jobs) != 1 {
		t.Fatalf("report %+v", rep)
	}
	e := rep.Jobs[0]
	if e.Status != protocol.ReportFinished || e.Result.Outcome != "interrupted" || e.Result.Resumable ||
		e.Result.InterruptedStep != "snapshot" || e.Result.ErrorClass != domain.ErrorUnknownOutcome || e.Result.Recovery == "" {
		t.Fatalf("entry %+v result %+v", e, e.Result)
	}
	// A second restart does not compensate again.
	newRunner(t, ctx, dir, simExecutor(jobspec.BackupRun, fx, "", nil))
	if len(fx.list()) != 1 {
		t.Fatalf("compensated twice: %v", fx.list())
	}
}

func TestJournalCorruptionAndVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, JournalDir), 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, JournalDir, JournalFile)
	if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(dir); err == nil {
		t.Fatal("corrupt journal accepted")
	}
	if err := os.WriteFile(p, []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(dir); err == nil {
		t.Fatal("unknown journal version accepted")
	}
	if _, err := New(context.Background(), Options{StateDir: t.TempDir()}); err == nil {
		t.Fatal("runner without sender accepted")
	}
	fx := &effects{}
	e := simExecutor(jobspec.StackStart, fx, "", nil)
	if _, err := New(context.Background(), Options{StateDir: t.TempDir(), Sender: &captureSender{}, Executors: []jobexec.Executor{e, e}}); err == nil {
		t.Fatal("duplicate executor accepted")
	}
}

// gatedReportSender holds every job_report frame until release is closed,
// closing entered once the report (and so its snapshot) is being sent.
type gatedReportSender struct {
	captureSender
	entered, release chan struct{}
}

func (g *gatedReportSender) Send(ctx context.Context, f *protocol.Frame) error {
	if f.Type == protocol.TypeJobReport {
		close(g.entered)
		<-g.release
	}
	return g.captureSender.Send(ctx, f)
}

// TestReportedRunningAttemptSendsResultAfterReport is the regression test for
// the TestKillAtEveryStage/prune/manager/engine.progress.committed hang: a
// job_report snapshotted an attempt as running, the attempt finished and sent
// its result before the report went out (the manager was not reading the new
// session yet, so the result was lost), and the manager waited forever for
// the result of an attempt it had just been told was running. The attempt
// must hold its result until the report is out.
func TestReportedRunningAttemptSendsResultAfterReport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		fx := &effects{}
		block := make(chan struct{})
		s := &gatedReportSender{entered: make(chan struct{}), release: make(chan struct{})}
		r, err := New(ctx, Options{StateDir: t.TempDir(), Clock: testutil.FakeClock(), Logger: testutil.Logger(t), Sender: s,
			Executors: []jobexec.Executor{simExecutor(jobspec.PruneRun, fx, "delete_candidates", block)}})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.HandleFrame(ctx, command(t, "j", 1, 1, jobspec.PruneRun)); err != nil {
			t.Fatal(err)
		}
		reportErr := make(chan error, 1)
		go func() { reportErr <- r.SendReport(ctx) }() // the agent reconnects mid-attempt
		<-s.entered                                    // the report says running
		close(block)                                   // the attempt finishes meanwhile
		synctest.Wait()                                // ... as far as it can get
		// t.Error, not t.Fatal: the report goroutine must still be released.
		if got := fx.list(); !slices.Equal(got, []string{"collect_candidates", "delete_candidates"}) {
			t.Errorf("effects %v", got)
		}
		if res := s.of(protocol.TypeResult); len(res) != 0 {
			t.Error("result sent while the report listing the attempt as running was still pending")
		}
		close(s.release)
		if err := <-reportErr; err != nil {
			t.Fatal(err)
		}
		r.Wait()

		s.mu.Lock()
		defer s.mu.Unlock()
		var order []protocol.Type
		var rep protocol.JobReportPayload
		for _, f := range s.frames {
			switch f.Type {
			case protocol.TypeJobReport:
				if rep, err = protocol.DecodePayload[protocol.JobReportPayload](f); err != nil {
					t.Fatal(err)
				}
			case protocol.TypeResult:
			default:
				continue
			}
			order = append(order, f.Type)
		}
		if !slices.Equal(order, []protocol.Type{protocol.TypeJobReport, protocol.TypeResult}) {
			t.Fatalf("frame order %v", order)
		}
		if len(rep.Jobs) != 1 || rep.Jobs[0].Status != protocol.ReportRunning {
			t.Fatalf("report %+v", rep)
		}
	})
}
