package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/humanize"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Notifications: every finished backup, restore, prune and update run
// (whoever started it; a cancelled one is not) is recorded with how it
// went and sent to the channels subscribed to that outcome of its kind:
//
//   - success: everything was done;
//   - warning (backups only): everything was backed up, but something
//     needs a look (unreadable files, an item removed before its turn);
//   - failure: it failed, partly failed or was interrupted.
//
// Facts come from the job's state, error class and result output (never
// its error message, recovery text, paths or input secrets: an update's
// container environment, a repository's location). A result too large to
// keep (ResultOutput nil) gives a notification without the numbers.

// maxListed bounds the names a fact lists.
const maxListed = 10

// listNames joins at most maxListed names ("a, b and 3 more").
func listNames(names []string) string {
	if len(names) <= maxListed {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:maxListed], ", ") + fmt.Sprintf(" and %d more", len(names)-maxListed)
}

// runOutcome is how a finished job went ("" for a cancelled one, which
// is not reported).
func runOutcome(j domain.Job) domain.NotificationOutcome {
	switch j.State {
	case domain.JobSucceeded:
		return domain.OutcomeSuccess
	case domain.JobFailed, domain.JobPartial, domain.JobInterrupted:
		return domain.OutcomeFailure
	}
	return ""
}

// runFacts are the facts every notification has: the job, who started it,
// its state, error class and duration.
func runFacts(ctx context.Context, db bun.IDB, j domain.Job) map[string]string {
	f := map[string]string{"jobId": j.ID, "jobKind": string(j.Kind), "jobState": string(j.State), "origin": string(j.Origin)}
	if j.ErrorClass != "" && j.State != domain.JobSucceeded {
		f["errorClass"] = j.ErrorClass
	}
	if j.Origin == domain.OriginManual && j.InitiatorUserID != "" {
		if u, err := store.GetUser(ctx, db, j.InitiatorUserID); err == nil {
			name := u.DisplayName
			if name == "" {
				name = u.Username
			}
			f["startedBy"] = name
		}
	}
	if j.StartedAt != nil && j.FinishedAt != nil && !j.FinishedAt.Before(*j.StartedAt) {
		f["durationSeconds"] = strconv.FormatInt(int64(j.FinishedAt.Sub(*j.StartedAt)/time.Second), 10)
	}
	return f
}

// factTarget is the target a notification's facts name (targetType,
// targetId).
func factTarget(f map[string]string) domain.JobTarget {
	return domain.JobTarget{Type: domain.TargetType(f["targetType"]), ID: f["targetId"]}
}

// putTarget records target t in the facts (its page in messages).
func putTarget(f map[string]string, t domain.JobTarget) {
	if t.ID != "" && t.Type != "" {
		f["targetType"], f["targetId"] = string(t.Type), t.ID
	}
}

// policyPath is the page of a notification's policy ("" without one): an
// update run's is Updates (updatePolicyPath).
func policyPath(n domain.Notification) string {
	id := n.Facts["policyId"]
	switch {
	case n.Kind == domain.NotifyUpdates && n.Facts["policy"] != "":
		return updatePolicyPath(id)
	case id == "":
		return ""
	case n.Kind == domain.NotifyBackup && n.Facts["jobKind"] == "backup.run":
		return "/backups"
	case n.Kind == domain.NotifyPrune:
		return "/maintenance"
	}
	return ""
}

// putServices records a group of an update run's services: how many
// (<key>Count) and the first maxListed with their image changes
// (<key>Changes, encodeChanges).
func putServices(f map[string]string, key string, cs []serviceChange) {
	if len(cs) == 0 {
		return
	}
	f[key+"Count"] = strconv.Itoa(len(cs))
	if len(cs) > maxListed {
		cs = cs[:maxListed]
	}
	f[key+"Changes"] = encodeChanges(cs)
}

