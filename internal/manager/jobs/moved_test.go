package jobs_test

import (
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// TestDispatchRefusesJobsOfAMovedStack (#35): a job queued against a
// stack's environment while a migration held the stack lock is never sent
// to that environment once the stack moved; it fails with target_moved.
// Jobs in the stack's current environment, unknown stacks and the source
// removal (which acts on the former environment by design) still run.
func TestDispatchRefusesJobsOfAMovedStack(t *testing.T) {
	h := newHarness(t)
	now := h.clk.Now().UTC()
	for _, id := range []string{"e1", "e2"} {
		if err := store.InsertEnvironment(h.ctx, h.db, &domain.Environment{ID: id, Name: id, EngineID: "ENG-" + id, InstallID: "inst-" + id,
			Status: domain.EnvironmentActive, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	st := domain.Stack{ID: "st-shop", EnvironmentID: "e1", Name: "shop", Root: protocol.RootStacks, Dir: "shop",
		Origin: domain.StackOriginCreated, Status: domain.StackDeployed, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertStack(h.ctx, h.db, &st); err != nil {
		t.Fatal(err)
	}
	h.disp.Connect("e1")
	h.disp.Connect("e2")
	// Queued against e1 while the stack is there (a scheduled update run
	// and a manual restart), then the migration cuts over to e2.
	sched := h.enqueue(jobs.Request{Kind: jobspec.UpdateRun, Principal: authz.Service(), EnvironmentID: "e1",
		Targets: []domain.JobTarget{stack(st.ID)}})
	manual := h.enqueue(jobs.Request{Kind: jobspec.StackRestart, Principal: user("alice"), EnvironmentID: "e1",
		Targets: []domain.JobTarget{stack(st.ID)}})
	st.EnvironmentID = "e2"
	if err := store.UpdateStack(h.ctx, h.db, &st); err != nil {
		t.Fatal(err)
	}
	removal := h.enqueue(jobs.Request{Kind: jobspec.StackRemoveSource, Principal: user("alice"), EnvironmentID: "e1",
		Targets: []domain.JobTarget{stack(st.ID)}})
	here := h.enqueue(jobs.Request{Kind: jobspec.StackStart, Principal: user("alice"), EnvironmentID: "e2",
		Targets: []domain.JobTarget{stack(st.ID)}})
	unknown := h.enqueue(jobs.Request{Kind: jobspec.StackStart, Principal: user("alice"), EnvironmentID: "e1",
		Targets: []domain.JobTarget{stack("gone")}})
	h.dispatch()
	for _, id := range []string{sched.ID, manual.ID} {
		j := h.wantState(id, domain.JobFailed)
		if j.ErrorClass != domain.ErrorTargetMoved || !strings.Contains(j.ErrorMessage, "moved to environment e2") ||
			!strings.Contains(j.Recovery, "new environment") || j.DispatchedAt != nil {
			t.Fatalf("job queued before the cut-over %+v", j)
		}
	}
	for _, id := range []string{removal.ID, here.ID, unknown.ID} {
		h.wantState(id, domain.JobDispatched)
	}
	// Nothing of the refused jobs reached the source's agent.
	for _, f := range h.commands("e1") {
		if r := f.Ref(); r.JobID == sched.ID || r.JobID == manual.ID {
			t.Fatalf("a refused job was sent to the source: %+v", r)
		}
	}
}
