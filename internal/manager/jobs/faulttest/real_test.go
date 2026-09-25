//go:build faultinject

package faulttest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/backups"
	"github.com/neurekadev/dockyard/internal/agent/compose"
	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/engine/enginefake"
	"github.com/neurekadev/dockyard/internal/agent/lifecycle"
	"github.com/neurekadev/dockyard/internal/agent/protect"
	"github.com/neurekadev/dockyard/internal/agent/prune"
	"github.com/neurekadev/dockyard/internal/agent/stacks"
	"github.com/neurekadev/dockyard/internal/agent/storage"
	"github.com/neurekadev/dockyard/internal/backup"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/jobs/jobstest"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/restic/restictest"
)

// The #26 crash acceptance test with the features' own executors: the
// agent runs the real stack.deploy (#7), backup.run with container
// shutdown (#10) and prune.run (#14) executors. The Docker Engine is the
// in-memory enginefake and restic the in-memory restictest store, both
// backed by a file (Persist) so they outlive every killed agent process
// like a real Engine and a repository on disk would; Compose is a small
// converging fake over that Engine (the SDK needs a real Engine API). The
// harness (run, checkInvariants) is TestKillAtEveryStage's: every fault
// point a clean run reaches, in the manager and in the agent, is a crash
// scenario. On top of the job invariants (terminal state with recovery
// guidance, no held locks, non-idempotent steps at most once), each
// scenario checks the world it left behind.

const (
	faultRecoveryKey = "DYRK-FAULT-HARNESS-KEY"
	faultRepository  = "repo-1"
	shopStackID      = "st-shop"
)

var faultStart = time.Date(2026, 9, 25, 2, 0, 0, 0, time.UTC)

const faultShopYAML = `services:
  db:
    image: postgres:17
  web:
    image: nginx:1.27
    depends_on:
      db:
        condition: service_started
`

// world locates a scenario's persistent state below the run's directory.
type world struct{ base string }

func (w world) stacks() string     { return filepath.Join(w.base, "stacks") }
func (w world) volumes() string    { return filepath.Join(w.base, "volumes") }
func (w world) backups() string    { return filepath.Join(w.base, "backups") }
func (w world) enginePath() string { return filepath.Join(w.base, "engine.json") }
func (w world) resticPath() string { return filepath.Join(w.base, "restic.json") }

func (w world) storage() *storage.Result {
	s := filepath.ToSlash
	return &storage.Result{StacksDir: s(w.stacks()), VolumesDir: s(w.volumes()), DockerRootDir: s(filepath.Join(w.base, "docker")),
		Roots: []storage.Root{{Kind: storage.KindStacks, Path: s(w.stacks()), OK: true}, {Kind: storage.KindVolumes, Path: s(w.volumes()), OK: true}}}
}

// engine opens the persistent Engine (creating it on first use).
func (w world) engine() (*enginefake.Engine, error) {
	fe := enginefake.New("FAULT-ENGINE")
	return fe, fe.Persist(w.enginePath())
}

func (w world) restic() (*restictest.Store, error) {
	s := restictest.New(func() time.Time { return faultStart })
	return s, s.Persist(w.resticPath())
}