// verbOf is the title's ending for a finished run.
func verbOf(j domain.Job, warning bool) string {
	switch {
	case j.State == domain.JobPartial:
		return "partly failed"
	case j.State == domain.JobInterrupted:
		return "was interrupted"
	case j.State == domain.JobFailed:
		return "failed"
	case warning:
		return "finished with warnings"
	}
	return "succeeded"
}

// backupInput is what a backup notification reads of backup.run's input.
type backupInput struct {
	PolicyName string `json:"policyName"`
	Repository struct {
		RepositoryID string `json:"repositoryId"`
	} `json:"repository"`
}

// managerBackupOutput is manager.backup's result.
type managerBackupOutput struct {
	Member *backup.Member            `json:"member,omitempty"`
	Stats  *protocol.RepositoryStats `json:"stats,omitempty"`
}

// memberName names a backed up item as users know it.
func memberName(m backup.Member) string {
	switch {
	case m.StackName != "":
		return m.StackName
	case m.Volume != "":
		return m.Volume
	case m.Kind == "manager":
		return "Docker Manager"
	}
	return m.Item
}

// backupNotification describes a finished backup.run or manager.backup.
func backupNotification(ctx context.Context, db bun.IDB, j domain.Job, f map[string]string) (domain.NotificationOutcome, string) {
	var in backupInput
	_ = json.Unmarshal(j.Input, &in)
	// Runs of the backup setup (#246) and of the earlier policies.
	if in.PolicyName != "" {
		f["policy"] = in.PolicyName
	} else if j.PolicyID != "" {
		f["policy"] = "Backups"
	}
	if j.PolicyID != "" && f["policy"] != "" {
		f["policyId"] = j.PolicyID
	}
	repoID := in.Repository.RepositoryID
	var members []backup.Member
	var stats *protocol.RepositoryStats
	if j.Kind == "manager.backup" {
		var out managerBackupOutput
		if json.Unmarshal(j.ResultOutput, &out) == nil {
			if out.Member != nil {
				members = []backup.Member{*out.Member}
			}
			stats = out.Stats
		}
	} else {
		var out protocol.BackupRunOutput
		if json.Unmarshal(j.ResultOutput, &out) == nil {
			members, stats = out.Members, out.Stats
			if out.Shutdown != nil && len(out.Shutdown.Conflicts) > 0 {
				f["conflicts"] = strconv.Itoa(len(out.Shutdown.Conflicts))
			}
		}
	}
	if repoID == "" {
		for _, m := range members {
			if m.RepositoryID != "" {
				repoID = m.RepositoryID
				break
			}
		}
	}
	if repoID != "" {
		if r, err := store.GetBackupRepository(ctx, db, repoID); err == nil {
			f["repository"], f["repositoryId"] = r.Name, repoID
		}
	}
	var done, failed, skipped, unreadable []string
	var bytes int64
	for _, m := range members {
		name := memberName(m)
		switch m.State {
		case backup.StateComplete:
			done = append(done, name)
		case backup.StatePartial:
			done = append(done, name)
			if m.ErrorClass == "files_unreadable" {
				unreadable = append(unreadable, name)
			}
		case backup.StateSkipped:
			skipped = append(skipped, name)
		case backup.StateFailed, backup.StateMissing, backup.StatePending:
			failed = append(failed, name)
			if f["memberErrorClass"] == "" && m.ErrorClass != "" {
				f["memberErrorClass"] = m.ErrorClass
			}
		}
		bytes += max(m.Bytes, 0)
	}
	if len(members) > 0 {
		f["items"] = strconv.Itoa(len(members))
		f["backedUp"] = strconv.Itoa(len(done))
	}
	if len(failed) > 0 {
		f["failed"] = strconv.Itoa(len(failed))
		f["failedItems"] = listNames(failed)
	}
	if len(skipped) > 0 {
		f["skipped"] = strconv.Itoa(len(skipped))
		f["skippedItems"] = listNames(skipped)
	}
	if len(unreadable) > 0 {
		f["unreadable"] = strconv.Itoa(len(unreadable))
		f["unreadableItems"] = listNames(unreadable)
	}
	if bytes > 0 {
		f["bytes"] = strconv.FormatInt(bytes, 10)
	}
	if stats != nil {
		f["repositorySize"] = strconv.FormatInt(stats.SizeBytes, 10)
		f["snapshots"] = strconv.FormatInt(stats.Snapshots, 10)
	}
	outcome := runOutcome(j)
	warning := outcome == domain.OutcomeSuccess && (len(unreadable) > 0 || len(skipped) > 0)
	if warning {
		outcome = domain.OutcomeWarning
	}
	// The policy's name is the subject ("Daily Backups succeeded").
	what := "Backup"
	switch {
	case j.Kind == "manager.backup":
		what = "Docker Manager backup"
	case f["policy"] != "":
		what = f["policy"]
	}
	return outcome, what + " " + verbOf(j, warning)
}

