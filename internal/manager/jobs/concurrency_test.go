package jobs_test

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// fakeAgents executes commands for several environments and checks, at the
// moment each command arrives, that no job it is still executing holds a
// conflicting lock (the engine must never dispatch conflicting jobs
// concurrently). It completes jobs in random order.
type fakeAgents struct {
	h   *harness
	rnd *rand.Rand

	cmds    chan envFrame
	running map[string]runningJob // job ID -> job
	seen    map[string]int
	lastTok map[string]uint64
	errs    []string
}

type envFrame struct {
	env string
	f   *protocol.Frame
}

type runningJob struct {
	env   string
	cmd   *protocol.Frame
	locks []domain.JobLock
}

func (a *fakeAgents) receive(ef envFrame) {
	if ef.f.Type != protocol.TypeCommand {
		return
	}
	j, err := a.h.eng.Get(a.h.ctx, ef.f.JobID)
	if err != nil {
		a.errs = append(a.errs, err.Error())
		return
	}
	for id, r := range a.running {
		if _, _, conflict := jobspec.FirstConflict(j.Locks, r.locks); conflict {
			a.errs = append(a.errs, fmt.Sprintf("job %s (%s) dispatched while conflicting job %s runs", j.ID, j.Kind, id))
		}
	}
	if ef.f.FencingToken <= a.lastTok[ef.env] {
		a.errs = append(a.errs, fmt.Sprintf("token %d not above %d on %s", ef.f.FencingToken, a.lastTok[ef.env], ef.env))
	}
	a.lastTok[ef.env] = ef.f.FencingToken
	a.seen[j.ID]++
	a.running[j.ID] = runningJob{env: ef.env, cmd: ef.f, locks: j.Locks}
	a.send(ef.env, protocol.TypeAck, ef.f, protocol.AckPayload{Accepted: true})
}

// send feeds a frame to the engine from a non-test goroutine (errors are
// collected instead of calling t.Fatal).
func (a *fakeAgents) send(env string, typ protocol.Type, cmd *protocol.Frame, payload any) {
	f, err := protocol.NewFrame(typ, string(typ)+"-"+cmd.ID, cmd.ID, cmd.Ref(), payload)
	if err == nil {
		_, err = a.h.eng.HandleAgentFrame(a.h.ctx, env, f)
	}
	if err != nil {
		a.errs = append(a.errs, err.Error())
	}
}

func (a *fakeAgents) completeOne() bool {
	if len(a.running) == 0 {
		return false
	}
	ids := make([]string, 0, len(a.running))
	for id := range a.running {
		ids = append(ids, id)
	}
	id := ids[a.rnd.IntN(len(ids))]
	r := a.running[id]
	a.send(r.env, protocol.TypeResult, r.cmd, protocol.ResultPayload{Outcome: "succeeded"})
	delete(a.running, id) // only after the engine released the locks
	return true
}

// TestConcurrentDispatchNeverRunsConflictingJobs enqueues and dispatches from
// many goroutines at once (run with -race in CI) while a fake agent checks
// the lock invariant on every command.
func TestConcurrentDispatchNeverRunsConflictingJobs(t *testing.T) {
	h := newHarness(t)
	envs := []string{"e1", "e2"}
	for _, e := range envs {
		h.disp.Connect(e)
	}
	a := &fakeAgents{h: h, rnd: rand.New(rand.NewPCG(1, 2)), cmds: make(chan envFrame, 10000),
		running: map[string]runningJob{}, seen: map[string]int{}, lastTok: map[string]uint64{}}
	h.disp.OnSend(func(env string, f *protocol.Frame) { a.cmds <- envFrame{env, f} })

	kinds := []func(env string, i int) jobs.Request{
		func(env string, i int) jobs.Request {
			return jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: env, Targets: []domain.JobTarget{stack(fmt.Sprint("s", i%3))}}
		},
		func(env string, i int) jobs.Request {
			return jobs.Request{Kind: jobspec.ContainerRestart, EnvironmentID: env,
				Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: fmt.Sprint("c", i%4)}, stack(fmt.Sprint("s", i%3))}}
		},
		func(env string, _ int) jobs.Request { return jobs.Request{Kind: jobspec.PruneRun, EnvironmentID: env} },
		func(env string, i int) jobs.Request {
			return jobs.Request{Kind: jobspec.RestoreRun, EnvironmentID: env, Targets: []domain.JobTarget{volume(fmt.Sprint("v", i%2)), repo("r")}}
		},
		func(env string, i int) jobs.Request {
			return jobs.Request{Kind: jobspec.BackupRun, EnvironmentID: env, Targets: []domain.JobTarget{volume(fmt.Sprint("v", i%2)), stack(fmt.Sprint("s", i%3)), repo("r")}}
		},
		func(env string, i int) jobs.Request {
			return jobs.Request{Kind: jobspec.ImagePull, EnvironmentID: env, Targets: []domain.JobTarget{image(fmt.Sprint("img", i%5))}}
		},
	}
	const producers, perProducer = 4, 20
	var total int
	var wg sync.WaitGroup
	agentDone := make(chan struct{})
	stopAgent := make(chan struct{})
	go func() { // the agent: receives commands and completes some while producers run
		defer close(agentDone)
		for {
			select {
			case ef := <-a.cmds:
				a.receive(ef)
				if a.rnd.IntN(3) == 0 {
					a.completeOne()
				}
			case <-stopAgent:
				return
			}
		}
	}()
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				env := envs[(p+i)%len(envs)]
				req := kinds[(p*7+i)%len(kinds)](env, i)
				req.Principal = user("alice")
				if _, _, err := h.eng.Enqueue(h.ctx, req); err != nil {
					t.Error(err)
					return
				}
				if err := h.eng.DispatchPending(h.ctx); err != nil {
					t.Error(err)
					return
				}
			}
		}(p)
	}
	total = producers * perProducer
	wg.Wait()
	close(stopAgent)
	<-agentDone

	// Drain: concurrent dispatch passes, then let the agent finish everything.
	for round := 0; round < 10*total; round++ {
		var dw sync.WaitGroup
		for k := 0; k < 3; k++ {
			dw.Add(1)
			go func() {
				defer dw.Done()
				if err := h.eng.DispatchPending(h.ctx); err != nil {
					t.Error(err)
				}
			}()
		}
		dw.Wait()
		progressed := false
	drain:
		for {
			select {
			case ef := <-a.cmds:
				a.receive(ef)
				progressed = true
			default:
				break drain
			}
		}
		if a.completeOne() {
			progressed = true
		}
		if !progressed {
			break
		}
	}
	for _, e := range a.errs {
		t.Error(e)
	}
	all, err := h.eng.List(h.ctx, domain.JobFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != total {
		t.Fatalf("%d jobs, want %d", len(all), total)
	}
	for _, j := range all {
		if j.State != domain.JobSucceeded {
			t.Errorf("job %s (%s) ended %s (%s by %s)", j.ID, j.Kind, j.State, j.BlockedReason, j.BlockedBy)
		}
		if a.seen[j.ID] != 1 {
			t.Errorf("job %s dispatched %d times", j.ID, a.seen[j.ID])
		}
	}
	h.noLocksHeld()
}
