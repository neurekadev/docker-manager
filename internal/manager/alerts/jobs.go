package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Failed job alerts: a scheduled job, or one an API token started, that
// ends failed, partly failed or interrupted raises an alert (manual jobs
// stay the starting user's browser notices). One alert per policy, job
// kind and first target (an environment policy's run has one per
// target), or per kind, environment and first target without a policy.
// The key's next successful job resolves it (whoever started it); without
// a new run it expires after JobExpiry (no message). A job's error text
// never reaches an alert: only its state and error class. The alert's
// kind stays job_failed (the Alerts tab's "Failed Job"); its messages go
// out as its job's area (domain.JobEventKind, domain.Alert.SentAs): a
// failed backup verification as Backups, an update check as Image
// Updates.

// jobKey is the dedupe key of a job's alert.
func jobKey(j domain.Job) string {
	target := ""
	if len(j.Targets) > 0 {
		target = string(j.Targets[0].Type) + ":" + j.Targets[0].ID
	}
	if j.PolicyID != "" {
		return "job_failed/policy/" + j.PolicyID + "/" + string(j.Kind) + "/" + target
	}
	return "job_failed/job/" + string(j.Kind) + "/" + j.EnvironmentID + "/" + target
}

// kindNouns names what a job does, for alert titles (kindLabel for the
// Job field).
var kindNouns = map[domain.JobKind]string{
	"backup.run": "Backup", "backup.retention": "Backup retention", "backup.verify": "Backup verification",
	"backup.import": "Backup import", "manager.backup": "Docker Manager backup",
	"manager.retention": "Docker Manager backup retention", "manager.verify": "Docker Manager backup verification",
	"restore.run": "Restore", "update.check": "Update check", "update.run": "Update", "prune.run": "Prune",
	"stack.deploy": "Deploy", "stack.start": "Start", "stack.stop": "Stop", "stack.restart": "Restart", "stack.down": "Take down",
	"stack.pull": "Image pull", "stack.update": "Image update", "stack.build": "Build", "stack.remove": "Delete",
	"stack.migrate": "Migration", "environment.migrate": "Migration", "volume.migrate": "Migration", "image.pull": "Image pull",
	"image.build": "Image build", "image.remove": "Image removal", "container.start": "Start", "container.stop": "Stop",
	"container.restart": "Restart", "container.remove": "Removal", "container.create": "Container creation",
	"volume.create": "Volume creation", "volume.remove": "Volume removal", "network.create": "Network creation",
	"network.remove": "Network removal",
}

func kindNoun(k domain.JobKind) string {
	if n, ok := kindNouns[k]; ok {
		return n
	}
	return "A job"
}

// kindLabel names what a job does as a label (the Job field): kindNoun in
// Title Case ("Backup Retention", "Take Down").
func kindLabel(k domain.JobKind) string { return titleCase(kindNoun(k)) }

// minorWords stay lowercase inside a Title Case label.
var minorWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "but": true, "or": true, "nor": true,
	"as": true, "at": true, "by": true, "for": true, "in": true, "of": true, "on": true,
	"per": true, "to": true, "via": true, "with": true,
}

// titleCase capitalizes each word of a label but the minor ones inside it,
// keeping the rest of each word as it is (acronyms, "Docker Manager").
func titleCase(s string) string {
	words := strings.Split(s, " ")
	for i, w := range words {
		if w == "" || (i > 0 && i < len(words)-1 && minorWords[w]) {
			continue
		}
		parts := strings.Split(w, "-")
		for j, p := range parts {
			if p != "" {
				parts[j] = strings.ToUpper(p[:1]) + p[1:]
			}
		}
		words[i] = strings.Join(parts, "-")
	}
	return strings.Join(words, " ")
}

// targetName names a job's first target as users know it (a stack's
// display name, a repository's name; Docker object names as they are);
// "" when it has none worth naming.
func targetName(ctx context.Context, db bun.IDB, j domain.Job) string {
	if len(j.Targets) == 0 {
		return ""
	}
	t := j.Targets[0]
	name := ""
	switch t.Type {
	case domain.TargetStack:
		if st, err := store.GetStack(ctx, db, t.ID); err == nil {
			name = stackLabel(st)
		}
	case domain.TargetRepository:
		if r, err := store.GetBackupRepository(ctx, db, t.ID); err == nil {
			name = r.Name
		}
	case domain.TargetContainer, domain.TargetVolume, domain.TargetNetwork, domain.TargetImage:
		name = t.ID
	}
	if name == "" {
		return ""
	}
	more := 0
	for _, o := range j.Targets[1:] {
		if o.Type != domain.TargetPath && o.Type != domain.TargetDestinationPath && o.Type != domain.TargetRepository {
			more++
		}
	}
	if more > 0 {
		return fmt.Sprintf("%s and %d more", name, more)
	}
	return name
}

func environmentName(ctx context.Context, db bun.IDB, id string) string {
	if id == "" {
		return ""
	}
	if e, err := store.GetEnvironment(ctx, db, id); err == nil {
		return e.Name
	}
	return ""
}

