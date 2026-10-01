package alerts

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/humanize"
)

// Messages (#159): the title "[<instance name>] <title>" ("Resolved: …"
// for resolutions), a short plain body (Detail), labelled fields (the
// environment, what it is about, the numbers) and a link to the page it
// is about; each service renders them as richly as it can (notify).
// Never serial numbers, job error texts or anything secret: bodies and
// fields are built from the facts only, and a failure is explained from
// its error class (errors.go).

// Link returns the path of the page an alert is about: the environment's
// System tab (disks, RAID), the environment (offline, host usage), the
// job, the update policy.
func Link(a domain.Alert) string {
	switch a.Kind {
	case domain.NotifyDiskHealth, domain.NotifyRAID:
		if a.EnvironmentID != "" {
			return "/environments/" + url.PathEscape(a.EnvironmentID) + "?tab=system"
		}
	case domain.NotifyEnvironmentOffline, domain.NotifyTemperature, domain.NotifyDiskSpace, domain.NotifyMemory:
		if a.EnvironmentID != "" {
			return "/environments/" + url.PathEscape(a.EnvironmentID)
		}
	case domain.NotifyJobFailed:
		if a.ResourceID != "" {
			return "/jobs/" + url.PathEscape(a.ResourceID)
		}
	case domain.NotifyUpdates:
		if a.ResourceID != "" {
			return "/updates/" + url.PathEscape(a.ResourceID)
		}
	}
	return "/notifications?tab=alerts"
}

// NotificationLink returns the path of a notification's job.
func NotificationLink(n domain.Notification) string {
	if n.JobID != "" {
		return "/jobs/" + url.PathEscape(n.JobID)
	}
	return "/notifications"
}

func plural(n, one, many string) string {
	if n == "1" {
		return n + " " + one
	}
	return n + " " + many
}

func pluralN(n int, one, many string) string { return plural(strconv.Itoa(n), one, many) }

var diskErrorWords = map[string]string{
	"permission_denied": "the agent may not open it",
	"open_failed":       "it could not be opened",
	"unsupported":       "it reports no SMART data",
}

// MountLabel names a filesystem an agent reports usage of, like the app
// does.
func MountLabel(mount string) string {
	switch mount {
	case "docker":
		return "Docker data"
	case "stacks":
		return "Stacks"
	}
	if n, ok := strings.CutPrefix(mount, "bind-"); ok {
		return "Bind mount " + n
	}
	return mount
}

// bytesFact formats a byte count fact ("" when missing).
func bytesFact(v string) string {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return ""
	}
	return humanize.Bytes(n)
}

// durationWords formats seconds as "45 s", "3 min 12 s", "1 h 4 min".
func durationWords(v string) string {
	s, err := strconv.ParseInt(v, 10, 64)
	if err != nil || s < 0 {
		return ""
	}
	d := time.Duration(s) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", s)
	case d < time.Hour:
		if sec := s % 60; sec > 0 {
			return fmt.Sprintf("%d min %d s", s/60, sec)
		}
		return fmt.Sprintf("%d min", s/60)
	}
	if m := (s % 3600) / 60; m > 0 {
		return fmt.Sprintf("%d h %d min", s/3600, m)
	}
	return fmt.Sprintf("%d h", s/3600)
}

// thresholdWords says when a host alert fires ("warning at 80 °C,
// critical at 90 °C").
func thresholdWords(f map[string]string, unit string) string {
	var parts []string
	if f["warningAt"] != "" && f["warningAt"] != "0" {
		parts = append(parts, "warning at "+f["warningAt"]+unit)
	}
	if f["criticalAt"] != "" && f["criticalAt"] != "0" {
		parts = append(parts, "critical at "+f["criticalAt"]+unit)
	}
	return strings.Join(parts, ", ")
}

