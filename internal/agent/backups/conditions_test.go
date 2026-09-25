package backups

import (
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

// Dependency conditions on the way back up (#7 Done-when 1: "dependency
// order and conditions are verified for ... backup resume and restore,
// including an unhealthy dependency and a completed one-shot service").
// The "shop" project: db has a health check, migrate is a one-shot that
// completed before the backup (exited 0, not running), web needs db
// healthy and migrate completed successfully.

const shopYAML = `services:
  db:
    image: postgres:17
    healthcheck:
      test: ["CMD", "pg_isready"]
  migrate:
    image: example/migrate:1
    depends_on:
      db:
        condition: service_healthy
  web:
    image: nginx:1.27
    depends_on:
      db:
        condition: service_healthy
      migrate:
        condition: service_completed_successfully
`

func seedShop(t *testing.T, e *env) protocol.BackupItem {
	t.Helper()
	write(t, filepath.Join(e.stacks, "shop", "compose.yaml"), shopYAML)
	write(t, filepath.Join(e.stacks, "shop", "index.html"), "shop")
	lbl := func(svc string, deps ...lifecycle.Dependency) map[string]string {
		return map[string]string{lifecycle.ComposeProjectLabel: "shop", lifecycle.ComposeServiceLabel: svc,
			lifecycle.DependsOnLabel: lifecycle.FormatDependsOn(deps)}
	}
	healthyDB := lifecycle.Dependency{Service: "db", Condition: lifecycle.ConditionHealthy, Required: true}
	e.eng.AddContainer(engine.ContainerSpec{Name: "shop-db-1", Image: "postgres:17", Labels: lbl("db")}, false)
	e.eng.AddContainer(engine.ContainerSpec{Name: "shop-migrate-1", Image: "example/migrate:1", Labels: lbl("migrate", healthyDB)}, false)
	e.eng.AddContainer(engine.ContainerSpec{Name: "shop-web-1", Image: "nginx:1.27", Labels: lbl("web", healthyDB,
		lifecycle.Dependency{Service: "migrate", Condition: lifecycle.ConditionCompleted, Required: true})}, false)
	// db is up and healthy, migrate ran to completion (exit 0), web runs.
	e.eng.SetStartHealth("postgres:17", "healthy")
	ctx := testutil.Context(t)
	for _, n := range []string{"shop-db-1", "shop-web-1"} {
		if err := e.eng.Engine.StartContainer(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	e.eng.SetContainerState("shop-migrate-1", "exited")
	return protocol.BackupItem{Kind: backup.MemberStack, StackID: "st-shop", StackName: "shop",
		Project: &protocol.ProjectRef{Root: protocol.RootStacks, Dir: "shop", ProjectName: "shop"}}
}

// shopLog is the lifecycle log restricted to the shop services.
func shopLog(e *env) []string {
	var out []string
	for _, ev := range e.eng.log() {
		_, svc, _ := strings.Cut(ev, ":")
		if svc == "db" || svc == "migrate" || svc == "web" {
			out = append(out, ev)
		}
	}
	return out
}

func TestBackupResumeHonorsDependencyConditions(t *testing.T) {
	t.Run("completed one-shot and healthy dependency", func(t *testing.T) {
		e := newEnv(t)
		shop := seedShop(t, e)
		res, _, err := e.run(testutil.Context(t), jobspec.BackupRun, e.runInput(true, shop), e.credential("DYRK-TEST"), nil)
		if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
			t.Fatalf("outcome %+v, %v", res, err)
		}
		// Dependents stop first; db comes back healthy before web starts;
		// the completed one-shot satisfies web and is never re-run.
		if want := []string{"stop:web", "stop:db", "start:db", "start:web"}; !slices.Equal(shopLog(e), want) {
			t.Fatalf("lifecycle %v, want %v", shopLog(e), want)
		}
		if !e.eng.containerRunning("shop-web-1") || !e.eng.containerRunning("shop-db-1") || e.eng.containerRunning("shop-migrate-1") {
			t.Error("web and db must run again, the one-shot must not")
		}
	})
	t.Run("unhealthy dependency", func(t *testing.T) {
		e := newEnv(t)
		shop := seedShop(t, e)
		// db comes back unhealthy after the snapshot.
		e.eng.SetStartHealth("postgres:17", "unhealthy")
		res, _, err := e.run(testutil.Context(t), jobspec.BackupRun, e.runInput(true, shop), e.credential("DYRK-TEST"), nil)
		if err != nil || res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != "restart_failed" {
			t.Fatalf("outcome %+v, %v", res, err)
		}
		if e.eng.containerRunning("shop-web-1") {
			t.Errorf("web was started although its healthy dependency is unhealthy: %v %+v", shopLog(e), res)
		}
		if !e.eng.containerRunning("shop-db-1") {
			t.Error("db was not started again")
		}
		out := outputOf(t, res)
		if out.Shutdown == nil || !strings.Contains(out.Shutdown.RestartError, "db") {
			t.Errorf("shutdown report %+v", out.Shutdown)
		}
		// The snapshot itself was taken (the backup completed).
		for _, m := range out.Members {
			if m.StackID == "st-shop" && (m.State != backup.StateComplete || m.SnapshotID == "") {
				t.Errorf("member %+v", m)
			}
		}
	})
}

func TestRestoreHonorsDependencyConditions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		health    string
		outcome   string
		webRunsAt bool
	}{
		{"completed one-shot and healthy dependency", "healthy", jobexec.OutcomeSucceeded, true},
		{"unhealthy dependency", "unhealthy", jobexec.OutcomeFailed, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			shop := seedShop(t, e)
			res, _, err := e.run(testutil.Context(t), jobspec.BackupRun, e.runInput(false, shop), e.credential(restoreKey), nil)
			if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
				t.Fatalf("backup %+v, %v", res, err)
			}
			var m backup.Member
			for _, x := range outputOf(t, res).Members {
				if x.StackID == "st-shop" {
					m = x
				}
			}
			write(t, filepath.Join(e.stacks, "shop", "index.html"), "edited")
			e.eng.SetStartHealth("postgres:17", tc.health)
			in := protocol.RestoreRunInput{Repository: e.repoRef(), SnapshotID: m.SnapshotID, Scope: protocol.RestoreScopeStack,
				SnapshotPaths: m.Paths, Shutdown: true, StackID: "st-shop", StackName: "shop", Project: shop.Project, ProjectSource: m.ProjectPath}
			res, _, err = e.run(testutil.Context(t), jobspec.RestoreRun, in, e.credential(restoreKey), nil)
			if err != nil || res.Outcome != tc.outcome {
				t.Fatalf("restore %+v, %v", res, err)
			}
			if read(t, filepath.Join(e.stacks, "shop", "index.html")) != "shop" {
				t.Error("the project directory was not restored")
			}
			// Dependents stop first, db starts first; web only once db is
			// healthy (an unhealthy db fails the job, and its start_containers
			// compensation tries once more without starting web either).
			got := shopLog(e)
			if tc.webRunsAt && !slices.Equal(got, []string{"stop:web", "stop:db", "start:db", "start:web"}) ||
				!tc.webRunsAt && (len(got) < 3 || !slices.Equal(got[:3], []string{"stop:web", "stop:db", "start:db"}) || slices.Contains(got, "start:web")) {
				t.Fatalf("lifecycle %v", got)
			}
			if e.eng.containerRunning("shop-web-1") != tc.webRunsAt || !e.eng.containerRunning("shop-db-1") || e.eng.containerRunning("shop-migrate-1") {
				t.Errorf("after the restore: web %v db %v migrate %v", e.eng.containerRunning("shop-web-1"), e.eng.containerRunning("shop-db-1"), e.eng.containerRunning("shop-migrate-1"))
			}
		})
	}
}
