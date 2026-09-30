package stacks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/compose"
	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/engine/enginefake"
	"github.com/neurekadev/docker-manager/internal/agent/lifecycle"
	"github.com/neurekadev/docker-manager/internal/agent/storage"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// diskEngine is the in-memory Engine whose volumes also get their data
// directory on disk, like Docker's local driver.
type diskEngine struct {
	*enginefake.Engine
	volumes string
}

func (d diskEngine) CreateVolume(ctx context.Context, spec engine.VolumeSpec) (engine.Volume, error) {
	v, err := d.Engine.CreateVolume(ctx, spec)
	if err == nil {
		_ = os.MkdirAll(filepath.Join(d.volumes, spec.Name, "_data"), 0o755)
	}
	return v, err
}

type renameEnv struct {
	stacks, volumes string
	eng             diskEngine
	c               *fakeComposer
	svc             *Service
}

const renameYAML = `services:
  web:
    image: nginx:1.27
    volumes:
      - data:/data
  db:
    image: postgres:17
volumes:
  data:
`

// newRenameEnv deploys project app (web with the named volume data, db with
// an anonymous volume), both running.
func newRenameEnv(t *testing.T, yaml string) *renameEnv {
	t.Helper()
	e := &renameEnv{stacks: t.TempDir(), volumes: t.TempDir()}
	vols := filepath.ToSlash(e.volumes)
	res := &storage.Result{StacksDir: filepath.ToSlash(e.stacks), VolumesDir: vols, Roots: []storage.Root{
		{Kind: storage.KindStacks, Path: filepath.ToSlash(e.stacks), OK: true}, {Kind: storage.KindVolumes, Path: vols, OK: true}}}
	fake := enginefake.New("engine-1")
	fake.SetVolumeRoot(vols)
	e.eng = diskEngine{Engine: fake, volumes: e.volumes}
	fake.AddImage("nginx:1.27")
	fake.AddImage("postgres:17")
	writeTree(t, filepath.Join(e.stacks, "app"), map[string]string{"compose.yaml": yaml})
	ctx := testutil.Context(t)
	if _, err := e.eng.CreateVolume(ctx, engine.VolumeSpec{Name: "app_data",
		Labels: map[string]string{protocol.ComposeProjectLabel: "app", protocol.ComposeVolumeLabel: "data"}}); err != nil {
		t.Fatal(err)
	}
	writeTree(t, filepath.Join(e.volumes, "app_data", "_data"), map[string]string{"orders.db": "orders"})
	e.addService("app", "web", "nginx:1.27", []engine.MountSpec{{Type: "volume", Source: "app_data", Target: "/data"}}, true)
	db := e.addService("app", "db", "postgres:17", []engine.MountSpec{{Type: "volume", Target: "/var/lib/postgresql/data"}}, true)
	d, _ := fake.InspectContainer(ctx, db)
	writeTree(t, filepath.Join(e.volumes, d.Mounts[0].Name, "_data"), map[string]string{"PG_VERSION": "17"})
	e.c = &fakeComposer{
		onDown: func(name string) {
			list, _ := lifecycle.ProjectContainers(context.Background(), fake, name)
			for _, c := range list {
				_ = fake.RemoveContainer(context.Background(), c.ID, engine.RemoveOptions{Force: true})
			}
		},
		onCreate: func(p *compose.Project, o compose.CreateOptions) error {
			for _, svc := range o.Services {
				var mounts []engine.MountSpec
				for _, v := range p.Volumes {
					if v.Service == svc {
						mounts = append(mounts, engine.MountSpec{Type: "volume", Source: v.Name, Target: v.Target})
					}
				}
				for _, v := range p.AnonymousVolumes {
					if v.Service == svc {
						mounts = append(mounts, engine.MountSpec{Type: "volume", Target: v.Target})
					}
				}
				img := "nginx:1.27"
				if svc == "db" {
					// The image's VOLUME: Docker gives it an anonymous volume.
					img = "postgres:17"
					mounts = append(mounts, engine.MountSpec{Type: "volume", Target: "/var/lib/postgresql/data"})
				}
				id := e.addService(p.Name, svc, img, mounts, false)
				d, _ := fake.InspectContainer(context.Background(), id)
				for _, m := range d.Mounts {
					// Docker fills a new anonymous volume from the image.
					writeTree(t, filepath.Join(e.volumes, m.Name, "_data"), map[string]string{"from-image": "x"})
				}
			}
			return nil
		},
	}
	e.svc = New(Options{Deps: fakeDeps{c: e.c, eng: e.eng, st: res}, Clock: testutil.FakeClock(), Logger: testutil.Logger(t)})
	return e
}

