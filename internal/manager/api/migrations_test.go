package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/authztest"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/authz/policy"
	"github.com/neurekadev/docker-manager/internal/manager/migrations"
	"github.com/neurekadev/docker-manager/internal/manager/permissions"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// fakeMigrations records calls (the real service is tested in
// internal/manager/migrations).
type fakeMigrations struct {
	mu      sync.Mutex
	calls   []string
	owner   []bool
	blocked bool
	err     error
}

func (f *fakeMigrations) record(call string, owner bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	f.owner = append(f.owner, owner)
}

func plan(kind domain.MigrationKind) migrations.Plan {
	return migrations.Plan{Kind: kind, SourceEnvironmentID: "env-1", TargetEnvironmentID: "env-2", ProjectName: "shop", TargetDir: "shop",
		Warnings: []migrations.Finding{{Code: migrations.FindingPlainHTTP, Message: "plain"},
			{Code: migrations.FindingExternalBind, Message: "binds a host path", Service: "web", Resource: "/srv/secret-bind"}},
		Services: []migrations.ServicePlan{{Name: "web", Image: "nginx:1.27", Action: migrations.ImagePull}},
		Volumes:  []migrations.VolumePlan{{Source: "shop_data", Target: "shop_data", Action: migrations.VolumeCopy, Bytes: 10}},
		Access: migrations.AccessPreview{Complete: true, Changes: []permissions.AccessChange{{UserID: "u1", Username: "ann",
			Lost: []string{"stack.read"}}}}}
}

func (f *fakeMigrations) PreviewStack(_ context.Context, _ authz.Principal, owner bool, st domain.Stack, r migrations.StackRequest) (migrations.Plan, error) {
	f.record("preview-stack:"+st.ID+">"+r.TargetEnvironmentID, owner)
	return plan(domain.MigrationKindStack), f.err
}

func (f *fakeMigrations) StartStack(_ context.Context, _ authz.Principal, st domain.Stack, r migrations.StackRequest) (domain.Job, domain.Migration, error) {
	f.record("start-stack:"+st.ID+">"+r.TargetEnvironmentID+":"+strings.Join(r.Selection.ExcludeVolumes, ","), false)
	if f.blocked {
		p := plan(domain.MigrationKindStack)
		p.Blockers = []migrations.Finding{{Code: migrations.FindingPortConflict, Message: "host port 8080/tcp is taken"}}
		return domain.Job{}, domain.Migration{}, &migrations.BlockedError{Plan: p}
	}
	return domain.Job{ID: "job-mig", Kind: "stack.migrate", State: domain.JobQueued, Attempt: 1}, domain.Migration{ID: "job-mig"}, f.err
}

func (f *fakeMigrations) RemoveSource(_ context.Context, _ authz.Principal, stackID, migrationID, _ string) (domain.Job, error) {
	f.record("remove-source:"+stackID+"/"+migrationID, false)
	if f.err != nil {
		return domain.Job{}, f.err
	}
	return domain.Job{ID: "job-rm", Kind: "stack.remove_source", State: domain.JobQueued, Attempt: 1}, nil
}

func (f *fakeMigrations) PreviewVolume(_ context.Context, _ authz.Principal, owner bool, env, volume string, r migrations.VolumeRequest) (migrations.Plan, error) {
	f.record("preview-volume:"+env+"/"+volume+">"+r.TargetEnvironmentID, owner)
	return plan(domain.MigrationKindVolume), f.err
}

func (f *fakeMigrations) StartVolume(_ context.Context, _ authz.Principal, env, volume string, r migrations.VolumeRequest) (domain.Job, domain.Migration, error) {
	f.record("start-volume:"+env+"/"+volume+">"+r.TargetEnvironmentID+":"+r.TargetName, false)
	return domain.Job{ID: "job-vol", Kind: "volume.migrate", State: domain.JobQueued, Attempt: 1}, domain.Migration{ID: "job-vol"}, f.err
}

var _ MigrationService = (*fakeMigrations)(nil)

