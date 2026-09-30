package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/authztest"
	"github.com/neurekadev/docker-manager/internal/manager/migrations"
)

func environmentPlan() migrations.EnvironmentPlan {
	p := plan(domain.MigrationKindStack)
	p.StackID = "st-1"
	return migrations.EnvironmentPlan{SourceEnvironmentID: "env-1", TargetEnvironmentID: "env-2",
		Stacks:   []migrations.EnvironmentStack{{StackID: "st-1", Name: "shop", Plan: p}},
		Groups:   [][]string{{"st-1"}},
		Networks: []migrations.NetworkCreation{{Name: "proxy", Driver: "bridge", UsedBy: []string{"st-1"}}},
		Skipped:  []migrations.SkippedStack{{StackID: "st-dm", Name: "docker-manager", Reason: migrations.SkipDockerManager}},
		Blockers: []migrations.Finding{}, Warnings: []migrations.Finding{}}
}

func (f *fakeMigrations) PreviewEnvironment(_ context.Context, _ authz.Principal, owner bool, source string, r migrations.EnvironmentRequest) (migrations.EnvironmentPlan, error) {
	f.record("preview-environment:"+source+">"+r.TargetEnvironmentID+":"+strings.Join(r.Stacks, ","), owner)
	return environmentPlan(), f.err
}

func (f *fakeMigrations) StartEnvironment(_ context.Context, _ authz.Principal, source string, r migrations.EnvironmentRequest) (domain.Job, domain.EnvironmentMigration, error) {
	f.record("start-environment:"+source+">"+r.TargetEnvironmentID, false)
	if f.blocked {
		p := environmentPlan()
		p.Stacks[0].Plan.Blockers = []migrations.Finding{{Code: migrations.FindingPortConflict, Message: "host port 8080/tcp is taken"}}
		return domain.Job{}, domain.EnvironmentMigration{}, &migrations.EnvironmentBlockedError{Plan: p}
	}
	return domain.Job{ID: "job-env", Kind: "environment.migrate", State: domain.JobQueued, Attempt: 1}, domain.EnvironmentMigration{ID: "job-env"}, f.err
}

func (f *fakeMigrations) GetEnvironmentMigration(_ context.Context, id string) (domain.EnvironmentMigration, error) {
	f.record("get-environment:"+id, false)
	if id != "job-env" {
		return domain.EnvironmentMigration{}, domain.ErrEnvironmentMigrationNotFound
	}
	return domain.EnvironmentMigration{ID: "job-env", SourceEnvironmentID: "env-1", TargetEnvironmentID: "env-2", State: domain.MigrationFailed,
		Groups: [][]string{{"st-1"}, {"st-gone"}},
		Stacks: []domain.EnvironmentMigrationStack{{StackID: "st-1", Name: "shop", MigrationID: "job-mig", State: domain.EnvironmentStackMoved},
			{StackID: "st-gone", Name: "gone", State: domain.EnvironmentStackFailed}}}, nil
}

func (f *fakeMigrations) EnvironmentMigrations(ctx context.Context, source string, _ int) ([]domain.EnvironmentMigration, error) {
	m, _ := f.GetEnvironmentMigration(ctx, "job-env")
	return []domain.EnvironmentMigration{m}, nil
}

var _ EnvironmentMigrationService = (*fakeMigrations)(nil)

// TestEnvironmentMigrationRoutes (#35): a visible source and stack.create
// on a visible destination; the service leaves out the stacks the caller
// may not migrate. Records list only the stacks the caller can see.
func TestEnvironmentMigrationRoutes(t *testing.T) {
	mig := &fakeMigrations{}
	pol := authztest.New().
		User("mover", "allow stack.migrate @env:env-1", "allow stack.create @env:env-2").
		User("nocreate", "allow stack.migrate @env:env-1", "allow stack.read @env:env-2").
		User("blind", "allow stack.migrate @env:env-1")
	f := newDockerFixtureWith(t, pol, func(d *Deps) { d.EnvironmentMigrations, d.Stacks = mig, newFakeStacks() })
	body := map[string]any{"targetEnvironmentId": "env-2", "stacks": []string{"st-1"}}
	preview := authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-1/migration-previews", Body: body}
	start := authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-1/migrations", Body: body,
		Headers: map[string]string{"Idempotency-Key": "k1"}}

	r := f.do("mover", preview)
	var p EnvironmentMigrationPreview
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &p) != nil || !p.Allowed || len(p.Stacks) != 1 || p.Stacks[0].Preview.Kind != "stack" ||
		p.Networks[0].Name != "proxy" || p.Skipped[0].Reason != "docker_manager" {
		t.Fatalf("preview %d %s", r.Status, r.Body)
	}
	authztest.AssertAbsent(t, "preview without stack.definition.read", r.Body, "/srv/secret-bind")
	if r := f.do("mover", start); r.Status != http.StatusAccepted || r.Header.Get("Location") != "/api/v1/jobs/job-env" {
		t.Fatalf("start %d %s", r.Status, r.Body)
	}
	if got := strings.Join(mig.calls, " "); got != "preview-environment:env-1>env-2:st-1 start-environment:env-1>env-2" {
		t.Fatalf("calls %s", got)
	}
	for user, want := range map[string]int{"nocreate": http.StatusForbidden, "blind": http.StatusNotFound} {
		for _, c := range []authztest.Call{preview, start} {
			if r := f.do(user, c); r.Status != want {
				t.Errorf("%s %s: %d %s", user, c.Path, r.Status, r.Body)
			}
		}
	}
	if len(mig.calls) != 2 {
		t.Fatalf("a refused request reached the service: %v", mig.calls)
	}

	// The record: the stack that no longer exists is not listed.
	r = f.do("mover", authztest.Call{Method: http.MethodGet, Path: "/api/v1/environments/env-1/migrations/job-env"})
	var m EnvironmentMigration
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &m) != nil || len(m.Stacks) != 1 || m.Stacks[0].MigrationID != "job-mig" ||
		m.Stacks[0].State != "moved" || len(m.Groups) != 1 || m.State != "failed" {
		t.Fatalf("get %d %s", r.Status, r.Body)
	}
	authztest.AssertAbsent(t, "a stack the caller cannot see", r.Body, "st-gone")
	r = f.do("mover", authztest.Call{Method: http.MethodGet, Path: "/api/v1/environments/env-1/migrations"})
	if r.Status != http.StatusOK || !strings.Contains(string(r.Body), `"id":"job-env"`) {
		t.Fatalf("list %d %s", r.Status, r.Body)
	}
	if r := f.do("mover", authztest.Call{Method: http.MethodGet, Path: "/api/v1/environments/env-1/migrations/other"}); r.Status != http.StatusNotFound {
		t.Errorf("unknown migration %d", r.Status)
	}
	// Another environment's record is not found under this one (where the
	// caller migrates no stack: refused before any lookup).
	if r := f.do("mover", authztest.Call{Method: http.MethodGet, Path: "/api/v1/environments/env-2/migrations/job-env"}); r.Status != http.StatusForbidden && r.Status != http.StatusNotFound {
		t.Errorf("wrong environment %d %s", r.Status, r.Body)
	}
}

