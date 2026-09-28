package stacks_test

import (
	"strings"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// TestUnchangedDeployKeepsTheLastDeployTime: a deploy that started no
// container still counts as a deploy (status, applied revision), but the
// stack's last deploy time stays the one of the deploy that changed
// something. Results without the flag (older agents) move it.
func TestUnchangedDeployKeepsTheLastDeployTime(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	snap := protocol.NewSourceSnapshot([]protocol.SourceFile{{Path: "compose.yaml", Content: []byte(shopYAML)}, {Path: ".env", Content: []byte(shopEnv)}})
	deploy := func(unchanged bool) domain.Stack {
		t.Helper()
		j := h.deploy(h.get(st.ID))
		h.finishWith("succeeded", protocol.StackJobOutput{Sources: &snap, Unchanged: unchanged,
			After: []protocol.ServiceState{{Service: "web", Containers: 1, Running: 1}}})
		if j = h.job(j.ID); j.State != domain.JobSucceeded {
			t.Fatalf("deploy %s", j.State)
		}
		return h.get(st.ID)
	}
	first := deploy(false)
	if first.AppliedAt == nil || first.Applied == nil {
		t.Fatalf("first deploy: applied %+v at %v", first.Applied, first.AppliedAt)
	}
	h.clk.Advance(time.Hour)
	same := deploy(true)
	if same.AppliedAt == nil || !same.AppliedAt.Equal(*first.AppliedAt) {
		t.Errorf("unchanged deploy moved the last deploy time: %v -> %v", first.AppliedAt, same.AppliedAt)
	}
	if same.Applied == nil || same.Applied.ID != first.Applied.ID || same.Status != domain.StackDeployed {
		t.Errorf("unchanged deploy: applied %+v (was %+v) status %s", same.Applied, first.Applied, same.Status)
	}
	h.clk.Advance(time.Hour)
	older := deploy(false)
	if older.AppliedAt == nil || !older.AppliedAt.After(*first.AppliedAt) {
		t.Errorf("a deploy without the flag kept the time: %v", older.AppliedAt)
	}
}

// TestDeployNumbersARevisionOnlyWhenTheFilesChange: a revision is a version
// of the definition files. Redeploying the same bytes (to run a pulled
// image, say) applies the revision that already holds them, the newest one
// on disk or else the applied one; only changed bytes the watcher has not
// recorded yet become a new "deploy" revision.
func TestDeployNumbersARevisionOnlyWhenTheFilesChange(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	created := *st.Observed
	snapOf := func(composeYAML string) protocol.SourceSnapshot {
		return protocol.NewSourceSnapshot([]protocol.SourceFile{{Path: "compose.yaml", Content: []byte(composeYAML)}, {Path: ".env", Content: []byte(shopEnv)}})
	}
	deploy := func(outcome string, snap protocol.SourceSnapshot) (domain.Job, domain.Stack) {
		t.Helper()
		j := h.deploy(h.get(st.ID))
		h.finishWith(outcome, protocol.StackJobOutput{Sources: &snap})
		return h.job(j.ID), h.get(st.ID)
	}

	// The created bytes: the creation's revision is applied, none is added.
	if _, cur := deploy("succeeded", snapOf(shopYAML)); cur.Applied == nil || cur.Applied.ID != created.ID || cur.Observed.ID != created.ID {
		t.Fatalf("same bytes: applied %+v observed %+v", cur.Applied, cur.Observed)
	}
	if revs := revisions(t, h, st.ID); len(revs) != 1 {
		t.Fatalf("%d revisions after redeploying the created files", len(revs))
	}

	// Bytes changed on disk but not recorded yet: a new deploy revision.
	edited := strings.Replace(shopYAML, "nginx:1.27", "nginx:1.28", 1)
	j, cur := deploy("succeeded", snapOf(edited))
	revs := revisions(t, h, st.ID)
	if len(revs) != 2 || revs[0].Seq != 2 || revs[0].Source != domain.RevisionDeploy || revs[0].JobID != j.ID || revs[0].AuthorUserID != "alice" ||
		cur.Applied == nil || cur.Applied.ID != revs[0].ID || cur.Observed.ID != revs[0].ID {
		t.Fatalf("changed bytes: revisions %+v applied %+v", revs, cur.Applied)
	}
	deployed := *cur.Applied

	// An edit is recorded, then undone on disk before the watcher saw it:
	// the deploy of the applied bytes reuses the applied revision.
	h.write(edited+"# later\n", "shop", "compose.yaml")
	if rev, err := h.svc.RecordObserved(h.ctx, st.ID, domain.RevisionExternal, authz.Service()); err != nil || rev == nil || rev.Seq != 3 {
		t.Fatalf("observed %+v %v", rev, err)
	}
	if _, cur = deploy("succeeded", snapOf(edited)); cur.Applied.ID != deployed.ID || cur.Observed.ID != deployed.ID || cur.UndeployedChanges() {
		t.Fatalf("applied bytes again: applied %+v observed %+v", cur.Applied, cur.Observed)
	}

	// A failed redeploy of the same bytes names that revision as failed.
	if j, cur = deploy("failed", snapOf(edited)); j.State != domain.JobFailed || cur.Failed == nil || cur.Failed.ID != deployed.ID ||
		cur.Applied.ID != deployed.ID || cur.Status != domain.StackFailed {
		t.Fatalf("failed redeploy %s: failed %+v applied %+v status %s", j.State, cur.Failed, cur.Applied, cur.Status)
	}
	if n := len(revisions(t, h, st.ID)); n != 3 {
		t.Errorf("%d revisions, want created, deployed edit and observed edit", n)
	}
}
