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

// Messages (#159, #174) follow one convention, for alerts and
// notifications alike:
//
//   - a status line: the kind and outcome as "What to send" names them
//     ("Disk health · Critical", "Image updates · Available");
//   - the title: the subject first, then what happened ("Disk /dev/sda is
//     failing", "Update of Paperless succeeded"); the environment only
//     when it is the subject ("homelab is offline"), it has its own field;
//     "Resolved: <title>" for resolutions; never the instance's name;
//   - a short plain body (Detail): what it means and what to do;
//   - labeled fields, the short ones (side by side) before lists: the
//     environment, the target and policies linked to their pages, a
//     list's entries linked (services) with their change as code (image
//     digests);
//   - the footer: the instance's name; the link: the page it is about.
//
// Each service renders them as richly as it can (notify). Never serial
// numbers, job error texts or anything secret: bodies and fields are
// built from the facts only, and a failure is explained from its error
// class (errors.go).

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

// Pages fields link to (paths, completed with the public URL when a
// message is built; "" when there is none).

func environmentPath(id string) string {
	if id == "" {
		return ""
	}
	return "/environments/" + url.PathEscape(id)
}

// targetPath is the page of a job's or alert's target in environment env.
func targetPath(env string, t domain.JobTarget) string {
	if t.ID == "" {
		return ""
	}
	id := url.PathEscape(t.ID)
	switch t.Type {
	case domain.TargetStack:
		return "/stacks/" + id
	case domain.TargetRepository:
		return "/backups/repositories/" + id
	}
	if env == "" {
		return ""
	}
	switch t.Type {
	case domain.TargetContainer:
		return "/containers/" + url.PathEscape(env) + "/" + id
	case domain.TargetVolume:
		return "/volumes/" + url.PathEscape(env) + "/" + id
	case domain.TargetNetwork:
		return "/networks/" + url.PathEscape(env) + "/" + id
	case domain.TargetImage:
		return "/images/" + url.PathEscape(env) + "/" + id
	}
	return ""
}

// servicePath is the page of a service of target t: a stack service's
// logs (a service can have several containers), a standalone container's
// page.
func servicePath(env string, t domain.JobTarget, service string) string {
	switch t.Type {
	case domain.TargetStack:
		if t.ID != "" {
			return "/stacks/" + url.PathEscape(t.ID) + "/logs?service=" + url.QueryEscape(service)
		}
	case domain.TargetContainer:
		return targetPath(env, domain.JobTarget{Type: domain.TargetContainer, ID: service})
	}
	return ""
}

// firstTarget is the first of a job's or alert's targets (zero without
// one).
func firstTarget(ts []domain.JobTarget) domain.JobTarget {
	if len(ts) == 0 {
		return domain.JobTarget{}
	}
	return ts[0]
}

// stackLabel names a stack as the app shows it: its display name, else
// its Compose project name.
func stackLabel(st domain.Stack) string {
	if st.DisplayName != "" {
		return st.DisplayName
	}
	return st.Name
}

// serviceChange is a service and its image change (short digests, ""
// when unknown).
type serviceChange struct{ service, from, to string }

// encodeChanges is the fact of services and their image changes: one
// "service<TAB>from<TAB>to" line each.
func encodeChanges(cs []serviceChange) string {
	lines := make([]string, len(cs))
	for i, c := range cs {
		lines[i] = c.service + "\t" + c.from + "\t" + c.to
	}
	return strings.Join(lines, "\n")
}

