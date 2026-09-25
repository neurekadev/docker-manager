//go:build faultinject

package faulttest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uptrace/bun"

	agentjobs "github.com/neurekadev/dockyard/internal/agent/jobs"
	"github.com/neurekadev/dockyard/internal/db/migrations"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/faultinject"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/jobs/jobstest"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
)

const (
	envRole     = "FT_ROLE"
	envDB       = "FT_DB"
	envState    = "FT_STATE"
	envEffects  = "FT_EFFECTS"
	envScenario = "FT_SCENARIO"
	// envHoldTail makes the manager hold the engine goroutine that
	// committed a job's terminal state until the parent hung up (see
	// tailGate), forcing the interleaving of TestManagerDrainsJobTail.
	envHoldTail = "FT_HOLD_TAIL"

	roleManager = "manager"
	roleAgent   = "agent"

	environmentID = "env-1"
	idemKey       = "fault-scenario"
)

type scenario struct {
	name    string
	kind    domain.JobKind
	targets []domain.JobTarget

	// Real-executor scenarios (real_test.go): seed prepares the persistent
	// world before the processes start, input is the job input, secrets
	// what the manager resolves at every dispatch, executors builds the
	// agent's real executors over the world, and check asserts the world
	// the job left behind. Without executors the simulated steps run.
	seed      func(t *testing.T, w world)
	input     func(w world) any
	secrets   *protocol.CommandSecrets
	executors func(w world, log *slog.Logger) ([]jobexec.Executor, error)
	check     func(t *testing.T, w world, res outcome)
}

// The simulated step sequences mirror the real features: deploy (resolve,
// pull, build, apply), backup with container shutdown (stop containers with
// a restart compensation, non-idempotent snapshot) and prune.
var scenarios = []scenario{
	{name: "deploy", kind: jobspec.StackDeploy, targets: []domain.JobTarget{{Type: domain.TargetStack, ID: "web"}}},
	{name: "backup_with_shutdown", kind: jobspec.BackupRun, targets: []domain.JobTarget{{Type: domain.TargetStack, ID: "web"},
		{Type: domain.TargetVolume, ID: "data"}, {Type: domain.TargetRepository, ID: "repo-1"}}},
	{name: "prune", kind: jobspec.PruneRun},
	// Manager-local kinds: resume (retention) and interrupt (manager backup)
	// restart policies.
	{name: "retention", kind: jobspec.ManagerRetention, targets: []domain.JobTarget{{Type: domain.TargetRepository, ID: "repo-1"}}},
	{name: "manager_backup", kind: jobspec.ManagerBackup, targets: []domain.JobTarget{{Type: domain.TargetRepository, ID: "repo-1"}}},
}

func scenarioByName(name string) scenario {
	for _, s := range append(slices.Clone(scenarios), realScenarios...) {
		if s.name == name {
			return s
		}
	}
	panic("unknown scenario " + name)
}

type envelope struct {
	Ctl   string          `json:"ctl,omitempty"`
	Frame json.RawMessage `json:"frame,omitempty"`
}

func TestMain(m *testing.M) {
	switch os.Getenv(envRole) {
	case roleManager:
		os.Exit(managerMain())
	case roleAgent:
		os.Exit(agentMain())
	}
	os.Exit(m.Run())
}

// ---------------------------------------------------------------- children

type lineWriter struct{ mu sync.Mutex }

func (lw *lineWriter) write(e envelope) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	lw.mu.Lock()
	defer lw.mu.Unlock()
	_, err = os.Stdout.Write(append(b, '\n'))
	return err
}

func (lw *lineWriter) Send(_ context.Context, f *protocol.Frame) error {
	b, err := protocol.Encode(f)
	if err != nil {
		return err
	}
	return lw.write(envelope{Frame: b})
}

type stdioDispatcher struct {
	out *lineWriter

	mu               sync.Mutex
	attached, online bool
}

func (d *stdioDispatcher) set(attached, online bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.attached, d.online = attached, online
}

func (d *stdioDispatcher) Online(env string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return env == environmentID && d.online
}

