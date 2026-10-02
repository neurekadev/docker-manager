package domain

import (
	"errors"
	"slices"
	"strings"
	"time"
)

// Alerts (#159): problems Docker Manager raises by itself (a failing
// disk, a degraded RAID array, a host running hot, out of disk space or
// memory, an environment offline past its grace period, a failed
// scheduled job, available image updates), kept until
// they are resolved, shown in the app and sent through the notification
// channels subscribed to their kind (NotificationEventKind). One alert per
// problem: a dedupe key identifies it while it fires.

// AlertSeverity ranks an alert.
type AlertSeverity string

// Severities, in increasing order.
const (
	AlertInfo     AlertSeverity = "info"
	AlertWarning  AlertSeverity = "warning"
	AlertCritical AlertSeverity = "critical"
)

// Rank orders severities (info 1, warning 2, critical 3; unknown 0).
func (s AlertSeverity) Rank() int {
	switch s {
	case AlertInfo:
		return 1
	case AlertWarning:
		return 2
	case AlertCritical:
		return 3
	}
	return 0
}

// Valid reports whether s is a known severity.
func (s AlertSeverity) Valid() bool { return s.Rank() > 0 }

// AlertState is firing or resolved.
type AlertState string

// Alert states.
const (
	AlertFiring   AlertState = "firing"
	AlertResolved AlertState = "resolved"
)

// Resolutions say why an alert stopped firing. Only AlertResolvedFixed
// is announced to channels (when they send resolved problems); the others
// end silently.
const (
	// AlertResolvedFixed: the problem is gone (the disk is ok again, the
	// environment is back online, the next job succeeded, no update left).
	AlertResolvedFixed = "resolved"
	// AlertResolvedRemoved: the disk or array is no longer reported, or
	// the update policy was deleted.
	AlertResolvedRemoved = "removed"
	// AlertResolvedExpired: a failed job's alert saw no new run in
	// AlertJobExpiry.
	AlertResolvedExpired = "expired"
	// AlertResolvedArchived: the environment was archived.
	AlertResolvedArchived = "archived"
)

// Alert resource types (what an alert is about).
const (
	AlertResourceDisk         = "disk"
	AlertResourceRAID         = "raid_array"
	AlertResourceZFS          = "zfs_pool"
	AlertResourceEnvironment  = "environment"
	AlertResourceJob          = "job"
	AlertResourceUpdatePolicy = "update_policy"
	// AlertResourceFilesystem is a filesystem an agent reports usage of
	// (its ID: "docker", "stacks", "bind-1", ...).
	AlertResourceFilesystem = "filesystem"
)

// Outcome is the outcome a message about the alert has: a resolution, or
// the severity in the words of its kind (a failed job's critical is a
// failure; available updates are info).
func (a Alert) Outcome(event string) NotificationOutcome {
	if event == AlertEventResolved {
		return OutcomeResolved
	}
	switch {
	case a.Kind == NotifyUpdates:
		return OutcomeAvailable
	case a.Kind == NotifyJobFailed && a.Severity == AlertCritical:
		return OutcomeFailure
	case a.Severity == AlertCritical:
		return OutcomeCritical
	}
	return OutcomeWarning
}

// SentAs returns the kind and outcome of a message about event: the ones
// channels subscribe to (NotificationChannel.Wants). A failed job's alert
// is sent as the kind of its job's area (JobEventKind) when it has one:
// failed is a failure; partly failed or interrupted a warning where the
// area has warnings (backups), else a failure. Its resolution (the next
// run succeeded) has the outcome of the alert's severity; a told channel
// that does not send it gets the resolution with an outcome it was told
// (alerts.write) (the area has no "resolved"; the message still says it is resolved and
// looks like one, AlertDelivery.Tone). Any other alert: its kind and
// Outcome.
func (a Alert) SentAs(event string) (NotificationEventKind, NotificationOutcome) {
	area := JobEventKind(a.JobKind)
	if a.Kind != NotifyJobFailed || area == NotifyJobFailed {
		return a.Kind, a.Outcome(event)
	}
	if a.Severity == AlertWarning && slices.Contains(area.Outcomes(), OutcomeWarning) {
		return area, OutcomeWarning
	}
	return area, OutcomeFailure
}

// Alert is one problem (firing) or a past one (resolved).
type Alert struct {
	ID string
	// DedupeKey identifies the problem: at most one firing alert per key.
	DedupeKey string
	Kind      NotificationEventKind
	Severity  AlertSeverity
	State     AlertState
	// EnvironmentID is the environment the problem is in ("" for none).
	EnvironmentID string
	// ResourceType (AlertResource*) and ResourceID name what it is about:
	// a disk (its path and smartctl type), an array or pool (its name),
	// the environment, the last failed job, the update policy.
	ResourceType string
	ResourceID   string
	// JobKind and Targets are the failed job's kind and targets (job
	// alerts) or the update policy's target (update alerts): they decide
	// who sees the alert (#17), like the job or policy does.
	JobKind JobKind
	Targets []JobTarget
	// Title is one line in words ("Disk /dev/sda on homelab is failing").
	Title string
	// Facts are small, non-secret values about the problem (a disk's
	// model and counters, an array's level and progress, a job's kind);
	// never serial numbers, error texts or secrets.
	Facts map[string]string
	// Fingerprint is the set of problem tokens (sorted, comma separated):
	// a token that was not there before makes the alert worse. Tokens mark
	// problems that can only be added (a failing attribute, a failed
	// member, "at least 2 disks missing", an image digest), never states
	// that replace each other: an improvement must not add a token.
	Fingerprint string
	// Escalation counts how often the alert got worse (a higher severity
	// or a new token); it keys dismissals kept by a browser.
	Escalation int
	// StartedAt is when it started firing; UpdatedAt its last change;
	// LastSeenAt when the problem was last observed (disks, arrays and
	// environments: the last evaluation; jobs: the last failed run).
	StartedAt  time.Time
	UpdatedAt  time.Time
	LastSeenAt time.Time
	ResolvedAt *time.Time
	// Resolution is one of AlertResolved* once resolved.
	Resolution string
	// DismissedAt, DismissedBy (user ID) and DismissedByName record who
	// dismissed the alert for everyone; it re-opens when it gets worse.
	DismissedAt     *time.Time
	DismissedBy     string
	DismissedByName string
	Revision        int64
}

