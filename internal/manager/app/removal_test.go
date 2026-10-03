package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

type removalPreviewJSON struct {
	EnvironmentID string `json:"environmentId"`
	Revision      int64  `json:"revision"`
	Action        string `json:"action"`
	HostUntouched bool   `json:"hostUntouched"`
	Dependents    []struct {
		Kind      string `json:"kind"`
		OnArchive string `json:"onArchive"`
		Count     int    `json:"count"`
		Items     []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	} `json:"dependents"`
	BackupSnapshots int `json:"backupSnapshots"`
	Migration       struct {
		Stacks int `json:"stacks"`
	} `json:"migration"`
}

func (p removalPreviewJSON) count(kind string) int {
	for _, d := range p.Dependents {
		if d.Kind == kind {
			return d.Count
		}
	}
	return -1
}

// TestArchiveAndReattachThroughTheManager (#34 Done-when 3, #24): the
// removal preview lists the environment's dependents; archiving revokes
// the agent, removes the permission rules scoped to the environment
// (audited), keeps stacks, policies and backups, pauses scheduled work and
// touches nothing on the host; re-enrolling the same Engine with a
// reattach enrollment brings the environment back with its stacks, and
// its policies run again.
func TestArchiveAndReattachThroughTheManager(t *testing.T) {
	b := newBackupEnv(t)
	owner, _ := b.setupOwner()
	b.connectBackupAgent("prod")
	env := b.agent.env
	ctx := testutil.Context(t)

	// Backups of the environment, run once by their schedule.
	repo := b.createS3Repo(owner, "Offsite")
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-repositories/"+repo.Repository.ID+"/recovery-confirmations",
		map[string]any{"recoveryKey": repo.RecoveryKey.Key, "backedUp": true})
	b.settings(owner, map[string]any{"enabled": true, "schedule": map[string]any{"cron": "*/5 * * * *", "timeZone": "UTC"}})
	backupRuns := func() int {
		t.Helper()
		js, err := b.m.Jobs().List(ctx, domain.JobFilter{Kinds: []domain.JobKind{"backup.run"}, EnvironmentID: env})
		if err != nil {
			t.Fatal(err)
		}
		return len(js)
	}
	tick := func() {
		t.Helper()
		b.clk.Advance(5 * time.Minute)
		if err := b.m.Scheduler().Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.m.Scheduler().Tick(ctx); err != nil {
		t.Fatal(err)
	}
	tick()
	js, _ := b.m.Jobs().List(ctx, domain.JobFilter{Kinds: []domain.JobKind{"backup.run"}, EnvironmentID: env})
	if len(js) != 1 {
		t.Fatalf("scheduled backups before archiving: %d", len(js))
	}
	if got := b.runJob(js[0].ID); got.State != domain.JobSucceeded {
		t.Fatalf("backup: %s %s", got.State, got.ErrorMessage)
	}
	// Every run also backs up the manager state; it finishes so the next
	// run starts.
	runManagerState := func() {
		t.Helper()
		ms, err := b.m.Jobs().List(ctx, domain.JobFilter{Kinds: []domain.JobKind{"manager.backup"},
			States: []domain.JobState{domain.JobQueued}})
		if err != nil || len(ms) != 1 {
			t.Fatalf("manager state backups: %d %v", len(ms), err)
		}
		if got := b.runJob(ms[0].ID); got.State != domain.JobSucceeded {
			t.Fatalf("manager state backup: %s %s", got.State, got.ErrorMessage)
		}
	}
	runManagerState()
	// Rules of another user: two name the environment, one the stack.
	sam, _, _ := b.newUser(owner, "sam")
	samRules := "/api/v1/users/" + b.userID("sam") + "/permissions"
	owner.putRules(samRules, "allow environment.read @env:"+env, "allow container.restart @container:"+env+"/app-web-1",
		"allow stack.read @stack:"+b.stackID)
	sam.must(http.StatusOK, http.MethodGet, "/api/v1/environments/"+env, nil)

	// The preview: every kind is listed, the stack can be migrated first.
	var prev removalPreviewJSON
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/environments/"+env+"/removal-previews", nil).json(t, &prev)
	if prev.Action != "archive" || !prev.HostUntouched || prev.Migration.Stacks != 1 || len(prev.Dependents) != len(domain.DependentKinds()) {
		t.Fatalf("preview %+v", prev)
	}
	for kind, atLeast := range map[string]int{domain.DependentStack: 1,
		domain.DependentBackupRepository: 1, domain.DependentBackupSet: 1, domain.DependentPermissionRule: 2, domain.DependentSchedule: 1} {
		if prev.count(kind) < atLeast {
			t.Errorf("preview lists %d %s, want >= %d", prev.count(kind), kind, atLeast)
		}
	}
	if prev.count(domain.DependentPermissionRule) != 2 || prev.BackupSnapshots == 0 {
		t.Errorf("rules %d snapshots %d", prev.count(domain.DependentPermissionRule), prev.BackupSnapshots)
	}
	// A user without environment.remove cannot preview.
	sam.fail(http.StatusForbidden, "forbidden", http.MethodPost, "/api/v1/environments/"+env+"/removal-previews", nil)

	// Archive. Nothing on the host changes.
	containersBefore := b.fe.ContainerNames()
	jobsBefore, _ := b.m.Jobs().List(ctx, domain.JobFilter{EnvironmentID: env})
	etag := owner.must(http.StatusOK, http.MethodGet, "/api/v1/environments/"+env, nil).header.Get("ETag")
	owner.must(http.StatusNoContent, http.MethodDelete, "/api/v1/environments/"+env, nil, header("If-Match", etag))
	var archived struct {
		Status string `json:"status"`
		Online bool   `json:"online"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/environments/"+env, nil).json(t, &archived)
	if archived.Status != "archived" || archived.Online {
		t.Fatalf("after archiving %+v", archived)
	}
	if got := b.fe.ContainerNames(); strings.Join(got, ",") != strings.Join(containersBefore, ",") {
		t.Fatalf("containers changed: %v -> %v", containersBefore, got)
	}
	for _, n := range containersBefore {
		if c, ok := b.fe.Container(n); !ok || !c.Details.State.Running {
			t.Fatalf("container %s stopped by the archive", n)
		}
	}
	if jobsAfter, _ := b.m.Jobs().List(ctx, domain.JobFilter{EnvironmentID: env}); len(jobsAfter) != len(jobsBefore) {
		t.Fatalf("archiving created jobs: %d -> %d", len(jobsBefore), len(jobsAfter))
	}
	// The environment's rules are gone (audited); the stack rule stays.
	var doc struct {
		Rules []struct {
			Capability string `json:"capability"`
		} `json:"rules"`
	}
	owner.must(http.StatusOK, http.MethodGet, samRules, nil).json(t, &doc)
	if len(doc.Rules) != 1 || doc.Rules[0].Capability != "stack.read" {
		t.Fatalf("sam's rules after archiving %+v", doc.Rules)
	}
	found := false
	for _, r := range b.auditRows() {
		if r.Action == AuditEnvironmentRulesRemoved {
			var d map[string]any
			_ = json.Unmarshal([]byte(r.Details), &d)
			found = d["count"] == float64(2) && r.ActorKind == "user"
		}
	}
	if !found {
		t.Fatal("no audit record of the removed rules")
	}
	// Hidden from operations: manual work is refused, scheduled work paused.
	owner.fail(http.StatusConflict, "environment_archived", http.MethodPost,
		"/api/v1/environments/"+env+"/containers/app-web-1/restart", nil)
	tick()
	if n := backupRuns(); n != 1 {
		t.Fatalf("scheduled backups while archived: %d", n)
	}
	runManagerState()

	// Re-attach the same Engine: the environment, its stack and its
	// policies come back.
	h := b.connectHost(hostOpts{name: "prod", stacks: b.stacks, volumes: b.volumes, reattach: env, fe: b.fe})
	if h.agent.env != env {
		t.Fatalf("re-attached as %s, want %s", h.agent.env, env)
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/environments/"+env, nil).json(t, &archived)
	if archived.Status != "active" || !archived.Online {
		t.Fatalf("after re-attaching %+v", archived)
	}
	st, err := b.m.Stacks().Get(ctx, b.stackID)
	if err != nil || st.EnvironmentID != env {
		t.Fatalf("stack after re-attaching %+v %v", st, err)
	}
	tick()
	if n := backupRuns(); n != 2 {
		t.Fatalf("scheduled backups after re-attaching: %d", n)
	}
	// The resumed backup runs on the re-attached agent.
	js, _ = b.m.Jobs().List(ctx, domain.JobFilter{Kinds: []domain.JobKind{"backup.run"}, EnvironmentID: env})
	if got := b.runJob(js[0].ID); got.State != domain.JobSucceeded {
		t.Fatalf("backup after re-attaching: %s %s %s", got.State, got.ErrorClass, got.ErrorMessage)
	}
}