func (e *renameEnv) addService(project, svc, image string, mounts []engine.MountSpec, running bool) string {
	return e.eng.AddContainer(engine.ContainerSpec{Name: project + "-" + svc + "-1", Image: image, Mounts: mounts,
		Labels: map[string]string{lifecycle.ComposeProjectLabel: project, lifecycle.ComposeServiceLabel: svc, labelContainerNumber: "1"}}, running)
}

func renameTo(name string) protocol.StackJobInput {
	return protocol.StackJobInput{StackID: "st-1", Stack: ref("app"), Rename: &protocol.StackRename{ProjectName: name, Dir: name}}
}

func readData(t *testing.T, e *renameEnv, volume, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.volumes, volume, "_data", file))
	if err != nil {
		return ""
	}
	return string(b)
}

// TestRenameMovesEverything: the named volume's data moves to the new
// project's volume (labeled as Compose would), the anonymous volume's data
// follows its service, the directory is renamed, a container outside the
// stack is recreated on the new volume name and everything that ran runs
// again under the new name.
func TestRenameMovesEverything(t *testing.T) {
	e := newRenameEnv(t, renameYAML)
	ctx := testutil.Context(t)
	outside := e.eng.AddContainer(engine.ContainerSpec{Name: "backup", Image: "nginx:1.27",
		Mounts: []engine.MountSpec{{Type: "volume", Source: "app_data", Target: "/backup", ReadOnly: true}}}, true)

	res, out := run(t, e.svc, jobspec.StackRename, renameTo("shop"))
	if res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("rename: %+v", res)
	}
	r := out.Rename
	if r == nil || !r.Switched || !r.DirMoved || r.From != "app" || r.To != "shop" {
		t.Fatalf("report %+v", r)
	}
	if _, err := os.Stat(filepath.Join(e.stacks, "shop", "compose.yaml")); err != nil {
		t.Errorf("directory not renamed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.stacks, "app")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old directory still there: %v", err)
	}
	vol, err := e.eng.InspectVolume(ctx, "shop_data")
	if err != nil {
		t.Fatal(err)
	}
	if vol.Labels[protocol.ComposeProjectLabel] != "shop" || vol.Labels[protocol.ComposeVolumeLabel] != "data" ||
		vol.Labels["com.docker.compose.config-hash"] == "" {
		t.Errorf("new volume labels %v", vol.Labels)
	}
	if got := readData(t, e, "shop_data", "orders.db"); got != "orders" {
		t.Errorf("named volume data = %q", got)
	}
	if _, err := e.eng.InspectVolume(ctx, "app_data"); !engine.IsCode(err, engine.CodeNotFound) {
		t.Errorf("old volume kept: %v", err)
	}
	// The anonymous volume's data is in the new db container's volume.
	db, ok := e.eng.Container("shop-db-1")
	if !ok || !db.Details.State.Running {
		t.Fatalf("shop-db-1 = %+v", db.Details.State)
	}
	anon := db.Details.Mounts[0].Name
	if got := readData(t, e, anon, "PG_VERSION"); got != "17" {
		t.Errorf("anonymous volume data = %q", got)
	}
	if got := readData(t, e, anon, "from-image"); got != "" {
		t.Error("the image's copy was not replaced by the old data")
	}
	if web, ok := e.eng.Container("shop-web-1"); !ok || !web.Details.State.Running {
		t.Errorf("shop-web-1 = %+v", web.Details.State)
	}
	if list, _ := lifecycle.ProjectContainers(ctx, e.eng, "app"); len(list) != 0 {
		t.Errorf("old project containers left: %d", len(list))
	}
	// The outside container was recreated on the new name and runs again.
	b, ok := e.eng.Container("backup")
	if !ok || b.Details.ID == outside || !b.Details.State.Running {
		t.Fatalf("backup = %+v", b.Details)
	}
	if m := b.Details.Mounts; len(m) != 1 || m[0].Name != "shop_data" || m[0].ReadWrite {
		t.Errorf("backup mounts %+v", m)
	}
	if len(r.Containers) != 1 || r.Containers[0].NewID != b.Details.ID {
		t.Errorf("report containers %+v", r.Containers)
	}
	if !slices.Contains(e.c.calls, "down:app") || !slices.Contains(e.c.calls, "create:shop:db,web") {
		t.Errorf("compose calls %v", e.c.calls)
	}
	if out.Sources == nil || len(out.Sources.Files) != 1 {
		t.Errorf("sources %+v", out.Sources)
	}
}

