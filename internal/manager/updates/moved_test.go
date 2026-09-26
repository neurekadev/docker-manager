package updates_test

import (
	"context"
	"errors"
	"testing"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/scheduler"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/updates"
)

// TestPolicyFollowsMigratedStack (#35 × #20): when a stack migrates, its
// update policy moves with it in the migration's completing transaction.
// Before the hook the scheduled check and run are refused with
// target_not_found (the stack left the policy's environment); after it
// both validate again, the policy keeps its ID, schedules and history,
// its candidates need a new check, and a name taken in the destination
// gets a suffix.
func TestPolicyFollowsMigratedStack(t *testing.T) {
	h := newHarness(t)
	h.publish("acme/web", "1.4", "")
	h.publish("acme/db", "16", " db")
	st, _, _ := h.shop()
	p := h.policy(updates.NewPolicy{TargetType: domain.UpdateTargetStack, TargetID: st.ID})
	on := func(s domain.UpdateSchedule) *domain.UpdateSchedule { s.Enabled = true; return &s }
	_, p, err := h.svc.Update(h.ctx, p.ID, p.Revision, domain.UpdatePolicyPatch{Check: on(p.Check), Run: on(p.Run)})
	if err != nil {
		t.Fatal(err)
	}
	h.publish("acme/web", "1.4", " v2")
	h.check(p)
	if c := h.candidates(p)["web"]; c.Status != domain.CandidateAvailable {
		t.Fatalf("candidate before the move %+v", c)
	}
	// Another policy in the destination already uses the name.
	const dst = "env-2"
	now := h.clk.Now().UTC()
	taken := domain.UpdatePolicy{ID: ids.New(), EnvironmentID: dst, Name: p.Name, TargetType: domain.UpdateTargetStack, TargetID: "st-other",
		Check: p.Check, Run: p.Run, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertUpdatePolicy(h.ctx, h.db, &taken); err != nil {
		t.Fatal(err)
	}

	// The migration moves the stack record: without the hook the policy's
	// schedules are refused.
	st.EnvironmentID = dst
	h.stacks.put(st)
	var rej *scheduler.Rejection
	if err := h.svc.CheckSource().Validate(h.ctx, p.ID); !errors.As(err, &rej) || rej.Class != scheduler.RejectTargetNotFound {
		t.Fatalf("check before the hook: %v", err)
	}
	// The hook, in the completing transaction.
	if err := h.db.RunInTx(h.ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return h.svc.StackMoved(ctx, tx, st.ID, env, dst)
	}); err != nil {
		t.Fatal(err)
	}
	moved, err := h.svc.Get(h.ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.EnvironmentID != dst || moved.TargetID != st.ID || moved.Revision != p.Revision+1 || !moved.Check.Enabled || !moved.Run.Enabled ||
		moved.Name != p.Name+" (moved)" {
		t.Fatalf("moved policy %+v", moved)
	}
	if err := h.svc.CheckSource().Validate(h.ctx, p.ID); err != nil {
		t.Fatalf("scheduled check after the move: %v", err)
	}
	if err := h.svc.RunSource().Validate(h.ctx, p.ID); err != nil {
		t.Fatalf("scheduled run after the move: %v", err)
	}
	if got, err := h.svc.ForTarget(h.ctx, dst, domain.UpdateTargetStack, st.ID); err != nil || got == nil || got.ID != p.ID {
		t.Fatalf("policy of the stack in the destination: %+v %v", got, err)
	}
	// The digest found on the source's host is not run on the destination
	// without a new check.
	for name, c := range h.candidates(moved) {
		if c.Status != domain.CandidateUnchecked || c.CandidateDigest != "" {
			t.Errorf("candidate %s after the move %+v", name, c)
		}
	}
	recs, err := h.audit.Records(h.ctx, domain.AuditFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range recs {
		if r.Action == updates.ActionPolicyMoved && r.EnvironmentID == dst {
			found = true
		}
	}
	if !found {
		t.Error("no update_policy.move audit record")
	}
	// Moving again (e.g. a repeated finalize) is a no-op for the source.
	if err := h.db.RunInTx(h.ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return h.svc.StackMoved(ctx, tx, st.ID, env, dst)
	}); err != nil {
		t.Fatal(err)
	}
	if again, _ := h.svc.Get(h.ctx, p.ID); again.Revision != moved.Revision {
		t.Errorf("a second hook call changed the policy: %+v", again)
	}
}
