package migrations

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

func project(name string, networks ...protocol.MigrationNetworkFacts) *protocol.MigrationProjectFacts {
	return &protocol.MigrationProjectFacts{Name: name, Networks: networks}
}

func own(name string) protocol.MigrationNetworkFacts {
	return protocol.MigrationNetworkFacts{Key: name, Name: name}
}

func external(name string) protocol.MigrationNetworkFacts {
	return protocol.MigrationNetworkFacts{Key: name, Name: name, External: true}
}

func missingNetwork(name string) Finding {
	return Finding{Code: FindingExternalNetwork, Message: "missing", Resource: name}
}

// entry is a previewed stack with 10 bytes of project data, a 30 s
// downtime and a destination with room for 100 bytes.
func entry(id string, p *protocol.MigrationProjectFacts, blockers ...Finding) envEntry {
	return envEntry{stack: domain.Stack{ID: id, Name: p.Name}, g: gathered{
		plan: Plan{Blockers: append([]Finding{}, blockers...), Warnings: []Finding{},
			Data:     DataPlan{ProjectBytes: 10, TotalBytes: 10, TargetStacksFree: 100, TargetVolumesFree: 100},
			Downtime: Downtime{EstimatedSeconds: 30}},
		source: &protocol.MigrationSourceFacts{Project: p}, target: &protocol.MigrationDestinationFacts{}}}
}

// TestPlanEnvironment: the proxy moves before the stacks joining its
// network, whose "external network missing" blockers are settled; a
// network made by hand on the source is created first; Docker Manager's
// own stack and stacks the caller may not migrate are left out; the
// downtime is the longest group's.
func TestPlanEnvironment(t *testing.T) {
	entries := []envEntry{
		entry("st-app", project("app", external("proxy")), missingNetwork("proxy")),
		entry("st-blog", project("blog", external("legacy")), missingNetwork("legacy")),
		entry("st-proxy", project("proxy", own("proxy"))),
		{stack: domain.Stack{ID: "st-dm", Name: "docker-manager"}, g: gathered{source: &protocol.MigrationSourceFacts{
			Project: &protocol.MigrationProjectFacts{Name: "docker-manager", Protected: true}}}},
		{stack: domain.Stack{ID: "st-secret", Name: "secret"}, skipped: SkipNotPermitted},
	}
	nets := []protocol.NetworkInfo{{Name: "legacy", Driver: "bridge", Attachable: true,
		Labels: map[string]string{"team": "web", protocol.LabelMigration: "x"}}}
	p := planEnvironment("src", "dst", entries, nets, true)
	if !p.Allowed() {
		t.Fatalf("blocked: %v %+v", codes(p.Blockers), p.Stacks)
	}
	if want := [][]string{{"st-blog"}, {"st-proxy", "st-app"}}; !reflect.DeepEqual(p.Groups, want) {
		t.Errorf("groups %v, want %v", p.Groups, want)
	}
	var order []string
	for _, s := range p.Stacks {
		order = append(order, s.StackID)
	}
	if want := []string{"st-blog", "st-proxy", "st-app"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order %v", order)
	}
	if app := p.Stacks[2]; app.Group != 1 || !reflect.DeepEqual(app.DependsOn, []string{"st-proxy"}) || len(app.Plan.Blockers) != 0 {
		t.Errorf("app %+v", app)
	}
	if len(p.Networks) != 1 || p.Networks[0].Name != "legacy" || !p.Networks[0].Attachable ||
		!reflect.DeepEqual(p.Networks[0].UsedBy, []string{"st-blog"}) || p.Networks[0].Labels[protocol.LabelMigration] != "" {
		t.Errorf("networks %+v", p.Networks)
	}
	if !reflect.DeepEqual(codes(p.Warnings), []string{FindingNetworkCreated}) {
		t.Errorf("warnings %v", codes(p.Warnings))
	}
	want := []SkippedStack{{StackID: "st-dm", Name: "docker-manager", Reason: SkipDockerManager},
		{StackID: "st-secret", Name: "secret", Reason: SkipNotPermitted}}
	if !reflect.DeepEqual(p.Skipped, want) {
		t.Errorf("skipped %+v", p.Skipped)
	}
	if p.Data.ProjectBytes != 30 || p.Downtime.EstimatedSeconds != 60 {
		t.Errorf("data %+v downtime %+v", p.Data, p.Downtime)
	}
}