func migrationAPIFor(t *testing.T, pol *authztest.Policy) (http.Handler, *fakeMigrations) {
	t.Helper()
	svc, mig := newFakeStacks(), &fakeMigrations{}
	pol.Locate(func(ref authz.ResourceRef) policy.Location {
		if st, ok := svc.stacks[ref.ID]; ok && ref.Type == catalog.TypeStack {
			return policy.Location{Found: true, EnvironmentID: st.EnvironmentID}
		}
		return policy.Location{}
	})
	mux := http.NewServeMux()
	New(mux, Deps{Stacks: svc, Migrations: mig, Agents: newFakeAgents(), Authorizer: pol, Clock: testutil.FakeClock(), Idempotency: &memIdempotency{}})
	return authztest.Authenticate(withTestContext(t, mux, "")), mig
}

var migrateBody = map[string]any{"targetEnvironmentId": "env-2", "excludeVolumes": []string{"shop_cache"}}

// TestStackMigrationNeedsBothEnds (#35): stack.migrate on the stack plus
// stack.create and stack.deploy on the destination; the preview and the
// migration check both ends; an invisible destination is not found.
func TestStackMigrationNeedsBothEnds(t *testing.T) {
	pol := authztest.New().
		User("mover", "allow stack.migrate @stack:st-1", "allow stack.create @env:env-2", "allow stack.deploy @env:env-2").
		User("nodeploy", "allow stack.migrate @stack:st-1", "allow stack.create @env:env-2").
		User("blind", "allow stack.migrate @stack:st-1").
		User("reader", "allow stack.read @stack:st-1", "allow stack.create @env:env-2", "allow stack.deploy @env:env-2").
		Owner("olga")
	h, mig := migrationAPIFor(t, pol)
	preview := authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/migration-previews", Body: migrateBody}
	start := authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/migrations", Body: migrateBody,
		Headers: map[string]string{"Idempotency-Key": "k1"}}
	remove := authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/migrations/job-mig/source-removals",
		Headers: map[string]string{"Idempotency-Key": "k2"}}

	r := authztest.Do(t, h, "mover", preview)
	var p MigrationPreview
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &p) != nil || p.Kind != "stack" || !p.Allowed || len(p.Warnings) != 2 ||
		p.Volumes[0].Action != "copy" || p.Access.Changes[0].Username != "ann" {
		t.Fatalf("preview %d %s", r.Status, r.Body)
	}
	// Bind sources come from the definition: stack.definition.read only.
	authztest.AssertAbsent(t, "preview without stack.definition.read", r.Body, "/srv/secret-bind")
	if r := authztest.Do(t, h, "mover", start); r.Status != http.StatusAccepted || r.Header.Get("Location") != "/api/v1/jobs/job-mig" {
		t.Fatalf("start %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "mover", remove); r.Status != http.StatusAccepted {
		t.Fatalf("source removal %d %s", r.Status, r.Body)
	}
	if got := strings.Join(mig.calls, " "); got != "preview-stack:st-1>env-2 start-stack:st-1>env-2:shop_cache remove-source:st-1/job-mig" {
		t.Fatalf("calls %s", got)
	}
	if mig.owner[0] {
		t.Error("a non-owner must not see every user's access change")
	}
	// The destination is visible but deploying there is not granted: 403.
	for _, c := range []authztest.Call{preview, start} {
		if r := authztest.Do(t, h, "nodeploy", c); r.Status != http.StatusForbidden {
			t.Errorf("nodeploy %s: %d %s", c.Path, r.Status, r.Body)
		}
		// The destination is invisible: 404.
		if r := authztest.Do(t, h, "blind", c); r.Status != http.StatusNotFound {
			t.Errorf("blind %s: %d %s", c.Path, r.Status, r.Body)
		}
		// stack.migrate is missing on the (visible) stack: 403.
		if r := authztest.Do(t, h, "reader", c); r.Status != http.StatusForbidden {
			t.Errorf("reader %s: %d %s", c.Path, r.Status, r.Body)
		}
	}
	if r := authztest.Do(t, h, "reader", remove); r.Status != http.StatusForbidden {
		t.Errorf("reader removal: %d", r.Status)
	}
	if len(mig.calls) != 3 {
		t.Fatalf("a refused request reached the service: %v", mig.calls)
	}
	// The owner sees every affected user (and the bind sources).
	if r := authztest.Do(t, h, "olga", preview); r.Status != http.StatusOK || !mig.owner[len(mig.owner)-1] || !strings.Contains(string(r.Body), "/srv/secret-bind") {
		t.Fatalf("owner preview %d %s", r.Status, r.Body)
	}
	// The destination is required (validated after authorization).
	if r := authztest.Do(t, h, "mover", authztest.Call{Method: http.MethodPost, Path: preview.Path, Body: map[string]any{}}); r.Status != http.StatusUnprocessableEntity {
		t.Errorf("missing destination: %d", r.Status)
	}
	if r := authztest.Do(t, h, "reader", authztest.Call{Method: http.MethodPost, Path: preview.Path, Body: map[string]any{}}); r.Status != http.StatusForbidden {
		t.Errorf("missing destination without the capability: %d", r.Status)
	}
}