// TestEnvironmentMigrationRecordsAfterEveryStackMoved (#35): once every
// stack the caller migrates moved away, the source has none left; the
// records stay readable to whoever may migrate one of their stacks where
// it is now (the old copies wait for their removal), and to no one else.
func TestEnvironmentMigrationRecordsAfterEveryStackMoved(t *testing.T) {
	mig := &fakeMigrations{}
	stacks := newFakeStacks()
	moved := stacks.stacks["st-1"]
	moved.EnvironmentID = "env-2"
	stacks.stacks["st-1"] = moved
	pol := authztest.New().
		User("mover", "allow stack.migrate @env:env-2", "allow stack.read @env:env-1").
		User("reader", "allow stack.read @env:env-1", "allow stack.read @env:env-2")
	f := newDockerFixtureWith(t, pol, func(d *Deps) { d.EnvironmentMigrations, d.Stacks = mig, stacks })
	list := authztest.Call{Method: http.MethodGet, Path: "/api/v1/environments/env-1/migrations"}
	get := authztest.Call{Method: http.MethodGet, Path: "/api/v1/environments/env-1/migrations/job-env"}

	r := f.do("mover", get)
	var m EnvironmentMigration
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &m) != nil || len(m.Stacks) != 1 || m.Stacks[0].StackID != "st-1" {
		t.Fatalf("get %d %s", r.Status, r.Body)
	}
	if r := f.do("mover", list); r.Status != http.StatusOK || !strings.Contains(string(r.Body), `"id":"job-env"`) {
		t.Fatalf("list %d %s", r.Status, r.Body)
	}
	if r := f.do("mover", authztest.Call{Method: http.MethodGet, Path: "/api/v1/environments/env-1/migrations/other"}); r.Status != http.StatusForbidden {
		t.Errorf("unknown migration without stack.migrate on the source: %d", r.Status)
	}
	for _, c := range []authztest.Call{list, get} {
		if r := f.do("reader", c); r.Status != http.StatusForbidden {
			t.Errorf("reader %s: %d %s", c.Path, r.Status, r.Body)
		}
	}
	// Starting still needs a stack of the source the caller may migrate.
	start := authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-1/migrations",
		Body: map[string]any{"targetEnvironmentId": "env-2"}, Headers: map[string]string{"Idempotency-Key": "k1"}}
	if r := f.do("mover", start); r.Status != http.StatusForbidden {
		t.Errorf("start %d %s", r.Status, r.Body)
	}
}

// TestEnvironmentMigrationBlocked: blockers are 409 migration_blocked,
// with each stack's blockers in details under its ID.
func TestEnvironmentMigrationBlocked(t *testing.T) {
	mig := &fakeMigrations{blocked: true}
	pol := authztest.Only("mover", "allow stack.migrate @env:env-1", "allow stack.create @env:env-2")
	f := newDockerFixtureWith(t, pol, func(d *Deps) { d.EnvironmentMigrations, d.Stacks = mig, newFakeStacks() })
	r := f.do("mover", authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-1/migrations",
		Body: map[string]any{"targetEnvironmentId": "env-2"}})
	var e errJSON
	_ = json.Unmarshal(r.Body, &e)
	if r.Status != http.StatusConflict || e.Code != CodeMigrationBlocked || len(e.Details) != 1 || e.Details[0].Field != "preflight.st-1.port_conflict" ||
		!strings.HasPrefix(e.Details[0].Message, "shop: ") {
		t.Fatalf("blocked %d %s", r.Status, r.Body)
	}
}