// Detail describes an alert in one or two plain sentences (the message
// body and the Alerts page's second line).
func Detail(a domain.Alert) string {
	f := a.Facts
	switch a.Kind {
	case domain.NotifyDiskHealth:
		var parts []string
		if f["selfAssessment"] == "failed" {
			parts = append(parts, "SMART self-assessment failed")
		}
		if f["criticalWarning"] != "" {
			parts = append(parts, "critical warning "+f["criticalWarning"])
		}
		if f["failingAttributes"] != "" {
			parts = append(parts, "attributes at or below their threshold: "+f["failingAttributes"])
		}
		for _, c := range []struct{ key, one, many string }{
			{"reallocatedSectors", "reallocated sector", "reallocated sectors"},
			{"pendingSectors", "pending sector", "pending sectors"},
			{"reportedUncorrectable", "uncorrectable error", "uncorrectable errors"},
			{"offlineUncorrectable", "offline uncorrectable sector", "offline uncorrectable sectors"},
			{"mediaErrors", "media error", "media errors"},
			{"grownDefects", "grown defect", "grown defects"},
			{"uncorrectedErrors", "uncorrected error", "uncorrected errors"},
		} {
			if f[c.key] != "" {
				parts = append(parts, plural(f[c.key], c.one, c.many))
			}
		}
		if f["percentageUsed"] != "" {
			parts = append(parts, "wear at "+f["percentageUsed"]+"%")
		}
		if f["availableSpare"] != "" {
			parts = append(parts, fmt.Sprintf("spare at %s%% (threshold %s%%)", f["availableSpare"], f["availableSpareThreshold"]))
		}
		var b strings.Builder
		if f["state"] == "error" {
			b.WriteString("The disk can't be read")
			if w := diskErrorWords[f["errorCode"]]; w != "" {
				b.WriteString(": " + w)
			}
			b.WriteString(".")
		}
		if len(parts) > 0 {
			if b.Len() > 0 {
				b.WriteString(" Last values: ")
				b.WriteString(strings.Join(parts, ", "))
			} else {
				s := strings.Join(parts, ", ")
				b.WriteString(strings.ToUpper(s[:1]) + s[1:])
			}
			b.WriteString(".")
		}
		if f["state"] == "failing" {
			b.WriteString(" Back up its data and replace the disk.")
		}
		if f["model"] != "" {
			b.WriteString(" Model " + f["model"] + ".")
		}
		return strings.TrimSpace(b.String())
	case domain.NotifyRAID:
		if f["arrayKind"] == "zfs" {
			return "Pool health: " + f["health"] + "."
		}
		var b strings.Builder
		if f["level"] != "" {
			b.WriteString(f["level"] + ": ")
		}
		switch {
		case f["devices"] != "":
			fmt.Fprintf(&b, "%s of %s disks working.", f["active"], f["devices"])
		case f["state"] != "":
			b.WriteString("state " + f["state"] + ".")
		}
		if f["failedMembers"] != "" {
			b.WriteString(" Failed: " + f["failedMembers"] + ".")
		}
		if f["progress"] != "" {
			action := "Sync"
			switch f["action"] {
			case "recovery":
				action = "Rebuild"
			case "resync":
				action = "Resync"
			case "reshape":
				action = "Reshape"
			case "check", "repair":
				action = "Check"
			}
			b.WriteString(" " + action + ": " + f["progress"] + "% done.")
		}
		return strings.Join(strings.Fields(b.String()), " ")
	case domain.NotifyTemperature:
		s := "The hottest sensor"
		if f["sensor"] != "" {
			s += ", " + f["sensor"] + ","
		}
		s += " reached " + f["celsius"] + " °C"
		if t := thresholdWords(f, " °C"); t != "" {
			s += " (" + t + ")"
		}
		return s + ". Check the host's cooling and load."
	case domain.NotifyDiskSpace:
		s := "The " + MountLabel(f["mount"]) + " filesystem is " + f["usedPercent"] + "% full"
		if free, total := bytesFact(f["freeBytes"]), bytesFact(f["totalBytes"]); free != "" && total != "" {
			s += ": " + free + " free of " + total
		}
		if t := thresholdWords(f, "%"); t != "" {
			s += " (" + t + ")"
		}
		return s + ". Prune unused images and volumes, or free space on the host."
	case domain.NotifyMemory:
		s := "Memory use reached " + f["usedPercent"] + "%"
		if used, total := bytesFact(f["usedBytes"]), bytesFact(f["totalBytes"]); used != "" && total != "" {
			s += " (" + used + " of " + total + ")"
		}
		if t := thresholdWords(f, "%"); t != "" {
			s += "; " + t
		}
		return s + ". Check which containers use the most memory."
	case domain.NotifyEnvironmentOffline:
		if t, err := time.Parse(time.RFC3339, f["since"]); err == nil {
			return "Not connected since " + t.UTC().Format("Jan 2, 2006, 15:04") + " UTC. Actions on it wait until it reconnects. Check that the host and its agent are running."
		}
		return "Not connected. Actions on it wait until it reconnects. Check that the host and its agent are running."
	case domain.NotifyJobFailed:
		who := "A scheduled job"
		if f["origin"] == string(domain.OriginAPIToken) {
			who = "A job started by an API token"
		}
		class := jobErrorClass(f)
		why := describeError(class)
		if f["jobState"] == string(domain.JobPartial) {
			why = "Some of its items failed."
			if f["failedItems"] != "" {
				why += " Failed: " + f["failedItems"] + "."
			}
			if d := describeError(f["itemErrorClass"]); d != "" {
				why += " " + d
			} else {
				why += " Open the job to see which ones."
			}
		}
		if why == "" {
			why = "Open the job to see what happened and try again."
		}
		return who + " did not finish successfully. " + why
	case domain.NotifyUpdates:
		if f["services"] != "" {
			return "Newer images: " + f["services"] + "."
		}
	}
	return ""
}