func (d *stdioDispatcher) Send(ctx context.Context, env string, f *protocol.Frame) error {
	d.mu.Lock()
	ok := env == environmentID && d.attached
	d.mu.Unlock()
	if !ok {
		return jobs.ErrAgentOffline
	}
	return d.out.Send(ctx, f)
}

func readLines(fn func(envelope)) {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		var e envelope
		if err := json.Unmarshal(sc.Bytes(), &e); err == nil {
			fn(e)
		}
	}
}

// callerCtx marks contexts of managerMain's own calls (Recover, Run,
// HandleAgentFrame, Enqueue) so tailGate only holds commits made on the
// engine's internal lifetime context, i.e. by manager-local job goroutines.
type callerCtx struct{}

// tailGate is a bun query hook forcing the interleaving of the flaky #26
// extended run 36075563917: it holds the manager-local job goroutine right
// after the COMMIT that made its job terminal, before the goroutine signals
// subscribers and reaches engine.manager_job.committed, until the parent
// closed stdin. The done watcher waits for held, so the parent always sees
// "done" and hangs up while the goroutine's tail is still pending.
//
// SQLite runs on one connection, so every query between an engine-internal
// BEGIN and its COMMIT belongs to that transaction; a terminal state in one
// of its jobs UPDATEs marks the terminal commit without reading the DB.
type tailGate struct {
	out      *lineWriter
	log      *slog.Logger
	terminal atomic.Bool // the open engine-internal transaction finishes a job
	fired    atomic.Bool
	held     chan struct{} // closed once the goroutine is held
	released <-chan struct{}
}

func (g *tailGate) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context { return ctx }

func (g *tailGate) AfterQuery(ctx context.Context, ev *bun.QueryEvent) {
	if ctx.Value(callerCtx{}) != nil || ev.Err != nil || g.fired.Load() {
		return
	}
	switch q := ev.Query; {
	case q == "BEGIN" || q == "ROLLBACK":
		g.terminal.Store(false)
	case strings.HasPrefix(q, `UPDATE "jobs"`):
		for _, s := range domain.JobStates() {
			if s.Terminal() && strings.Contains(q, `"state" = '`+string(s)+`'`) {
				g.terminal.Store(true)
			}
		}
	case q == "COMMIT" && g.terminal.Load():
		g.fired.Store(true)
		g.log.Debug("holding the job goroutine after its terminal commit")
		_ = g.out.write(envelope{Ctl: "tail_held"})
		close(g.held)
		<-g.released
		g.log.Debug("released the job goroutine")
	}
}