// jobObservation describes a failed job (never its error message): "<what
// it does> of <target> failed".
func jobObservation(ctx context.Context, db bun.IDB, j domain.Job) Observation {
	sev, verb := domain.AlertCritical, "failed"
	switch j.State {
	case domain.JobPartial:
		sev, verb = domain.AlertWarning, "partly failed"
	case domain.JobInterrupted:
		sev, verb = domain.AlertWarning, "was interrupted"
	}
	var b strings.Builder
	b.WriteString(kindNoun(j.Kind))
	target := targetName(ctx, db, j)
	if target != "" {
		b.WriteString(" of " + target)
	}
	b.WriteString(" " + verb)
	facts := map[string]string{"jobId": j.ID, "jobKind": string(j.Kind), "jobState": string(j.State), "origin": string(j.Origin)}
	if j.ErrorClass != "" {
		facts["errorClass"] = j.ErrorClass
	}
	if j.PolicyID != "" {
		facts["policyId"] = j.PolicyID
	}
	if target != "" {
		facts["target"] = target
	}
	if j.Kind == "update.check" {
		checkFailures(ctx, db, j, facts)
	}
	return Observation{
		Key: jobKey(j), Kind: domain.NotifyJobFailed, Severity: sev, EnvironmentID: j.EnvironmentID,
		ResourceType: domain.AlertResourceJob, ResourceID: j.ID, JobKind: j.Kind, Targets: j.Targets, Title: b.String(),
		// The state replaces itself: the severity says whether it got worse.
		Facts: facts, Fingerprint: domain.Fingerprint("failed"),
	}
}

// checkFailures adds the services an update check could not check and
// the first one's error class (a registry's answer in words, never its
// message) to a failed check's facts.
func checkFailures(ctx context.Context, db bun.IDB, j domain.Job, facts map[string]string) {
	var in checkInput
	if err := json.Unmarshal(j.Input, &in); err != nil || in.PolicyID == "" {
		return
	}
	cands, err := store.UpdateCandidates(ctx, db, in.PolicyID)
	if err != nil {
		return
	}
	var failed []string
	for _, c := range cands {
		if c.Status != domain.CandidateCheckFailed {
			continue
		}
		failed = append(failed, c.Service)
		if facts["itemErrorClass"] == "" && c.ErrorClass != "" {
			facts["itemErrorClass"] = c.ErrorClass
		}
	}
	if len(failed) > 0 {
		facts["failedItems"] = listNames(failed)
	}
}

// inHook runs fn inside a job's finishing transaction, in a savepoint: a
// failure rolls back the alert's writes only (the job's outcome matters
// more than its alert; it is logged). The changed alert is announced once
// the job's transaction committed.
func (s *Service) inHook(ctx context.Context, db bun.IDB, j domain.Job, fn func(ctx context.Context, tx bun.Tx) (*domain.Alert, error)) {
	var changed *domain.Alert
	err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var err error
		changed, err = fn(ctx, tx)
		return err
	})
	if err != nil {
		s.log.Warn("could not record an alert of a finished job", "job_id", j.ID, "job_kind", string(j.Kind), "error", err)
		return
	}
	if changed != nil {
		s.hold(j.ID, []domain.Alert{*changed})
	}
}

// onJobFinished is the finish hook of every job kind, in the job's
// finishing transaction.
func (s *Service) onJobFinished(ctx context.Context, db bun.IDB, j domain.Job) error {
	now := s.now()
	switch j.State {
	case domain.JobSucceeded:
		s.inHook(ctx, db, j, func(ctx context.Context, tx bun.Tx) (*domain.Alert, error) {
			return resolveKey(ctx, tx, jobKey(j), domain.AlertResolvedFixed, now)
		})
	case domain.JobFailed, domain.JobPartial, domain.JobInterrupted:
		if j.Origin != domain.OriginScheduled && j.Origin != domain.OriginAPIToken {
			return nil
		}
		s.inHook(ctx, db, j, func(ctx context.Context, tx bun.Tx) (*domain.Alert, error) {
			return raise(ctx, tx, jobObservation(ctx, tx, j), now)
		})
	}
	return nil
}

// ExpireJobs ends failed job alerts without a new run for JobExpiry.
func (s *Service) ExpireJobs(ctx context.Context) error {
	now := s.now()
	return s.inTx(ctx, func(ctx context.Context, tx bun.Tx) ([]domain.Alert, error) {
		as, err := store.FiringAlerts(ctx, tx, domain.NotifyJobFailed, "")
		if err != nil {
			return nil, err
		}
		var out []domain.Alert
		for _, a := range as {
			if now.Sub(a.LastSeenAt) < JobExpiry {
				continue
			}
			if out, err = collect(out)(resolve(ctx, tx, a, domain.AlertResolvedExpired, now)); err != nil {
				return nil, err
			}
		}
		return out, nil
	})
}

