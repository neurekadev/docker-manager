package alerts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// backupJob is a verification job of a backup policy (a failed job alert; backups
// themselves are notifications).
func backupJob(state domain.JobState, origin domain.JobOrigin) domain.Job {
	return domain.Job{ID: ids.New(), Kind: "backup.verify", Origin: origin, PolicyID: "pol-1", EnvironmentID: "env-1", State: state,
		Targets:    []domain.JobTarget{{Type: domain.TargetVolume, ID: "silo_data"}},
		ErrorClass: domain.ErrorStepFailed, ErrorMessage: "restic: wrong password JOB-ERROR-CANARY"}
}

func TestScheduledJobFailureRaisesAndTheNextSuccessResolves(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	sub := f.bus.Subscribe(8, func(e events.Event) bool { return e.Type == events.AlertUpdated })
	defer sub.Close()
	failed := backupJob(domain.JobFailed, domain.OriginScheduled)
	f.finish(failed)
	a := f.one()
	if a.Kind != domain.NotifyJobFailed || a.Severity != domain.AlertCritical || a.Title != "Backup verification of silo_data failed" ||
		a.ResourceID != failed.ID || a.JobKind != "backup.verify" || len(a.Targets) != 1 || a.Facts["errorClass"] != domain.ErrorStepFailed {
		t.Fatalf("%+v", a)
	}
	// Announced once the job's transaction committed (OnChange).
	select {
	case e := <-sub.C():
		if e.ResourceID != a.ID {
			t.Fatalf("%+v", e)
		}
	default:
		t.Fatal("the alert was not announced")
	}
	got := f.dispatch()
	if len(got) != 1 || got[0].msg.URL != "https://docker.example.com/jobs/"+failed.ID {
		t.Fatalf("%+v", got)
	}
	// It fails again: the same alert, now pointing at the new job, not
	// sent again.
	again := backupJob(domain.JobFailed, domain.OriginScheduled)
	f.finish(again)
	if b := f.one(); b.ID != a.ID || b.ResourceID != again.ID {
		t.Fatalf("%+v", b)
	}
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	// A manual run of the same policy succeeds: resolved.
	f.finish(backupJob(domain.JobSucceeded, domain.OriginManual))
	if len(f.firing()) != 0 {
		t.Fatal("still firing")
	}
	if got := f.dispatch(); len(got) != 1 || !strings.HasPrefix(got[0].msg.Title, "Resolved: Backup verification of silo_data failed") {
		t.Fatalf("%+v", got)
	}
}

func TestManualJobFailuresStayBrowserNotices(t *testing.T) {
	f := newFixture(t)
	f.finish(backupJob(domain.JobFailed, domain.OriginManual))
	f.finish(backupJob(domain.JobCancelled, domain.OriginScheduled))
	if as := f.firing(); len(as) != 0 {
		t.Fatalf("%+v", as)
	}
	// API tokens count like schedules.
	j := backupJob(domain.JobPartial, domain.OriginAPIToken)
	j.PolicyID = ""
	f.finish(j)
	if a := f.one(); a.Severity != domain.AlertWarning || !strings.Contains(a.Title, "partly failed") || a.Facts["origin"] != "api_token" {
		t.Fatalf("%+v", a)
	}
	if !strings.Contains(Detail(f.one()), "API token") {
		t.Fatal(Detail(f.one()))
	}
}

func TestJobAlertsAreKeyedPerPolicyKindAndTarget(t *testing.T) {
	f := newFixture(t)
	one := backupJob(domain.JobFailed, domain.OriginScheduled)
	other := backupJob(domain.JobFailed, domain.OriginScheduled)
	other.Targets = []domain.JobTarget{{Type: domain.TargetVolume, ID: "shop_db"}}
	retention := backupJob(domain.JobFailed, domain.OriginScheduled)
	retention.Kind = "backup.retention"
	for _, j := range []domain.Job{one, other, retention} {
		f.hooks.finish[j.Kind] = f.hooks.finish["backup.verify"]
		f.finish(j)
	}
	if as := f.firing(); len(as) != 3 {
		t.Fatalf("%+v", as)
	}
	// The success of one target resolves that target's alert only.
	ok := one
	ok.ID, ok.State = ids.New(), domain.JobSucceeded
	f.finish(ok)
	if as := f.firing(); len(as) != 2 {
		t.Fatalf("%+v", as)
	}
}

