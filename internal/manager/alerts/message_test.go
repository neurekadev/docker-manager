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
		// Updates: the target's record has no page.
		{domain.Alert{Kind: domain.NotifyUpdates, ResourceID: "rec-1", Facts: map[string]string{"policyId": "pol-1"}}, "/updates"},
		{domain.Alert{Kind: domain.NotifyUpdates, ResourceID: "rec-1"}, "/updates"},
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
		// The disk's own temperature limit and time above it (#212).
		{domain.Alert{Kind: domain.NotifyDiskHealth, Facts: map[string]string{"state": "warning", "temperatureC": "72",
			"temperatureLimitC": "70", "overTemperatureMinutes": "34", "criticalTemperatureMinutes": "1"}},
			"Too hot: 72 °C, its limit is 70 °C, 34 minutes above its temperature limit so far, 1 minute above its critical temperature so far."},
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
		// The services are a field of their own.
		{domain.Alert{Kind: domain.NotifyUpdates, Facts: map[string]string{"services": "web, db"}},
			"A check found newer images. To install them, open Updates and press Preview Updates, then Apply Updates."},
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
	a := domain.Alert{Kind: domain.NotifyDiskSpace, Severity: domain.AlertCritical, EnvironmentID: "env-1", Facts: map[string]string{
		"mount": "stacks", "usedPercent": "97", "freeBytes": "1073741824", "totalBytes": "107374182400", "warningAt": "85", "criticalAt": "95"}}
	fs := Fields(a, "homelab")
	for name, want := range map[string]string{"Environment": "homelab", "Filesystem": "Stacks", "Used": "97%",
		"Free": "1 GiB", "Size": "100 GiB", "Thresholds": "warning at 85%, critical at 95%"} {
		if got, _ := field(fs, name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	// The status line says the severity; the environment links to its
	// page; the short fields come first.
	if _, ok := field(fs, "Severity"); ok {
		t.Error("a severity field")
	}
	if fs[0].Name != "Environment" || fs[0].Link != "/environments/env-1" || fs[len(fs)-1].Name != "Thresholds" {
		t.Errorf("%+v", fs)
	}
	// A failed job's fields say what went wrong and what to do; its
	// resolution does not.
	j := domain.Alert{Kind: domain.NotifyJobFailed, Severity: domain.AlertCritical, Facts: map[string]string{"jobKind": "backup.verify",
		"jobState": "failed", "origin": "scheduled", "errorClass": "repository_damaged", "target": "Nightly"}}
	fs = alertFields(j, "homelab", domain.AlertEventFiring)
	if v, _ := field(fs, "What Went Wrong"); v != "The repository check found damaged or missing data." {
		t.Errorf("%q", v)
	}
	if v, _ := field(fs, "Job"); v != "Backup Verification" {
		t.Errorf("%q", v)
	}
	if v, _ := field(fs, "Started By"); v != "Schedule" {
		t.Errorf("%q", v)
	}
	if _, ok := field(alertFields(j, "homelab", domain.AlertEventResolved), "What to Do"); ok {
		t.Error("a resolution says what to do")
	}
	// A failed job's target links to its page.
	j.EnvironmentID, j.Targets = "env-1", []domain.JobTarget{{Type: domain.TargetVolume, ID: "silo_data"}}
	for _, f := range alertFields(j, "homelab", domain.AlertEventFiring) {
		if f.Name == "Target" && f.Link != "/volumes/env-1/silo_data" {
			t.Errorf("%+v", f)
		}
	}
}

func TestUpdateAlertsListTheServicesWithTheirDigests(t *testing.T) {
	a := domain.Alert{Kind: domain.NotifyUpdates, Severity: domain.AlertInfo, EnvironmentID: "env-1",
		Targets: []domain.JobTarget{{Type: domain.TargetStack, ID: "s1"}},
		Facts: map[string]string{"count": "12", "services": "web, db", "target": "Paperless",
			"changes": encodeChanges([]serviceChange{{"web", "1a2b", "3c4d"}, {"db", "", "5e6f"}})}}
	fs := Fields(a, "homelab")
	var services domain.NotificationField
	for _, f := range fs {
		if f.Name == "Target" && (f.Value != "Paperless" || f.Link != "/stacks/s1") {
			t.Errorf("%+v", f)
		}
		if f.Name == "Services" {
			services = f
		}
	}
	want := []domain.NotificationItem{
		{Text: "web", Link: "/stacks/s1/logs?service=web", From: "1a2b", To: "3c4d"},
		{Text: "db", Link: "/stacks/s1/logs?service=db", To: "5e6f"},
		{Text: "…and 10 more"},
	}
	if services.Inline || len(services.Items) != len(want) || services.Value != "web: 1a2b → 3c4d\ndb: 5e6f\n…and 10 more" {
		t.Fatalf("%+v", services)
	}
	for i, it := range services.Items {
		if it != want[i] {
			t.Errorf("%d: %+v, want %+v", i, it, want[i])
		}
	}
	// A standalone container's service is the container.
	a.Targets = []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}}
	a.Facts["count"] = "1"
	a.Facts["changes"] = encodeChanges([]serviceChange{{"web", "1a2b", "3c4d"}})
	for _, f := range Fields(a, "homelab") {
		if (f.Name == "Target" || f.Name == "Services") && f.Link+firstLink(f) != "/containers/env-1/web" {
			t.Errorf("%+v", f)
		}
	}
	// An alert recorded before the digests: the names as they were.
	delete(a.Facts, "changes")
	if v, _ := field(Fields(a, "homelab"), "Services"); v != "web, db" {
		t.Errorf("%q", v)
	}
}