func (w world) repository() protocol.BackupRepositoryRef {
	return protocol.BackupRepositoryRef{RepositoryID: faultRepository,
		Destination: backup.Destination{Kind: backup.KindLocal, Path: filepath.ToSlash(w.backups())},
		Scope:       backup.EnvironmentScope(environmentID), KeyGeneration: 1, KeyFingerprint: "rk_00000000000000ff"}
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func shopRef() protocol.ProjectRef {
	return protocol.ProjectRef{Root: protocol.RootStacks, Dir: "shop", ProjectName: "shop"}
}

func shopLabels(svc string, deps ...lifecycle.Dependency) map[string]string {
	return map[string]string{lifecycle.ComposeProjectLabel: "shop", lifecycle.ComposeServiceLabel: svc,
		lifecycle.DependsOnLabel: lifecycle.FormatDependsOn(deps)}
}

// convergingComposer is Compose over the Engine for the deploy scenario:
// Load is the real loader; Up converges like `compose up` (dependencies
// first, a missing container is created once, a stopped one is started,
// a running one is left alone), so repeating it after a crash is safe.
type convergingComposer struct{ eng engine.Engine }

func (c convergingComposer) Load(ctx context.Context, spec compose.ProjectSpec) (*compose.Project, error) {
	return compose.LoadProject(ctx, spec)
}

func (c convergingComposer) Up(ctx context.Context, p *compose.Project, _ compose.UpOptions) error {
	done := map[string]bool{}
	var up func(svc compose.ServiceInfo) error
	byName := map[string]compose.ServiceInfo{}
	for _, s := range p.Services {
		byName[s.Name] = s
	}
	up = func(svc compose.ServiceInfo) error {
		if done[svc.Name] {
			return nil
		}
		done[svc.Name] = true
		var deps []lifecycle.Dependency
		for _, d := range svc.DependsOn {
			if dep, ok := byName[d.Service]; ok {
				if err := up(dep); err != nil {
					return err
				}
			}
			deps = append(deps, lifecycle.Dependency{Service: d.Service, Condition: d.Condition, Required: d.Required, Restart: d.Restart})
		}
		name := p.Name + "-" + svc.Name + "-1"
		d, err := c.eng.InspectContainer(ctx, name)
		switch {
		case engine.IsCode(err, engine.CodeNotFound):
			if _, err := c.eng.InspectImage(ctx, svc.Image); engine.IsCode(err, engine.CodeNotFound) {
				if _, err := c.eng.PullImage(ctx, svc.Image, engine.PullOptions{}); err != nil {
					return err
				}
			}
			labels := map[string]string{lifecycle.ComposeProjectLabel: p.Name, lifecycle.ComposeServiceLabel: svc.Name,
				lifecycle.DependsOnLabel: lifecycle.FormatDependsOn(deps)}
			id, _, err := c.eng.CreateContainer(ctx, engine.ContainerSpec{Name: name, Image: svc.Image, Labels: labels})
			if err != nil {
				return err
			}
			d.ID = id
		case err != nil:
			return err
		}
		if d.State.Running {
			return nil
		}
		return c.eng.StartContainer(ctx, d.ID)
	}
	for _, s := range p.Services {
		if err := up(s); err != nil {
			return err
		}
	}
	return nil
}

func (convergingComposer) Pull(context.Context, *compose.Project, compose.RunOptions) error {
	return nil
}
func (convergingComposer) Build(context.Context, *compose.Project, compose.BuildOptions) error {
	return nil
}
func (convergingComposer) Down(context.Context, string, *compose.Project, compose.DownOptions) error {
	return fmt.Errorf("not used by the deploy scenario")
}
func (convergingComposer) Create(context.Context, *compose.Project, compose.CreateOptions) error {
	return fmt.Errorf("not used by the deploy scenario")
}

type stackDeps struct {
	c   stacks.Composer
	eng engine.Engine
	st  *storage.Result
}

func (d stackDeps) Composer() stacks.Composer { return d.c }
func (d stackDeps) Engine() engine.Engine     { return d.eng }
func (d stackDeps) Storage() *storage.Result  { return d.st }

type projectLoader struct{}

func (projectLoader) Load(ctx context.Context, spec compose.ProjectSpec) (*compose.Project, error) {
	return compose.LoadProject(ctx, spec)
}

func pick(execs []jobexec.Executor, kind domain.JobKind) []jobexec.Executor {
	for _, x := range execs {
		if x.Kind == kind {
			return []jobexec.Executor{x}
		}
	}
	return nil
}

// withEffects records "<job>:<step>" when a real step returns nil and
// "*:compensate:<name>" when a compensation succeeds (a compensation does
// not know its job; the harness runs one job), in the same effects file as
// the simulated executors.
func withEffects(execs []jobexec.Executor, fx *jobstest.Effects) []jobexec.Executor {
	out := make([]jobexec.Executor, 0, len(execs))
	for _, x := range execs {
		steps := make(map[string]jobexec.StepFunc, len(x.Steps))
		for name, fn := range x.Steps {
			steps[name] = func(ctx context.Context, sc *jobexec.StepContext) error {
				if err := fn(ctx, sc); err != nil {
					return err
				}
				fx.Add(sc.JobID + ":" + name)
				return nil
			}
		}
		comps := make(map[string]jobexec.CompensationFunc, len(x.Compensations))
		for name, fn := range x.Compensations {
			comps[name] = func(ctx context.Context, args json.RawMessage) error {
				if err := fn(ctx, args); err != nil {
					return err
				}
				fx.Add("*:compensate:" + name)
				return nil
			}
		}
		x.Steps, x.Compensations = steps, comps
		out = append(out, x)
	}
	return out
}

var realScenarios = []scenario{
	{
		name: "real_deploy", kind: jobspec.StackDeploy, targets: []domain.JobTarget{{Type: domain.TargetStack, ID: shopStackID}},
		seed: func(t *testing.T, w world) {
			writeFile(t, filepath.Join(w.stacks(), "shop", "compose.yaml"), faultShopYAML)
			if _, err := w.engine(); err != nil {
				t.Fatal(err)
			}
		},
		input: func(world) any { return protocol.StackJobInput{StackID: shopStackID, Stack: shopRef()} },
		executors: func(w world, log *slog.Logger) ([]jobexec.Executor, error) {
			fe, err := w.engine()
			if err != nil {
				return nil, err
			}
			svc := stacks.New(stacks.Options{Deps: stackDeps{c: convergingComposer{eng: fe}, eng: fe, st: w.storage()}, Logger: log})
			return pick(svc.Executors(), jobspec.StackDeploy), nil
		},
		check: func(t *testing.T, w world, res outcome) {
			fe, err := w.engine()
			if err != nil {
				t.Fatal(err)
			}
			// Each service's container was created at most once, whatever
			// ran again after the crash; a finished deploy runs both.
			creates := 0
			for _, c := range fe.Calls() {
				if c == "container.create" {
					creates++
				}
			}
			names := fe.ContainerNames()
			if creates > 2 || len(names) > 2 {
				t.Fatalf("containers %v created %d times: a step ran twice", names, creates)
			}
			if res.job.State == domain.JobSucceeded {
				for _, n := range []string{"shop-db-1", "shop-web-1"} {
					if c, ok := fe.Container(n); !ok || !c.Details.State.Running {
						t.Fatalf("deploy succeeded but %s does not run", n)
					}
				}
			}
			// The deploy never wrote the definition.
			if b, _ := os.ReadFile(filepath.Join(w.stacks(), "shop", "compose.yaml")); string(b) != faultShopYAML {
				t.Fatal("the deploy changed compose.yaml")
			}
		},
	},
	{
		name: "real_backup_with_shutdown", kind: jobspec.BackupRun,
		targets: []domain.JobTarget{{Type: domain.TargetStack, ID: shopStackID}, {Type: domain.TargetRepository, ID: faultRepository}},
		seed: func(t *testing.T, w world) {
			writeFile(t, filepath.Join(w.stacks(), "shop", "compose.yaml"), faultShopYAML)
			writeFile(t, filepath.Join(w.stacks(), "shop", "html", "index.html"), "<h1>shop</h1>")
			if err := os.MkdirAll(w.backups(), 0o750); err != nil {
				t.Fatal(err)
			}
			fe, err := w.engine()
			if err != nil {
				t.Fatal(err)
			}
			fe.AddContainer(engine.ContainerSpec{Name: "shop-db-1", Image: "postgres:17", Labels: shopLabels("db")}, true)
			fe.AddContainer(engine.ContainerSpec{Name: "shop-web-1", Image: "nginx:1.27",
				Labels: shopLabels("web", lifecycle.Dependency{Service: "db", Condition: lifecycle.ConditionStarted, Required: true})}, true)
			if _, err := w.restic(); err != nil {
				t.Fatal(err)
			}
		},
		input: func(w world) any {
			return protocol.BackupRunInput{SetID: "set-1", PolicyID: "pol-1", PolicyName: "Nightly", InstanceID: "inst-1", EnvironmentName: "fault",
				Repository: w.repository(), Shutdown: true, StartedAt: faultStart,
				Items: []protocol.BackupItem{{Kind: backup.MemberStack, StackID: shopStackID, StackName: "shop", Project: func() *protocol.ProjectRef {
					r := shopRef()
					return &r
				}()}}}
		},
		secrets: &protocol.CommandSecrets{Repositories: []protocol.RepositoryCredential{{RepositoryID: faultRepository, Password: faultRecoveryKey}}},
		executors: func(w world, log *slog.Logger) ([]jobexec.Executor, error) {
			fe, err := w.engine()
			if err != nil {
				return nil, err
			}
			store, err := w.restic()
			if err != nil {
				return nil, err
			}
			svc := backups.New(backups.Options{Engine: func() engine.Engine { return fe }, Loader: func() backups.Loader { return projectLoader{} },
				Storage: func() *storage.Result { return w.storage() }, Guard: protect.New(protect.Options{Logger: log}), Restic: store,
				LocalRoots: []string{filepath.ToSlash(w.backups())}, Logger: log, WaitTimeout: 10 * time.Second})
			return pick(svc.Executors(), jobspec.BackupRun), nil
		},
		check: func(t *testing.T, w world, res outcome) {
			fe, err := w.engine()
			if err != nil {
				t.Fatal(err)
			}
			// Whatever happened, the containers stopped for the backup run
			// again: the start_containers step or its compensation.
			for _, n := range []string{"shop-db-1", "shop-web-1"} {
				if c, ok := fe.Container(n); !ok || !c.Details.State.Running {
					t.Fatalf("%s left stopped (job %s %s)", n, res.job.State, res.job.ErrorClass)
				}
			}
			store, err := w.restic()
			if err != nil {
				t.Fatal(err)
			}
			snaps := 0
			for _, sn := range store.Snapshots(w.repository().Destination.Repository(w.repository().Scope)) {
				if !sn.HasTag(backup.TagManifest) {
					snaps++
				}
			}
			// The snapshot (not idempotent) is written at most once; a
			// finished backup wrote exactly one.
			if snaps > 1 || (res.job.State == domain.JobSucceeded && snaps != 1) {
				t.Fatalf("%d stack snapshots (job %s)", snaps, res.job.State)
			}
		},
	},
	{
		name: "real_prune", kind: jobspec.PruneRun,
		seed: func(t *testing.T, w world) {
			fe, err := w.engine()
			if err != nil {
				t.Fatal(err)
			}
			old := faultStart.AddDate(0, -2, 0)
			fe.SetImageCreated(fe.AddImage("app:old"), old)
			fe.AddContainer(engine.ContainerSpec{Name: "web", Image: "nginx:1.27"}, true)
			for name, labels := range map[string]map[string]string{"old-job": nil, "dockyard-agent-old": {protocol.LabelRole: "agent"}} {
				fe.AddContainer(engine.ContainerSpec{Name: name, Image: "nginx:1.27", Labels: labels}, false)
				fe.SetContainerState(name, "exited")
				fe.SetContainerTimes(name, old, old)
			}
		},
		input: func(world) any {
			return protocol.PruneInput{PolicyID: "pol-1", Rules: []protocol.PruneRule{
				{Category: protocol.PruneStoppedContainers, MinAgeSeconds: 3600},
				{Category: protocol.PruneUnusedImages, MinAgeSeconds: 3600}}}
		},
		executors: func(w world, log *slog.Logger) ([]jobexec.Executor, error) {
			fe, err := w.engine()
			if err != nil {
				return nil, err
			}
			svc := prune.New(prune.Options{Engine: func() engine.Engine { return fe }, Guard: protect.New(protect.Options{Logger: log}), Logger: log})
			return []jobexec.Executor{svc.Executor()}, nil
		},
		check: func(t *testing.T, w world, res outcome) {
			fe, err := w.engine()
			if err != nil {
				t.Fatal(err)
			}
			names := fe.ContainerNames()
			// DockYard's own stopped container and the used ones survive
			// every outcome; a finished prune removed exactly the candidates.
			if !slices.Contains(names, "dockyard-agent-old") || !slices.Contains(names, "web") {
				t.Fatalf("containers after the prune: %v", names)
			}
			images := map[string]bool{}
			for _, tags := range fe.Images() {
				for _, tag := range tags {
					images[tag] = true
				}
			}
			if !images["docker.io/library/nginx:1.27"] && !images["nginx:1.27"] {
				t.Fatalf("a used image was removed: %v", images)
			}
			if res.job.State == domain.JobSucceeded && (slices.Contains(names, "old-job") || images["app:old"] || images["docker.io/library/app:old"]) {
				t.Fatalf("a finished prune left candidates: %v %v", names, images)
			}
			removes := map[string]int{}
			for _, c := range fe.Calls() {
				if strings.HasSuffix(c, ".remove") {
					removes[c]++
				}
			}
			// One candidate per category: at most one removal each, plus
			// at most one repeated attempt after a crash (revalidation then
			// finds it gone).
			if removes["container.remove"] > 2 || removes["image.remove"] > 2 {
				t.Fatalf("removals %v", removes)
			}
		},
	},
}

// TestKillRealExecutorsAtEveryStage is #26 Done-when 1 with the real
// executors: kill the manager or the agent at every fault point of a
// deploy, a backup with container shutdown and a prune.
func TestKillRealExecutorsAtEveryStage(t *testing.T) {
	for _, sc := range realScenarios {
		t.Run(sc.name, func(t *testing.T) {
			traced = map[string][]string{}
			clean := run(t, sc, "", "", true)
			checkInvariants(t, sc, clean)
			sc.check(t, world{base: clean.dir}, clean)
			if clean.job.State != domain.JobSucceeded || clean.crashed {
				t.Fatalf("clean run: %+v", clean.job)
			}
			spec, _ := jobspec.Lookup(sc.kind)
			if len(clean.effects) != len(spec.Steps) {
				t.Fatalf("clean run effects %v", clean.effects)
			}
			points := map[string][]string{roleManager: traced[roleManager], roleAgent: traced[roleAgent]}
			if len(points[roleManager]) < 4 || len(points[roleAgent]) < 4*len(spec.Steps) {
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
						sc.check(t, world{base: res.dir}, res)
						t.Logf("%s killed at %s: job %s (%s) attempt %d", role, p, res.job.State, res.job.ErrorClass, res.job.Attempt)
					})
				}
			}
		})
	}
}
