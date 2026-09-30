package app

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/engine/enginefake"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// Docker maintenance (#14) through the real manager, a real agent session
// and the agent's prune executor over a fake Engine.

type maintRule struct {
	Category    string `json:"category"`
	Enabled     bool   `json:"enabled"`
	MinAgeHours int64  `json:"minAgeHours"`
	VolumeOptIn bool   `json:"volumeOptIn,omitempty"`
}

type maintPolicy struct {
	ID       string      `json:"id"`
	Enabled  bool        `json:"enabled"`
	Revision int64       `json:"revision"`
	Rules    []maintRule `json:"rules"`
	Schedule struct {
		Cron     string `json:"cron"`
		TimeZone string `json:"timeZone"`
		Enabled  bool   `json:"enabled"`
		CatchUp  string `json:"catchUp"`
	} `json:"schedule"`
	LastRun *struct {
		JobID          string `json:"jobId"`
		State          string `json:"state"`
		Origin         string `json:"origin"`
		Removed        int    `json:"removed"`
		BytesReclaimed int64  `json:"bytesReclaimed"`
	} `json:"lastRun"`
}

type maintPreview struct {
	Remove     int `json:"remove"`
	Categories []struct {
		Category string `json:"category"`
		Items    []struct {
			Name     string `json:"name"`
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		} `json:"items"`
	} `json:"categories"`
}

func (p maintPreview) decisions() []string {
	var out []string
	for _, c := range p.Categories {
		for _, it := range c.Items {
			out = append(out, c.Category+" "+it.Decision+" "+it.Name)
		}
	}
	slices.Sort(out)
	return out
}

func etag(rev int64) reqOpt { return header("If-Match", `"`+itoa(int(rev))+`"`) }

// maintenanceHost is an Engine with old candidates of four categories,
// used objects and a stopped Docker Agent container.
func maintenanceHost(e *env) *enginefake.Engine {
	fe := enginefake.New("ENGINE-MAINT")
	old := e.clk.Now().Add(-60 * 24 * time.Hour)
	fe.AddImage("nginx:1.27")
	fe.SetImageCreated("nginx:1.27", old)
	fe.SetImageCreated(fe.AddImage("app:old"), old)
	fe.AddContainer(engine.ContainerSpec{Name: "web", Image: "nginx:1.27"}, true)
	for name, labels := range map[string]map[string]string{"old-job": nil, "docker-agent-old": {protocol.LabelRole: "agent"}} {
		fe.AddContainer(engine.ContainerSpec{Name: name, Image: "nginx:1.27", Labels: labels}, false)
		fe.SetContainerState(name, "exited")
		fe.SetContainerTimes(name, old, old)
	}
	fe.AddVolume("olddata", nil)
	fe.SetVolumeCreated("olddata", old)
	fe.SetVolumeSize("olddata", 4096)
	fe.AddNetwork("oldnet", nil)
	fe.SetNetworkCreated("oldnet", old)
	return fe
}