// firstLink is the link of a field's first entry ("" without entries).
func firstLink(f domain.NotificationField) string {
	if len(f.Items) == 0 {
		return ""
	}
	return f.Items[0].Link
}

func TestLabelsNameTheKindAndOutcomeLikeWhatToSend(t *testing.T) {
	for _, c := range []struct {
		kind    domain.NotificationEventKind
		outcome domain.NotificationOutcome
		want    string
	}{
		{domain.NotifyDiskHealth, domain.OutcomeCritical, "Disk Health · Critical"},
		{domain.NotifyEnvironmentOffline, domain.OutcomeCritical, "Environment Offline · Offline"},
		{domain.NotifyEnvironmentOffline, domain.OutcomeResolved, "Environment Offline · Back Online"},
		{domain.NotifyBackup, domain.OutcomeWarning, "Backups · Warning"},
		{domain.NotifyRestore, domain.OutcomeFailure, "Restores · Failure"},
		{domain.NotifyUpdates, domain.OutcomeAvailable, "Image Updates · Available"},
		{domain.NotifyUpdates, domain.OutcomeSuccess, "Image Updates · Applied"},
		{domain.NotifyJobFailed, domain.OutcomeFailure, "Other Jobs · Failure"},
	} {
		if got := Label(c.kind, c.outcome); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.kind, c.outcome, got, c.want)
		}
	}
	// Every kind and outcome has its words.
	for _, k := range domain.NotificationEventKinds() {
		for _, o := range k.Outcomes() {
			if l := Label(k, o); !strings.Contains(l, " · ") {
				t.Errorf("%s %s: %q", k, o, l)
			}
		}
	}
}

// snap is the delivery of a with a snapshot taken now.
func snap(a domain.Alert, event string) domain.AlertDelivery {
	return newDelivery(alertSnapshot(a, event), "c", time.Time{}, time.Time{})
}