func managerMain() int {
	ctx := context.WithValue(context.Background(), callerCtx{}, true)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})).With("role", roleManager)
	sc := scenarioByName(os.Getenv(envScenario))
	dbPath := os.Getenv(envDB)
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		log.Error("open", "error", err)
		return 3
	}
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations,
		SnapshotDir: filepath.Join(filepath.Dir(dbPath), "snap"), Logger: log}); err != nil {
		log.Error("migrate", "error", err)
		return 3
	}
	out := &lineWriter{}
	stdinClosed := make(chan struct{})
	var gate *tailGate
	if os.Getenv(envHoldTail) != "" {
		gate = &tailGate{out: out, log: log, held: make(chan struct{}), released: stdinClosed}
		db.AddQueryHook(gate)
	}
	disp := &stdioDispatcher{out: out}
	// Credentials an agent command carries are resolved at every dispatch
	// (never journaled), like the backup service's Recovery Key.
	secrets := func(context.Context, *domain.Job) (*protocol.CommandSecrets, error) {
		if sc.secrets == nil {
			return nil, nil
		}
		s := *sc.secrets
		return &s, nil
	}
	eng, err := jobs.New(jobs.Options{DB: db, Logger: log, Dispatcher: disp, PollInterval: 50 * time.Millisecond, CommandSecrets: secrets})
	if err != nil {
		log.Error("engine", "error", err)
		return 3
	}
	if spec, _ := jobspec.Lookup(sc.kind); spec.Executor == domain.ExecutorManager {
		fx := &jobstest.Effects{File: os.Getenv(envEffects)}
		if err := eng.RegisterManagerExecutor(jobstest.SimExecutor(sc.kind, jobstest.SimOptions{Effects: fx})); err != nil {
			log.Error("register", "error", err)
			return 3
		}
	}
	if err := eng.Recover(ctx); err != nil {
		log.Error("recover", "error", err)
		return 3
	}
	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = eng.Run(runCtx)
	}()
	connected := make(chan struct{})
	var connectOnce sync.Once
	go func() {
		defer close(stdinClosed)
		readLines(func(e envelope) {
			if e.Ctl == "disconnected" {
				disp.set(false, false)
				eng.AgentDisconnected(environmentID)
				return
			}
			f, err := protocol.Decode(e.Frame)
			if err != nil {
				log.Error("bad frame", "error", err)
				return
			}
			if f.Type == protocol.TypeJobReport {
				disp.set(true, false)
			}
			replies, err := eng.HandleAgentFrame(ctx, environmentID, f)
			if err != nil {
				log.Error("handle frame", "type", f.Type, "error", err)
			}
			for _, r := range replies {
				_ = out.Send(ctx, r)
			}
			if f.Type == protocol.TypeJobReport && err == nil {
				disp.set(true, true)
				eng.Wake()
			}
			if f.Type == protocol.TypeJobReport {
				connectOnce.Do(func() { close(connected) })
			}
		})
	}()
	// Enqueue once the agent session is established, so every run passes
	// the same stages in the same order (connect, reconcile, enqueue, ...).
	select {
	case <-connected:
	case <-stdinClosed:
		return 4
	}
	var input any
	if sc.input != nil {
		input = sc.input(world{base: filepath.Dir(dbPath)})
	}
	job, _, err := eng.Enqueue(ctx, jobs.Request{Kind: sc.kind, Principal: authz.Service(), EnvironmentID: environmentID,
		Targets: sc.targets, Input: input, IdempotencyKey: idemKey})
	if err != nil {
		log.Error("enqueue", "error", err)
		return 3
	}
	changed, _ := eng.Subscribe(job.ID)
	done := make(chan struct{})
	go func() {
		for {
			if j, err := eng.Get(ctx, job.ID); err == nil && j.State.Terminal() {
				if gate != nil {
					<-gate.held
				}
				// Close before telling the parent: its hang-up must never
				// look like one that came before the job finished.
				close(done)
				_ = out.write(envelope{Ctl: "done"})
				return
			}
			select {
			case <-changed:
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	select {
	case <-done:
	case <-stdinClosed:
		select {
		case <-done: // both ready: the hang-up answered "done"
		default:
			return 4 // the parent hung up before the job finished
		}
	}
	// Keep handling frames until the parent hangs up, so stage boundaries
	// after the terminal commit (e.g. engine.result.committed) are reached.
	<-stdinClosed
	// A manager-local job commits its outcome in its own goroutine, which
	// reaches engine.manager_job.committed only after that commit (and
	// after the done watcher may already have seen it). Stop dispatching
	// and let that goroutine finish before exiting, like the manager's
	// shutdown (Engine.Close) does, so the point is reached on every run
	// (extended run 36075563917, TestManagerDrainsJobTail). Previously the
	// child returned right after the hang-up: with 0, or with 4 when the
	// hang-up won the race against close(done).
	log.Debug("parent hung up; draining the engine")
	stopRun()
	<-runDone
	eng.Wait()
	log.Debug("engine drained")
	return 0
}

func agentMain() int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})).With("role", roleAgent)
	sc := scenarioByName(os.Getenv(envScenario))
	fx := &jobstest.Effects{File: os.Getenv(envEffects)}
	out := &lineWriter{}
	var execs []jobexec.Executor
	switch spec, _ := jobspec.Lookup(sc.kind); {
	case sc.executors != nil:
		// The feature's real executors over the persistent world.
		real, err := sc.executors(world{base: filepath.Dir(os.Getenv(envState))}, log)
		if err != nil || len(real) == 0 {
			log.Error("executors", "error", err)
			return 3
		}
		execs = withEffects(real, fx)
	case spec.Executor == domain.ExecutorAgent:
		execs = append(execs, jobstest.SimExecutor(sc.kind, jobstest.SimOptions{Effects: fx}))
	}
	r, err := agentjobs.New(ctx, agentjobs.Options{StateDir: os.Getenv(envState), Logger: log, Sender: out, Executors: execs})
	if err != nil {
		log.Error("runner", "error", err)
		return 3
	}
	if err := r.SendReport(ctx); err != nil {
		return 3
	}
	readLines(func(e envelope) {
		if e.Ctl == "reconnect" {
			_ = r.SendReport(ctx)
			return
		}
		f, err := protocol.Decode(e.Frame)
		if err != nil {
			log.Error("bad frame", "error", err)
			return
		}
		if err := r.HandleFrame(ctx, f); err != nil {
			log.Error("handle frame", "error", err)
		}
	})
	cancel()
	r.Wait()
	return 0
}