// restoreInput is what a restore notification reads of restore.run's
// input.
type restoreInput struct {
	Repository struct {
		RepositoryID string `json:"repositoryId"`
	} `json:"repository"`
	Scope     string `json:"scope"`
	StackID   string `json:"stackId"`
	StackName string `json:"stackName"`
	Volumes   []struct {
		Name string `json:"name"`
	} `json:"volumes"`
}

// restoreNotification describes a finished restore.run.
func restoreNotification(ctx context.Context, db bun.IDB, j domain.Job, f map[string]string) (domain.NotificationOutcome, string) {
	var in restoreInput
	_ = json.Unmarshal(j.Input, &in)
	target := in.StackName
	switch {
	case in.StackID != "":
		if st, err := store.GetStack(ctx, db, in.StackID); err == nil {
			target = stackLabel(st)
		}
		putTarget(f, domain.JobTarget{Type: domain.TargetStack, ID: in.StackID})
	case target == "" && len(in.Volumes) > 0:
		names := make([]string, 0, len(in.Volumes))
		for _, v := range in.Volumes {
			names = append(names, v.Name)
		}
		target = listNames(names)
		if len(names) == 1 {
			putTarget(f, domain.JobTarget{Type: domain.TargetVolume, ID: names[0]})
		}
	}
	if target != "" {
		f["target"] = target
	}
	if in.Repository.RepositoryID != "" {
		if r, err := store.GetBackupRepository(ctx, db, in.Repository.RepositoryID); err == nil {
			f["repository"], f["repositoryId"] = r.Name, in.Repository.RepositoryID
		}
	}
	var out protocol.RestoreRunOutput
	if json.Unmarshal(j.ResultOutput, &out) == nil && out.RedeploySuggested {
		f["redeploy"] = "true"
	}
	what := "Restore"
	if target != "" {
		what += " of " + target
	}
	return runOutcome(j), what + " " + verbOf(j, false)
}

// pruneGroups sum prune categories the way users count them.
var pruneGroups = []struct {
	key        string
	categories []string
}{
	{"containers", []string{protocol.PruneStoppedContainers}},
	{"images", []string{protocol.PruneDanglingImages, protocol.PruneUnusedImages}},
	{"volumes", []string{protocol.PruneAnonymousVolumes, protocol.PruneNamedVolumes}},
	{"networks", []string{protocol.PruneUnusedNetworks}},
	{"buildCache", []string{protocol.PruneBuildCache}},
}