// Dismissed reports whether the alert is dismissed.
func (a Alert) Dismissed() bool { return a.DismissedAt != nil }

// Active reports whether the alert fires and is not dismissed (the bell,
// "Needs attention", environment notices).
func (a Alert) Active() bool { return a.State == AlertFiring && a.DismissedAt == nil }

// Tokens returns the fingerprint's problem tokens.
func (a Alert) Tokens() []string { return FingerprintTokens(a.Fingerprint) }

// Fingerprint builds a fingerprint from problem tokens (sorted, without
// duplicates or empty tokens).
func Fingerprint(tokens ...string) string {
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(strings.ReplaceAll(t, ",", ";"))
		if t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return strings.Join(out, ",")
}

// FingerprintTokens splits a fingerprint into its tokens.
func FingerprintTokens(fp string) []string {
	if fp == "" {
		return nil
	}
	return strings.Split(fp, ",")
}

// NewTokens reports whether fingerprint next holds a token prev lacks
// (the problem got worse: a new failing attribute, a new failed member, a
// new image digest).
func NewTokens(prev, next string) bool {
	old := FingerprintTokens(prev)
	for _, t := range FingerprintTokens(next) {
		if !slices.Contains(old, t) {
			return true
		}
	}
	return false
}

// AlertFilter selects alerts.
type AlertFilter struct {
	// State: "active" (firing, not dismissed), "dismissed" (firing,
	// dismissed), "firing" (both) or "resolved"; "" for every alert.
	State         string
	Kind          NotificationEventKind
	EnvironmentID string
}

// Alert list states.
const (
	AlertListActive    = "active"
	AlertListDismissed = "dismissed"
	AlertListFiring    = "firing"
	AlertListResolved  = "resolved"
)

// Alert errors.
var (
	ErrAlertNotFound = errors.New("alert not found")
	// ErrAlertNotFiring: only firing alerts can be dismissed.
	ErrAlertNotFiring = errors.New("the alert is no longer firing")
)

// AlertDelivery is one message of an alert or a notification to one
// channel (the outbox).
type AlertDelivery struct {
	ID string
	// AlertID or NotificationID is what the message is about (the other
	// is empty).
	AlertID        string
	NotificationID string
	ChannelID      string
	// Event is AlertEventFiring, AlertEventWorse, AlertEventResolved or
	// DeliveryEventNotification.
	Event string
	// Kind, EnvironmentID, Severity, Outcome, Title, Body, Fields and Link
	// are the alert or notification as it was when the message was
	// written: a delayed or retried message says what happened then, not
	// what the alert says later.
	Kind          NotificationEventKind
	EnvironmentID string
	Severity      AlertSeverity
	Outcome       NotificationOutcome
	Title         string
	Body          string
	Fields        []NotificationField
	Link          string
	// State is pending, sent, failed (gave up) or dropped (the channel was
	// deleted, turned off or no longer subscribed).
	State         string
	Attempts      int
	NextAttemptAt time.Time
	// LastError is the last send's error class (domain.NotifyErr*).
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
	SentAt    *time.Time
}

// Delivery events.
const (
	AlertEventFiring   = "firing"
	AlertEventWorse    = "worse"
	AlertEventResolved = "resolved"
	// DeliveryEventNotification is the message of a notification.
	DeliveryEventNotification = "notification"
)

// Tone is how the message looks: red for critical problems and failures,
// amber for warnings, green for resolved problems and successes, blue
// for news (updates available). A resolution is green whatever outcome
// it was sent with (a failed job's, Alert.SentAs).
func (d AlertDelivery) Tone() NotificationTone {
	if d.Event == AlertEventResolved {
		return ToneSuccess
	}
	switch d.Outcome {
	case OutcomeResolved, OutcomeSuccess:
		return ToneSuccess
	case OutcomeCritical, OutcomeFailure:
		return ToneCritical
	case OutcomeWarning:
		return ToneWarning
	case OutcomeAvailable:
		return ToneInfo
	}
	switch d.Severity {
	case AlertCritical:
		return ToneCritical
	case AlertWarning:
		return ToneWarning
	}
	return ToneInfo
}

// Delivery states.
const (
	DeliveryPending = "pending"
	DeliverySent    = "sent"
	DeliveryFailed  = "failed"
	DeliveryDropped = "dropped"
)
