package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
)

func TestLinksPointAtTheSource(t *testing.T) {
	for _, c := range []struct {
		a    domain.Alert
		want string
	}{
		{domain.Alert{Kind: domain.NotifyDiskHealth, EnvironmentID: "env 1"}, "/environments/env%201?tab=system"},
		{domain.Alert{Kind: domain.NotifyRAID, EnvironmentID: "env-1"}, "/environments/env-1?tab=system"},
		{domain.Alert{Kind: domain.NotifyEnvironmentOffline, EnvironmentID: "env-1"}, "/environments/env-1"},
		{domain.Alert{Kind: domain.NotifyTemperature, EnvironmentID: "env-1"}, "/environments/env-1"},
		{domain.Alert{Kind: domain.NotifyDiskSpace, EnvironmentID: "env-1"}, "/environments/env-1"},
		{domain.Alert{Kind: domain.NotifyMemory, EnvironmentID: "env-1"}, "/environments/env-1"},
		{domain.Alert{Kind: domain.NotifyJobFailed, ResourceID: "job-1"}, "/jobs/job-1"},
		{domain.Alert{Kind: domain.NotifyUpdates, ResourceID: "pol-1"}, "/updates/pol-1"},
		{domain.Alert{Kind: domain.NotifyDiskHealth}, "/notifications?tab=alerts"},
	} {
		if got := Link(c.a); got != c.want {
			t.Errorf("%s: %q, want %q", c.a.Kind, got, c.want)
		}
	}
	if got := NotificationLink(domain.Notification{JobID: "job-1"}); got != "/jobs/job-1" {
		t.Errorf("%q", got)
	}
}

func TestDetailInWords(t *testing.T) {
	for _, c := range []struct {
		a    domain.Alert
		want string
	}{
		{domain.Alert{Kind: domain.NotifyDiskHealth, Facts: map[string]string{"state": "failing", "selfAssessment": "failed",
			"reallocatedSectors": "1", "pendingSectors": "8", "model": "WDC WD40EFZX"}},
			"SMART self-assessment failed, 1 reallocated sector, 8 pending sectors. Back up its data and replace the disk. Model WDC WD40EFZX."},
		{domain.Alert{Kind: domain.NotifyDiskHealth, Facts: map[string]string{"state": "warning", "percentageUsed": "93"}},
			"Wear at 93%."},
		{domain.Alert{Kind: domain.NotifyDiskHealth, Facts: map[string]string{"state": "error", "errorCode": "open_failed"}},
			"The disk can't be read: it could not be opened."},
		{domain.Alert{Kind: domain.NotifyRAID, Facts: map[string]string{"arrayKind": "md", "level": "raid5", "devices": "3", "active": "2",
			"failedMembers": "sdc1"}}, "raid5: 2 of 3 disks working. Failed: sdc1."},
		// Unknown facts are left out, never shown empty.
		{domain.Alert{Kind: domain.NotifyRAID, Facts: map[string]string{"arrayKind": "md", "level": "raid5", "progress": "37.52",
			"action": "recovery"}}, "raid5: Rebuild: 37.52% done."},
		{domain.Alert{Kind: domain.NotifyTemperature, Facts: map[string]string{"sensor": "coretemp: Package id 0", "celsius": "92",
			"warningAt": "80", "criticalAt": "90"}},
			"The hottest sensor, coretemp: Package id 0, reached 92 °C (warning at 80 °C, critical at 90 °C). Check the host's cooling and load."},
		{domain.Alert{Kind: domain.NotifyDiskSpace, Facts: map[string]string{"mount": "docker", "usedPercent": "93",
			"freeBytes": "37580963840", "totalBytes": "536870912000", "warningAt": "85", "criticalAt": "0"}},
			"The Docker data filesystem is 93% full: 35 GiB free of 500 GiB (warning at 85%). Prune unused images and volumes, or free space on the host."},
		{domain.Alert{Kind: domain.NotifyMemory, Facts: map[string]string{"usedPercent": "96", "usedBytes": "16106127360",
			"totalBytes": "17179869184", "warningAt": "90", "criticalAt": "95"}},
			"Memory use reached 96% (15 GiB of 16 GiB); warning at 90%, critical at 95%. Check which containers use the most memory."},
		{domain.Alert{Kind: domain.NotifyEnvironmentOffline, Facts: map[string]string{"since": "2026-09-30T08:05:00Z"}},
			"Not connected since Sep 30, 2026, 08:05 UTC. Actions on it wait until it reconnects. Check that the host and its agent are running."},
		{domain.Alert{Kind: domain.NotifyJobFailed, Facts: map[string]string{"origin": "scheduled"}},
			"A scheduled job did not finish successfully. Open the job to see what happened and try again."},
		// A failure is explained from its error class.
		{domain.Alert{Kind: domain.NotifyJobFailed, Facts: map[string]string{"origin": "scheduled", "errorClass": "storage_unreachable"}},
			"A scheduled job did not finish successfully. The backup storage could not be reached (DNS, network or TLS). " +
				"Check that the host can reach the storage endpoint."},
		{domain.Alert{Kind: domain.NotifyJobFailed, Facts: map[string]string{"origin": "api_token", "jobState": "partial",
			"errorClass": "step_failed"}},
			"A job started by an API token did not finish successfully. Some of its items failed. Open the job to see which ones."},
		{domain.Alert{Kind: domain.NotifyUpdates, Facts: map[string]string{"services": "web, db"}}, "Newer images: web, db."},
	} {
		if got := Detail(c.a); got != c.want {
			t.Errorf("%s: %q, want %q", c.a.Kind, got, c.want)
		}
	}
}