// pruneNotification describes a finished prune.run: the space reclaimed
// in total and per kind of object.
func pruneNotification(ctx context.Context, db bun.IDB, j domain.Job, f map[string]string) (domain.NotificationOutcome, string) {
	if j.PolicyID != "" {
		// Maintenance's run (one-off prunes have no policy).
		f["policy"], f["policyId"] = "Maintenance", j.PolicyID
	}
	var out protocol.PruneRunOutput
	known := json.Unmarshal(j.ResultOutput, &out) == nil && j.ResultOutput != nil
	if known {
		f["reclaimedBytes"] = strconv.FormatInt(max(out.BytesReclaimed, 0), 10)
		f["removed"] = strconv.Itoa(out.Removed)
		if out.Skipped > 0 {
			f["skipped"] = strconv.Itoa(out.Skipped)
		}
		if out.Failed > 0 {
			f["failed"] = strconv.Itoa(out.Failed)
		}
		if out.Deferred > 0 {
			f["deferred"] = strconv.Itoa(out.Deferred)
		}
		for _, g := range pruneGroups {
			n, bytes := 0, int64(0)
			for _, it := range out.Items {
				if it.Status == protocol.PruneItemRemoved && slices.Contains(g.categories, it.Category) {
					n++
					bytes += max(it.Bytes, 0)
				}
			}
			if n > 0 {
				f[g.key+"Removed"] = strconv.Itoa(n)
				f[g.key+"Bytes"] = strconv.FormatInt(bytes, 10)
			}
		}
	}
	outcome := runOutcome(j)
	title := "Prune " + verbOf(j, false)
	if outcome == domain.OutcomeSuccess && known {
		if out.Removed == 0 {
			title = "Prune found nothing to remove"
		} else {
			title = "Prune reclaimed " + humanize.Bytes(max(out.BytesReclaimed, 0))
		}
	}
	return outcome, title
}

// updateInput is what an update notification reads of update.run's input
// (never the container's specification: it holds its environment).
type updateInput struct {
	PolicyID  string `json:"policyId"`
	StackID   string `json:"stackId"`
	Container *struct {
		Name string `json:"name"`
	} `json:"container"`
}

// shortDigest is the first 12 hex digits of a digest.
func shortDigest(d string) string {
	_, hex, ok := strings.Cut(d, ":")
	if !ok {
		hex = d
	}
	if len(hex) > 12 {
		hex = hex[:12]
	}
	return hex
}

// updateNotification describes a finished update.run.
func updateNotification(ctx context.Context, db bun.IDB, j domain.Job, f map[string]string) (domain.NotificationOutcome, string) {
	var in updateInput
	_ = json.Unmarshal(j.Input, &in)
	target := ""
	var t domain.JobTarget
	switch {
	case in.StackID != "":
		t = domain.JobTarget{Type: domain.TargetStack, ID: in.StackID}
		if st, err := store.GetStack(ctx, db, in.StackID); err == nil {
			target = stackLabel(st)
		}
	case in.Container != nil:
		t = domain.JobTarget{Type: domain.TargetContainer, ID: in.Container.Name}
		target = in.Container.Name
	}
	if target == "" {
		target, t = targetName(ctx, db, j), firstTarget(j.Targets)
	}
	if target != "" {
		f["target"] = target
		putTarget(f, t)
	}
	// The input's policy is the target's record: the notification names
	// the updates setup that manages it.
	if in.PolicyID != "" {
		if p, err := store.GetUpdatePolicy(ctx, db, in.PolicyID); err == nil {
			putUpdatePolicy(ctx, db, p, f)
		}
	}
	var out protocol.UpdateRunOutput
	updated := 0
	if json.Unmarshal(j.ResultOutput, &out) == nil {
		var changed, unchanged, stopped, failed []serviceChange
		var unchangedNames, stoppedNames, failedNames []string
		for _, sv := range out.Services {
			switch sv.Outcome {
			case protocol.UpdateUpdated:
				changed = append(changed, serviceChange{sv.Service, shortDigest(sv.FromDigest), shortDigest(sv.ToDigest)})
			case protocol.UpdateUnchanged:
				unchanged = append(unchanged, serviceChange{service: sv.Service})
				unchangedNames = append(unchangedNames, sv.Service)
			case protocol.UpdateKeptStopped:
				stopped = append(stopped, serviceChange{sv.Service, shortDigest(sv.FromDigest), shortDigest(sv.ToDigest)})
				stoppedNames = append(stoppedNames, sv.Service)
			case protocol.UpdateFailed:
				failed = append(failed, serviceChange{service: sv.Service})
				failedNames = append(failedNames, sv.Service)
			}
		}
		updated = len(changed)
		if updated > 0 {
			f["updated"] = strconv.Itoa(updated)
		}
		putServices(f, "updated", changed)
		putServices(f, "unchanged", unchanged)
		putServices(f, "keptStopped", stopped)
		putServices(f, "failed", failed)
		// In words for the body.
		if len(unchangedNames) > 0 {
			f["unchangedServices"] = listNames(unchangedNames)
		}
		if len(stoppedNames) > 0 {
			f["keptStoppedServices"] = listNames(stoppedNames)
		}
		if len(failedNames) > 0 {
			f["failedServices"] = listNames(failedNames)
		}
	}
	outcome := runOutcome(j)
	switch {
	case outcome != domain.OutcomeSuccess && target == "":
		return outcome, "Update " + verbOf(j, false)
	case outcome != domain.OutcomeSuccess:
		return outcome, "Update of " + target + " " + verbOf(j, false)
	case target == "":
		return outcome, "Update succeeded"
	case updated == 0 && out.Services != nil:
		return outcome, target + " is already up to date"
	}
	return outcome, "Update of " + target + " succeeded"
}