// ------------------------------------------------------------------ parent

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type proc struct {
	gen    int
	stdin  io.WriteCloser
	stderr *syncBuffer
}

type event struct {
	role string
	gen  int
	line string
	exit bool
	code int
}

type harness struct {
	t      *testing.T
	sc     scenario
	dir    string
	events chan event
	procs  map[string]*proc
	gens   map[string]int
	logs   []*syncBuffer
}

func (h *harness) baseEnv(role string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, faultinject.EnvVar+"=") && !strings.HasPrefix(kv, faultinject.TraceEnv+"=") && !strings.HasPrefix(kv, "FT_") {
			env = append(env, kv)
		}
	}
	return append(env, envRole+"="+role, envScenario+"="+h.sc.name,
		envDB+"="+filepath.Join(h.dir, "dockyard.db"), envState+"="+filepath.Join(h.dir, "agent-state"),
		envEffects+"="+filepath.Join(h.dir, "effects.log"))
}

func (h *harness) start(role string, extraEnv ...string) {
	h.t.Helper()
	h.gens[role]++
	gen := h.gens[role]
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(h.baseEnv(role), extraEnv...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		h.t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		h.t.Fatal(err)
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	h.logs = append(h.logs, stderr)
	if err := cmd.Start(); err != nil {
		h.t.Fatal(err)
	}
	h.procs[role] = &proc{gen: gen, stdin: stdin, stderr: stderr}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			h.events <- event{role: role, gen: gen, line: sc.Text()}
		}
		err := cmd.Wait()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			code = -1
		}
		h.events <- event{role: role, gen: gen, exit: true, code: code}
	}()
}

func (h *harness) send(role string, e envelope) {
	p := h.procs[role]
	if p == nil {
		return
	}
	b, _ := json.Marshal(e)
	_, _ = p.stdin.Write(append(b, '\n')) // a dead process drops the frame, like a lost connection
}

func (h *harness) dumpLogs() string {
	var sb strings.Builder
	for i, l := range h.logs {
		fmt.Fprintf(&sb, "--- process %d stderr ---\n%s\n", i, l.String())
	}
	return sb.String()
}

type outcome struct {
	crashed bool
	// tailHeld: the manager held a job goroutine after its terminal commit
	// until the parent hung up (FT_HOLD_TAIL).
	tailHeld bool
	job      domain.Job
	locks    int
	effects  []string
	// dir holds the run's database, agent state and world.
	dir string
}

// run executes the scenario. faultRole/point arm one crash in the first
// process of that role; trace records every point reached; managerEnv is
// added to the first manager process.
func run(t *testing.T, sc scenario, faultRole, point string, trace bool, managerEnv ...string) outcome {
	t.Helper()
	h := &harness{t: t, sc: sc, dir: t.TempDir(), events: make(chan event, 10000), procs: map[string]*proc{}, gens: map[string]int{}}
	// Dump the children's logs whenever the test fails, also on checks
	// made after run returned.
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(h.dumpLogs())
		}
	})
	firstEnv := func(role string) []string {
		var env []string
		if role == faultRole {
			env = append(env, faultinject.EnvVar+"="+point+":crash")
		}
		if trace {
			env = append(env, faultinject.TraceEnv+"="+filepath.Join(h.dir, "trace-"+role))
		}
		return env
	}
	if sc.seed != nil {
		sc.seed(t, world{base: h.dir})
	}
	h.start(roleManager, append(firstEnv(roleManager), managerEnv...)...)
	h.start(roleAgent, firstEnv(roleAgent)...)

	res := outcome{dir: h.dir}
	alive := map[string]bool{roleManager: true, roleAgent: true}
	linked, done := false, false
	timeout := time.NewTimer(120 * time.Second)
	defer timeout.Stop()