// jobErrorClass is the class that explains a failed job: an item's when
// the job only says a step failed.
func jobErrorClass(f map[string]string) string {
	if (f["errorClass"] == "" || f["errorClass"] == "step_failed") && f["itemErrorClass"] != "" {
		return f["itemErrorClass"]
	}
	return f["errorClass"]
}

// resolvedDetail is the body of a resolution message.
func resolvedDetail(a domain.Alert) string {
	switch a.Kind {
	case domain.NotifyDiskHealth:
		return "The disk is healthy again."
	case domain.NotifyRAID:
		return "The array is healthy again."
	case domain.NotifyTemperature:
		return "The temperature is back below the warning level."
	case domain.NotifyDiskSpace:
		return "The filesystem is back below the warning level."
	case domain.NotifyMemory:
		return "Memory use is back below the warning level."
	case domain.NotifyEnvironmentOffline:
		return "The environment is connected again."
	case domain.NotifyJobFailed:
		return "The next run succeeded."
	case domain.NotifyUpdates:
		return "No update is left."
	}
	return "The problem is gone."
}

// severityWords are the severities as the app shows them.
var severityWords = map[domain.AlertSeverity]string{
	domain.AlertCritical: "Critical", domain.AlertWarning: "Warning", domain.AlertInfo: "Info",
}

// originWords say who started a job.
func originWords(f map[string]string) string {
	switch f["origin"] {
	case string(domain.OriginScheduled):
		return "Schedule"
	case string(domain.OriginAPIToken):
		return "API token"
	case string(domain.OriginManual):
		if f["startedBy"] != "" {
			return f["startedBy"]
		}
		return "A user"
	}
	return ""
}

// jobStateWords name a finished job's state.
var jobStateWords = map[string]string{
	string(domain.JobFailed): "Failed", string(domain.JobPartial): "Partly failed", string(domain.JobInterrupted): "Interrupted",
	string(domain.JobSucceeded): "Succeeded",
}

// fieldList appends the non-empty fields.
type fieldList []domain.NotificationField

func (l *fieldList) add(name, value string, inline bool) {
	if value != "" {
		*l = append(*l, domain.NotificationField{Name: name, Value: value, Inline: inline})
	}
}

// Fields are an alert's labelled values as it fires (env names its
// environment).
func Fields(a domain.Alert, env string) []domain.NotificationField {
	return alertFields(a, env, domain.AlertEventFiring)
}