func TestJobAlertsExpireSilently(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	f.finish(backupJob(domain.JobInterrupted, domain.OriginScheduled))
	f.dispatch()
	f.clk.Advance(JobExpiry - time.Minute)
	if err := f.svc.ExpireJobs(f.ctx); err != nil || len(f.firing()) != 1 {
		t.Fatalf("%v %+v", err, f.firing())
	}
	f.clk.Advance(2 * time.Minute)
	if err := f.svc.ExpireJobs(f.ctx); err != nil || len(f.firing()) != 0 {
		t.Fatalf("%v %+v", err, f.firing())
	}
	res, _ := f.svc.List(f.ctx, domain.AlertFilter{State: domain.AlertListResolved}, "", 0)
	if len(res) != 1 || res[0].Resolution != domain.AlertResolvedExpired {
		t.Fatalf("%+v", res)
	}
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// updatePolicy stores a container update policy with candidates.
func (f *fixture) updatePolicy(id string) domain.UpdatePolicy {
	f.t.Helper()
	now := f.clk.Now().UTC()
	p := domain.UpdatePolicy{ID: id, EnvironmentID: "env-1", Name: "Automatic updates for web", TargetType: domain.UpdateTargetContainer,
		TargetID: "web", Check: domain.UpdateSchedule{Cron: "0 3 * * *", TimeZone: "UTC", Enabled: true},
		Run: domain.UpdateSchedule{Cron: "0 4 * * *", TimeZone: "UTC"}, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertUpdatePolicy(f.ctx, f.db, &p); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fixture) candidate(policyID, service string, status domain.UpdateCandidateStatus, digest string) {
	f.t.Helper()
	c := domain.UpdateCandidate{ID: ids.New(), PolicyID: policyID, Service: service, Reference: "nginx:1", Eligible: true, Status: status,
		CandidateDigest: digest, UpdatedAt: f.clk.Now().UTC()}
	if err := store.UpsertUpdateCandidate(f.ctx, f.db, &c); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) check(policyID string) {
	f.t.Helper()
	in, _ := json.Marshal(checkInput{PolicyID: policyID})
	f.finish(domain.Job{ID: ids.New(), Kind: "update.check", Origin: domain.OriginScheduled, PolicyID: policyID, EnvironmentID: "env-1",
		State: domain.JobSucceeded, Input: in, Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}}})
}

func TestUpdatesAreSentAgainOnlyForNewDigests(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	p := f.updatePolicy("pol-u")
	f.candidate(p.ID, "web", domain.CandidateAvailable, "sha256:aaa")
	f.candidate(p.ID, "db", domain.CandidateUpToDate, "")
	f.check(p.ID)
	a := f.one()
	if a.Kind != domain.NotifyUpdates || a.Severity != domain.AlertInfo || a.Title != "web has an update available" ||
		a.ResourceID != p.ID || a.Facts["services"] != "web" {
		t.Fatalf("%+v", a)
	}
	if got := f.dispatch(); len(got) != 1 || got[0].msg.URL != "https://docker.example.com/updates/"+p.ID {
		t.Fatalf("%+v", got)
	}
	// Checked again, same digest: nothing new.
	f.check(p.ID)
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	// A newer image of the same service: sent again.
	f.candidate(p.ID, "web", domain.CandidateAvailable, "sha256:bbb")
	f.check(p.ID)
	if got := f.dispatch(); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	// A second service: sent again, two updates.
	f.candidate(p.ID, "db", domain.CandidateAvailable, "sha256:ccc")
	f.check(p.ID)
	if b := f.one(); b.Title != "web has 2 updates available" || b.Facts["count"] != "2" {
		t.Fatalf("%+v", b)
	}
	if got := f.dispatch(); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	// One is applied (up to date): fewer, not sent.
	f.candidate(p.ID, "web", domain.CandidateUpToDate, "sha256:bbb")
	f.check(p.ID)
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	// None left (after the run, seen by the reconcile loop): resolved.
	f.candidate(p.ID, "db", domain.CandidateUpToDate, "sha256:ccc")
	if err := f.svc.ReconcileUpdates(f.ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.firing()) != 0 {
		t.Fatal("still firing")
	}
}

func TestDeletedPolicyEndsItsUpdateAlertSilently(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	p := f.updatePolicy("pol-d")
	f.candidate(p.ID, "web", domain.CandidateAvailable, "sha256:aaa")
	f.check(p.ID)
	f.dispatch()
	if err := store.DeleteUpdatePolicy(f.ctx, f.db, p.ID, p.Revision); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ReconcileUpdates(f.ctx); err != nil {
		t.Fatal(err)
	}
	res, _ := f.svc.List(f.ctx, domain.AlertFilter{State: domain.AlertListResolved}, "", 0)
	if len(res) != 1 || res[0].Resolution != domain.AlertResolvedRemoved {
		t.Fatalf("%+v", res)
	}
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// A failed update check names the services it could not check and why,
// from their error classes (never the registry's message).
func TestAFailedUpdateCheckSaysWhichServicesAndWhy(t *testing.T) {
	f := newFixture(t)
	p := f.updatePolicy("pol-u")
	f.candidate(p.ID, "web", domain.CandidateCheckFailed, "")
	c, err := store.UpdateCandidates(f.ctx, f.db, p.ID)
	if err != nil || len(c) != 1 {
		t.Fatalf("%+v %v", c, err)
	}
	c[0].ErrorClass, c[0].ErrorMessage = "unauthorized", "401 from registry REGISTRY-CANARY"
	if err := store.UpsertUpdateCandidate(f.ctx, f.db, &c[0]); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(checkInput{PolicyID: p.ID})
	f.finish(domain.Job{ID: ids.New(), Kind: "update.check", Origin: domain.OriginScheduled, PolicyID: p.ID, EnvironmentID: "env-1",
		State: domain.JobPartial, ErrorClass: domain.ErrorStepFailed, Input: in,
		Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}}})
	var a domain.Alert
	for _, x := range f.firing() {
		if x.Kind == domain.NotifyJobFailed {
			a = x
		}
	}
	want := "A scheduled job did not finish successfully. Some of its items failed. Failed: web. " +
		"The registry refused the credentials. Check the registry connection's username and token."
	if got := Detail(a); got != want {
		t.Fatalf("%q", got)
	}
	if v, _ := field(Fields(a, "homelab"), "What to do"); v != "Check the registry connection's username and token." {
		t.Fatalf("%q", v)
	}
	if strings.Contains(string(output(t, a)), "REGISTRY-CANARY") {
		t.Fatal("the registry's message reached the alert")
	}
}