loop:
	for {
		select {
		case ev := <-h.events:
			if ev.gen != h.gens[ev.role] {
				continue // output of a process generation that was replaced
			}
			if ev.exit {
				t.Logf("%s process %d exited with %d (done=%v)", ev.role, ev.gen, ev.code, done)
				alive[ev.role] = false
				if done {
					if ev.code == faultinject.ExitCode && ev.role == faultRole {
						res.crashed = true // killed right after its last contribution
					}
					if !alive[roleManager] && !alive[roleAgent] {
						break loop
					}
					continue
				}
				if ev.code != faultinject.ExitCode || ev.role != faultRole || res.crashed {
					t.Fatalf("%s exited with %d before the job finished\n%s", ev.role, ev.code, h.dumpLogs())
				}
				res.crashed = true
				linked = false
				if ev.role == roleAgent {
					h.send(roleManager, envelope{Ctl: "disconnected"})
					h.start(roleAgent)
				} else {
					h.start(roleManager)
					h.send(roleAgent, envelope{Ctl: "reconnect"})
				}
				alive[ev.role] = true
				continue
			}
			var e envelope
			if err := json.Unmarshal([]byte(ev.line), &e); err != nil {
				continue
			}
			switch {
			case ev.role == roleManager && e.Ctl == "tail_held":
				res.tailHeld = true
			case ev.role == roleManager && e.Ctl == "done":
				done = true
				for _, p := range h.procs {
					_ = p.stdin.Close()
				}
			case ev.role == roleManager && e.Frame != nil && linked:
				h.send(roleAgent, e)
			case ev.role == roleAgent && e.Frame != nil:
				f, err := protocol.Decode(e.Frame)
				if err != nil {
					t.Fatalf("agent sent an invalid frame: %v", err)
				}
				if f.Type == protocol.TypeJobReport {
					linked = true // a (re)connect starts with the report
				}
				if linked && alive[roleManager] {
					h.send(roleManager, e)
				}
			}
		case <-timeout.C:
			t.Fatalf("scenario did not finish (done=%v)\n%s", done, h.dumpLogs())
		}
	}

	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(h.dir, "dockyard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	all, err := store.ListJobs(ctx, db, domain.JobFilter{})
	if err != nil || len(all) != 1 {
		t.Fatalf("jobs %d err %v (duplicate enqueue?)", len(all), err)
	}
	res.job = all[0]
	held, err := store.HeldLocks(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	res.locks = len(held)
	if res.effects, err = jobstest.ReadEffects(filepath.Join(h.dir, "effects.log")); err != nil {
		t.Fatal(err)
	}
	if trace {
		for _, role := range []string{roleManager, roleAgent} {
			b, _ := os.ReadFile(filepath.Join(h.dir, "trace-"+role))
			for _, p := range strings.Fields(string(b)) {
				if !slices.Contains(traced[role], p) {
					traced[role] = append(traced[role], p)
				}
			}
		}
	}
	return res
}

// traced collects fault points reached per role during the trace runs.
var traced = map[string][]string{}

// checkInvariants asserts the #26 crash guarantees.
func checkInvariants(t *testing.T, sc scenario, res outcome) {
	t.Helper()
	j := res.job
	if !j.State.Terminal() {
		t.Fatalf("job ended in non-terminal state %s", j.State)
	}
	if j.State != domain.JobSucceeded && (j.ErrorClass == "" || j.Recovery == "") {
		t.Fatalf("job %s without error class/recovery guidance: %+v", j.State, j)
	}
	if res.locks != 0 {
		t.Fatalf("%d locks still held", res.locks)
	}
	spec, _ := jobspec.Lookup(sc.kind)
	count := map[string]int{}
	var order []string
	for _, e := range res.effects {
		jobID, what, ok := strings.Cut(e, ":")
		// "*": a real executor's compensation (it does not know its job;
		// every run has exactly one).
		if !ok || (jobID != j.ID && jobID != "*") {
			t.Fatalf("effect %q of an unknown job", e)
		}
		count[what]++
		order = append(order, what)
	}
	for _, st := range spec.Steps {
		n := count[st.Name]
		if !st.Idempotent && n > 1 {
			t.Fatalf("non-idempotent step %s executed %d times", st.Name, n)
		}
		if n > 2 {
			t.Fatalf("step %s executed %d times", st.Name, n)
		}
		if j.State == domain.JobSucceeded && n == 0 {
			t.Fatalf("job succeeded but step %s never ran", st.Name)
		}
	}
	// Containers stopped for a backup are always started again.
	lastStop, lastStart := -1, -1
	for i, w := range order {
		switch w {
		case "stop_containers":
			lastStop = i
		case "start_containers", "compensate:start_containers":
			lastStart = i
		}
	}
	if lastStop >= 0 && lastStart < lastStop {
		t.Fatalf("containers left stopped: effects %v, state %s", order, j.State)
	}
}

// TestKillAtEveryStage is the #26 crash acceptance test.
func TestKillAtEveryStage(t *testing.T) {
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			traced = map[string][]string{}
			clean := run(t, sc, "", "", true)
			checkInvariants(t, sc, clean)
			if clean.job.State != domain.JobSucceeded || clean.crashed {
				t.Fatalf("clean run: %+v", clean.job)
			}
			spec, _ := jobspec.Lookup(sc.kind)
			if len(clean.effects) != len(spec.Steps) {
				t.Fatalf("clean run effects %v", clean.effects)
			}
			points := map[string][]string{roleManager: traced[roleManager], roleAgent: traced[roleAgent]}
			stepPoints := points[roleAgent]
			if spec.Executor == domain.ExecutorManager {
				stepPoints = points[roleManager]
				points[roleAgent] = nil // the agent takes no part in manager-local jobs
			}
			if len(points[roleManager]) < 4 || len(stepPoints) < 4*len(spec.Steps) {
				t.Fatalf("too few fault points traced: %v", points)
			}
			for _, role := range []string{roleManager, roleAgent} {
				for _, p := range points[role] {
					t.Run(role+"/"+p, func(t *testing.T) {
						t.Parallel()
						res := run(t, sc, role, p, false)
						if !res.crashed {
							t.Fatalf("fault point %s was not reached", p)
						}
						checkInvariants(t, sc, res)
						t.Logf("%s killed at %s: job %s (%s) attempt %d", role, p, res.job.State, res.job.ErrorClass, res.job.Attempt)
					})
				}
			}
		})
	}
}

