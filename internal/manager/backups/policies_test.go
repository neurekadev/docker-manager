package backups

import (
	"context"
	"slices"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
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
		p    domain.BackupPolicy
		want []string
	}{
		{"anonymous volumes are off by default", domain.BackupPolicy{EnvironmentID: "e1", ExcludeVolumes: []string{"scratch"}}, []string{"media"}},
		{"the switch includes them", domain.BackupPolicy{EnvironmentID: "e1", ExcludeVolumes: []string{"scratch"}, AnonymousVolumes: true}, []string{"media", "3f2a"}},
		{"all environments key exclusions by environment", domain.BackupPolicy{ExcludeVolumes: []string{"e1/media", "e2/scratch"}}, []string{"scratch"}},
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

func TestExcludedVolumesPerEnvironment(t *testing.T) {
	one := domain.BackupPolicy{EnvironmentID: "e1", ExcludeVolumes: []string{"shop_db", "media"}}
	if got := excludedVolumes(one, "e1"); !slices.Equal(got, []string{"shop_db", "media"}) {
		t.Errorf("one environment: %v", got)
	}
	all := domain.BackupPolicy{ExcludeVolumes: []string{"e1/shop_db", "e2/media", "e1/cache"}}
	if got := excludedVolumes(all, "e1"); !slices.Equal(got, []string{"shop_db", "cache"}) {
		t.Errorf("all environments, e1: %v", got)
	}
	if got := excludedVolumes(all, "e3"); len(got) != 0 {
		t.Errorf("all environments, e3: %v", got)
	}
}
