package backups

import (
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/downvolumes"
	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestStoppedStackBacksUpTheAnonymousVolumesItHad: after Docker Manager
// brought the stack down (Compose down), no container ties its anonymous
// volumes to it any more; its backups include the volumes the down
// recorded, while the stack has no containers and under the usual rules
// (#276).
func TestStoppedStackBacksUpTheAnonymousVolumesItHad(t *testing.T) {
	e := newEnv(t)
	ctx := testutil.Context(t)
	gone := strings.Repeat("f", 64)
	store := downvolumes.New(t.TempDir())
	if err := store.Record("app", []downvolumes.Volume{
		{Name: e.anon, Service: "db", Destination: "/scratch"},
		{Name: gone, Service: "web", Destination: "/cache"},
	}); err != nil {
		t.Fatal(err)
	}
	e.svc.opts.DownVolumes = store
	on := stackItem(protocol.BackupRules{AnonymousVolumes: true})

	// While the stack has containers, they decide (a stale record is not used).
	p := e.svc.plan(ctx, on, false)
	if s, _ := sourceState(p, protocol.SourceAnonymous, e.anon); s.State != protocol.SourceIncluded || s.Reason == downVolumeReason {
		t.Errorf("volume of a running container: %+v", s)
	}
	if _, ok := sourceState(p, protocol.SourceAnonymous, gone); ok {
		t.Error("the record was used while the stack has containers")
	}

	// Compose down: the containers go, their anonymous volumes stay.
	for _, name := range []string{"app-db-1", "app-api-1", "app-web-1", "app-worker-1"} {
		c, ok := e.eng.Container(name)
		if !ok {
			t.Fatalf("container %s missing", name)
		}
		if err := e.eng.RemoveContainer(ctx, c.Details.ID, engine.RemoveOptions{Force: true}); err != nil {
			t.Fatal(err)
		}
	}
	p = e.svc.plan(ctx, on, false)
	if p.err != nil {
		t.Fatal(p.err)
	}
	s, ok := sourceState(p, protocol.SourceAnonymous, e.anon)
	if !ok || s.State != protocol.SourceIncluded || s.Reason != downVolumeReason || s.Service != "db" || !slices.Contains(p.volumes, e.anon) {
		t.Errorf("recorded volume of the stopped stack: %+v (%v), volumes %v", s, ok, p.volumes)
	}
	if s, _ := sourceState(p, protocol.SourceAnonymous, gone); s.State != protocol.SourceExcluded ||
		s.Reason != "the stack was stopped and the volume no longer exists" {
		t.Errorf("recorded volume removed since: %+v", s)
	}
	// The named volume still comes from the Compose file.
	if s, _ := sourceState(p, protocol.SourceVolume, "app_dbdata"); s.State != protocol.SourceIncluded {
		t.Errorf("named volume of the stopped stack: %+v", s)
	}

	// The usual rules apply: anonymous volumes off, or excluded by name.
	p = e.svc.plan(ctx, stackItem(protocol.BackupRules{}), false)
	if s, _ := sourceState(p, protocol.SourceAnonymous, e.anon); s.State != protocol.SourceExcluded || slices.Contains(p.volumes, e.anon) {
		t.Errorf("anonymous volumes off: %+v", s)
	}
	p = e.svc.plan(ctx, stackItem(protocol.BackupRules{AnonymousVolumes: true, VolumeExclude: []string{e.anon}}), false)
	if s, _ := sourceState(p, protocol.SourceAnonymous, e.anon); s.State != protocol.SourceExcluded || s.Reason != "excluded by the policy" {
		t.Errorf("excluded by name: %+v", s)
	}
}
