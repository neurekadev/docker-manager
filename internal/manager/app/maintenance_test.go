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

// Docker maintenance (#14, #238) through the real manager, a real agent session
// and the agent's prune executor over a fake Engine.

type maintRule struct {
	Category    string `json:"category"`
	Enabled     bool   `json:"enabled"`
	MinAgeHours int64  `json:"minAgeHours"`
	VolumeOptIn bool   `json:"volumeOptIn,omitempty"`
}

type maintSettings struct {
	ID         string      `json:"id"`
	Enabled    bool        `json:"enabled"`
	Revision   int64       `json:"revision"`
	Rules      []maintRule `json:"rules"`
	Categories []struct {
		Category    string   `json:"category"`
		Limitations []string `json:"limitations"`
	} `json:"categories"`
	ExcludeEnvironments []string `json:"excludeEnvironments"`
	Schedule            struct {
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

// maintPreviews is the preview of every covered environment.
type maintPreviews struct {
	Items []struct {
		EnvironmentID string        `json:"environmentId"`
		Preview       *maintPreview `json:"preview"`
		ErrorClass    string        `json:"errorClass"`
	} `json:"items"`
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

// onlyPreview is the preview of the one environment of a test.
func onlyPreview(t *testing.T, r response) maintPreview {
	t.Helper()
	var pv maintPreviews
	r.json(t, &pv)
	if len(pv.Items) != 1 || pv.Items[0].Preview == nil {
		t.Fatalf("previews %s", r.body)
	}
	return *pv.Items[0].Preview
}

// runJobs are the IDs of the jobs a maintenance run started.
func runJobs(t *testing.T, r response) []string {
	t.Helper()
	var out struct {
		Jobs []struct {
			ID string `json:"id"`
		} `json:"jobs"`
	}
	r.json(t, &out)
	var ids []string
	for _, j := range out.Jobs {
		ids = append(ids, j.ID)
	}
	return ids
}

func etag(rev int64) reqOpt { return header("If-Match", `"`+itoa(int(rev))+`"`) }

const maintPath = "/api/v1/maintenance-settings"

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

// TestMaintenanceLifecycle covers #14 and #238 end to end: safe defaults
// (no rule enabled, maintenance disabled, nothing pruned on first
// install), the separate volume opt-in, an accurate preview, confirmation,
// a repeated request returning the same jobs, overlap refusal, Docker
// Manager's own objects surviving, the latest result on the settings, a
// scheduled run as the service identity, and an offline agent.
func TestMaintenanceLifecycle(t *testing.T) {
	e := newEnv(t)
	fe := maintenanceHost(e)
	a := e.connectAgent("Maint", fe)
	owner, _ := e.setupOwner()
	ctx := testutil.Context(t)

	// Maintenance starts disabled with every rule off, 30 days.
	var st maintSettings
	owner.must(http.StatusOK, http.MethodGet, maintPath, nil).json(t, &st)
	if st.ID == "" || st.Enabled || st.Schedule.Enabled || st.Schedule.Cron != "0 3 * * 0" || st.Schedule.TimeZone != "UTC" ||
		st.Schedule.CatchUp != "skip" || len(st.Rules) != 7 || len(st.Categories) != 7 || len(st.ExcludeEnvironments) != 0 {
		t.Fatalf("settings %+v", st)
	}
	for _, r := range st.Rules {
		if r.Enabled || r.MinAgeHours != 720 {
			t.Fatalf("rule %+v", r)
		}
	}
	if pv := onlyPreview(t, owner.must(http.StatusOK, http.MethodPost, maintPath+"/previews", nil)); pv.Remove != 0 || len(pv.Categories) != 0 {
		t.Fatalf("preview without rules: %+v", pv)
	}
	owner.fail(http.StatusConflict, "maintenance_empty", http.MethodPost, maintPath+"/runs", map[string]any{"confirm": true})

	// Volume rules need their own explicit opt-in.
	owner.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPatch, maintPath,
		map[string]any{"rules": []maintRule{{Category: "named_volumes", Enabled: true, MinAgeHours: 720}}}, etag(st.Revision))
	owner.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPatch, maintPath,
		map[string]any{"rules": []maintRule{{Category: "anonymous_volumes", Enabled: true, MinAgeHours: 720},
			{Category: "named_volumes", Enabled: true, MinAgeHours: 720, VolumeOptIn: true}}}, etag(st.Revision))
	owner.must(http.StatusOK, http.MethodPatch, maintPath, map[string]any{"rules": []maintRule{
		{Category: "stopped_containers", Enabled: true, MinAgeHours: 720},
		{Category: "unused_images", Enabled: true, MinAgeHours: 720},
		{Category: "unused_networks", Enabled: true, MinAgeHours: 720},
		{Category: "named_volumes", Enabled: true, MinAgeHours: 720, VolumeOptIn: true},
	}}, etag(st.Revision)).json(t, &st)
	for _, r := range st.Rules {
		if r.Category == "anonymous_volumes" && r.Enabled {
			t.Fatal("the named-volume opt-in enabled anonymous volumes")
		}
	}

	// Preview: candidates with reasons, Docker Manager's container protected.
	pv := onlyPreview(t, owner.must(http.StatusOK, http.MethodPost, maintPath+"/previews", nil))
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

	// Manual runs need confirmation; a repeated request (same key) returns
	// the jobs it started.
	owner.fail(http.StatusConflict, "prune_confirmation_required", http.MethodPost, maintPath+"/runs", map[string]any{})
	jobs := runJobs(t, owner.must(http.StatusOK, http.MethodPost, maintPath+"/runs", map[string]any{"confirm": true},
		header("Idempotency-Key", "prune-1")))
	if len(jobs) != 1 {
		t.Fatalf("jobs %v", jobs)
	}
	if again := runJobs(t, owner.must(http.StatusOK, http.MethodPost, maintPath+"/runs", map[string]any{"confirm": true},
		header("Idempotency-Key", "prune-1"))); !slices.Equal(again, jobs) {
		t.Fatalf("retry started %v, want %v", again, jobs)
	}
	// Another run while this one is not finished is refused.
	owner.fail(http.StatusConflict, "maintenance_run_active", http.MethodPost, maintPath+"/runs", map[string]any{"confirm": true})

	job := jobs[0]
	j := e.runJob(job)
	if j.State != domain.JobSucceeded || j.Kind != jobspec.PruneRun || j.Origin != domain.OriginManual || j.PolicyID != st.ID ||
		j.EnvironmentID != a.env {
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
	owner.must(http.StatusOK, http.MethodGet, maintPath, nil).json(t, &st)
	if st.LastRun == nil || st.LastRun.JobID != job || st.LastRun.State != "succeeded" || st.LastRun.Removed != 4 ||
		st.LastRun.BytesReclaimed < 4096 || st.LastRun.Origin != "manual" {
		t.Fatalf("last run %+v", st.LastRun)
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

	// Offline agent: its environment reports why it has no preview. (The
	// agent goes offline before the clock jumps below, which would
	// otherwise end its session at random.)
	a.stop()
	var offline maintPreviews
	owner.must(http.StatusOK, http.MethodPost, maintPath+"/previews", nil).json(t, &offline)
	if len(offline.Items) != 1 || offline.Items[0].Preview != nil || offline.Items[0].ErrorClass != "environment_offline" {
		t.Fatalf("offline preview %+v", offline)
	}

	// Scheduled runs: the manager's service identity, never a user, always
	// a background job; they wait for an offline agent up to the offline
	// deadline and fail without touching anything. The hourly run is due
	// two minutes ahead (not at the next full hour, up to an hour away,
	// which would outlive the owner's idle session depending on where the
	// clock starts).
	minute := (e.clk.Now().Minute() + 2) % 60
	owner.must(http.StatusOK, http.MethodPatch, maintPath, map[string]any{"enabled": true,
		"schedule": map[string]any{"cron": itoa(minute) + " * * * *", "timeZone": "UTC"}}, etag(st.Revision)).json(t, &st)
	if !st.Enabled || !st.Schedule.Enabled {
		t.Fatalf("enabled settings %+v", st)
	}
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
		Target: &domain.JobTarget{Type: domain.TargetMaintenancePolicy, ID: st.ID}})
	if err != nil {
		t.Fatal(err)
	}
	var scheduled *domain.Job
	for i := range js {
		if js[i].Origin == domain.OriginScheduled {
			scheduled = &js[i]
		}
	}
	if scheduled == nil || scheduled.InitiatorUserID != "" || scheduled.PolicyID != st.ID || scheduled.IdempotencyKey == "" {
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
	owner.fail(http.StatusConflict, "maintenance_run_active", http.MethodPost, maintPath+"/runs", map[string]any{"confirm": true})
	e.clk.Advance(time.Hour)
	if err := sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	_, runs, ok, err := sched.Status(ctx, "prune", st.ID, 5)
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
	got, err := e.m.Maintenance().Setup(ctx)
	if err != nil || got.LastRun == nil || got.LastRun.JobID != scheduled.ID || got.LastRun.State != domain.JobFailed ||
		got.LastRun.Origin != domain.OriginScheduled {
		t.Fatalf("last run %+v (%v)", got.LastRun, err)
	}
}

// TestMaintenanceEditCancelsWaitingRuns: a run that has not started when
// the rules or the environments left out change is cancelled: it carries
// the old ones. Turning the schedule on keeps it.
func TestMaintenanceEditCancelsWaitingRuns(t *testing.T) {
	e := newEnv(t)
	fe := maintenanceHost(e)
	a := e.connectAgent("Maint", fe)
	owner, _ := e.setupOwner()
	ctx := testutil.Context(t)
	var st maintSettings
	owner.must(http.StatusOK, http.MethodGet, maintPath, nil).json(t, &st)
	owner.must(http.StatusOK, http.MethodPatch, maintPath, map[string]any{"rules": []maintRule{{Category: "stopped_containers",
		Enabled: true, MinAgeHours: 1}}}, etag(st.Revision)).json(t, &st)
	a.stop()
	job := runJobs(t, owner.must(http.StatusOK, http.MethodPost, maintPath+"/runs", map[string]any{"confirm": true}))[0]
	// Turning it on keeps the waiting run.
	owner.must(http.StatusOK, http.MethodPatch, maintPath, map[string]any{"enabled": true}, etag(st.Revision)).json(t, &st)
	if j, _ := e.m.Jobs().Get(ctx, job); j.State.Terminal() {
		t.Fatalf("turning it on cancelled the run: %+v", j)
	}
	owner.must(http.StatusOK, http.MethodPatch, maintPath, map[string]any{"rules": []maintRule{{Category: "stopped_containers", Enabled: true,
		MinAgeHours: 2000}}}, etag(st.Revision)).json(t, &st)
	if j, _ := e.m.Jobs().Get(ctx, job); j.State != domain.JobCancelled {
		t.Fatalf("the waiting run survived a rule change: %+v", j)
	}
	job2 := runJobs(t, owner.must(http.StatusOK, http.MethodPost, maintPath+"/runs", map[string]any{"confirm": true}))[0]
	owner.must(http.StatusOK, http.MethodPatch, maintPath, map[string]any{"excludeEnvironments": []string{a.env}}, etag(st.Revision)).json(t, &st)
	if !slices.Equal(st.ExcludeEnvironments, []string{a.env}) {
		t.Fatalf("left out %v", st.ExcludeEnvironments)
	}
	if j, _ := e.m.Jobs().Get(ctx, job2); j.State != domain.JobCancelled {
		t.Fatalf("the waiting run survived leaving its environment out: %+v", j)
	}
	owner.fail(http.StatusConflict, "maintenance_no_environments", http.MethodPost, maintPath+"/runs", map[string]any{"confirm": true})
	if !slices.Contains(fe.ContainerNames(), "old-job") {
		t.Fatal("a cancelled run removed something")
	}
}