func TestErrorClassesInWords(t *testing.T) {
	if got := describeError("recovery_key_rejected"); got != "The repository did not accept the Recovery Key. "+
		"Check that the repository belongs to this Docker Manager and that a key change finished." {
		t.Errorf("%q", got)
	}
	// An unknown class is named as it is (a stable word, never an error
	// text); none says nothing.
	if got := describeError("brand_new_class"); got != "It failed (brand new class)." {
		t.Errorf("%q", got)
	}
	if describeError("") != "" || errorFix("cancelled") != "" {
		t.Error("empty")
	}
	// Every explanation is a sentence.
	for class, w := range errorTexts {
		if !strings.HasSuffix(w.what, ".") || (w.fix != "" && !strings.HasSuffix(w.fix, ".")) {
			t.Errorf("%s: %+v", class, w)
		}
	}
}

// field returns the value of a field (ok false when absent).
func field(fs []domain.NotificationField, name string) (string, bool) {
	for _, f := range fs {
		if f.Name == name {
			return f.Value, true
		}
	}
	return "", false
}

func TestAlertFieldsLabelTheNumbers(t *testing.T) {
	a := domain.Alert{Kind: domain.NotifyDiskSpace, Severity: domain.AlertCritical, Facts: map[string]string{"mount": "stacks",
		"usedPercent": "97", "freeBytes": "1073741824", "totalBytes": "107374182400", "warningAt": "85", "criticalAt": "95"}}
	fs := Fields(a, "homelab")
	for name, want := range map[string]string{"Environment": "homelab", "Severity": "Critical", "Filesystem": "Stacks", "Used": "97%",
		"Free": "1 GiB", "Size": "100 GiB", "Thresholds": "warning at 85%, critical at 95%"} {
		if got, _ := field(fs, name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	// A failed job's fields say what went wrong and what to do; its
	// resolution does not.
	j := domain.Alert{Kind: domain.NotifyJobFailed, Severity: domain.AlertCritical, Facts: map[string]string{"jobKind": "backup.verify",
		"jobState": "failed", "origin": "scheduled", "errorClass": "repository_damaged", "target": "Nightly"}}
	fs = alertFields(j, "homelab", domain.AlertEventFiring)
	if v, _ := field(fs, "What went wrong"); v != "The repository check found damaged or missing data." {
		t.Errorf("%q", v)
	}
	if v, _ := field(fs, "Job"); v != "Backup verification" {
		t.Errorf("%q", v)
	}
	if v, _ := field(fs, "Started by"); v != "Schedule" {
		t.Errorf("%q", v)
	}
	if _, ok := field(alertFields(j, "homelab", domain.AlertEventResolved), "What to do"); ok {
		t.Error("a resolution says what to do")
	}
}

// snap is the delivery of a with a snapshot taken now.
func snap(a domain.Alert, event string) domain.AlertDelivery {
	return newDelivery(alertSnapshot(a, event), "c", time.Time{}, time.Time{})
}

func TestMessagesAndDigests(t *testing.T) {
	disk := domain.Alert{Kind: domain.NotifyDiskHealth, Severity: domain.AlertCritical, EnvironmentID: "env-1",
		Title: "Disk /dev/sda on homelab is failing", Facts: map[string]string{"state": "failing", "selfAssessment": "failed"}}
	offline := domain.Alert{Kind: domain.NotifyEnvironmentOffline, Severity: domain.AlertCritical, EnvironmentID: "env-2",
		Title: "office is offline"}
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	one := buildMessage("Home", "https://docker.example.com/", []domain.AlertDelivery{snap(disk, domain.AlertEventFiring)}, now)
	if one.Title != "[Home] Disk /dev/sda on homelab is failing" ||
		one.Body != "SMART self-assessment failed. Back up its data and replace the disk." ||
		one.URL != "https://docker.example.com/environments/env-1?tab=system" || one.Tone != domain.ToneCritical || one.Footer != "Docker Manager" {
		t.Fatalf("%+v", one)
	}
	res := buildMessage("Home", "https://docker.example.com", []domain.AlertDelivery{snap(offline, domain.AlertEventResolved)}, now)
	if res.Title != "[Home] Resolved: office is offline" || res.Body != "The environment is connected again." || res.Tone != domain.ToneSuccess {
		t.Fatalf("%+v", res)
	}
	note := domain.AlertDelivery{Event: domain.DeliveryEventNotification, Kind: domain.NotifyPrune, Outcome: domain.OutcomeSuccess,
		Severity: domain.AlertInfo, Title: "Prune on homelab reclaimed 4.2 GiB"}
	digest := buildMessage("Home", "https://docker.example.com", []domain.AlertDelivery{snap(disk, domain.AlertEventFiring),
		snap(offline, domain.AlertEventResolved), note}, now)
	if digest.Title != "[Home] 1 alert, 1 resolved, 1 notification" ||
		digest.Body != "• Critical: Disk /dev/sda on homelab is failing\n• Resolved: office is offline\n• Done: Prune on homelab reclaimed 4.2 GiB" ||
		digest.URL != "https://docker.example.com/notifications" || digest.Tone != domain.ToneCritical || !digest.Time.Equal(now) {
		t.Fatalf("%+v", digest)
	}
	// Alerts only: the digest links to the Alerts tab.
	alertsOnly := buildMessage("Home", "https://docker.example.com", []domain.AlertDelivery{snap(disk, domain.AlertEventFiring),
		snap(offline, domain.AlertEventResolved)}, now)
	if alertsOnly.URL != "https://docker.example.com/notifications?tab=alerts" {
		t.Fatalf("%+v", alertsOnly)
	}
	// Without a public URL, messages carry no link.
	if m := buildMessage("", "", []domain.AlertDelivery{snap(disk, domain.AlertEventWorse)}, now); m.URL != "" || m.Title != disk.Title {
		t.Fatalf("%+v", m)
	}
}

func TestTonesFollowTheOutcome(t *testing.T) {
	for _, c := range []struct {
		d    domain.AlertDelivery
		want domain.NotificationTone
	}{
		{domain.AlertDelivery{Outcome: domain.OutcomeCritical}, domain.ToneCritical},
		{domain.AlertDelivery{Outcome: domain.OutcomeFailure}, domain.ToneCritical},
		{domain.AlertDelivery{Outcome: domain.OutcomeWarning}, domain.ToneWarning},
		{domain.AlertDelivery{Outcome: domain.OutcomeResolved}, domain.ToneSuccess},
		{domain.AlertDelivery{Outcome: domain.OutcomeSuccess}, domain.ToneSuccess},
		{domain.AlertDelivery{Outcome: domain.OutcomeAvailable}, domain.ToneInfo},
	} {
		if got := c.d.Tone(); got != c.want {
			t.Errorf("%s: %s, want %s", c.d.Outcome, got, c.want)
		}
	}
	for _, c := range []struct {
		a     domain.Alert
		event string
		want  domain.NotificationOutcome
	}{
		{domain.Alert{Kind: domain.NotifyJobFailed, Severity: domain.AlertCritical}, domain.AlertEventFiring, domain.OutcomeFailure},
		{domain.Alert{Kind: domain.NotifyJobFailed, Severity: domain.AlertWarning}, domain.AlertEventWorse, domain.OutcomeWarning},
		{domain.Alert{Kind: domain.NotifyUpdates, Severity: domain.AlertInfo}, domain.AlertEventFiring, domain.OutcomeAvailable},
		{domain.Alert{Kind: domain.NotifyMemory, Severity: domain.AlertCritical}, domain.AlertEventFiring, domain.OutcomeCritical},
		{domain.Alert{Kind: domain.NotifyMemory, Severity: domain.AlertCritical}, domain.AlertEventResolved, domain.OutcomeResolved},
	} {
		if got := c.a.Outcome(c.event); got != c.want {
			t.Errorf("%s %s: %s, want %s", c.a.Kind, c.event, got, c.want)
		}
	}
}

func TestFingerprintTokens(t *testing.T) {
	fp := domain.Fingerprint("pending", "reallocated", "pending", "", "a,b")
	if fp != "a;b,pending,reallocated" {
		t.Fatalf("%q", fp)
	}
	if domain.NewTokens(fp, domain.Fingerprint("pending")) || !domain.NewTokens(fp, domain.Fingerprint("pending", "worn")) {
		t.Fatal("NewTokens")
	}
	if !domain.NewTokens("", "x") || domain.NewTokens("x", "") {
		t.Fatal("NewTokens with empty fingerprints")
	}
}