func TestMessagesAndDigests(t *testing.T) {
	disk := domain.Alert{Kind: domain.NotifyDiskHealth, Severity: domain.AlertCritical, EnvironmentID: "env-1",
		Title: "Disk /dev/sda is failing", Facts: map[string]string{"state": "failing", "selfAssessment": "failed"}}
	offline := domain.Alert{Kind: domain.NotifyEnvironmentOffline, Severity: domain.AlertCritical, EnvironmentID: "env-2",
		Title: "office is offline"}
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	// The title is the alert's own (no instance name: the footer says it),
	// under the status line.
	one := buildMessage("Home", "https://docker.example.com/", []domain.AlertDelivery{snap(disk, domain.AlertEventFiring)}, now)
	if one.Title != "Disk /dev/sda is failing" || one.Label != "Disk Health · Critical" ||
		one.Body != "SMART self-assessment failed. Back up its data and replace the disk." ||
		one.URL != "https://docker.example.com/environments/env-1?tab=system" || one.Tone != domain.ToneCritical || one.Footer != "Home" {
		t.Fatalf("%+v", one)
	}
	res := buildMessage("Home", "https://docker.example.com", []domain.AlertDelivery{snap(offline, domain.AlertEventResolved)}, now)
	if res.Title != "Resolved: office is offline" || res.Label != "Environment Offline · Back Online" ||
		res.Body != "The environment is connected again." || res.Tone != domain.ToneSuccess {
		t.Fatalf("%+v", res)
	}
	note := domain.AlertDelivery{Event: domain.DeliveryEventNotification, Kind: domain.NotifyPrune, Outcome: domain.OutcomeSuccess,
		Severity: domain.AlertInfo, Title: "Prune reclaimed 4.2 GiB"}
	digest := buildMessage("Home", "https://docker.example.com", []domain.AlertDelivery{snap(disk, domain.AlertEventFiring),
		snap(offline, domain.AlertEventResolved), note}, now)
	if digest.Title != "1 alert, 1 resolved, 1 notification" || digest.Label != "Summary" || digest.Body != "" || len(digest.Fields) != 1 ||
		digest.URL != "https://docker.example.com/notifications" || digest.Tone != domain.ToneCritical || !digest.Time.Equal(now) {
		t.Fatalf("%+v", digest)
	}
	// The digest lists its entries, each linked to its page; a finished
	// run's title says how it went by itself.
	list := digest.Fields[0]
	want := []domain.NotificationItem{
		{Text: "Critical: Disk /dev/sda is failing", Link: "https://docker.example.com/environments/env-1?tab=system"},
		{Text: "Resolved: office is offline", Link: "https://docker.example.com/environments/env-2"},
		{Text: "Prune reclaimed 4.2 GiB"},
	}
	if list.Name != "What Happened" || len(list.Items) != len(want) {
		t.Fatalf("%+v", list)
	}
	for i, it := range list.Items {
		if it != want[i] {
			t.Errorf("%d: %+v, want %+v", i, it, want[i])
		}
	}
	// Alerts only: the digest links to the Alerts tab.
	alertsOnly := buildMessage("Home", "https://docker.example.com", []domain.AlertDelivery{snap(disk, domain.AlertEventFiring),
		snap(offline, domain.AlertEventResolved)}, now)
	if alertsOnly.URL != "https://docker.example.com/notifications?tab=alerts" {
		t.Fatalf("%+v", alertsOnly)
	}
	// Without a public URL, messages carry no link; without a name, the
	// footer is Docker Manager.
	if m := buildMessage("", "", []domain.AlertDelivery{snap(disk, domain.AlertEventWorse)}, now); m.URL != "" || m.Title != disk.Title ||
		m.Footer != "Docker Manager" {
		t.Fatalf("%+v", m)
	}
}

func TestDigestEntriesNameTheirEnvironment(t *testing.T) {
	env := func(name string) []domain.NotificationField {
		return []domain.NotificationField{{Name: "Environment", Value: name, Inline: true}}
	}
	items := []domain.AlertDelivery{
		{Event: domain.AlertEventFiring, Kind: domain.NotifyDiskHealth, Severity: domain.AlertCritical, Outcome: domain.OutcomeCritical,
			Title: "Disk /dev/sda is failing", Fields: env("homelab")},
		{Event: domain.AlertEventFiring, Kind: domain.NotifyDiskHealth, Severity: domain.AlertCritical, Outcome: domain.OutcomeCritical,
			Title: "Disk /dev/sda is failing", Fields: env("office")},
		// The title names it already.
		{Event: domain.AlertEventFiring, Kind: domain.NotifyEnvironmentOffline, Severity: domain.AlertCritical,
			Outcome: domain.OutcomeCritical, Title: "nas is offline", Fields: env("nas")},
		// A name that is only part of a word in the title is named.
		{Event: domain.DeliveryEventNotification, Kind: domain.NotifyUpdates, Outcome: domain.OutcomeSuccess,
			Title: "Update of prod-api succeeded", Fields: env("prod")},
		// News needs no severity word.
		{Event: domain.AlertEventFiring, Kind: domain.NotifyUpdates, Severity: domain.AlertInfo, Outcome: domain.OutcomeAvailable,
			Title: "web has an update available", Fields: env("prod")},
	}
	got := buildMessage("Home", "", items, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)).Fields[0].Items
	want := []string{"Critical: Disk /dev/sda is failing (homelab)", "Critical: Disk /dev/sda is failing (office)", "Critical: nas is offline",
		"Update of prod-api succeeded (prod)", "web has an update available (prod)"}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i, it := range got {
		if it.Text != want[i] {
			t.Errorf("%d: %q, want %q", i, it.Text, want[i])
		}
	}
}