// TestMaintenancePolicyLifecycle covers #14 end to end: safe defaults (no
// rule enabled, schedule disabled, nothing pruned on first install), the
// separate volume opt-in, an accurate preview, confirmation, one durable
// job for foreground and background presentations, overlap refusal,
// Docker Manager's own objects surviving, the latest result on the policy, a
// scheduled run as the service identity, and an offline agent.
func TestMaintenancePolicyLifecycle(t *testing.T) {
	e := newEnv(t)
	fe := maintenanceHost(e)
	a := e.connectAgent("Maint", fe)
	owner, _ := e.setupOwner()
	ctx := testutil.Context(t)

	// Suggested defaults: every rule disabled, 30 days.
	var defaults struct {
		Rules      []maintRule `json:"rules"`
		Categories []struct {
			Category    string   `json:"category"`
			Limitations []string `json:"limitations"`
		} `json:"categories"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/maintenance-defaults", nil).json(t, &defaults)
	if len(defaults.Rules) != 7 || len(defaults.Categories) != 7 {
		t.Fatalf("defaults %+v", defaults)
	}
	for _, r := range defaults.Rules {
		if r.Enabled || r.MinAgeHours != 720 {
			t.Fatalf("default rule %+v", r)
		}
	}

	// A new policy starts with every rule and its schedule disabled.
	var pol maintPolicy
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/maintenance-policies",
		map[string]any{"environmentId": a.env, "name": "Weekly cleanup"}).json(t, &pol)
	if pol.Enabled || pol.Schedule.Enabled || pol.Schedule.Cron != "0 3 * * 0" || pol.Schedule.TimeZone != "UTC" || pol.Schedule.CatchUp != "skip" {
		t.Fatalf("new policy %+v", pol)
	}
	for _, r := range pol.Rules {
		if r.Enabled {
			t.Fatalf("rule enabled by default: %+v", r)
		}
	}
	base := "/api/v1/maintenance-policies/" + pol.ID
	var pv maintPreview
	owner.must(http.StatusOK, http.MethodPost, base+"/previews", nil).json(t, &pv)
	if pv.Remove != 0 || len(pv.Categories) != 0 {
		t.Fatalf("preview of a new policy: %+v", pv)
	}
	owner.fail(http.StatusConflict, "maintenance_policy_empty", http.MethodPost, base+"/runs", map[string]any{"confirm": true})

	// Volume rules need their own explicit opt-in.
	owner.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPatch, base,
		map[string]any{"rules": []maintRule{{Category: "named_volumes", Enabled: true, MinAgeHours: 720}}}, etag(pol.Revision))
	owner.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPatch, base,
		map[string]any{"rules": []maintRule{{Category: "anonymous_volumes", Enabled: true, MinAgeHours: 720},
			{Category: "named_volumes", Enabled: true, MinAgeHours: 720, VolumeOptIn: true}}}, etag(pol.Revision))
	owner.must(http.StatusOK, http.MethodPatch, base, map[string]any{"rules": []maintRule{
		{Category: "stopped_containers", Enabled: true, MinAgeHours: 720},
		{Category: "unused_images", Enabled: true, MinAgeHours: 720},
		{Category: "unused_networks", Enabled: true, MinAgeHours: 720},
		{Category: "named_volumes", Enabled: true, MinAgeHours: 720, VolumeOptIn: true},
	}}, etag(pol.Revision)).json(t, &pol)
	for _, r := range pol.Rules {
		if r.Category == "anonymous_volumes" && r.Enabled {
			t.Fatal("the named-volume opt-in enabled anonymous volumes")
		}
	}

	// Preview: candidates with reasons, Docker Manager's container protected.
	owner.must(http.StatusOK, http.MethodPost, base+"/previews", nil).json(t, &pv)
	want := []string{
		"named_volumes remove olddata",
		"stopped_containers protected docker-agent-old",
		"stopped_containers remove old-job",
		"unused_images remove app:old",
		"unused_networks protected bridge", "unused_networks protected host", "unused_networks protected none",
		"unused_networks remove oldnet",
	}
	if got := pv.decisions(); !slices.Equal(got, want) || pv.Remove != 4 {
		t.Fatalf("preview:\n got %v\nwant %v", got, want)
	}
	if len(fe.ContainerNames()) != 3 {
		t.Fatal("the preview removed something")
	}

	// Manual runs need confirmation.
	owner.fail(http.StatusConflict, "prune_confirmation_required", http.MethodPost, base+"/runs", map[string]any{"background": true})
	// Background and foreground are presentations of the same durable job:
	// a repeated request (same key) returns it whatever the preference.
	job := jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, base+"/runs", map[string]any{"confirm": true, "background": true},
		header("Idempotency-Key", "prune-1")))
	if again := jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, base+"/runs", map[string]any{"confirm": true, "background": false},
		header("Idempotency-Key", "prune-1"))); again != job {
		t.Fatalf("foreground retry started job %s, want %s", again, job)
	}
	// Another run while this one is not finished is refused.
	owner.fail(http.StatusConflict, "maintenance_run_active", http.MethodPost, base+"/runs", map[string]any{"confirm": true})

	j := e.runJob(job)
	if j.State != domain.JobSucceeded || j.Kind != jobspec.PruneRun || j.Origin != domain.OriginManual || j.PolicyID != pol.ID {
		t.Fatalf("job %+v", j)
	}
	if len(j.Items) != 4 {
		t.Fatalf("job items %+v", j.Items)
	}
	if got := fe.ContainerNames(); !slices.Equal(got, []string{"docker-agent-old", "web"}) {
		t.Fatalf("containers left: %v", got)
	}
	if slices.Contains(fe.VolumeNames(), "olddata") || slices.Contains(fe.NetworkNames(), "oldnet") {
		t.Fatal("volume or network survived")
	}
	for _, tags := range fe.Images() {
		if slices.Contains(tags, "app:old") {
			t.Fatal("app:old survived")
		}
	}
	owner.must(http.StatusOK, http.MethodGet, base, nil).json(t, &pol)
	if pol.LastRun == nil || pol.LastRun.JobID != job || pol.LastRun.State != "succeeded" || pol.LastRun.Removed != 4 ||
		pol.LastRun.BytesReclaimed < 4096 || pol.LastRun.Origin != "manual" {
		t.Fatalf("last run %+v", pol.LastRun)
	}
	// The job is readable through /jobs with its items (audited with it).
	var jb struct {
		Items []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"items"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/jobs/"+job, nil).json(t, &jb)
	if len(jb.Items) != 4 {
		t.Fatalf("job items via API: %+v", jb)
	}

	// A new candidate for the scheduled run.
	fe.AddContainer(engine.ContainerSpec{Name: "later-job", Image: "nginx:1.27"}, false)
	fe.SetContainerState("later-job", "exited")
	fe.SetContainerTimes("later-job", e.clk.Now().Add(-40*24*time.Hour), e.clk.Now().Add(-40*24*time.Hour))

	// Offline agent: no preview. (The agent goes offline before the clock
	// jumps below, which would otherwise end its session at random.)
	a.stop()
	owner.fail(http.StatusServiceUnavailable, "environment_offline", http.MethodPost, base+"/previews", nil)

	// Scheduled runs: the manager's service identity, never the policy
	// creator, always a background job; they wait for an offline agent up
	// to the offline deadline and fail without touching anything.
	// The hourly run is due two minutes ahead (not at the next full hour,
	// up to an hour away, which would outlive the owner's idle session
	// depending on where the clock starts).
	minute := (e.clk.Now().Minute() + 2) % 60
	owner.must(http.StatusOK, http.MethodPatch, base, map[string]any{"schedule": map[string]any{"cron": itoa(minute) + " * * * *", "timeZone": "UTC",
		"enabled": true}}, etag(pol.Revision)).json(t, &pol)
	sched := e.m.Scheduler()
	if err := sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	// One minute after the next due instant: the run is on time (a run
	// missed while the manager was down would be skipped: CatchUp skip).
	now := e.clk.Now()
	due := now.Truncate(time.Hour).Add(time.Duration(minute) * time.Minute)
	for !due.After(now) {
		due = due.Add(time.Hour)
	}
	e.clk.Advance(due.Add(time.Minute).Sub(now))
	if err := sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	js, err := e.m.Jobs().List(ctx, domain.JobFilter{Kinds: []domain.JobKind{jobspec.PruneRun},
		Target: &domain.JobTarget{Type: domain.TargetMaintenancePolicy, ID: pol.ID}})
	if err != nil {
		t.Fatal(err)
	}
	var scheduled *domain.Job
	for i := range js {
		if js[i].Origin == domain.OriginScheduled {
			scheduled = &js[i]
		}
	}
	if scheduled == nil || scheduled.InitiatorUserID != "" || scheduled.PolicyID != pol.ID || scheduled.IdempotencyKey == "" {
		t.Fatalf("scheduled jobs: %+v", js)
	}
	if err := e.m.Jobs().DispatchPending(ctx); err != nil {
		t.Fatal(err)
	}
	if j, _ := e.m.Jobs().Get(ctx, scheduled.ID); j.State != domain.JobBlocked || j.BlockedReason != "agent_offline" {
		t.Fatalf("scheduled run with the agent offline: %+v", j)
	}
	// A manual run while the scheduled one waits is refused; so is the
	// next scheduled instant (overlap).
	owner.fail(http.StatusConflict, "maintenance_run_active", http.MethodPost, base+"/runs", map[string]any{"confirm": true})
	e.clk.Advance(time.Hour)
	if err := sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	_, runs, ok, err := sched.Status(ctx, "prune", pol.ID, 5)
	if err != nil || !ok || len(runs) < 2 || runs[0].Outcome != domain.RunSkipped || runs[1].Outcome != domain.RunEnqueued {
		t.Fatalf("schedule runs %+v (%v)", runs, err)
	}
	// Past the offline deadline the waiting run fails; nothing changed.
	if err := e.m.Jobs().DispatchPending(ctx); err != nil {
		t.Fatal(err)
	}
	if j, _ := e.m.Jobs().Get(ctx, scheduled.ID); j.State != domain.JobFailed || j.ErrorClass != domain.ErrorAgentOffline {
		t.Fatalf("scheduled run after the offline deadline %+v", j)
	}
	if !slices.Contains(fe.ContainerNames(), "later-job") {
		t.Fatal("an offline run removed something")
	}
	// (Two hours passed: the owner's session expired; read the service.)
	got, err := e.m.Maintenance().Get(ctx, pol.ID)
	if err != nil || got.LastRun == nil || got.LastRun.JobID != scheduled.ID || got.LastRun.State != domain.JobFailed ||
		got.LastRun.Origin != domain.OriginScheduled {
		t.Fatalf("last run %+v (%v)", got.LastRun, err)
	}
}

// TestMaintenanceEditCancelsWaitingRuns: a run that has not started when
// the policy's rules change (or the policy is deleted) is cancelled: it
// carries the old rules.
func TestMaintenanceEditCancelsWaitingRuns(t *testing.T) {
	e := newEnv(t)
	fe := maintenanceHost(e)
	a := e.connectAgent("Maint", fe)
	owner, _ := e.setupOwner()
	ctx := testutil.Context(t)
	var pol maintPolicy
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/maintenance-policies", map[string]any{"environmentId": a.env, "name": "p",
		"rules": []maintRule{{Category: "stopped_containers", Enabled: true, MinAgeHours: 1}}}).json(t, &pol)
	base := "/api/v1/maintenance-policies/" + pol.ID
	a.stop()
	job := jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, base+"/runs", map[string]any{"confirm": true}))
	// A name change keeps the waiting run.
	owner.must(http.StatusOK, http.MethodPatch, base, map[string]any{"name": "renamed"}, etag(pol.Revision)).json(t, &pol)
	if j, _ := e.m.Jobs().Get(ctx, job); j.State.Terminal() {
		t.Fatalf("rename cancelled the run: %+v", j)
	}
	owner.must(http.StatusOK, http.MethodPatch, base, map[string]any{"rules": []maintRule{{Category: "stopped_containers", Enabled: true,
		MinAgeHours: 2000}}}, etag(pol.Revision)).json(t, &pol)
	if j, _ := e.m.Jobs().Get(ctx, job); j.State != domain.JobCancelled {
		t.Fatalf("the waiting run survived a rule change: %+v", j)
	}
	job2 := jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, base+"/runs", map[string]any{"confirm": true}))
	owner.must(http.StatusNoContent, http.MethodDelete, base, nil, etag(pol.Revision))
	if j, _ := e.m.Jobs().Get(ctx, job2); j.State != domain.JobCancelled {
		t.Fatalf("the waiting run survived the deletion: %+v", j)
	}
	if !slices.Contains(fe.ContainerNames(), "old-job") {
		t.Fatal("a cancelled run removed something")
	}
}
