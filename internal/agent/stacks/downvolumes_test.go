package stacks

import (
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/downvolumes"
	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/lifecycle"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// TestDownRecordsTheAnonymousVolumesItLeavesBehind: a down records the
// anonymous volumes of the containers it removes (#276), not named
// volumes nor those only a temporary container mounts; a down of a
// project without containers keeps the record, a deploy and a removal
// forget it.
func TestDownRecordsTheAnonymousVolumesItLeavesBehind(t *testing.T) {
	e, _ := deployFixture(t)
	store := downvolumes.New(t.TempDir())
	e.svc.opts.DownVolumes = store
	anon, helper, webAnon := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	lbl := func(svc string) map[string]string {
		return map[string]string{lifecycle.ComposeProjectLabel: "app", lifecycle.ComposeServiceLabel: svc}
	}
	replacing := lbl("db")
	replacing[protocol.ComposeReplaceLabel] = "db1"
	running := func() {
		e.eng.mu.Lock()
		defer e.eng.mu.Unlock()
		e.eng.containers = []engine.Container{
			{ID: "db1", Names: []string{"/app-db-1"}, State: "running", Labels: lbl("db"), Mounts: []engine.Mount{
				{Type: "volume", Name: "app_data", Destination: "/var/lib/postgresql/data"},
				{Type: "volume", Name: anon, Destination: "/scratch"},
				{Type: "bind", Source: "/srv/html", Destination: "/html"},
			}},
			{ID: "web1", Names: []string{"/app-web-1"}, State: "running", Labels: lbl("web"), Mounts: []engine.Mount{
				{Type: "volume", Name: webAnon, Destination: "/cache"},
			}},
			// Compose's replacement during a recreate: a temporary container.
			{ID: "tmp1", Names: []string{"/0123456789ab_app-db-1"}, State: "exited", Labels: replacing, Mounts: []engine.Mount{
				{Type: "volume", Name: helper, Destination: "/tmp"},
			}},
		}
	}
	e.c.onDown = func(string) {
		e.eng.mu.Lock()
		defer e.eng.mu.Unlock()
		e.eng.containers = nil
	}
	// A record an earlier down left of a db container since replaced: this
	// down drops its volume (db's data is in new volumes now).
	if err := store.Record("app", []downvolumes.Container{{ID: "old1", Service: "db"}}, []downvolumes.Volume{{Name: strings.Repeat("d", 64), Service: "db", Destination: "/old"}}); err != nil {
		t.Fatal(err)
	}
	running()
	res, _ := run(t, e.svc, jobspec.StackDown, protocol.StackJobInput{Stack: ref("app")})
	want := []downvolumes.Volume{{Name: anon, Service: "db", Destination: "/scratch"}, {Name: webAnon, Service: "web", Destination: "/cache"}}
	if res.Outcome != jobexec.OutcomeSucceeded || !slices.Equal(store.Volumes("app"), want) {
		t.Fatalf("down: %+v record %v", res, store.Volumes("app"))
	}
	// A down after one that failed having removed some containers (the db
	// one is gone, the web one is left) keeps the volumes of the removed.
	running()
	e.eng.mu.Lock()
	e.eng.containers = slices.DeleteFunc(e.eng.containers, func(c engine.Container) bool { return c.ID != "web1" })
	e.eng.mu.Unlock()
	if res, _ := run(t, e.svc, jobspec.StackDown, protocol.StackJobInput{Stack: ref("app")}); res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("down of what was left: %+v", res)
	}
	if got := store.Volumes("app"); !slices.Equal(got, want) {
		t.Fatalf("after the down of what was left: %v", got)
	}
	// Nothing left to bring down: the record stays.
	if res, _ := run(t, e.svc, jobspec.StackDown, protocol.StackJobInput{Stack: ref("app")}); res.Outcome != jobexec.OutcomeSucceeded ||
		!slices.Equal(store.Volumes("app"), want) {
		t.Fatalf("second down: %+v record %v", res, store.Volumes("app"))
	}
	// A deploy gives the containers anonymous volumes of their own.
	if res, _ := run(t, e.svc, jobspec.StackDeploy, protocol.StackJobInput{Stack: ref("app")}); res.Outcome != jobexec.OutcomeSucceeded ||
		store.Volumes("app") != nil {
		t.Fatalf("deploy: %+v record %v", res, store.Volumes("app"))
	}
	// A removal brings the stack down and forgets it.
	running()
	if err := store.Record("app", []downvolumes.Container{{ID: "db1", Service: "db"}}, want); err != nil {
		t.Fatal(err)
	}
	if res, _ := run(t, e.svc, jobspec.StackRemove, protocol.StackJobInput{Stack: ref("app")}); res.Outcome != jobexec.OutcomeSucceeded ||
		store.Volumes("app") != nil {
		t.Fatalf("remove: %+v record %v", res, store.Volumes("app"))
	}
}

// TestStartBringsATakenDownStackUpFromItsDeployedFiles: a start of a
// project without containers (taken down) runs Compose up from the files
// on disk when they still hash to the applied revision, and refuses with
// stack_definition_changed when they changed; without the hash (an older
// manager) it still asks for a deploy (#280).
func TestStartBringsATakenDownStackUpFromItsDeployedFiles(t *testing.T) {
	e, dir := deployFixture(t)
	e.svc.opts.DownVolumes = downvolumes.New(t.TempDir())
	deployed := protocol.NewSourceSnapshot([]protocol.SourceFile{
		{Path: ".env", Content: []byte("DB_TAG=16\n")}, {Path: "compose.yaml", Content: []byte(appYAML)},
		{Path: "db.env", Content: []byte("POSTGRES_PASSWORD=pw\n")}}).Hash

	if res, _ := run(t, e.svc, jobspec.StackStart, protocol.StackJobInput{Stack: ref("app")}); res.Outcome == jobexec.OutcomeSucceeded {
		t.Fatalf("start without containers nor hash: %+v", res)
	}
	res, out := run(t, e.svc, jobspec.StackStart, protocol.StackJobInput{Stack: ref("app"), AppliedHash: deployed, Services: []string{"web"}})
	if res.Outcome != jobexec.OutcomeSucceeded || !slices.Equal(e.c.calls, []string{"up:app"}) ||
		!slices.Equal(e.c.upServices[0], []string{"web"}) || len(out.After) != 2 {
		t.Fatalf("start from the deployed files: %+v calls %v services %v after %+v", res, e.c.calls, e.c.upServices, out.After)
	}

	e.eng.mu.Lock()
	e.eng.containers = nil
	e.eng.mu.Unlock()
	writeTree(t, dir, map[string]string{".env": "DB_TAG=17\n"})
	res, _ = run(t, e.svc, jobspec.StackStart, protocol.StackJobInput{Stack: ref("app"), AppliedHash: deployed})
	if res.Outcome == jobexec.OutcomeSucceeded || res.ErrorClass != protocol.StackClassDefinitionChanged || len(e.c.calls) != 1 {
		t.Fatalf("start after the files changed: %+v calls %v", res, e.c.calls)
	}
}
