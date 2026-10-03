package backups

import (
	"context"
	"slices"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

type fakeVolumes struct {
	volumes    []protocol.VolumeInfo
	containers []protocol.ContainerSummary
}

func (f fakeVolumes) ListVolumes(context.Context, string) ([]protocol.VolumeInfo, error) {
	return f.volumes, nil
}

func (f fakeVolumes) ListContainers(context.Context, string) ([]protocol.ContainerSummary, error) {
	return f.containers, nil
}

// scopeVolumes: a named standalone volume, an anonymous one, one the
// policy excludes, one of the managed stack "shop" (by label) and one a
// shop container uses.
func scopeVolumes() fakeVolumes {
	anon := map[string]string{protocol.AnonymousVolumeLabel: ""}
	return fakeVolumes{
		volumes: []protocol.VolumeInfo{
			{Name: "media"},
			{Name: "3f2a", Labels: anon},
			{Name: "scratch"},
			{Name: "shop_db", Labels: map[string]string{protocol.ComposeProjectLabel: "shop"}},
			{Name: "shared", UsedBy: []protocol.ContainerRef{{ID: "c1"}}},
		},
		containers: []protocol.ContainerSummary{{ID: "c1", Labels: map[string]string{protocol.ComposeProjectLabel: "shop"}}},
	}
}

func TestStandaloneVolumesSkipAnonymousAndExcludedVolumes(t *testing.T) {
	ctx := testutil.Context(t)
	s := &Service{volumes: scopeVolumes()}
	stacks := []domain.Stack{{ID: "s1", EnvironmentID: "e1", Name: "shop"}}
	cases := []struct {
		name string
		p    domain.BackupSetup
		want []string
	}{
		{"anonymous volumes are off by default", domain.BackupSetup{ExcludeVolumes: []string{"e1/scratch"}}, []string{"media"}},
		{"the switch includes them", domain.BackupSetup{ExcludeVolumes: []string{"e1/scratch"}, AnonymousVolumes: true}, []string{"media", "3f2a"}},
		{"exclusions are keyed by environment", domain.BackupSetup{ExcludeVolumes: []string{"e1/media", "e2/scratch"}}, []string{"scratch"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.standaloneVolumes(ctx, c.p, "e1", stacks)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("volumes %v, want %v", got, c.want)
			}
		})
	}
}

// TestStandaloneVolumesSkipLabeledAndBuildxVolumes: the backup exclude
// label on a volume or on a container using it leaves the volume out, and
// buildx builder volumes (rebuildable build cache) are left out unless the
// setup includes them.
func TestStandaloneVolumesSkipLabeledAndBuildxVolumes(t *testing.T) {
	ctx := testutil.Context(t)
	exclude := map[string]string{protocol.LabelBackupExclude: "true"}
	s := &Service{volumes: fakeVolumes{
		volumes: []protocol.VolumeInfo{
			{Name: "media"},
			{Name: "cache", Labels: exclude},
			{Name: "dumps", UsedBy: []protocol.ContainerRef{{ID: "c1"}}},
			{Name: "buildx_buildkit_builder0_state"},
		},
		containers: []protocol.ContainerSummary{{ID: "c1", Labels: exclude}},
	}}
	var p domain.BackupSetup
	if got, err := s.standaloneVolumes(ctx, p, "e1", nil); err != nil || !slices.Equal(got, []string{"media"}) {
		t.Errorf("volumes %v %v, want only media", got, err)
	}
	p.BuildxVolumes = true
	if got, _ := s.standaloneVolumes(ctx, p, "e1", nil); !slices.Equal(got, []string{"media", "buildx_buildkit_builder0_state"}) {
		t.Errorf("with buildx volumes: %v", got)
	}
}

func TestExcludedVolumesPerEnvironment(t *testing.T) {
	all := domain.BackupSetup{ExcludeVolumes: []string{"e1/shop_db", "e2/media", "e1/cache"}}
	if got := excludedVolumes(all, "e1"); !slices.Equal(got, []string{"shop_db", "cache"}) {
		t.Errorf("e1: %v", got)
	}
	if got := excludedVolumes(all, "e3"); len(got) != 0 {
		t.Errorf("e3: %v", got)
	}
}

// TestExternalBindSources: the setup's ExternalBinds opts in each bind
// source outside the project directory once, never inside ones or paths
// the agent would refuse as rules.
func TestExternalBindSources(t *testing.T) {
	binds := []domain.StackBind{
		{Service: "app", Source: "/srv/media", External: true},
		{Service: "worker", Source: "/srv/media", External: true},
		{Service: "app", Source: "/opt/stacks/shop/data", RelPath: "data"},
		{Service: "app", Source: "/", External: true},
		{Service: "app", Source: "/srv/../etc", External: true},
		{Service: "db", Source: "/mnt/backups", External: true},
	}
	if got, want := externalBindSources(binds), []string{"/srv/media", "/mnt/backups"}; !slices.Equal(got, want) {
		t.Errorf("sources %v, want %v", got, want)
	}
	if got := externalBindSources(nil); len(got) != 0 {
		t.Errorf("no binds gave %v", got)
	}
}
