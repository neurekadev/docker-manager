package alerts

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// Messages (#159): the title "[<instance name>] <alert title>"
// ("Resolved: …" for resolutions), a short plain body from the alert's
// facts and a link to the page it is about. Never serial numbers, job
// error texts or anything secret: the body is built from the facts only.

// Link returns the path of the page an alert is about: the environment's
// System tab (disks, RAID), the environment, the job, the update policy.
func Link(a domain.Alert) string {
	switch a.Kind {
	case domain.NotifyDiskHealth, domain.NotifyRAID:
		if a.EnvironmentID != "" {
			return "/environments/" + url.PathEscape(a.EnvironmentID) + "?tab=system"
		}
	case domain.NotifyEnvironmentOffline:
		if a.EnvironmentID != "" {
			return "/environments/" + url.PathEscape(a.EnvironmentID)
		}
	case domain.NotifyJobFailed:
		if a.ResourceID != "" {
			return "/jobs/" + url.PathEscape(a.ResourceID)
		}
	case domain.NotifyUpdatesAvailable:
		if a.ResourceID != "" {
			return "/updates/" + url.PathEscape(a.ResourceID)
		}
	}
	return "/alerts"
}

func plural(n, one, many string) string {
	if n == "1" {
		return n + " " + one
	}
	return n + " " + many
}

var diskErrorWords = map[string]string{
	"permission_denied": "the agent may not open it",
	"open_failed":       "it could not be opened",
	"unsupported":       "it reports no SMART data",
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
		if f["devices"] != "" {
			b.WriteString(fmt.Sprintf("%s of %s disks working.", f["active"], f["devices"]))
		} else {
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
		return b.String()
	case domain.NotifyEnvironmentOffline:
		if t, err := time.Parse(time.RFC3339, f["since"]); err == nil {
			return "Not connected since " + t.UTC().Format("Jan 2, 2006, 15:04") + " UTC. Actions on it wait until it reconnects."
		}
		return "Not connected. Actions on it wait until it reconnects."
	case domain.NotifyJobFailed:
		who := "A scheduled job"
		if f["origin"] == string(domain.OriginAPIToken) {
			who = "A job started by an API token"
		}
		return who + " did not finish successfully. Open the job to see what happened and try again."
	case domain.NotifyUpdatesAvailable:
		if f["services"] != "" {
			return "Newer images: " + f["services"] + "."
		}
	}
	return ""
}

// severityWords are the severities as the app shows them.
var severityWords = map[domain.AlertSeverity]string{
	domain.AlertCritical: "Critical", domain.AlertWarning: "Warning", domain.AlertInfo: "Info",
}

// item is one alert of a message: the alert and what happened to it.
type item struct {
	alert domain.Alert
	event string
}

func (it item) line() string {
	if it.event == domain.AlertEventResolved {
		return "Resolved: " + it.alert.Title
	}
	return severityWords[it.alert.Severity] + ": " + it.alert.Title
}

// buildMessage builds the message of one or more alerts of a channel (a
// burst becomes one digest linking to the Alerts page).
func buildMessage(instance, publicURL string, items []item) domain.NotificationMessage {
	prefix := ""
	if instance != "" {
		prefix = "[" + instance + "] "
	}
	base := strings.TrimRight(publicURL, "/")
	if len(items) == 1 {
		it := items[0]
		title := it.alert.Title
		body := Detail(it.alert)
		if it.event == domain.AlertEventResolved {
			title = "Resolved: " + title
			body = "The problem is gone."
		}
		msg := domain.NotificationMessage{Title: prefix + title, Body: body}
		if base != "" {
			msg.URL = base + Link(it.alert)
		}
		return msg
	}
	fired, resolved := 0, 0
	for _, it := range items {
		if it.event == domain.AlertEventResolved {
			resolved++
		} else {
			fired++
		}
	}
	var what []string
	if fired > 0 {
		what = append(what, plural(fmt.Sprint(fired), "alert", "alerts"))
	}
	if resolved > 0 {
		what = append(what, fmt.Sprintf("%d resolved", resolved))
	}
	var lines []string
	for i, it := range items {
		if i == DigestMaxLines {
			lines = append(lines, fmt.Sprintf("…and %d more.", len(items)-i))
			break
		}
		lines = append(lines, "• "+it.line())
	}
	msg := domain.NotificationMessage{Title: prefix + strings.Join(what, ", "), Body: strings.Join(lines, "\n")}
	if base != "" {
		msg.URL = base + "/alerts"
	}
	return msg
}