// notificationOf describes a finished job of a notification kind (ok
// false: nothing to report, a cancelled run).
func notificationOf(ctx context.Context, db bun.IDB, j domain.Job, now time.Time) (domain.Notification, bool) {
	kind := domain.NotificationKindOfJob(j.Kind)
	if kind == "" || runOutcome(j) == "" {
		return domain.Notification{}, false
	}
	f := runFacts(ctx, db, j)
	var outcome domain.NotificationOutcome
	var title string
	switch j.Kind {
	case "backup.run", "manager.backup":
		outcome, title = backupNotification(ctx, db, j, f)
	case "restore.run":
		outcome, title = restoreNotification(ctx, db, j, f)
	case "prune.run":
		outcome, title = pruneNotification(ctx, db, j, f)
	case "update.run":
		outcome, title = updateNotification(ctx, db, j, f)
	}
	return domain.Notification{
		ID: ids.New(), Kind: kind, Outcome: outcome, EnvironmentID: j.EnvironmentID, JobID: j.ID, JobKind: j.Kind,
		Targets: j.Targets, Origin: j.Origin, Title: clipTitle(title), Facts: f, CreatedAt: now,
	}, true
}

// clipTitle keeps a title within the 300 characters the database allows.
func clipTitle(s string) string {
	r := []rune(s)
	if len(r) <= 300 {
		return s
	}
	return string(r[:299]) + "…"
}