// TestPlanEnvironmentBlockers: what the migration cannot settle stays a
// blocker and nothing starts.
func TestPlanEnvironmentBlockers(t *testing.T) {
	blog := func() []envEntry {
		return []envEntry{entry("st-blog", project("blog", external("legacy")), missingNetwork("legacy"))}
	}
	cases := []struct {
		name    string
		entries []envEntry
		nets    []protocol.NetworkInfo
		create  bool
		want    string
	}{
		{"a hand-made network the caller may not create", blog(), []protocol.NetworkInfo{{Name: "legacy", Driver: "bridge"}}, false, FindingNetworkDenied},
		{"a hand-made macvlan network", blog(), []protocol.NetworkInfo{{Name: "legacy", Driver: "macvlan"}}, true, FindingNetworkNotCreatable},
		{"no stack left", []envEntry{{stack: domain.Stack{ID: "st-x", Name: "x"}, skipped: SkipNotPermitted}}, nil, true, FindingNoStacks},
		{"together too large", []envEntry{
			func() envEntry { e := entry("a", project("a")); e.g.plan.Data.ProjectBytes = 60; return e }(),
			func() envEntry { e := entry("b", project("b")); e.g.plan.Data.ProjectBytes = 60; return e }(),
		}, nil, true, FindingInsufficientSpace},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := planEnvironment("src", "dst", c.entries, c.nets, c.create)
			if p.Allowed() || !reflect.DeepEqual(codes(p.Blockers), []string{c.want}) {
				t.Errorf("blockers %v, want %s", codes(p.Blockers), c.want)
			}
		})
	}
	// A network missing on both sides stays the stack's own blocker.
	p := planEnvironment("src", "dst", blog(), nil, true)
	if p.Allowed() || len(p.Stacks) != 1 || !reflect.DeepEqual(codes(p.Stacks[0].Plan.Blockers), []string{FindingExternalNetwork}) {
		t.Errorf("plan %+v", p)
	}
}

// runChildren makes the fake engine run what an environment migration
// enqueues: each stack migration's attempt (ending its job as the attempt
// did, with the finish hook the engine runs).
func (w *world) runChildren() {
	w.jobs.onEnqueue = func(j domain.Job) {
		if j.Kind != jobspec.StackMigrate {
			return
		}
		go func() {
			st, _, err := w.run(j, nil)
			done := j
			done.State = domain.JobSucceeded
			if err != nil || st.Outcome.Outcome != jobexec.OutcomeSucceeded {
				done.State = domain.JobFailed
			}
			if err := w.svc.onMigrationFinished(w.ctx, w.svc.db, done); err != nil {
				w.t.Error(err)
			}
			w.jobs.put(done)
		}()
	}
}

// runEnvironment previews and runs an environment migration to its end.
func (w *world) runEnvironment() (domain.Job, *jobexec.State) {
	w.t.Helper()
	w.runChildren()
	j, m, err := w.svc.StartEnvironment(w.ctx, w.user, srcEnv, EnvironmentRequest{TargetEnvironmentID: dstEnv})
	if err != nil {
		w.t.Fatal(err)
	}
	if m.ID != j.ID || len(m.Stacks) != 1 || m.Stacks[0].State != domain.EnvironmentStackPending {
		w.t.Fatalf("record %+v", m)
	}
	st := &jobexec.State{JobID: j.ID, Attempt: 1, Kind: j.Kind, Input: j.Input}
	res, err := jobexec.Run(w.ctx, w.svc.environmentExecutor(), st, jobexec.Options{Journal: memJournal{}})
	if err != nil {
		w.t.Fatal(err)
	}
	st.Outcome = &res
	done := j
	done.State = domain.JobSucceeded
	if res.Outcome != jobexec.OutcomeSucceeded {
		done.State = domain.JobFailed
	}
	if err := w.svc.onEnvironmentMigrationFinished(w.ctx, w.svc.db, done); err != nil {
		w.t.Fatal(err)
	}
	return j, st
}