// alertFields are an alert's labelled values in a message of event.
func alertFields(a domain.Alert, env, event string) []domain.NotificationField {
	f := a.Facts
	var l fieldList
	l.add("Environment", env, true)
	if event != domain.AlertEventResolved {
		l.add("Severity", severityWords[a.Severity], true)
	}
	switch a.Kind {
	case domain.NotifyDiskHealth:
		l.add("Disk", f["device"], true)
		l.add("Model", f["model"], true)
	case domain.NotifyRAID:
		l.add("Array", f["array"], true)
		l.add("Pool", f["pool"], true)
		l.add("Level", f["level"], true)
		l.add("Health", f["health"], true)
		if f["devices"] != "" {
			l.add("Disks working", f["active"]+" of "+f["devices"], true)
		}
		l.add("Failed disks", f["failedMembers"], false)
	case domain.NotifyTemperature:
		l.add("Sensor", f["sensor"], true)
		if f["celsius"] != "" {
			l.add("Highest", f["celsius"]+" °C", true)
		}
		l.add("Thresholds", thresholdWords(f, " °C"), false)
	case domain.NotifyDiskSpace:
		l.add("Filesystem", MountLabel(f["mount"]), true)
		if f["usedPercent"] != "" {
			l.add("Used", f["usedPercent"]+"%", true)
		}
		l.add("Free", bytesFact(f["freeBytes"]), true)
		l.add("Size", bytesFact(f["totalBytes"]), true)
		l.add("Thresholds", thresholdWords(f, "%"), false)
	case domain.NotifyMemory:
		if f["usedPercent"] != "" {
			l.add("Used", f["usedPercent"]+"%", true)
		}
		if used, total := bytesFact(f["usedBytes"]), bytesFact(f["totalBytes"]); used != "" && total != "" {
			l.add("Memory", used+" of "+total, true)
		}
		l.add("Thresholds", thresholdWords(f, "%"), false)
	case domain.NotifyEnvironmentOffline:
		if t, err := time.Parse(time.RFC3339, f["since"]); err == nil {
			l.add("Offline since", t.UTC().Format("Jan 2, 2006, 15:04")+" UTC", true)
		}
	case domain.NotifyJobFailed:
		l.add("Job", kindNoun(domain.JobKind(f["jobKind"])), true)
		l.add("Target", f["target"], true)
		l.add("State", jobStateWords[f["jobState"]], true)
		l.add("Started by", originWords(f), true)
		if event != domain.AlertEventResolved {
			l.add("Failed", f["failedItems"], false)
			l.add("What went wrong", errorReason(jobErrorClass(f)), false)
			l.add("What to do", errorFix(jobErrorClass(f)), false)
		}
	case domain.NotifyUpdates:
		l.add("Target", f["target"], true)
		l.add("Updates", f["count"], true)
		l.add("Services", f["services"], false)
	}
	return l
}

// line is a delivery's line in a digest.
func line(d domain.AlertDelivery) string {
	switch {
	case d.Event == domain.AlertEventResolved:
		return "Resolved: " + d.Title
	case d.Event == domain.DeliveryEventNotification:
		return outcomeWords[d.Outcome] + ": " + d.Title
	}
	return severityWords[d.Severity] + ": " + d.Title
}

// outcomeWords name a notification's outcome.
var outcomeWords = map[domain.NotificationOutcome]string{
	domain.OutcomeSuccess: "Done", domain.OutcomeWarning: "Warning", domain.OutcomeFailure: "Failed",
}

// footer is the line under every message.
const footer = "Docker Manager"

// buildMessage builds the message of one or more deliveries of a channel
// from their snapshots (a burst becomes one digest linking to the
// Notifications page, in the color of its worst item).
func buildMessage(instance, publicURL string, items []domain.AlertDelivery, now time.Time) domain.NotificationMessage {
	prefix := ""
	if instance != "" {
		prefix = "[" + instance + "] "
	}
	base := strings.TrimRight(publicURL, "/")
	if len(items) == 1 {
		it := items[0]
		title := it.Title
		if it.Event == domain.AlertEventResolved {
			title = "Resolved: " + title
		}
		msg := domain.NotificationMessage{Title: prefix + title, Body: it.Body, Tone: it.Tone(), Fields: it.Fields,
			Footer: footer, Time: it.CreatedAt}
		if base != "" && it.Link != "" {
			msg.URL = base + it.Link
		}
		return msg
	}
	fired, resolved, finished := 0, 0, 0
	tone := domain.ToneSuccess
	rank := map[domain.NotificationTone]int{domain.ToneSuccess: 0, domain.ToneInfo: 1, domain.ToneWarning: 2, domain.ToneCritical: 3}
	for _, it := range items {
		switch it.Event {
		case domain.AlertEventResolved:
			resolved++
		case domain.DeliveryEventNotification:
			finished++
		default:
			fired++
		}
		if t := it.Tone(); rank[t] > rank[tone] {
			tone = t
		}
	}
	var what []string
	if fired > 0 {
		what = append(what, pluralN(fired, "alert", "alerts"))
	}
	if resolved > 0 {
		what = append(what, fmt.Sprintf("%d resolved", resolved))
	}
	if finished > 0 {
		what = append(what, pluralN(finished, "notification", "notifications"))
	}
	var lines []string
	for i, it := range items {
		if i == DigestMaxLines {
			lines = append(lines, fmt.Sprintf("…and %d more.", len(items)-i))
			break
		}
		lines = append(lines, "• "+line(it))
	}
	msg := domain.NotificationMessage{Title: prefix + strings.Join(what, ", "), Body: strings.Join(lines, "\n"), Tone: tone,
		Footer: footer, Time: now}
	if base != "" {
		msg.URL = base + "/notifications"
		if finished == 0 {
			msg.URL += "?tab=alerts"
		}
	}
	return msg
}