// TestManagerDrainsJobTail is the regression test for extended run
// 36075563917 (TestKillAtEveryStage/manager_backup failed with "fault point
// engine.manager_job.committed was not reached"). That point sits in the
// manager-local job goroutine after the terminal commit; the manager child
// used to return as soon as the parent hung up after "done" (exit 0, or 4
// when the hang-up beat close(done)), so when the goroutine was descheduled
// between commit and point the process exited first. The gate forces
// exactly that interleaving: the manager must still let the goroutine
// finish its tail (and crash there) before exiting.
func TestManagerDrainsJobTail(t *testing.T) {
	for _, name := range []string{"manager_backup", "retention"} {
		t.Run(name, func(t *testing.T) {
			sc := scenarioByName(name)
			res := run(t, sc, roleManager, jobs.PointManagerJobCommitted, false, envHoldTail+"=1")
			if !res.tailHeld {
				t.Fatal("the job goroutine's tail was not held; the interleaving was not forced")
			}
			if !res.crashed {
				t.Fatalf("fault point %s was not reached: the manager exited before its job goroutine finished", jobs.PointManagerJobCommitted)
			}
			checkInvariants(t, sc, res)
			if res.job.State != domain.JobSucceeded {
				t.Fatalf("job %s (%s), want succeeded: the crash came after the terminal commit", res.job.State, res.job.ErrorClass)
			}
		})
	}
}
