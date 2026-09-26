package jobs_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/uptrace/bun"

	agentjobs "code.neureka.dev/docker-manager/docker-manager/internal/agent/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/db/migrations"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs/jobstest"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// switchAuthz allows everything unless a capability/user pair is revoked.
type switchAuthz struct {
	mu      sync.Mutex
	revoked map[string]bool // "<user>/<capability>"
	calls   int
}

func (s *switchAuthz) Can(_ context.Context, p authz.Principal, capability string, _ authz.Resource) authz.Decision {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.revoked[p.UserID+"/"+capability] || s.revoked[p.UserID+"/*"] {
		return authz.Deny("revoked")
	}
	return authz.Allow("test")
}

func (s *switchAuthz) revoke(user, capability string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked[user+"/"+capability] = true
}

type harness struct {
	t    *testing.T
	ctx  context.Context
	db   *bun.DB
	clk  *clock.Fake
	disp *jobstest.Dispatcher
	az   *switchAuthz
	eng  *jobs.Engine
	opts []func(*jobs.Options)
}

func openDB(t *testing.T) *bun.DB {
	t.Helper()
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "docker-manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: filepath.Join(dir, "snap"),
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	return db
}

func newHarness(t *testing.T, opts ...func(*jobs.Options)) *harness {
	t.Helper()
	h := &harness{t: t, ctx: testutil.Context(t), db: openDB(t), clk: testutil.FakeClock(), disp: jobstest.New(),
		az: &switchAuthz{revoked: map[string]bool{}}, opts: opts}
	h.eng = h.newEngine()
	return h
}

func (h *harness) newEngine() *jobs.Engine {
	h.t.Helper()
	o := jobs.Options{DB: h.db, Clock: h.clk, Logger: testutil.Logger(h.t), Dispatcher: h.disp, Authorizer: h.az}
	for _, f := range h.opts {
		f(&o)
	}
	e, err := jobs.New(o)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(e.Close)
	return e
}

func user(id string) authz.Principal { return authz.Principal{Kind: authz.KindUser, UserID: id} }

func stack(id string) domain.JobTarget  { return domain.JobTarget{Type: domain.TargetStack, ID: id} }
func volume(id string) domain.JobTarget { return domain.JobTarget{Type: domain.TargetVolume, ID: id} }
func image(id string) domain.JobTarget  { return domain.JobTarget{Type: domain.TargetImage, ID: id} }
func repo(id string) domain.JobTarget   { return domain.JobTarget{Type: domain.TargetRepository, ID: id} }

func (h *harness) enqueue(req jobs.Request) domain.Job {
	h.t.Helper()
	if !req.Principal.Valid() {
		req.Principal = user("alice")
	}
	j, created, err := h.eng.Enqueue(h.ctx, req)
	if err != nil {
		h.t.Fatalf("enqueue %s: %v", req.Kind, err)
	}
	if !created {
		h.t.Fatalf("enqueue %s returned an existing job", req.Kind)
	}
	return j
}