// serviceItems are the entries of a changes fact (encodeChanges), each
// linked to its service's page, then "…and n more" for the services not
// listed (total is how many there are).
func serviceItems(changes, env string, t domain.JobTarget, total int) []domain.NotificationItem {
	if changes == "" {
		return nil
	}
	var items []domain.NotificationItem
	for l := range strings.SplitSeq(changes, "\n") {
		parts := strings.SplitN(l, "\t", 3)
		for len(parts) < 3 {
			parts = append(parts, "")
		}
		if parts[0] == "" {
			continue
		}
		it := domain.NotificationItem{Text: parts[0], Link: servicePath(env, t, parts[0]), To: parts[2]}
		if parts[1] != parts[2] {
			it.From = parts[1]
		}
		items = append(items, it)
	}
	if len(items) > 0 && total > len(items) {
		items = append(items, domain.NotificationItem{Text: fmt.Sprintf("…and %d more", total-len(items))})
	}
	return items
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
	"timeout":           "it did not answer in time",
	"missing":           "the agent no longer finds it",
	"smart_disabled":    "SMART is turned off on the disk",
	"no_data":           "it reported no health data",
}

// monitoringDetail explains a monitoring alert (the facts monitoring and
// reason).
func monitoringDetail(f map[string]string) string {
	since := ""
	if t, err := time.Parse(time.RFC3339, f["since"]); err == nil {
		since = t.UTC().Format("Jan 2, 2006, 15:04") + " UTC"
	}
	switch f["reason"] {
	case reasonScanFailed:
		return "The agent could not scan the disks, so disk problems may go unnoticed. It tries again with the next check."
	case reasonNoAccess:
		return "The agent can't open the disks, so their health is not watched. Run the agent with privileged: true, or turn disk checks off with DOCKER_AGENT_SMART_ENABLED=false."
	case reasonNotInstalled:
		return "The agent image has no smartctl, so the disks' health is not watched. Update the agent to the current image."
	case reasonStale:
		s := "The agent has not finished reading the disks"
		if since != "" {
			s += " since " + since
		}
		return s + ", so new disk problems go unnoticed. Check that the agent is running and restart it if needed."
	case reasonNoReport:
		s := "The agent is connected but has not reported"
		if f["monitoring"] == "raid" {
			s += " its RAID state"
		} else {
			s += " disk health"
		}
		if since != "" {
			s += " since " + since
		}
		return s + ", so new problems go unnoticed. Update the agent, or restart it if it is current."
	case reasonRAIDRead:
		return "The software RAID or ZFS pool state could not be read, so a degraded array may go unnoticed. Docker Manager tries again with the next check."
	}
	return ""
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
		if f["monitoring"] != "" {
			return monitoringDetail(f)
		}
		var parts []string
		if f["selfAssessment"] == "failed" {
			parts = append(parts, "SMART self-assessment failed")
		}
		switch {
		case f["overTemperature"] == "true":
			parts = append(parts, "too hot (NVMe temperature warning)")
		case f["criticalWarning"] != "":
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
			{"endToEndErrors", "end-to-end error", "end-to-end errors"},
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
		if f["monitoring"] != "" {
			return monitoringDetail(f)
		}
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
		return "A check found newer images. To install them, open the update policy and press Preview updates, then Apply updates."
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
		if a.Facts["monitoring"] != "" {
			return "Disk health is watched again."
		}
		return "The disk is healthy again."
	case domain.NotifyRAID:
		if a.Facts["monitoring"] != "" {
			return "The RAID state is read again."
		}
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
	l.addLink(name, value, "", inline)
}

// addLink adds a field whose value names the page at path link.
func (l *fieldList) addLink(name, value, link string, inline bool) {
	if value != "" {
		*l = append(*l, domain.NotificationField{Name: name, Value: value, Inline: inline, Link: link})
	}
}

// addItems adds a list (never inline); its value is the entries as plain
// text, one per line.
func (l *fieldList) addItems(name string, items []domain.NotificationItem) {
	if len(items) == 0 {
		return
	}
	lines := make([]string, len(items))
	for i, it := range items {
		lines[i] = it.Plain()
	}
	*l = append(*l, domain.NotificationField{Name: name, Value: strings.Join(lines, "\n"), Items: items})
}

// ordered returns the short (inline) fields first, then the others, each
// in their order: services show the short ones side by side.
func (l fieldList) ordered() []domain.NotificationField {
	if len(l) == 0 {
		return nil
	}
	out := make([]domain.NotificationField, 0, len(l))
	for _, inline := range []bool{true, false} {
		for _, f := range l {
			if f.Inline == inline {
				out = append(out, f)
			}
		}
	}
	return out
}

// Fields are an alert's labeled values as it fires (env names its
// environment).
func Fields(a domain.Alert, env string) []domain.NotificationField {
	return alertFields(a, env, domain.AlertEventFiring)
}

// alertFields are an alert's labeled values in a message of event.
func alertFields(a domain.Alert, env, event string) []domain.NotificationField {
	f := a.Facts
	var l fieldList
	l.addLink("Environment", env, environmentPath(a.EnvironmentID), true)
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
		l.addLink("Target", f["target"], targetPath(a.EnvironmentID, firstTarget(a.Targets)), true)
		l.add("State", jobStateWords[f["jobState"]], true)
		l.add("Started by", originWords(f), true)
		if event != domain.AlertEventResolved {
			l.add("Failed", f["failedItems"], false)
			l.add("What went wrong", errorReason(jobErrorClass(f)), false)
			l.add("What to do", errorFix(jobErrorClass(f)), false)
		}
	case domain.NotifyUpdates:
		t := firstTarget(a.Targets)
		l.addLink("Target", f["target"], targetPath(a.EnvironmentID, t), true)
		l.add("Updates", f["count"], true)
		total, _ := strconv.Atoi(f["count"])
		if items := serviceItems(f["changes"], a.EnvironmentID, t, total); len(items) > 0 {
			l.addItems("Services", items)
		} else {
			l.add("Services", f["services"], false)
		}
	}
	return l.ordered()
}

