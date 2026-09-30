package stacks

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// volEngine adds volumes to the fake Engine.
type volEngine struct {
	*fakeEngine
	volumes map[string]map[string]string // name -> labels
	removed []string
}

func (v *volEngine) InspectVolume(_ context.Context, name string) (engine.Volume, error) {
	labels, ok := v.volumes[name]
	if !ok {
		return engine.Volume{}, engine.Errorf("volume.inspect", engine.CodeNotFound, "no such volume")
	}
	return engine.Volume{Name: name, Labels: labels}, nil
}

func (v *volEngine) RemoveVolume(_ context.Context, name string, force bool) error {
	if force {
		panic("volumes must never be force-removed")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, c := range v.containers {
		for _, m := range c.Mounts {
			if m.Name == name {
				return engine.Errorf("volume.remove", engine.CodeConflict, "volume is in use")
			}
		}
	}
	delete(v.volumes, name)
	v.removed = append(v.removed, name)
	return nil
}

const volYAML = "services:\n" +
	"  web:\n" +
	"    image: nginx\n" +
	"    volumes:\n" +
	"      - data:/data\n" +
	"      - shared:/shared\n" +
	"      - named:/named\n" +
	"      - /cache\n" +
	"volumes:\n" +
	"  data: {}\n" +
	"  shared:\n" +
	"    external: true\n" +
	"  named:\n" +
	"    name: custom-named\n"

func volFixture(t *testing.T) (*env, *volEngine) {
	t.Helper()
	e := newEnv(t)
	writeTree(t, filepath.Join(e.root, "vols"), map[string]string{"compose.yaml": volYAML})
	ve := &volEngine{fakeEngine: e.eng, volumes: map[string]map[string]string{
		// Compose's own volumes of this project.
		"vols_data":    {protocol.ComposeProjectLabel: "vols", protocol.ComposeVolumeLabel: "data"},
		"custom-named": {protocol.ComposeProjectLabel: "vols", protocol.ComposeVolumeLabel: "named"},
		// External: never removed.
		"shared": {},
		// The anonymous volume of the stack's container.
		"a1b2c3": {protocol.AnonymousVolumeLabel: ""},
		// Another stack's volume.
		"other_data": {protocol.ComposeProjectLabel: "other", protocol.ComposeVolumeLabel: "data"},
	}}
	e.svc = New(Options{Deps: fakeDeps{c: e.c, eng: ve, st: e.svc.opts.Deps.(fakeDeps).st}, Clock: e.svc.opts.Clock, Logger: e.svc.log})
	mount := func(name string) engine.Mount { return engine.Mount{Type: "volume", Name: name} }
	e.eng.containers = []engine.Container{
		{ID: "web1", Names: []string{"/vols-web-1"}, State: "running",
			Labels: map[string]string{protocol.ComposeProjectLabel: "vols", "com.docker.compose.service": "web"},
			Mounts: []engine.Mount{mount("vols_data"), mount("shared"), mount("custom-named"), mount("a1b2c3")}},
		{ID: "x1", Names: []string{"/other-1"}, State: "running", Labels: map[string]string{protocol.ComposeProjectLabel: "other"},
			Mounts: []engine.Mount{mount("other_data"), mount("shared")}},
	}
	e.c.onDown = func(name string) {
		e.eng.mu.Lock()
		defer e.eng.mu.Unlock()
		e.eng.containers = slices.DeleteFunc(e.eng.containers, func(c engine.Container) bool {
			return c.Labels[protocol.ComposeProjectLabel] == name
		})
	}
	return e, ve
}

// TestRemoveKeepsVolumesByDefault: without the option nothing is removed.
func TestRemoveKeepsVolumesByDefault(t *testing.T) {
	e, ve := volFixture(t)
	res, out := run(t, e.svc, jobspec.StackRemove, protocol.StackJobInput{Stack: ref("vols")})
	if res.Outcome != jobexec.OutcomeSucceeded || len(ve.removed) != 0 || out.VolumesPlanned || len(out.Volumes) != 0 {
		t.Errorf("result %+v removed %v output %+v", res, ve.removed, out.Volumes)
	}
}

// TestRemoveWithVolumesRemovesOnlyTheStacksOwn: declared non-external
// volumes Compose created for the project and the stack's anonymous
// volumes go; external, other projects' and in-use ones stay.
func TestRemoveWithVolumesRemovesOnlyTheStacksOwn(t *testing.T) {
	e, ve := volFixture(t)
	// A container of another project also mounts the stack's named volume.
	e.eng.containers[1].Mounts = append(e.eng.containers[1].Mounts, engine.Mount{Type: "volume", Name: "custom-named"})
	res, out := run(t, e.svc, jobspec.StackRemove, protocol.StackJobInput{Stack: ref("vols"), RemoveVolumes: true})
	if res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("result %+v", res)
	}
	sort.Strings(ve.removed)
	if !slices.Equal(ve.removed, []string{"a1b2c3", "vols_data"}) {
		t.Errorf("removed %v", ve.removed)
	}
	for _, keep := range []string{"shared", "other_data", "custom-named"} {
		if _, ok := ve.volumes[keep]; !ok {
			t.Errorf("%s was removed", keep)
		}
	}
	status := map[string]string{}
	for _, v := range out.Volumes {
		status[v.Name] = v.Status + " " + v.Reason
	}
	want := map[string]string{"vols_data": "removed ", "custom-named": "kept in use by another container", "a1b2c3": "removed "}
	if len(status) != len(want) {
		t.Errorf("planned %v, want %v", status, want)
	}
	for k, v := range want {
		if status[k] != v {
			t.Errorf("%s: %q, want %q", k, status[k], v)
		}
	}
	items := map[string]string{}
	for _, it := range res.Items {
		items[it.Name] = it.Status
	}
	if items["volume vols_data"] != domain.ItemSucceeded || items["volume custom-named"] != domain.ItemSkipped {
		t.Errorf("items %v", items)
	}
}

// TestRemoveWithVolumesChecksOwnershipAndHolds: a declared volume Compose
// did not create for this project, and a volume the manager holds, stay.
func TestRemoveWithVolumesChecksOwnershipAndHolds(t *testing.T) {
	e, ve := volFixture(t)
	ve.volumes["custom-named"] = map[string]string{} // created by hand before the stack
	res, out := run(t, e.svc, jobspec.StackRemove, protocol.StackJobInput{Stack: ref("vols"), RemoveVolumes: true,
		KeepVolumes: []string{"vols_data"}})
	if res.Outcome != jobexec.OutcomeSucceeded || !slices.Equal(ve.removed, []string{"a1b2c3"}) {
		t.Fatalf("result %+v removed %v", res, ve.removed)
	}
	reasons := map[string]string{}
	for _, v := range out.Volumes {
		reasons[v.Name] = v.Reason
	}
	if reasons["custom-named"] != "not created by this stack" || reasons["vols_data"] != "held by a migrated stack" {
		t.Errorf("reasons %v", reasons)
	}
}

// TestRemoveWithVolumesNeedsTheDefinition: without a readable definition
// nothing is taken down.
func TestRemoveWithVolumesNeedsTheDefinition(t *testing.T) {
	e, ve := volFixture(t)
	res, _ := run(t, e.svc, jobspec.StackRemove, protocol.StackJobInput{Stack: ref("missing"), RemoveVolumes: true})
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != classProjectUnreadable || len(e.c.calls) != 0 || len(ve.removed) != 0 {
		t.Errorf("result %+v calls %v removed %v", res, e.c.calls, ve.removed)
	}
}