// NotificationDetail describes a notification in one or two plain
// sentences (the message body and the Notifications page's second line).
func NotificationDetail(n domain.Notification) string {
	f := n.Facts
	if n.Outcome == domain.OutcomeFailure {
		class := f["errorClass"]
		if class == "" || class == "step_failed" {
			class = f["memberErrorClass"]
		}
		var b strings.Builder
		switch {
		case f["jobState"] == string(domain.JobPartial):
			b.WriteString("Some of its items failed.")
			if why := errorReason(class); why != "" {
				b.WriteString(" " + why)
			}
		case describeError(class) != "":
			b.WriteString(describeError(class))
		default:
			b.WriteString("Open the job to see what happened.")
		}
		switch n.Kind {
		case domain.NotifyBackup:
			if f["failedItems"] != "" {
				b.WriteString(" Not backed up: " + f["failedItems"] + ".")
			}
		case domain.NotifyUpdates:
			if f["failedServices"] != "" {
				b.WriteString(" Failed: " + f["failedServices"] + ".")
			}
		}
		if fix := errorFix(class); fix != "" && f["jobState"] == string(domain.JobPartial) {
			b.WriteString(" " + fix)
		}
		return b.String()
	}
	// A restore recorded before restores had a kind of their own is a
	// backup with restore.run's facts.
	if n.Kind == domain.NotifyRestore || f["jobKind"] == "restore.run" {
		if f["redeploy"] == "true" {
			return "The data is back in place. Deploy the stack to apply its restored Compose file."
		}
		return "The data is back in place."
	}
	switch n.Kind {
	case domain.NotifyBackup:
		var parts []string
		if f["items"] != "" {
			parts = append(parts, fmt.Sprintf("%s of %s backed up", f["backedUp"], plural(f["items"], "item", "items")))
		}
		if b := bytesFact(f["bytes"]); b != "" {
			parts = append(parts, b+" read")
		}
		s := ""
		if len(parts) > 0 {
			s = strings.ToUpper(parts[0][:1]) + parts[0][1:]
			if len(parts) > 1 {
				s += " (" + parts[1] + ")"
			}
			if d := durationWords(f["durationSeconds"]); d != "" {
				s += " in " + d
			}
			s += "."
		}
		if f["unreadableItems"] != "" {
			s += " Some files in " + f["unreadableItems"] + " could not be read: check their permissions on the host."
		}
		if f["skippedItems"] != "" {
			s += " Skipped because they were removed before their turn: " + f["skippedItems"] + "."
		}
		return strings.TrimSpace(s)
	case domain.NotifyPrune:
		if f["reclaimedBytes"] == "" {
			return ""
		}
		if f["removed"] == "0" {
			return "There was nothing unused to remove."
		}
		s := fmt.Sprintf("Removed %s and reclaimed %s.", plural(f["removed"], "object", "objects"), bytesFact(f["reclaimedBytes"]))
		if f["deferred"] != "" {
			s += " " + plural(f["deferred"], "object waits", "objects wait") + " for the next run."
		}
		return s
	case domain.NotifyUpdates:
		var parts []string
		if f["updated"] != "" {
			parts = append(parts, plural(f["updated"], "service", "services")+" recreated with the new image.")
		}
		if f["keptStoppedServices"] != "" {
			parts = append(parts, "Kept stopped: "+f["keptStoppedServices"]+". They use the new image when they start.")
		}
		if len(parts) == 0 && f["unchangedServices"] != "" {
			parts = append(parts, "Every service already runs the newest image.")
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// updateGroups are an update run's lists of services: the field, the
// fact key (putServices) and the fact of notifications recorded before
// the lists (their plain text).
var updateGroups = []struct{ name, key, plain string }{
	{"Updated", "updated", "updatedServices"},
	{"Already Up to Date", "unchanged", "unchangedServices"},
	{"Kept Stopped", "keptStopped", "keptStoppedServices"},
	{"Failed", "failed", "failedServices"},
}

// NotificationFields are a notification's labeled values (the short ones
// first).
func NotificationFields(n domain.Notification, env string) []domain.NotificationField {
	f := n.Facts
	var l fieldList
	l.addLink("Environment", env, environmentPath(n.EnvironmentID), true)
	target := factTarget(f)
	switch n.Kind {
	case domain.NotifyBackup, domain.NotifyRestore:
		l.addLink("Policy", f["policy"], policyPath(n), true)
		l.addLink("Target", f["target"], targetPath(n.EnvironmentID, target), true)
		repo := ""
		if f["repositoryId"] != "" {
			repo = targetPath("", domain.JobTarget{Type: domain.TargetRepository, ID: f["repositoryId"]})
		}
		l.addLink("Repository", f["repository"], repo, true)
		if f["items"] != "" {
			v := f["backedUp"] + " of " + f["items"] + " backed up"
			if f["failed"] != "" {
				v += " · " + f["failed"] + " failed"
			}
			if f["skipped"] != "" {
				v += " · " + f["skipped"] + " skipped"
			}
			l.add("Items", v, true)
		}
		l.add("Data Read", bytesFact(f["bytes"]), true)
		l.add("Repository Size", bytesFact(f["repositorySize"]), true)
		if f["snapshots"] != "" && f["snapshots"] != "0" {
			l.add("Snapshots", f["snapshots"], true)
		}
		l.add("Not Backed Up", f["failedItems"], false)
		l.add("Unreadable Files In", f["unreadableItems"], false)
	case domain.NotifyPrune:
		l.addLink("Policy", f["policy"], policyPath(n), true)
		l.add("Reclaimed", bytesFact(f["reclaimedBytes"]), true)
		for _, g := range []struct{ key, name, one, many string }{
			{"containers", "Containers", "container", "containers"},
			{"images", "Images", "image", "images"},
			{"volumes", "Volumes", "volume", "volumes"},
			{"networks", "Networks", "network", "networks"},
			{"buildCache", "Build Cache", "entry", "entries"},
		} {
			if f[g.key+"Removed"] == "" {
				continue
			}
			v := plural(f[g.key+"Removed"], g.one, g.many)
			if b := bytesFact(f[g.key+"Bytes"]); b != "" && f[g.key+"Bytes"] != "0" {
				v += " · " + b
			}
			l.add(g.name, v, true)
		}
		l.add("Skipped", f["skipped"], true)
		l.add("Failed", f["failed"], true)
		l.add("Left for the Next Run", f["deferred"], true)
	case domain.NotifyUpdates:
		l.addLink("Target", f["target"], targetPath(n.EnvironmentID, target), true)
		l.addLink("Policy", f["policy"], policyPath(n), true)
		for _, g := range updateGroups {
			total, _ := strconv.Atoi(f[g.key+"Count"])
			if items := serviceItems(f[g.key+"Changes"], n.EnvironmentID, target, total); len(items) > 0 {
				l.addItems(g.name, items)
			} else {
				l.add(g.name, f[g.plain], false)
			}
		}
	}
	l.add("Duration", durationWords(f["durationSeconds"]), true)
	l.add("Started By", originWords(f), true)
	if n.Outcome == domain.OutcomeFailure {
		class := f["errorClass"]
		if class == "" || class == "step_failed" {
			class = f["memberErrorClass"]
		}
		l.add("What to Do", errorFix(class), false)
	}
	return l.ordered()
}

// recordNotification stores a notification and writes its messages.
func recordNotification(ctx context.Context, db bun.IDB, n domain.Notification, now time.Time) error {
	if err := store.InsertNotification(ctx, db, &n); err != nil {
		return err
	}
	m := snapshot{notificationID: n.ID, event: domain.DeliveryEventNotification, kind: n.Kind, environmentID: n.EnvironmentID,
		severity: n.Severity(), outcome: n.Outcome, title: n.Title, body: NotificationDetail(n), link: NotificationLink(n)}
	return write(ctx, db, m, now, nil, func() []domain.NotificationField {
		return NotificationFields(n, environmentName(ctx, db, n.EnvironmentID))
	})
}

// onRunFinished is the finish hook of backups, restores, prunes and
// update runs, in the job's finishing transaction (a savepoint: a failure
// loses the notification only, never the job's outcome). The
// notification is announced once the job's change committed.
func (s *Service) onRunFinished(ctx context.Context, db bun.IDB, j domain.Job) error {
	now := s.now()
	var recorded *domain.Notification
	err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		n, ok := notificationOf(ctx, tx, j, now)
		if !ok {
			return nil
		}
		if err := recordNotification(ctx, tx, n, now); err != nil {
			return err
		}
		recorded = &n
		return nil
	})
	if err != nil {
		s.log.Warn("could not record the notification of a finished job", "job_id", j.ID, "job_kind", string(j.Kind), "error", err)
		return nil
	}
	if recorded != nil {
		s.holdNotification(j.ID, *recorded)
	}
	return nil
}
