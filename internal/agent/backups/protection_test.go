package backups

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/lifecycle"
	"github.com/neurekadev/dockyard/internal/backup"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestShutdownPlanExcludesDockYardProject (#32 × #10): a backup with
// container shutdown never stops DockYard's own Compose project (it runs
// the manager and the agent): the preview gives none of its containers a
// stop order and says it is backed up live, and the run stops only the
// other stack of the same backup, then snapshots DockYard's project live.
func TestShutdownPlanExcludesDockYardProject(t *testing.T) {
	e := newEnv(t)
	write(t, filepath.Join(e.stacks, "dockyard", "compose.yaml"), "services:\n  dockyard-manager:\n    image: code.neureka.dev/dockyard/dockyard-manager:edge\n"+
		"  dockyard-agent:\n    image: code.neureka.dev/dockyard/dockyard-agent:edge\n    depends_on: [dockyard-manager]\n"+
		"  caddy:\n    image: caddy:2\n    depends_on: [dockyard-manager]\n")
	lbl := func(svc, role, deps string) map[string]string {
		l := map[string]string{lifecycle.ComposeProjectLabel: "dockyard", lifecycle.ComposeServiceLabel: svc}
		if role != "" {
			l[protocol.LabelRole] = role
		}
		if deps != "" {
			l[lifecycle.DependsOnLabel] = deps
		}
		return l
	}
	e.eng.AddContainer(engine.ContainerSpec{Name: "dockyard-dockyard-manager-1", Image: "code.neureka.dev/dockyard/dockyard-manager:edge",
		Labels: lbl("dockyard-manager", "manager", "")}, true)
	e.eng.AddContainer(engine.ContainerSpec{Name: "dockyard-dockyard-agent-1", Image: "code.neureka.dev/dockyard/dockyard-agent:edge",
		Labels: lbl("dockyard-agent", "agent", "dockyard-manager:service_started:false:true")}, true)
	e.eng.AddContainer(engine.ContainerSpec{Name: "dockyard-caddy-1", Image: "caddy:2",
		Labels: lbl("caddy", "", "dockyard-manager:service_started:false:true")}, true)
	own := protocol.BackupItem{Kind: backup.MemberStack, StackID: "st-dockyard", StackName: "dockyard",
		Project: &protocol.ProjectRef{Root: protocol.RootStacks, Dir: "dockyard", ProjectName: "dockyard"}}

	raw, _ := json.Marshal(protocol.BackupScopePreviewInput{Items: []protocol.BackupItem{own}, Shutdown: true})
	out, err := e.svc.scopePreview(testutil.Context(t), raw)
	if err != nil {
		t.Fatal(err)
	}
	pv := out.(protocol.BackupScopePreviewOutput)
	if len(pv.Items) != 1 || pv.Items[0].Error != "" {
		t.Fatalf("preview %+v", pv)
	}
	protected := 0
	for _, a := range pv.Items[0].Affected {
		if a.StopOrder != 0 {
			t.Errorf("DockYard's container %s would be stopped (order %d)", a.Name, a.StopOrder)
		}
		if a.Protected != "" {
			protected++
		}
	}
	// Every container of DockYard's project is protected (the proxy too).
	if protected != 3 || !slices.ContainsFunc(pv.Items[0].Conflicts, func(c string) bool { return strings.Contains(c, "backed up live") }) {
		t.Errorf("preview of DockYard's project: %+v", pv.Items[0])
	}

	res, _, err := e.run(testutil.Context(t), jobspec.BackupRun, e.runInput(true, own, stackItem(protocol.BackupRules{})), e.credential("DYRK-TEST"), nil)
	if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("outcome %+v, %v", res, err)
	}
	for _, ev := range e.eng.log() {
		if strings.HasPrefix(ev, "stop:dockyard-") || ev == "stop:caddy" {
			t.Errorf("DockYard's project was stopped: %v", e.eng.log())
		}
	}
	if want := []string{"stop:web", "stop:api", "stop:db", "start:db", "start:api", "start:web"}; !slices.Equal(e.eng.log(), want) {
		t.Errorf("lifecycle = %v, want %v (the other stack only)", e.eng.log(), want)
	}
	o := outputOf(t, res)
	if o.Shutdown == nil || !slices.ContainsFunc(o.Shutdown.Conflicts, func(c string) bool { return strings.Contains(c, "dockyard") && strings.Contains(c, "live") }) {
		t.Errorf("shutdown report %+v", o.Shutdown)
	}
	for _, m := range o.Members {
		want := backup.ConsistencyShutdown
		if m.StackID == "st-dockyard" {
			want = backup.ConsistencyLive
		}
		if m.Kind == backup.MemberStack && (m.State != backup.StateComplete || m.Consistency != want) {
			t.Errorf("member %s: %s %s, want complete %s", m.StackID, m.State, m.Consistency, want)
		}
	}
}