// line is a delivery's line in a digest.
func line(d domain.AlertDelivery) string {
	switch d.Event {
	case domain.AlertEventResolved:
		return "Resolved: " + d.Title
	case domain.DeliveryEventNotification:
		return outcomeWords[d.Outcome] + ": " + d.Title
	}
	return severityWords[d.Severity] + ": " + d.Title
}

// outcomeWords name a notification's outcome.
var outcomeWords = map[domain.NotificationOutcome]string{
	domain.OutcomeSuccess: "Done", domain.OutcomeWarning: "Warning", domain.OutcomeFailure: "Failed",
}

// footer is the line under a message when the instance has no name.
const footer = "Docker Manager"

// digestLabel and digestField are a digest's status line and the name
// of its list.
const (
	digestLabel = "Summary"
	digestField = "What happened"
)

// kindLabels name the event kinds as "What to send" does.
var kindLabels = map[domain.NotificationEventKind]string{
	domain.NotifyDiskHealth: "Disk health", domain.NotifyRAID: "RAID", domain.NotifyTemperature: "Temperature",
	domain.NotifyDiskSpace: "Disk space", domain.NotifyMemory: "Memory", domain.NotifyEnvironmentOffline: "Environment offline",
	domain.NotifyBackup: "Backups and restores", domain.NotifyPrune: "Prune", domain.NotifyUpdates: "Image updates",
	domain.NotifyJobFailed: "Other failed jobs",
}

// outcomeLabels name the outcomes as "What to send" does (outcomeLabel
// has the kinds that name one otherwise).
var outcomeLabels = map[domain.NotificationOutcome]string{
	domain.OutcomeWarning: "Warning", domain.OutcomeCritical: "Critical", domain.OutcomeResolved: "Resolved",
	domain.OutcomeAvailable: "Available", domain.OutcomeFailure: "Failure", domain.OutcomeSuccess: "Success",
}

func outcomeLabel(kind domain.NotificationEventKind, o domain.NotificationOutcome) string {
	switch {
	case kind == domain.NotifyEnvironmentOffline && o == domain.OutcomeCritical:
		return "Offline"
	case kind == domain.NotifyEnvironmentOffline && o == domain.OutcomeResolved:
		return "Back online"
	case kind == domain.NotifyUpdates && o == domain.OutcomeSuccess:
		return "Applied"
	}
	return outcomeLabels[o]
}

