package stacks

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/compose"
	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/volumelabels"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func (f *fakeEngine) InspectVolume(_ context.Context, name string) (engine.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.volumes {
		if v.Name == name {
			return v, nil
		}
	}
	return engine.Volume{}, engine.Errorf("volume.inspect", engine.CodeNotFound, "no such volume %s", name)
}

// TestDeployRecordsComposeVolumeLabels: a deploy records the Docker
// Manager labels the definition declares on its existing volumes that
// Docker kept off them (the volumes predate the label); external volumes,
// volumes not created yet and other labels are left alone.
func TestDeployRecordsComposeVolumeLabels(t *testing.T) {
	e := newEnv(t)
	ctx := testutil.Context(t)
	dir := filepath.Join(e.root, "media")
	writeTree(t, dir, map[string]string{"compose.yaml": `services:
  app:
    image: example/app:1
    volumes: [data:/data, cache:/cache, shared:/shared, fresh:/fresh]
volumes:
  data:
    labels:
      docker-manager.backup.exclude: "true"
      tier: cold
  cache: {}
  shared:
    external: true
    labels:
      docker-manager.backup.exclude: "true"
  fresh:
    labels:
      docker-manager.maintenance.exclude: "true"
`})
	p, err := compose.LoadProject(ctx, compose.ProjectSpec{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	e.eng.volumes = []engine.Volume{
		{Name: "media_data", CreatedAt: created, Labels: map[string]string{protocol.ComposeProjectLabel: "media"}},
		{Name: "media_cache", CreatedAt: created},
		{Name: "shared", CreatedAt: created},
	}
	store := volumelabels.New(t.TempDir())
	e.svc.opts.VolumeLabels = store
	e.svc.recordVolumeLabels(ctx, e.eng, "media", p)

	got := store.Compose("media_data", created)
	if len(got) != 1 || got[protocol.LabelBackupExclude] != "true" {
		t.Errorf("media_data = %v", got)
	}
	for _, name := range []string{"media_cache", "shared", "media_fresh"} {
		if got := store.Compose(name, created); got != nil {
			t.Errorf("%s recorded %v", name, got)
		}
	}
	if err := store.Forget("media"); err != nil || store.Compose("media_data", created) != nil {
		t.Errorf("forget: %v", err)
	}
}
