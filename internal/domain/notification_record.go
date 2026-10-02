package domain

import (
	"errors"
	"time"
)

// Notifications: runs that finished (a backup, a restore, a prune, an
// update run), recorded once with how they went (success, warning,
// failure) and sent through the channels subscribed to that outcome of
// their kind. Unlike alerts they do not fire or resolve: they are a
// history. Every run is recorded, whoever started it.

// Notification is one finished run.
type Notification struct {
	ID      string
	Kind    NotificationEventKind
	Outcome NotificationOutcome
	// EnvironmentID is the environment the run was on ("" for the
	// manager's own backup).
	EnvironmentID string
	// JobID, JobKind, Targets and Origin are the run's job: who may see
	// the notification is decided like for the job (#17).
	JobID   string
	JobKind JobKind
	Targets []JobTarget
	Origin  JobOrigin
	// Title is one line in words ("Daily Backups succeeded").
	Title string
	// Facts are small, non-secret values about the run (counts, sizes,
	// names, durations, error classes); never error texts, paths of
	// files or secrets.
	Facts     map[string]string
	CreatedAt time.Time
}

// Severity is the severity of a notification's message.
func (n Notification) Severity() AlertSeverity {
	switch n.Outcome {
	case OutcomeFailure:
		return AlertCritical
	case OutcomeWarning:
		return AlertWarning
	}
	return AlertInfo
}

// NotificationKindOfJob returns the kind of notification a finished job
// of kind k records ("" for none): backups, restores, prunes, update
// runs.
func NotificationKindOfJob(k JobKind) NotificationEventKind {
	switch k {
	case "backup.run", "manager.backup":
		return NotifyBackup
	case "restore.run":
		return NotifyRestore
	case "prune.run":
		return NotifyPrune
	case "update.run":
		return NotifyUpdates
	}
	return ""
}

// JobEventKind returns the event kind a failed job of kind k is sent as:
// the area it belongs to (backups: runs, retention, verification and
// imports, of backup policies and of Docker Manager's own backup;
// restores; prune; image updates: checks and runs), else job_failed
// (other jobs).
func JobEventKind(k JobKind) NotificationEventKind {
	if n := NotificationKindOfJob(k); n != "" {
		return n
	}
	switch k {
	case "backup.retention", "backup.verify", "backup.import", "manager.retention", "manager.verify":
		return NotifyBackup
	case "update.check":
		return NotifyUpdates
	}
	return NotifyJobFailed
}

// NotificationFilter selects notifications.
type NotificationFilter struct {
	Kind          NotificationEventKind
	Outcome       NotificationOutcome
	EnvironmentID string
}

// ErrNotificationNotFound is returned when no notification has the ID.
var ErrNotificationNotFound = errors.New("notification not found")
