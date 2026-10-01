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

// on returns " on <environment>" ("" without one).
func on(env string) string {
	if env == "" {
		return ""
	}
	return " on " + env
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
func backupNotification(ctx context.Context, db bun.IDB, j domain.Job, env string, f map[string]string) (domain.NotificationOutcome, string) {
	var in backupInput
	_ = json.Unmarshal(j.Input, &in)
	if in.PolicyName != "" {
		f["policy"] = in.PolicyName
	} else if j.PolicyID != "" {
		if p, err := store.GetBackupPolicy(ctx, db, j.PolicyID); err == nil {
			f["policy"] = p.Name
		}
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
			f["repository"] = r.Name
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
	what := "Backup"
	switch {
	case j.Kind == "manager.backup":
		what = "Docker Manager backup"
	case f["policy"] != "":
		what = "Backup " + f["policy"]
	}
	return outcome, what + on(env) + " " + verbOf(j, warning)
}

// restoreInput is what a restore notification reads of restore.run's
// input.
type restoreInput struct {
	Repository struct {
		RepositoryID string `json:"repositoryId"`
	} `json:"repository"`
	Scope     string `json:"scope"`
	StackName string `json:"stackName"`
	Volumes   []struct {
		Name string `json:"name"`
	} `json:"volumes"`
}

// restoreNotification describes a finished restore.run.
func restoreNotification(ctx context.Context, db bun.IDB, j domain.Job, env string, f map[string]string) (domain.NotificationOutcome, string) {
	var in restoreInput
	_ = json.Unmarshal(j.Input, &in)
	target := in.StackName
	if target == "" && len(in.Volumes) > 0 {
		names := make([]string, 0, len(in.Volumes))
		for _, v := range in.Volumes {
			names = append(names, v.Name)
		}
		target = listNames(names)
	}
	if target != "" {
		f["target"] = target
	}
	if in.Repository.RepositoryID != "" {
		if r, err := store.GetBackupRepository(ctx, db, in.Repository.RepositoryID); err == nil {
			f["repository"] = r.Name
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
	return runOutcome(j), what + on(env) + " " + verbOf(j, false)
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
func pruneNotification(ctx context.Context, db bun.IDB, j domain.Job, env string, f map[string]string) (domain.NotificationOutcome, string) {
	if j.PolicyID != "" {
		if p, err := store.GetMaintenancePolicy(ctx, db, j.PolicyID); err == nil {
			f["policy"] = p.Name
		}
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
	title := "Prune" + on(env) + " " + verbOf(j, false)
	if outcome == domain.OutcomeSuccess && known {
		if out.Removed == 0 {
			title = "Prune" + on(env) + " found nothing to remove"
		} else {
			title = "Prune" + on(env) + " reclaimed " + humanize.Bytes(max(out.BytesReclaimed, 0))
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
func updateNotification(ctx context.Context, db bun.IDB, j domain.Job, env string, f map[string]string) (domain.NotificationOutcome, string) {
	var in updateInput
	_ = json.Unmarshal(j.Input, &in)
	target := ""
	switch {
	case in.StackID != "":
		if st, err := store.GetStack(ctx, db, in.StackID); err == nil {
			target = st.Name
		}
	case in.Container != nil:
		target = in.Container.Name
	}
	if target == "" {
		target = targetName(ctx, db, j)
	}
	if target != "" {
		f["target"] = target
	}
	var out protocol.UpdateRunOutput
	updated := 0
	if json.Unmarshal(j.ResultOutput, &out) == nil {
		var lines, unchanged, failed, stopped []string
		for _, sv := range out.Services {
			switch sv.Outcome {
			case protocol.UpdateUpdated:
				updated++
				l := sv.Service
				if sv.Reference != "" {
					l += " (" + sv.Reference + ")"
				}
				if from, to := shortDigest(sv.FromDigest), shortDigest(sv.ToDigest); from != "" && to != "" && from != to {
					l += ": " + from + " → " + to
				}
				lines = append(lines, l)
			case protocol.UpdateUnchanged:
				unchanged = append(unchanged, sv.Service)
			case protocol.UpdateKeptStopped:
				stopped = append(stopped, sv.Service)
			case protocol.UpdateFailed:
				failed = append(failed, sv.Service)
			}
		}
		if len(lines) > maxListed {
			lines = append(lines[:maxListed], fmt.Sprintf("and %d more", len(lines)-maxListed))
		}
		if len(lines) > 0 {
			f["updated"] = strconv.Itoa(updated)
			f["updatedServices"] = strings.Join(lines, "\n")
		}
		if len(unchanged) > 0 {
			f["unchangedServices"] = listNames(unchanged)
		}
		if len(stopped) > 0 {
			f["keptStoppedServices"] = listNames(stopped)
		}
		if len(failed) > 0 {
			f["failedServices"] = listNames(failed)
		}
	}
	outcome := runOutcome(j)
	name := target
	if name == "" {
		name = "an update"
	}
	switch {
	case outcome != domain.OutcomeSuccess:
		return outcome, "Update of " + name + on(env) + " " + verbOf(j, false)
	case updated == 0 && f["updatedServices"] == "" && out.Services != nil:
		return outcome, name + on(env) + " was already up to date"
	case updated == 1:
		return outcome, "Updated " + name + on(env) + ": 1 service"
	case updated > 1:
		return outcome, fmt.Sprintf("Updated %s%s: %d services", name, on(env), updated)
	}
	return outcome, "Updated " + name + on(env)
}

// notificationOf describes a finished job of a notification kind (ok
// false: nothing to report, a cancelled run).
func notificationOf(ctx context.Context, db bun.IDB, j domain.Job, now time.Time) (domain.Notification, bool) {
	kind := domain.NotificationKindOfJob(j.Kind)
	if kind == "" || runOutcome(j) == "" {
		return domain.Notification{}, false
	}
	env := environmentName(ctx, db, j.EnvironmentID)
	f := runFacts(ctx, db, j)
	var outcome domain.NotificationOutcome
	var title string
	switch j.Kind {
	case "backup.run", "manager.backup":
		outcome, title = backupNotification(ctx, db, j, env, f)
	case "restore.run":
		outcome, title = restoreNotification(ctx, db, j, env, f)
	case "prune.run":
		outcome, title = pruneNotification(ctx, db, j, env, f)
	case "update.run":
		outcome, title = updateNotification(ctx, db, j, env, f)
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
	switch n.Kind {
	case domain.NotifyBackup:
		if f["jobKind"] == "restore.run" {
			if f["redeploy"] == "true" {
				return "The data is back in place. Deploy the stack to apply its restored Compose file."
			}
			return "The data is back in place."
		}
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
			s += " Some files could not be read in " + f["unreadableItems"] + ": check their permissions on the host."
		}
		if f["skippedItems"] != "" {
			s += " Skipped (removed before their turn): " + f["skippedItems"] + "."
		}
		return strings.TrimSpace(s)
	case domain.NotifyPrune:
		if f["reclaimedBytes"] == "" {
			return ""
		}
		if f["removed"] == "0" {
			return "Nothing was unused."
		}
		s := fmt.Sprintf("Removed %s and reclaimed %s.", plural(f["removed"], "object", "objects"), bytesFact(f["reclaimedBytes"]))
		if f["deferred"] != "" {
			s += " " + plural(f["deferred"], "object waits", "objects wait") + " for the next run."
		}
		return s
	case domain.NotifyUpdates:
		var parts []string
		if f["updated"] != "" {
			parts = append(parts, "Recreated with the new image: "+plural(f["updated"], "service", "services")+".")
		}
		if f["keptStoppedServices"] != "" {
			parts = append(parts, "Kept stopped (they use the new image at their next start): "+f["keptStoppedServices"]+".")
		}
		if len(parts) == 0 && f["unchangedServices"] != "" {
			parts = append(parts, "Every service already runs the newest image.")
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// NotificationFields are a notification's labelled values.
func NotificationFields(n domain.Notification, env string) []domain.NotificationField {
	f := n.Facts
	var l fieldList
	l.add("Environment", env, true)
	switch n.Kind {
	case domain.NotifyBackup:
		l.add("Policy", f["policy"], true)
		l.add("Target", f["target"], true)
		l.add("Repository", f["repository"], true)
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
		l.add("Data read", bytesFact(f["bytes"]), true)
		l.add("Repository size", bytesFact(f["repositorySize"]), true)
		if f["snapshots"] != "" && f["snapshots"] != "0" {
			l.add("Snapshots", f["snapshots"], true)
		}
		l.add("Not backed up", f["failedItems"], false)
		l.add("Files unreadable in", f["unreadableItems"], false)
	case domain.NotifyPrune:
		l.add("Policy", f["policy"], true)
		l.add("Reclaimed", bytesFact(f["reclaimedBytes"]), true)
		for _, g := range []struct{ key, name, one, many string }{
			{"containers", "Containers", "container", "containers"},
			{"images", "Images", "image", "images"},
			{"volumes", "Volumes", "volume", "volumes"},
			{"networks", "Networks", "network", "networks"},
			{"buildCache", "Build cache", "entry", "entries"},
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
		l.add("Left for the next run", f["deferred"], true)
	case domain.NotifyUpdates:
		l.add("Target", f["target"], true)
		l.add("Updated", f["updatedServices"], false)
		l.add("Already up to date", f["unchangedServices"], false)
		l.add("Kept stopped", f["keptStoppedServices"], false)
		l.add("Failed", f["failedServices"], false)
	}
	l.add("Duration", durationWords(f["durationSeconds"]), true)
	l.add("Started by", originWords(f), true)
	if n.Outcome == domain.OutcomeFailure {
		class := f["errorClass"]
		if class == "" || class == "step_failed" {
			class = f["memberErrorClass"]
		}
		l.add("What to do", errorFix(class), false)
	}
	return l
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