// TestStackMigrationErrors: blockers are 409 migration_blocked with each
// blocker in details; source-removal states map to their codes.
func TestStackMigrationErrors(t *testing.T) {
	pol := authztest.Only("mover", "allow stack.migrate @stack:st-1", "allow stack.create @env:env-2", "allow stack.deploy @env:env-2")
	h, mig := migrationAPIFor(t, pol)
	mig.blocked = true
	r := authztest.Do(t, h, "mover", authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/migrations", Body: migrateBody})
	var e errJSON
	_ = json.Unmarshal(r.Body, &e)
	if r.Status != http.StatusConflict || e.Code != CodeMigrationBlocked || len(e.Details) != 1 || e.Details[0].Field != "preflight.port_conflict" {
		t.Fatalf("blocked %d %s", r.Status, r.Body)
	}
	for err, want := range map[error]string{migrations.ErrNotCompleted: CodeMigrationNotCompleted, migrations.ErrSourceRemoved: CodeMigrationSourceRemoved,
		migrations.ErrSourceInUse: CodeMigrationSourceInUse, domain.ErrMigrationNotFound: CodeNotFound} {
		mig.err = err
		r := authztest.Do(t, h, "mover", authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/migrations/m1/source-removals"})
		_ = json.Unmarshal(r.Body, &e)
		if e.Code != want {
			t.Errorf("%v: %d %s", err, r.Status, r.Body)
		}
	}
	mig.err = &migrations.AgentError{Side: "destination", Offline: true}
	r = authztest.Do(t, h, "mover", authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/migration-previews", Body: migrateBody})
	_ = json.Unmarshal(r.Body, &e)
	if r.Status != http.StatusServiceUnavailable || e.Code != CodeEnvironmentOffline {
		t.Errorf("offline %d %s", r.Status, r.Body)
	}
}

// TestVolumeMigrationRoutes: volume.migrate on the volume and
// volume.create on the destination.
func TestVolumeMigrationRoutes(t *testing.T) {
	mig := &fakeMigrations{}
	pol := authztest.New().
		User("mover", "allow volume.migrate @volume:env-1/scratch", "allow volume.read @volume:env-1/scratch", "allow volume.create @env:env-2").
		User("nocreate", "allow volume.migrate @volume:env-1/scratch", "allow volume.read @env:env-2").
		User("reader", "allow volume.read @volume:env-1/scratch", "allow volume.create @env:env-2")
	f := newDockerFixtureWith(t, pol, func(d *Deps) { d.Migrations = mig })
	body := map[string]any{"targetEnvironmentId": "env-2", "targetName": "scratch-copy", "acknowledgeCrashConsistency": true}
	preview := authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-1/volumes/scratch/migration-previews", Body: body}
	start := authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-1/volumes/scratch/migrations", Body: body}
	if r := f.do("mover", preview); r.Status != http.StatusOK {
		t.Fatalf("preview %d %s", r.Status, r.Body)
	}
	if r := f.do("mover", start); r.Status != http.StatusAccepted {
		t.Fatalf("start %d %s", r.Status, r.Body)
	}
	if got := strings.Join(mig.calls, " "); got != "preview-volume:env-1/scratch>env-2 start-volume:env-1/scratch>env-2:scratch-copy" {
		t.Fatalf("calls %s", got)
	}
	for user, want := range map[string]int{"nocreate": http.StatusForbidden, "reader": http.StatusForbidden} {
		for _, c := range []authztest.Call{preview, start} {
			if r := f.do(user, c); r.Status != want {
				t.Errorf("%s %s: %d %s", user, c.Path, r.Status, r.Body)
			}
		}
	}
	if len(mig.calls) != 2 {
		t.Fatalf("a refused request reached the service: %v", mig.calls)
	}
}