// TestRenameUndoesBeforeTheSwitch: a failure while moving puts the moved
// volume back, removes what was created and starts the stack again under
// its old name.
func TestRenameUndoesBeforeTheSwitch(t *testing.T) {
	e := newRenameEnv(t, strings.Replace(renameYAML, "      - data:/data\n", "      - data:/data\n      - logs:/logs\n", 1)+"  logs:\n")
	ctx := testutil.Context(t)
	if _, err := e.eng.CreateVolume(ctx, engine.VolumeSpec{Name: "app_logs",
		Labels: map[string]string{protocol.ComposeProjectLabel: "app", protocol.ComposeVolumeLabel: "logs"}}); err != nil {
		t.Fatal(err)
	}
	// data moves (sorted first), creating logs' new volume fails.
	e.eng.Fail("volume.create", nil)
	e.eng.Fail("volume.create", errors.New("disk full"))

	res, out := run(t, e.svc, jobspec.StackRename, renameTo("shop"))
	if res.Outcome == jobexec.OutcomeSucceeded || res.ErrorClass != classRenameRefused {
		t.Fatalf("rename: %+v", res)
	}
	if out.Rename == nil || out.Rename.Switched || !out.Rename.Volumes[0].Done {
		t.Fatalf("report %+v", out.Rename)
	}
	if got := readData(t, e, "app_data", "orders.db"); got != "orders" {
		t.Errorf("data not back in app_data: %q", got)
	}
	if _, err := e.eng.InspectVolume(ctx, "shop_data"); !engine.IsCode(err, engine.CodeNotFound) {
		t.Errorf("new volume left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.stacks, "app", "compose.yaml")); err != nil {
		t.Errorf("directory moved: %v", err)
	}
	for _, n := range []string{"app-web-1", "app-db-1"} {
		if c, ok := e.eng.Container(n); !ok || !c.Details.State.Running {
			t.Errorf("%s not running again: %+v", n, c.Details.State)
		}
	}
}

func TestRenamePlanBlockers(t *testing.T) {
	ctx := testutil.Context(t)
	has := func(p protocol.StackRenamePlan, code string) bool {
		return slices.ContainsFunc(p.Blockers, func(i protocol.ComposeIssue) bool { return i.Code == code })
	}
	t.Run("name in file", func(t *testing.T) {
		e := newRenameEnv(t, "name: app\n"+renameYAML)
		p, err := e.svc.planRename(ctx, ref("app"), protocol.StackRename{ProjectName: "shop", Dir: "shop"}, nil)
		if err != nil || !has(p, renameNameInFile) || p.DeclaredName != "app" {
			t.Errorf("plan %+v %v", p, err)
		}
	})
	t.Run("name changed in file", func(t *testing.T) {
		e := newRenameEnv(t, "name: shop\n"+renameYAML)
		p, err := e.svc.planRename(ctx, ref("app"), protocol.StackRename{ProjectName: "shop", Dir: "shop"}, nil)
		if err != nil || len(p.Blockers) != 0 || p.DeclaredName != "shop" {
			t.Errorf("rename to the declared name: %+v %v", p.Blockers, err)
		}
		p, _ = e.svc.planRename(ctx, ref("app"), protocol.StackRename{ProjectName: "other", Dir: "other"}, nil)
		if !has(p, renameNameMismatch) {
			t.Errorf("another name: %+v", p.Blockers)
		}
	})
	t.Run("shared with another project", func(t *testing.T) {
		e := newRenameEnv(t, renameYAML)
		e.addService("reports", "cron", "nginx:1.27", []engine.MountSpec{{Type: "volume", Source: "app_data", Target: "/in"}}, false)
		p, _ := e.svc.planRename(ctx, ref("app"), protocol.StackRename{ProjectName: "shop", Dir: "shop"}, nil)
		if !has(p, renameVolumeShared) {
			t.Errorf("blockers %+v", p.Blockers)
		}
	})
	t.Run("targets exist", func(t *testing.T) {
		e := newRenameEnv(t, renameYAML)
		writeTree(t, filepath.Join(e.stacks, "shop"), map[string]string{"x": ""})
		_, _ = e.eng.CreateVolume(ctx, engine.VolumeSpec{Name: "shop_data"})
		p, _ := e.svc.planRename(ctx, ref("app"), protocol.StackRename{ProjectName: "shop", Dir: "shop"}, nil)
		if !has(p, renameDirectoryExists) || !has(p, renameVolumeExists) {
			t.Errorf("blockers %+v", p.Blockers)
		}
	})
	t.Run("plan", func(t *testing.T) {
		e := newRenameEnv(t, renameYAML)
		e.eng.AddContainer(engine.ContainerSpec{Name: "backup", Image: "nginx:1.27",
			Mounts: []engine.MountSpec{{Type: "volume", Source: "app_data", Target: "/backup"}}}, false)
		p, err := e.svc.planRename(ctx, ref("app"), protocol.StackRename{ProjectName: "shop", Dir: "shop"}, nil)
		if err != nil || len(p.Blockers) != 0 {
			t.Fatalf("plan %+v %v", p.Blockers, err)
		}
		if strings.Join(p.Running, ",") != "db,web" || len(p.Containers) != 1 || p.Containers[0].Name != "backup" {
			t.Errorf("running %v, containers %+v", p.Running, p.Containers)
		}
		var named, anon int
		for _, v := range p.Volumes {
			switch {
			case v.Key == "data" && v.NewName == "shop_data" && v.Action == protocol.RenameVolumeMove:
				named++
			case v.Key == "" && v.Service == "db":
				anon++
			}
		}
		if named != 1 || anon != 1 {
			t.Errorf("volumes %+v", p.Volumes)
		}
	})
}

// TestDeployRefusesARenamedProject: a deploy never starts a second project
// next to the running one when name: changed in the file.
func TestDeployRefusesARenamedProject(t *testing.T) {
	e := newRenameEnv(t, "name: shop\n"+renameYAML)
	res, _ := run(t, e.svc, jobspec.StackDeploy, protocol.StackJobInput{Stack: ref("app")})
	if res.Outcome == jobexec.OutcomeSucceeded || res.ErrorClass != classProjectRenamed || !strings.Contains(res.Recovery, "Rename the stack to shop") {
		t.Errorf("deploy: %+v", res)
	}
	if len(e.c.calls) != 0 {
		t.Errorf("compose calls %v", e.c.calls)
	}
}
