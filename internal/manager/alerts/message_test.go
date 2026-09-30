package alerts

import (
	"testing"

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
		{domain.Alert{Kind: domain.NotifyJobFailed, ResourceID: "job-1"}, "/jobs/job-1"},
		{domain.Alert{Kind: domain.NotifyUpdatesAvailable, ResourceID: "pol-1"}, "/updates/pol-1"},
		{domain.Alert{Kind: domain.NotifyDiskHealth}, "/alerts"},
	} {
		if got := Link(c.a); got != c.want {
			t.Errorf("%s: %q, want %q", c.a.Kind, got, c.want)
		}
	}
}

func TestDetailInWords(t *testing.T) {
	for _, c := range []struct {
		a    domain.Alert
		want string
	}{
		{domain.Alert{Kind: domain.NotifyDiskHealth, Facts: map[string]string{"state": "failing", "selfAssessment": "failed",
			"reallocatedSectors": "1", "pendingSectors": "8", "model": "WDC WD40EFZX"}},
			"SMART self-assessment failed, 1 reallocated sector, 8 pending sectors. Model WDC WD40EFZX."},
		{domain.Alert{Kind: domain.NotifyDiskHealth, Facts: map[string]string{"state": "warning", "percentageUsed": "93"}},
			"Wear at 93%."},
		{domain.Alert{Kind: domain.NotifyDiskHealth, Facts: map[string]string{"state": "error", "errorCode": "open_failed"}},
			"The disk can't be read: it could not be opened."},
		{domain.Alert{Kind: domain.NotifyRAID, Facts: map[string]string{"arrayKind": "md", "level": "raid5", "devices": "3", "active": "2",
			"failedMembers": "sdc1"}}, "raid5: 2 of 3 disks working. Failed: sdc1."},
		{domain.Alert{Kind: domain.NotifyEnvironmentOffline, Facts: map[string]string{"since": "2026-09-30T08:05:00Z"}},
			"Not connected since Sep 30, 2026, 08:05 UTC. Actions on it wait until it reconnects."},
		{domain.Alert{Kind: domain.NotifyJobFailed, Facts: map[string]string{"origin": "scheduled"}},
			"A scheduled job did not finish successfully. Open the job to see what happened and try again."},
		{domain.Alert{Kind: domain.NotifyUpdatesAvailable, Facts: map[string]string{"services": "web, db"}}, "Newer images: web, db."},
	} {
		if got := Detail(c.a); got != c.want {
			t.Errorf("%s: %q, want %q", c.a.Kind, got, c.want)
		}
	}
}

func TestMessagesAndDigests(t *testing.T) {
	disk := domain.Alert{Kind: domain.NotifyDiskHealth, Severity: domain.AlertCritical, EnvironmentID: "env-1",
		Title: "Disk /dev/sda on homelab is failing", Facts: map[string]string{"state": "failing", "selfAssessment": "failed"}}
	offline := domain.Alert{Kind: domain.NotifyEnvironmentOffline, Severity: domain.AlertCritical, EnvironmentID: "env-2",
		Title: "office is offline"}
	one := buildMessage("Home", "https://docker.example.com/", []item{{alert: disk, event: domain.AlertEventFiring}})
	if one.Title != "[Home] Disk /dev/sda on homelab is failing" || one.Body != "SMART self-assessment failed." ||
		one.URL != "https://docker.example.com/environments/env-1?tab=system" {
		t.Fatalf("%+v", one)
	}
	res := buildMessage("Home", "https://docker.example.com", []item{{alert: offline, event: domain.AlertEventResolved}})
	if res.Title != "[Home] Resolved: office is offline" || res.Body != "The problem is gone." {
		t.Fatalf("%+v", res)
	}
	digest := buildMessage("Home", "https://docker.example.com", []item{{alert: disk, event: domain.AlertEventFiring},
		{alert: offline, event: domain.AlertEventResolved}})
	if digest.Title != "[Home] 1 alert, 1 resolved" || digest.Body != "• Critical: Disk /dev/sda on homelab is failing\n• Resolved: office is offline" ||
		digest.URL != "https://docker.example.com/alerts" {
		t.Fatalf("%+v", digest)
	}
	// Without a public URL, messages carry no link.
	if m := buildMessage("", "", []item{{alert: disk, event: domain.AlertEventWorse}}); m.URL != "" || m.Title != disk.Title {
		t.Fatalf("%+v", m)
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
