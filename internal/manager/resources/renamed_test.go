package resources

import (
	"context"
	"slices"
	"testing"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestStackRenamedRewritesSavedSpecs (#7 × #6): after a rename moved a
// stack's volumes, the saved recreate specifications that mount them name
// the new volumes; other mounts, other specifications and the sealed
// environment values are untouched.
func TestStackRenamedRewritesSavedSpecs(t *testing.T) {
	svc, _, _, _, _ := fixture(t)
	ctx := testutil.Context(t)
	backup := protocol.ContainerSpec{Name: "backup", Image: "nginx:1.27", Env: []string{"TOKEN=canary"}, Mounts: []protocol.MountSpec{
		{Type: "volume", Source: "shop_data", Target: "/data"},
		{Type: "bind", Source: "/srv/shop_data", Target: "/host"},
		{Type: "volume", Source: "other", Target: "/other"},
	}}
	unrelated := protocol.ContainerSpec{Name: "api", Image: "nginx:1.27", Mounts: []protocol.MountSpec{{Type: "volume", Source: "other", Target: "/o"}}}
	for id, spec := range map[string]protocol.ContainerSpec{"spec-backup": backup, "spec-api": unrelated} {
		if _, err := svc.saveSpec(ctx, domain.ManagedContainer{ID: id, EnvironmentID: "env-1", Name: spec.Name}, spec); err != nil {
			t.Fatal(err)
		}
	}
	ev := domain.StackRenamed{StackID: "stack-shop", EnvironmentID: "env-1", From: "shop", To: "store",
		Volumes: map[string]string{"shop_data": "store_data"}}
	if err := svc.opts.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error { return svc.StackRenamed(ctx, tx, ev) }); err != nil {
		t.Fatal(err)
	}
	m, got, err := svc.ManagedSpec(ctx, "env-1", map[string]string{protocol.LabelManaged: protocol.ManagedStandalone, protocol.LabelSpec: "spec-backup"})
	if err != nil || m == nil {
		t.Fatalf("spec %v %v", m, err)
	}
	want := []protocol.MountSpec{{Type: "volume", Source: "store_data", Target: "/data"}, {Type: "bind", Source: "/srv/shop_data", Target: "/host"},
		{Type: "volume", Source: "other", Target: "/other"}}
	if !slices.Equal(got.Mounts, want) || !slices.Equal(got.Env, backup.Env) || m.Revision != 2 {
		t.Errorf("rewritten spec: mounts %+v env kept %v revision %d", got.Mounts, slices.Equal(got.Env, backup.Env), m.Revision)
	}
	if other, _, err := store.GetManagedContainer(ctx, svc.opts.DB, "spec-api"); err != nil || other.Revision != 1 {
		t.Errorf("unrelated spec: %+v %v", other, err)
	}
}