// Label is a message's status line: its kind and outcome as "What to
// send" names them ("Disk health · Critical").
func Label(kind domain.NotificationEventKind, o domain.NotificationOutcome) string {
	k, w := kindLabels[kind], outcomeLabel(kind, o)
	switch {
	case k == "":
		return w
	case w == "":
		return k
	}
	return k + " · " + w
}

// environmentSuffix names a delivery's environment in a digest entry
// (" (homelab)"): titles leave it to the Environment field, which a
// digest does not show. "" without one, or when the title names it (as
// its subject, first: "homelab is offline").
func environmentSuffix(d domain.AlertDelivery) string {
	for _, f := range d.Fields {
		if f.Name == "Environment" && f.Value != "" && !strings.HasPrefix(d.Title, f.Value+" ") {
			return " (" + f.Value + ")"
		}
	}
	return ""
}

// environmentOf is the name of a delivery's environment (its
// Environment field), "" without one.
func environmentOf(d domain.AlertDelivery) string {
	for _, f := range d.Fields {
		if f.Name == "Environment" {
			return f.Value
		}
	}
	return ""
}

// commonEnvironment is the environment every delivery names, "" when
// one names none or they differ.
func commonEnvironment(items []domain.AlertDelivery) string {
	env := ""
	for i, it := range items {
		e := environmentOf(it)
		if e == "" || (i > 0 && e != env) {
			return ""
		}
		env = e
	}
	return env
}

// withLinks returns fields with their pages as URLs (abs), or without
// links when there is no public URL.
func withLinks(fields []domain.NotificationField, abs func(string) string) []domain.NotificationField {
	if len(fields) == 0 {
		return fields
	}
	out := make([]domain.NotificationField, len(fields))
	for i, f := range fields {
		f.Link = abs(f.Link)
		if len(f.Items) > 0 {
			items := make([]domain.NotificationItem, len(f.Items))
			for j, it := range f.Items {
				it.Link = abs(it.Link)
				items[j] = it
			}
			f.Items = items
		}
		out[i] = f
	}
	return out
}

// buildMessage builds the message of one or more deliveries of a channel
// from their snapshots (a burst becomes one digest linking to the
// Notifications page, in the color of its worst item, its entries linked
// to their pages). instance is the instance's name (the footer).
func buildMessage(instance, publicURL string, items []domain.AlertDelivery, now time.Time) domain.NotificationMessage {
	foot := instance
	if foot == "" {
		foot = footer
	}
	base := strings.TrimRight(publicURL, "/")
	abs := func(path string) string {
		if base == "" || path == "" {
			return ""
		}
		return base + path
	}
	if len(items) == 1 {
		it := items[0]
		title := it.Title
		if it.Event == domain.AlertEventResolved {
			title = "Resolved: " + title
		}
		return domain.NotificationMessage{Label: Label(it.Kind, it.Outcome), Title: title, Body: it.Body,
			Tag: environmentOf(it), URL: abs(it.Link), Tone: it.Tone(), Fields: withLinks(it.Fields, abs),
			Footer: foot, Time: it.CreatedAt}
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
	var entries []domain.NotificationItem
	for i, it := range items {
		if i == DigestMaxLines {
			entries = append(entries, domain.NotificationItem{Text: fmt.Sprintf("…and %d more", len(items)-i)})
			break
		}
		entries = append(entries, domain.NotificationItem{Text: line(it) + environmentSuffix(it), Link: abs(it.Link)})
	}
	var l fieldList
	l.addItems(digestField, entries)
	msg := domain.NotificationMessage{Label: digestLabel, Title: strings.Join(what, ", "), Tag: commonEnvironment(items),
		Tone: tone, Fields: l.ordered(), Footer: foot, Time: now}
	if base != "" {
		msg.URL = base + "/notifications"
		if finished == 0 {
			msg.URL += "?tab=alerts"
		}
	}
	return msg
}