func TestMessagesNameTheirEnvironment(t *testing.T) {
	d := func(env string) domain.AlertDelivery {
		out := domain.AlertDelivery{Event: domain.AlertEventFiring, Kind: domain.NotifyDiskHealth, Severity: domain.AlertCritical,
			Outcome: domain.OutcomeCritical, Title: "Disk /dev/sda is failing"}
		if env != "" {
			out.Fields = []domain.NotificationField{{Name: "Environment", Value: env, Inline: true}}
		}
		return out
	}
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name  string
		items []domain.AlertDelivery
		want  string
	}{
		{"one", []domain.AlertDelivery{d("Hyperion")}, "Hyperion"},
		{"none", []domain.AlertDelivery{d("")}, ""},
		{"digest of one environment", []domain.AlertDelivery{d("Hyperion"), d("Hyperion")}, "Hyperion"},
		{"digest of several", []domain.AlertDelivery{d("Hyperion"), d("office")}, ""},
		{"digest with one without", []domain.AlertDelivery{d("Hyperion"), d("")}, ""},
	} {
		if got := buildMessage("Home", "", c.items, now).Tag; got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMessagesLinkFieldsWithThePublicURL(t *testing.T) {
	d := domain.AlertDelivery{Event: domain.DeliveryEventNotification, Kind: domain.NotifyUpdates, Outcome: domain.OutcomeSuccess,
		Title: "Update of Paperless succeeded", Link: "/jobs/j1", Fields: []domain.NotificationField{
			{Name: "Environment", Value: "homelab", Inline: true, Link: "/environments/env-1"},
			{Name: "Updated", Value: "web: 1a → 2b", Items: []domain.NotificationItem{{Text: "web", Link: "/stacks/s1/logs?service=web", From: "1a", To: "2b"}}},
		}}
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	m := buildMessage("Home", "https://docker.example.com/", []domain.AlertDelivery{d}, now)
	if m.Label != "Image Updates · Applied" || m.Fields[0].Link != "https://docker.example.com/environments/env-1" ||
		m.Fields[1].Items[0].Link != "https://docker.example.com/stacks/s1/logs?service=web" || m.Fields[1].Items[0].From != "1a" {
		t.Fatalf("%+v", m)
	}
	// The snapshot keeps its paths.
	if d.Fields[0].Link != "/environments/env-1" || d.Fields[1].Items[0].Link != "/stacks/s1/logs?service=web" {
		t.Fatalf("%+v", d.Fields)
	}
	m = buildMessage("Home", "", []domain.AlertDelivery{d}, now)
	if m.Fields[0].Link != "" || m.Fields[1].Items[0].Link != "" {
		t.Fatalf("%+v", m.Fields)
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
		// A failed job's resolution keeps its failure's outcome, but is green.
		{domain.AlertDelivery{Event: domain.AlertEventResolved, Outcome: domain.OutcomeFailure}, domain.ToneSuccess},
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

func TestLabelsAreTitleCaseAndSentencesAreNot(t *testing.T) {
	for kind, want := range map[domain.JobKind]string{
		"manager.retention": "Docker Manager Backup Retention", "stack.down": "Take Down",
		"update.check": "Update Check", "prune.run": "Prune", "unknown.kind": "A Job",
	} {
		if got := kindLabel(kind); got != want {
			t.Errorf("kindLabel(%s) = %q, want %q", kind, got, want)
		}
	}
	if got := titleCase("left for the next run"); got != "Left for the Next Run" {
		t.Errorf("%q", got)
	}
	if got := titleCase("built-in check of it"); got != "Built-In Check of It" {
		t.Errorf("%q", got)
	}
	// A filesystem is a label in its field and keeps its words in a sentence.
	a := domain.Alert{Kind: domain.NotifyDiskSpace, Facts: map[string]string{"mount": "docker", "usedPercent": "90"}}
	if v, _ := field(Fields(a, ""), "Filesystem"); v != "Docker Data" || MountLabel("bind-2") != "Bind Mount 2" {
		t.Errorf("%q", v)
	}
	if !strings.HasPrefix(Detail(a), "The Docker data filesystem is 90% full") {
		t.Errorf("%q", Detail(a))
	}
}
