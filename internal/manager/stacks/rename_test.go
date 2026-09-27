package stacks_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/stacks"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// renameAgent scripts the agent's compose.rename_preview and records its
// inputs.
type renameAgent struct {
	inputs []protocol.ComposeRenamePreviewInput
	plan   protocol.StackRenamePlan
}

func (h *harness) scriptRename(plan protocol.StackRenamePlan) *renameAgent {
	h.t.Helper()
	ra := &renameAgent{plan: plan}
	h.agents.mu.Lock()
	h.agents.features = map[string]bool{protocol.FeatureStackRename: true}
	h.agents.mu.Unlock()
	h.agents.handlers[protocol.ReqComposeRenamePreview] = func(_ context.Context, raw json.RawMessage) (any, error) {
		var in protocol.ComposeRenamePreviewInput
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		ra.inputs = append(ra.inputs, in)
		p := ra.plan
		p.From, p.To, p.FromDir, p.ToDir = in.Stack.ProjectName, in.Rename.ProjectName, in.Stack.Dir, in.Rename.Dir
		return p, nil
	}
	return ra
}

// finishWith dispatches the queued jobs and answers each command with a
// scripted result carrying out.
func (h *harness) finishWith(outcome string, out protocol.StackJobOutput) {
	h.t.Helper()
	if err := h.eng.DispatchPending(h.ctx); err != nil {
		h.t.Fatal(err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, f := range h.disp.Drain(env) {
		if f.Type != protocol.TypeCommand {
			continue
		}
		h.frame(protocol.TypeAck, f, protocol.AckPayload{Accepted: true})
		res := protocol.ResultPayload{Outcome: outcome, Output: raw}
		if outcome != "succeeded" {
			res.ErrorClass, res.Message = "engine_error", "scripted failure"
		}
		h.frame(protocol.TypeResult, f, res)
	}
}

func TestRenameDir(t *testing.T) {
	for _, c := range []struct{ dir, name, want string }{
		{"shop", "store", "store"},
		{"apps/shop", "store", "apps/store"},
		{"apps/web", "store", "apps/web"}, // not named after the project: kept
	} {
		if got := stacks.RenameDir(domain.Stack{Name: "shop", Dir: c.dir}, c.name); got != c.want {
			t.Errorf("RenameDir(%s, %s) = %s, want %s", c.dir, c.name, got, c.want)
		}
	}
}

// TestRenamePreviewValidates: the name is checked, taken names and
// directories refused, and agents without the feature answer
// agent_unsupported (environment_offline while offline) before anything
// reaches the agent.
func TestRenamePreviewValidates(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	h.create("store", shopYAML, shopEnv)
	var in *domain.InputError
	for _, name := range []string{"", "Shop", "-x", "shop"} {
		if _, err := h.svc.PreviewRename(h.ctx, st, name); !errors.As(err, &in) || in.Field != "name" {
			t.Errorf("name %q: %v", name, err)
		}
	}
	if _, err := h.svc.PreviewRename(h.ctx, st, "store"); !errors.Is(err, domain.ErrStackNameTaken) {
		t.Errorf("taken name: %v", err)
	}
	if _, err := h.svc.PreviewRename(h.ctx, st, "shop2"); stackErrCode(err) != domain.StackErrEnvironmentUnsupported {
		t.Errorf("agent without the feature: %v", err)
	}
	h.agents.setOnline(false)
	if _, err := h.svc.PreviewRename(h.ctx, st, "shop2"); stackErrCode(err) != domain.StackErrOffline {
		t.Errorf("offline: %v", err)
	}
	if slices.Contains(h.agents.Requests(), protocol.ReqComposeRenamePreview) {
		t.Error("a refused preview reached the agent")
	}
}

func TestRenamePreviewAsksTheAgent(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	h.svc.SetVolumeHolds(func(_ context.Context, _, project string) ([]string, error) {
		if project == "shop" {
			return []string{"shop_held"}, nil
		}
		return nil, nil
	})
	ra := h.scriptRename(protocol.StackRenamePlan{Running: []string{"web"},
		Volumes:  []protocol.StackRenameVolume{{Key: "data", Name: "shop_data", NewName: "store_data", Action: protocol.RenameVolumeMove}},
		Warnings: []protocol.ComposeIssue{{Code: "outside_containers", Message: "backup is recreated"}}})
	plan, err := h.svc.PreviewRename(h.ctx, st, "store")
	if err != nil {
		t.Fatal(err)
	}
	if len(ra.inputs) != 1 {
		t.Fatalf("%d previews", len(ra.inputs))
	}
	got := ra.inputs[0]
	if got.Rename != (protocol.StackRename{ProjectName: "store", Dir: "store"}) || got.Stack.ProjectName != "shop" ||
		!slices.Equal(got.KeepVolumes, []string{"shop_held"}) {
		t.Errorf("preview input %+v", got)
	}
	if plan.From != "shop" || plan.To != "store" || plan.ToDir != "store" || len(plan.Volumes) != 1 ||
		plan.Volumes[0].NewName != "store_data" || !slices.Equal(plan.Running, []string{"web"}) {
		t.Errorf("plan %+v", plan)
	}
	// The stack was never deployed: its files differ from the applied
	// revision, and the rename applies them.
	if len(plan.Warnings) != 2 || plan.Warnings[1].Code != stacks.RenameWarnUndeployed {
		t.Errorf("warnings %+v", plan.Warnings)
	}
}

func TestRenameRefusals(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	ra := h.scriptRename(protocol.StackRenamePlan{Blockers: []protocol.ComposeIssue{{Code: "declared_name", Message: "name: is set"}}})
	_, err := h.svc.Rename(h.ctx, alice, st, "store", domain.StackJobRequest{})
	var se *domain.StackError
	if !errors.As(err, &se) || se.Code != domain.StackErrRenameBlocked || len(se.Issues) != 1 || se.Issues[0].Code != "declared_name" {
		t.Fatalf("blocked rename: %v", err)
	}
	ra.plan.Blockers = nil
	h.svc.SetProtection(ownProject("shop"))
	var de *domain.DockerError
	if _, err := h.svc.Rename(h.ctx, alice, st, "store", domain.StackJobRequest{}); !errors.As(err, &de) || de.Code != domain.DockerProtected {
		t.Fatalf("own project: %v", err)
	}
	if cur := h.get(st.ID); cur.LastJobKind == jobspec.StackRename {
		t.Error("a refused rename was queued")
	}
}

// TestRenameNeedsRightsOnOutsideContainers: stack.rename alone never
// stops or replaces a container outside the stack; the refusal does not
// name a container the caller may not see.
func TestRenameNeedsRightsOnOutsideContainers(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	h.scriptRename(protocol.StackRenamePlan{Containers: []protocol.StackRenameContainer{
		{ID: "c1", Name: "secret-backup", Running: true, Volumes: []string{"shop_data"}}}})
	var asked []string
	deny := func(c domain.StackRenameContainer) bool { asked = append(asked, c.Name); return false }
	_, err := h.svc.Rename(h.ctx, alice, st, "store", domain.StackJobRequest{MayRecreate: deny})
	var se *domain.StackError
	if !errors.As(err, &se) || se.Code != domain.StackErrRenameBlocked || len(se.Issues) != 1 || se.Issues[0].Code != "container_not_permitted" {
		t.Fatalf("rename: %v", err)
	}
	if strings.Contains(se.Issues[0].Message, "secret-backup") || !strings.Contains(se.Issues[0].Message, "shop_data") {
		t.Errorf("message %q", se.Issues[0].Message)
	}
	if len(asked) != 1 || asked[0] != "secret-backup" {
		t.Errorf("asked about %v", asked)
	}
	if cur := h.get(st.ID); cur.LastJobKind == jobspec.StackRename {
		t.Error("a refused rename was queued")
	}
}

type renamedHook struct{ got []domain.StackRenamed }

func (r *renamedHook) hook(_ context.Context, _ bun.IDB, ev domain.StackRenamed) error {
	r.got = append(r.got, ev)
	return nil
}

func TestRenameJobRenamesTheStack(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	hook := &renamedHook{}
	h.svc.OnRenamed(hook.hook)
	h.scriptRename(protocol.StackRenamePlan{})
	j, err := h.svc.Rename(h.ctx, alice, st, "store", domain.StackJobRequest{IdempotencyKey: "k-1"})
	if err != nil {
		t.Fatal(err)
	}
	var in protocol.StackJobInput
	if err := json.Unmarshal(j.Input, &in); err != nil {
		t.Fatal(err)
	}
	if j.Kind != jobspec.StackRename || in.StackID != st.ID || in.Stack.ProjectName != "shop" || in.Rename == nil ||
		*in.Rename != (protocol.StackRename{ProjectName: "store", Dir: "store"}) {
		t.Fatalf("job %s input %+v", j.Kind, in)
	}
	snap := protocol.NewSourceSnapshot([]protocol.SourceFile{{Path: "compose.yaml", Content: []byte(shopYAML)}, {Path: ".env", Content: []byte(shopEnv)}})
	h.finishWith("succeeded", protocol.StackJobOutput{Sources: &snap,
		Services: []protocol.ComposeService{{Name: "web", Image: "nginx:1.27"}},
		Images:   []protocol.AppliedImage{{Service: "web", Image: "nginx:1.27", ImageID: "sha256:web"}},
		After:    []protocol.ServiceState{{Service: "web", Containers: 1, Running: 1}},
		Rename: &protocol.StackRenameReport{StackRenamePlan: protocol.StackRenamePlan{From: "shop", To: "store", FromDir: "shop", ToDir: "store",
			Running: []string{"web"},
			Volumes: []protocol.StackRenameVolume{{Key: "data", Name: "shop_data", NewName: "store_data", Action: protocol.RenameVolumeMove, Done: true},
				{Key: "cache", Name: "shop_cache", NewName: "store_cache", Action: protocol.RenameVolumeAbsent}},
			Containers: []protocol.StackRenameContainer{{ID: "c1", Name: "backup", Volumes: []string{"shop_data"}, NewID: "c2"}}},
			Stopped: true, DirMoved: true, Switched: true}})
	if j = h.job(j.ID); j.State != domain.JobSucceeded {
		t.Fatalf("rename %s: %s %s", j.State, j.ErrorClass, j.ErrorMessage)
	}
	cur := h.get(st.ID)
	if cur.Name != "store" || cur.Dir != "store" || cur.Revision != st.Revision+1 {
		t.Errorf("after rename: name %s dir %s revision %d", cur.Name, cur.Dir, cur.Revision)
	}
	if cur.Status != domain.StackDeployed || cur.Applied == nil || cur.Applied.Hash != snap.Hash || cur.UndeployedChanges() ||
		len(cur.Images) != 1 || cur.EngineState != domain.EngineStateRunning {
		t.Errorf("after rename: status %s applied %+v images %+v engine %s", cur.Status, cur.Applied, cur.Images, cur.EngineState)
	}
	if len(hook.got) != 1 {
		t.Fatalf("hook calls %+v", hook.got)
	}
	ev := hook.got[0]
	if ev.From != "shop" || ev.To != "store" || ev.EnvironmentID != env || len(ev.Volumes) != 1 || ev.Volumes["shop_data"] != "store_data" ||
		len(ev.Containers) != 1 || ev.Containers[0].NewID != "c2" {
		t.Errorf("hook event %+v", ev)
	}
	// The new name is taken now; the old one is free again.
	if _, err := h.svc.PreviewRename(h.ctx, h.create("other", shopYAML, shopEnv), "store"); !errors.Is(err, domain.ErrStackNameTaken) {
		t.Errorf("new name: %v", err)
	}
	if _, err := h.svc.PreviewRename(h.ctx, cur, "shop"); err != nil {
		t.Errorf("old name: %v", err)
	}
}

// TestRenameFailures: before the switch the agent undid everything and
// the stack keeps its name; after it the stack has the new name and is
// failed (a deploy finishes it).
func TestRenameFailures(t *testing.T) {
	for _, switched := range []bool{false, true} {
		h := newHarness(t)
		st := h.create("shop", shopYAML, shopEnv)
		hook := &renamedHook{}
		h.svc.OnRenamed(hook.hook)
		h.scriptRename(protocol.StackRenamePlan{})
		j, err := h.svc.Rename(h.ctx, alice, st, "store", domain.StackJobRequest{})
		if err != nil {
			t.Fatal(err)
		}
		snap := protocol.NewSourceSnapshot([]protocol.SourceFile{{Path: "compose.yaml", Content: []byte(shopYAML)}})
		h.finishWith("failed", protocol.StackJobOutput{Sources: &snap,
			Rename: &protocol.StackRenameReport{StackRenamePlan: protocol.StackRenamePlan{From: "shop", To: "store", FromDir: "shop", ToDir: "store"},
				Stopped: true, Switched: switched}})
		if j = h.job(j.ID); j.State != domain.JobFailed {
			t.Fatalf("switched %v: job %s", switched, j.State)
		}
		cur := h.get(st.ID)
		switch {
		case !switched && (cur.Name != "shop" || cur.Dir != "shop" || len(hook.got) != 0):
			t.Errorf("failed before the switch: name %s dir %s hooks %d", cur.Name, cur.Dir, len(hook.got))
		case switched && (cur.Name != "store" || cur.Status != domain.StackFailed || cur.Failed == nil || len(hook.got) != 1):
			t.Errorf("failed after the switch: name %s status %s failed %+v hooks %d", cur.Name, cur.Status, cur.Failed, len(hook.got))
		}
	}
}