// TestEnvironmentMigrationMovesTheStacks: the environment's stack moves as
// its own stack migration; the record lists it as moved with that
// migration; the source stays stopped and the destination runs it.
func TestEnvironmentMigrationMovesTheStacks(t *testing.T) {
	w := newWorld(t)
	j, st := w.runEnvironment()
	if st.Outcome.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("outcome %+v", st.Outcome)
	}
	m, err := w.svc.GetEnvironmentMigration(w.ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.State != domain.MigrationCompleted || m.FinishedAt == nil || len(m.Stacks) != 1 {
		t.Fatalf("record %+v", m)
	}
	s := m.Stacks[0]
	if s.StackID != "st-shop" || s.State != domain.EnvironmentStackMoved || s.MigrationID == "" {
		t.Fatalf("stack %+v", s)
	}
	if child := w.record(s.MigrationID); child.State != domain.MigrationCompleted || child.StackID != "st-shop" {
		t.Errorf("stack migration %+v", child)
	}
	if w.stack().EnvironmentID != dstEnv || !w.running(w.dst, "shop-db-1", "shop-web-1") || !w.stopped(w.src, "shop-db-1", "shop-web-1") {
		t.Error("the stack did not move")
	}
	if w.agents.called(srcEnv, protocol.ReqMigrationStart) != 0 {
		t.Error("the source was started again")
	}
	list, err := w.svc.EnvironmentMigrations(w.ctx, srcEnv, 5)
	if err != nil || len(list) != 1 || list[0].ID != j.ID {
		t.Errorf("list %+v %v", list, err)
	}
}

// TestEnvironmentMigrationStackFailureRestartsGroup: when a stack does
// not move (its destination deploy fails), it is back on the source and
// the services the environment migration stopped run there again; the
// run fails with guidance and the record says which stack failed.
func TestEnvironmentMigrationStackFailureRestartsGroup(t *testing.T) {
	w := newWorld(t)
	w.jobs.deployOutcome = domain.JobFailed
	j, st := w.runEnvironment()
	if st.Outcome.Outcome == jobexec.OutcomeSucceeded || st.Outcome.ErrorClass != ClassStackNotMoved {
		t.Fatalf("outcome %+v", st.Outcome)
	}
	if w.stack().EnvironmentID != srcEnv {
		t.Error("the stack must be back on the source")
	}
	if !w.running(w.src, "shop-db-1", "shop-web-1") {
		t.Error("the group's services must run on the source again")
	}
	m, err := w.svc.GetEnvironmentMigration(w.ctx, j.ID)
	if err != nil || m.State != domain.MigrationFailed || m.Stacks[0].State != domain.EnvironmentStackFailed {
		t.Errorf("record %+v %v", m, err)
	}
}

// TestEnvironmentMigrationBlocked: a blocker of a stack refuses the
// environment migration before anything stops.
func TestEnvironmentMigrationBlocked(t *testing.T) {
	w := newWorld(t)
	w.dst.Engine.AddContainer(engineSpec("proxy", 8080), true)
	_, _, err := w.svc.StartEnvironment(w.ctx, w.user, srcEnv, EnvironmentRequest{TargetEnvironmentID: dstEnv})
	var be *EnvironmentBlockedError
	if !errors.As(err, &be) || !errors.Is(err, ErrBlocked) || be.Plan.Stacks[0].Plan.Blockers[0].Code != FindingPortConflict {
		t.Fatalf("error %v", err)
	}
	if len(w.jobs.enqueued) != 0 || w.agents.called(srcEnv, protocol.ReqMigrationStop) != 0 {
		t.Fatal("a blocked migration changed something")
	}
}

// TestPlanEnvironmentHasNoStackLimit: any number of stacks moves in one
// migration; only the destination's space and conflicts can block it.
func TestPlanEnvironmentHasNoStackLimit(t *testing.T) {
	var entries []envEntry
	for i := range 150 {
		e := entry(fmt.Sprintf("st-%03d", i), project(fmt.Sprintf("app%03d", i)))
		e.g.plan.Data.ProjectBytes, e.g.plan.Data.TargetStacksFree = 1, 1000
		entries = append(entries, e)
	}
	p := planEnvironment("src", "dst", entries, nil, true)
	if !p.Allowed() || len(p.Stacks) != 150 || len(p.Groups) != 150 {
		t.Fatalf("blockers %v, %d stacks in %d groups", codes(p.Blockers), len(p.Stacks), len(p.Groups))
	}
}