// Update alerts: after an update check, the policy's candidates with an
// update available (the UI's "available") raise one info alert per
// target record; it is sent again only when a new digest appears, and
// resolves when none is left (after an update, or a check that finds
// none). A deleted or excluded policy's alert ends silently. It links to
// the environment policy that manages the target (putUpdatePolicy).

func updatesKey(policyID string) string { return "updates_available/" + policyID }

// checkInput is the policy of an update.check job (updates.CheckInput).
type checkInput struct {
	PolicyID string `json:"policyId"`
}

// onUpdateCheckFinished is the finish hook of update.check.
func (s *Service) onUpdateCheckFinished(ctx context.Context, db bun.IDB, j domain.Job) error {
	if j.State != domain.JobSucceeded && j.State != domain.JobPartial {
		return nil
	}
	var in checkInput
	if err := json.Unmarshal(j.Input, &in); err != nil || in.PolicyID == "" {
		return nil
	}
	now := s.now()
	s.inHook(ctx, db, j, func(ctx context.Context, tx bun.Tx) (*domain.Alert, error) {
		return evaluateUpdates(ctx, tx, in.PolicyID, now)
	})
	return nil
}

// evaluateUpdates raises, updates or resolves a policy's update alert
// from its stored candidates.
func evaluateUpdates(ctx context.Context, db bun.IDB, policyID string, now time.Time) (*domain.Alert, error) {
	key := updatesKey(policyID)
	p, err := store.GetUpdatePolicy(ctx, db, policyID)
	if errors.Is(err, domain.ErrUpdatePolicyNotFound) {
		return resolveKey(ctx, db, key, domain.AlertResolvedRemoved, now)
	}
	if err != nil {
		return nil, err
	}
	if p.Inactive {
		return resolveKey(ctx, db, key, domain.AlertResolvedRemoved, now)
	}
	cands, err := store.UpdateCandidates(ctx, db, policyID)
	if err != nil {
		return nil, err
	}
	var services, tokens []string
	var changes []serviceChange
	for _, c := range cands {
		if c.Status != domain.CandidateAvailable {
			continue
		}
		services = append(services, c.Service)
		changes = append(changes, serviceChange{c.Service, shortDigest(c.AppliedDigest), shortDigest(c.CandidateDigest)})
		tokens = append(tokens, c.Service+"@"+c.CandidateDigest)
	}
	if len(services) == 0 {
		return resolveKey(ctx, db, key, domain.AlertResolvedFixed, now)
	}
	target := domain.JobTarget{Type: domain.TargetStack, ID: p.TargetID}
	name := p.TargetID
	if p.TargetType == domain.UpdateTargetContainer {
		target.Type = domain.TargetContainer
	} else if st, err := store.GetStack(ctx, db, p.TargetID); err == nil {
		name = stackLabel(st)
	} else {
		name = p.Name
	}
	title := fmt.Sprintf("%s has %d updates available", name, len(services))
	if len(services) == 1 {
		title = name + " has an update available"
	}
	listed := services
	if len(listed) > maxListed {
		listed, changes = listed[:maxListed], changes[:maxListed]
	}
	facts := map[string]string{"count": fmt.Sprint(len(services)), "services": strings.Join(listed, ", "), "target": name,
		"changes": encodeChanges(changes)}
	putUpdatePolicy(ctx, db, p, facts)
	return raise(ctx, db, Observation{
		Key: key, Kind: domain.NotifyUpdates, Severity: domain.AlertInfo, EnvironmentID: p.EnvironmentID,
		ResourceType: domain.AlertResourceUpdatePolicy, ResourceID: p.ID, Targets: []domain.JobTarget{target}, Title: title,
		Facts: facts, Fingerprint: domain.Fingerprint(tokens...),
	}, now)
}

// putUpdatePolicy records the policy that manages target record p in the
// facts: the environment policy above it (policy and policyId, its page);
// the record's own name, without a page, for a policy from before
// environment policies. The record itself has no page of its own.
func putUpdatePolicy(ctx context.Context, db bun.IDB, p domain.UpdatePolicy, f map[string]string) {
	if p.ParentID == "" {
		f["policy"] = p.Name
		return
	}
	if ep, err := store.GetEnvironmentUpdatePolicy(ctx, db, p.ParentID); err == nil {
		f["policy"], f["policyId"] = ep.Name, ep.ID
	}
}

// ReconcileUpdates evaluates every firing update alert again (updates
// applied since, policies deleted).
func (s *Service) ReconcileUpdates(ctx context.Context) error {
	now := s.now()
	return s.inTx(ctx, func(ctx context.Context, tx bun.Tx) ([]domain.Alert, error) {
		as, err := store.FiringAlerts(ctx, tx, domain.NotifyUpdates, "")
		if err != nil {
			return nil, err
		}
		var out []domain.Alert
		for _, a := range as {
			if out, err = collect(out)(evaluateUpdates(ctx, tx, a.ResourceID, now)); err != nil {
				return nil, err
			}
		}
		return out, nil
	})
}
