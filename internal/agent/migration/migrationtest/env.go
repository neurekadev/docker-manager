package migrationtest

import (
	"context"
	"path"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine/enginefake"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/migration"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/storage"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// Env is one simulated environment for migration tests: an in-memory
// Engine whose volumes have directories in an in-memory host filesystem,
// a verified storage layout (stacks volume and volume directory below
// Base) and the agent's migration service over them.
type Env struct {
	Name   string
	Host   *Host
	Engine *enginefake.Engine
	// Base is an absolute slash path (absolute on the test's OS too, so
	// Compose accepts project directories below it).
	Base       string
	VolumesDir string
	StacksDir  string
	Storage    *storage.Result
	Service    *migration.Service
	free       int64
}

// HostEngine gives volumes created through the Engine a data directory
// in the host (as a real Engine does).
type HostEngine struct {
	*enginefake.Engine
	Host *Host
}

// CreateVolume implements engine.Engine.
func (e HostEngine) CreateVolume(ctx context.Context, spec engine.VolumeSpec) (engine.Volume, error) {
	v, err := e.Engine.CreateVolume(ctx, spec)
	if err == nil && !e.Host.Exists(v.Mountpoint) {
		e.Host.MkdirAll(v.Mountpoint)
	}
	return v, err
}

type deps struct{ e *Env }

func (d deps) Engine() engine.Engine    { return HostEngine{Engine: d.e.Engine, Host: d.e.Host} }
func (d deps) Storage() *storage.Result { s := *d.e.Storage; return &s }

// NewEnv returns an environment below base (use filepath.ToSlash of a
// t.TempDir(): only the in-memory host is used). free is the free space
// reported for both roots (-1 unknown).
func NewEnv(t testing.TB, name, base string, free int64) *Env {
	t.Helper()
	e := &Env{Name: name, Host: NewHost(), Engine: enginefake.New("ENGINE-" + name), Base: base, free: free}
	e.VolumesDir = path.Join(base, "docker", "volumes")
	e.StacksDir = path.Join(e.VolumesDir, "docker-manager_stacks", "_data")
	e.Engine.SetVolumeRoot(e.VolumesDir)
	e.Host.MkdirAll(e.StacksDir)
	e.Storage = &storage.Result{Containerized: true, DockerRootDir: path.Join(base, "docker"), StacksDir: e.StacksDir,
		VolumesDir: e.VolumesDir, Roots: []storage.Root{{Kind: storage.KindStacks, Path: e.StacksDir, OK: true},
			{Kind: storage.KindVolumes, Path: e.VolumesDir, OK: true}}}
	e.Service = migration.New(migration.Options{Deps: deps{e}, Open: e.Host.Opener(), FreeBytes: func(string) int64 { return e.free },
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t)})
	return e
}

// Deps returns the migration service's dependencies (for services built
// with other options).
func (e *Env) Deps() migration.Deps { return deps{e} }

// AddVolume creates a volume with a data directory and returns its
// mountpoint.
func (e *Env) AddVolume(name string, labels map[string]string) string {
	v, err := HostEngine{Engine: e.Engine, Host: e.Host}.CreateVolume(context.Background(), engine.VolumeSpec{Name: name, Labels: labels})
	if err != nil {
		panic(err)
	}
	return v.Mountpoint
}

// ProjectDir returns the host path of a project directory in the stacks
// volume.
func (e *Env) ProjectDir(dir string) string { return path.Join(e.StacksDir, dir) }