func (h *harness) dispatch() {
	h.t.Helper()
	if err := h.eng.DispatchPending(h.ctx); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) job(id string) domain.Job {
	h.t.Helper()
	j, err := h.eng.Get(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return j
}

func (h *harness) wantState(id string, want domain.JobState) domain.Job {
	h.t.Helper()
	j := h.job(id)
	if j.State != want {
		h.t.Fatalf("job %s (%s) state = %s, want %s (error %s: %s; blocked %s by %s)", id, j.Kind, j.State, want,
			j.ErrorClass, j.ErrorMessage, j.BlockedReason, j.BlockedBy)
	}
	return j
}

func (h *harness) heldLocks() []store.HeldLock {
	h.t.Helper()
	l, err := store.HeldLocks(h.ctx, h.db)
	if err != nil {
		h.t.Fatal(err)
	}
	return l
}

func (h *harness) noLocksHeld() {
	h.t.Helper()
	if l := h.heldLocks(); len(l) != 0 {
		h.t.Fatalf("locks still held: %+v", l)
	}
}

// commands drains the queued frames for env and returns the commands.
func (h *harness) commands(env string) []*protocol.Frame {
	var out []*protocol.Frame
	for _, f := range h.disp.Drain(env) {
		if f.Type == protocol.TypeCommand {
			out = append(out, f)
		}
	}
	return out
}

// agentFrame feeds a frame from env's agent into the engine.
func (h *harness) agentFrame(env string, f *protocol.Frame) []*protocol.Frame {
	h.t.Helper()
	replies, err := h.eng.HandleAgentFrame(h.ctx, env, f)
	if err != nil {
		h.t.Fatal(err)
	}
	return replies
}

func (h *harness) ack(env string, cmd *protocol.Frame, p protocol.AckPayload) {
	h.t.Helper()
	f, err := protocol.NewFrame(protocol.TypeAck, "ack-"+cmd.ID, cmd.ID, cmd.Ref(), p)
	if err != nil {
		h.t.Fatal(err)
	}
	h.agentFrame(env, f)
}

func (h *harness) result(env string, cmd *protocol.Frame, res protocol.ResultPayload) []*protocol.Frame {
	h.t.Helper()
	f, err := protocol.NewFrame(protocol.TypeResult, "res-"+cmd.ID, cmd.ID, cmd.Ref(), res)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.agentFrame(env, f)
}

func (h *harness) report(env string, rep protocol.JobReportPayload) protocol.AckPayload {
	h.t.Helper()
	if rep.Jobs == nil {
		rep.Jobs = []protocol.JobReportEntry{}
	}
	f, err := protocol.NewFrame(protocol.TypeJobReport, "rep-1", "", protocol.JobRef{}, rep)
	if err != nil {
		h.t.Fatal(err)
	}
	replies := h.agentFrame(env, f)
	if len(replies) != 1 {
		h.t.Fatalf("report replies = %d", len(replies))
	}
	p, err := protocol.DecodePayload[protocol.AckPayload](replies[0])
	if err != nil {
		h.t.Fatal(err)
	}
	return p
}

// completeAll acks and succeeds every queued command of env.
func (h *harness) completeAll(env string) int {
	h.t.Helper()
	n := 0
	for _, cmd := range h.commands(env) {
		h.ack(env, cmd, protocol.AckPayload{Accepted: true})
		h.result(env, cmd, protocol.ResultPayload{Outcome: "succeeded"})
		n++
	}
	return n
}

// loop connects the engine with a real agent runner through the fake
// dispatcher. Frames are delivered only by settle(), which makes every
// interleaving explicit and deterministic.
type loop struct {
	h      *harness
	env    string
	dir    string
	execs  []jobexec.Executor
	runner *agentjobs.Runner
	cancel context.CancelFunc

	mu     sync.Mutex
	outbox []*protocol.Frame
}

func (l *loop) Send(_ context.Context, f *protocol.Frame) error {
	b, err := protocol.Encode(f)
	if err != nil {
		return err
	}
	g, err := protocol.Decode(b)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.outbox = append(l.outbox, g)
	return nil
}

func (l *loop) takeOutbox() []*protocol.Frame {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.outbox
	l.outbox = nil
	return out
}

func newLoop(h *harness, env string, execs ...jobexec.Executor) *loop {
	h.t.Helper()
	l := &loop{h: h, env: env, dir: h.t.TempDir(), execs: execs}
	l.start()
	return l
}

// start creates a runner (recovering the journal) - an agent process start.
func (l *loop) start() {
	l.h.t.Helper()
	ctx, cancel := context.WithCancel(l.h.ctx)
	r, err := agentjobs.New(ctx, agentjobs.Options{StateDir: l.dir, Clock: l.h.clk, Logger: testutil.Logger(l.h.t), Sender: l, Executors: l.execs})
	if err != nil {
		cancel()
		l.h.t.Fatal(err)
	}
	l.runner, l.cancel = r, cancel
	l.h.t.Cleanup(func() { cancel(); r.Wait() })
}

// connect runs the reconnect handshake: job_report, reconcile, online.
func (l *loop) connect() {
	l.h.t.Helper()
	l.h.disp.Attach(l.env)
	if err := l.runner.SendReport(l.h.ctx); err != nil {
		l.h.t.Fatal(err)
	}
	l.toManager()
	l.h.disp.Connect(l.env)
}

// kill stops the agent process abruptly: running steps observe a cancelled
// context (outcome unknown, journal left in flight) and the connection drops.
func (l *loop) kill() []*protocol.Frame {
	l.cancel()
	l.runner.Wait()
	l.takeOutbox()
	dropped := l.h.disp.Disconnect(l.env)
	l.h.eng.AgentDisconnected(l.env)
	return dropped
}

func (l *loop) toManager() int {
	l.h.t.Helper()
	out := l.takeOutbox()
	for _, f := range out {
		for _, reply := range l.h.agentFrame(l.env, f) {
			if err := l.runner.HandleFrame(l.h.ctx, reply); err != nil {
				l.h.t.Fatal(err)
			}
		}
	}
	return len(out)
}

func (l *loop) toAgent() int {
	l.h.t.Helper()
	in := l.h.disp.Drain(l.env)
	for _, f := range in {
		if err := l.runner.HandleFrame(l.h.ctx, f); err != nil {
			l.h.t.Fatal(err)
		}
	}
	return len(in)
}

// settle dispatches and delivers frames both ways until nothing moves.
func (l *loop) settle() {
	l.h.t.Helper()
	for i := 0; i < 100; i++ {
		l.h.dispatch()
		n := l.toAgent()
		l.runner.Wait()
		n += l.toManager()
		if n == 0 {
			return
		}
	}
	l.h.t.Fatal("loop did not settle")
}

// simWithBlock simulates kind; step signals entered and waits for release.
func simWithBlock(kind domain.JobKind, fx *jobstest.Effects, step string, entered, release chan struct{}) jobexec.Executor {
	return jobstest.SimExecutor(kind, jobstest.SimOptions{Effects: fx, Before: map[string]func(context.Context, *jobexec.StepContext) error{
		step: func(ctx context.Context, _ *jobexec.StepContext) error {
			if entered != nil {
				close(entered)
			}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}})
}

func sim(kind domain.JobKind, fx *jobstest.Effects) jobexec.Executor {
	return jobstest.SimExecutor(kind, jobstest.SimOptions{Effects: fx})
}
