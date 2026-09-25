package migrations

import (
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/store"
)

// TestRetainedSources (#35): the stopped source of a stack migration is
// kept while the migration runs or completed without a confirmed source
// removal; failed, cancelled and interrupted migrations put the stack back
// (it protects its project itself) and volume copies keep their source
// anyway.
func TestRetainedSources(t *testing.T) {
	w := newWorld(t)
	now := w.clk.Now().UTC()
	add := func(id string, kind domain.MigrationKind, env, project string, state domain.MigrationState, vols ...string) {
		to := dstEnv
		if env == dstEnv {
			to = srcEnv
		}
		m := domain.Migration{ID: id, Kind: kind, StackID: "st-" + project, SourceEnvironmentID: env, TargetEnvironmentID: to,
			Source: domain.MigrationSource{Project: project}, State: state, CreatedAt: now, UpdatedAt: now}
		for _, v := range vols {
			m.Volumes = append(m.Volumes, domain.MigrationVolume{Source: v, Target: v})
		}
		if err := store.InsertMigration(w.ctx, w.svc.db, &m); err != nil {
			t.Fatal(err)
		}
	}
	add("m1", domain.MigrationKindStack, srcEnv, "shop", domain.MigrationCompleted, "shop_dbdata", "shop_cache", "shop_dbdata")
	add("m2", domain.MigrationKindStack, srcEnv, "blog", domain.MigrationRunning)
	add("m3", domain.MigrationKindStack, srcEnv, "wiki", domain.MigrationSourceRemoved, "wiki_data")
	add("m4", domain.MigrationKindStack, srcEnv, "chat", domain.MigrationFailed)
	add("m5", domain.MigrationKindStack, srcEnv, "mail", domain.MigrationInterrupted)
	add("m6", domain.MigrationKindVolume, srcEnv, "", domain.MigrationCompleted, "loose")
	add("m7", domain.MigrationKindStack, dstEnv, "other", domain.MigrationCompleted)

	rs, err := w.svc.RetainedSources(w.ctx, srcEnv)
	if err != nil {
		t.Fatal(err)
	}
	var projects []string
	for _, r := range rs {
		projects = append(projects, r.Project)
		if !strings.Contains(r.Reason, `migrated stack "`+r.Project+`"`) || len(r.Reason) > 160 {
			t.Errorf("reason %q", r.Reason)
		}
	}
	if !slices.Equal(projects, []string{"shop", "blog"}) {
		t.Fatalf("retained %v", projects)
	}
	if !slices.Equal(rs[0].Volumes, []string{"shop_dbdata", "shop_cache"}) || rs[0].MigrationID != "m1" || rs[0].StackID != "st-shop" {
		t.Fatalf("shop %+v", rs[0])
	}
	pm, err := w.svc.RetainedProjects(w.ctx, srcEnv)
	if err != nil || len(pm) != 2 || pm["shop"] == "" || pm["blog"] == "" {
		t.Fatalf("projects %v %v", pm, err)
	}
	if pm, _ := w.svc.RetainedProjects(w.ctx, "env-none"); len(pm) != 0 {
		t.Fatalf("another environment %v", pm)
	}
}
