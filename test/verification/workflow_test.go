package verification

import (
	"slices"
	"sort"
	"strings"
	"testing"
)

// TestExtendedWorkflowRunsEveryTaggedTest (#29): every test that only
// builds with the integration, e2e or faultinject tag is compiled and
// selected by at least one literal `go test` invocation of extended.yaml
// (its -tags, -run filter and packages), so no written-only test is
// silently never executed. Untagged tests run in the PR suite.
func TestExtendedWorkflowRunsEveryTaggedTest(t *testing.T) {
	tests, _ := index(t)
	runs := goTestRuns(t, loadWorkflow(t, "extended.yaml"))
	if len(runs) == 0 {
		t.Fatal("extended.yaml has no go test invocations")
	}
	var missing []string
	checked := 0
	for name, gs := range tests {
		if name == "TestMain" {
			continue
		}
		for _, g := range gs {
			if !g.needsTags() {
				continue
			}
			checked++
			if !slices.ContainsFunc(runs, func(r goTestRun) bool { return r.covers(g) }) {
				missing = append(missing, g.Dir+"."+name+" ["+g.Constraint.String()+"]")
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("tagged tests no extended.yaml job runs (fix a job's -tags/-run or rename the test):\n  %s", strings.Join(missing, "\n  "))
	}
	if checked < 30 {
		t.Errorf("only %d tagged tests found; the index looks broken", checked)
	}
}

// TestGoTestRunsParse keeps the workflow parser honest on the shapes the
// extended workflow uses.
func TestGoTestRunsParse(t *testing.T) {
	w := workflow{Jobs: map[string]workflowJob{"x": {Steps: []workflowStep{{Run: "" +
		"go test -tags integration,faultinject -count=1 -timeout 25m -run 'KilledMidTransfer$' -v ./internal/manager/migrations/ 2>&1 | tee x.log\n" +
		"go test -tags e2e \\\n  -run '^TestE2E' ./internal/manager/server/\n" +
		"go test -tags faultinject -count=1 ./...\n"}}}}}
	runs := goTestRuns(t, w)
	if len(runs) != 3 {
		t.Fatalf("runs %+v", runs)
	}
	g := goTest{Name: "TestEngineManagerKilledMidTransfer", Dir: "internal/manager/migrations"}
	var err error
	if g.Constraint, err = parseConstraint("//go:build integration && faultinject"); err != nil {
		t.Fatal(err)
	}
	if !runs[0].covers(g) || runs[1].covers(g) || runs[2].covers(g) {
		t.Errorf("coverage of %s: %v %v %v", g.Name, runs[0].covers(g), runs[1].covers(g), runs[2].covers(g))
	}
	e := goTest{Name: "TestE2ERoutes", Dir: "internal/manager/server"}
	if e.Constraint, err = parseConstraint("//go:build e2e"); err != nil {
		t.Fatal(err)
	}
	if !runs[1].covers(e) || runs[0].covers(e) {
		t.Error("TestE2ERoutes coverage")
	}
	if !g.needsTags() || !g.dockerTagged() || !e.dockerTagged() {
		t.Error("tag classification")
	}
	f := goTest{Name: "TestKillAtEveryStage", Dir: "internal/manager/jobs/faulttest"}
	if f.Constraint, err = parseConstraint("//go:build faultinject"); err != nil {
		t.Fatal(err)
	}
	if !f.needsTags() || f.dockerTagged() || !runs[2].covers(f) {
		t.Error("faultinject classification")
	}
}
