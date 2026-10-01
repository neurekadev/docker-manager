package domain

import (
	"errors"
	"slices"
	"time"
)

// Notification channels (#142): outgoing destinations (Discord, Slack,
// email, ntfy, a generic webhook, ... through Shoutrrr) the owner
// configures once. The channel's address (a Shoutrrr URL) holds its
// credentials (webhook tokens, passwords): it is sealed at rest, never
// logged, audited or returned by list and get, and only the owner can
// reveal it again. Each channel also stores what it is subscribed to
// (the outcomes of each event kind, and environments).

// NotificationEventKind is a kind of event a channel can be subscribed to:
// an alert kind (a problem that fires and resolves) or a notification
// kind (a run that finished: a backup or restore, a prune, an update).
// updates is both: its alerts say updates are available, its
// notifications that an update run finished.
type NotificationEventKind string

// Event kinds.
const (
	NotifyDiskHealth         NotificationEventKind = "disk_health"
	NotifyRAID               NotificationEventKind = "raid"
	NotifyTemperature        NotificationEventKind = "temperature"
	NotifyDiskSpace          NotificationEventKind = "disk_space"
	NotifyMemory             NotificationEventKind = "memory"
	NotifyEnvironmentOffline NotificationEventKind = "environment_offline"
	NotifyBackup             NotificationEventKind = "backup"
	NotifyPrune              NotificationEventKind = "prune"
	NotifyUpdates            NotificationEventKind = "updates"
	NotifyJobFailed          NotificationEventKind = "job_failed"
)

// NotificationEventKinds returns every event kind in display order.
func NotificationEventKinds() []NotificationEventKind {
	return []NotificationEventKind{NotifyDiskHealth, NotifyRAID, NotifyTemperature, NotifyDiskSpace, NotifyMemory,
		NotifyEnvironmentOffline, NotifyBackup, NotifyPrune, NotifyUpdates, NotifyJobFailed}
}

// AlertKinds returns the kinds alerts are raised with, in display order.
func AlertKinds() []NotificationEventKind {
	return []NotificationEventKind{NotifyDiskHealth, NotifyRAID, NotifyTemperature, NotifyDiskSpace, NotifyMemory,
		NotifyEnvironmentOffline, NotifyUpdates, NotifyJobFailed}
}

// NotificationKinds returns the kinds of notifications (finished runs),
// in display order.
func NotificationKinds() []NotificationEventKind {
	return []NotificationEventKind{NotifyBackup, NotifyPrune, NotifyUpdates}
}

// Valid reports whether k is a known event kind.
func (k NotificationEventKind) Valid() bool { return slices.Contains(NotificationEventKinds(), k) }

// NotificationOutcome is what a message says about its event: an alert's
// severity (warning, critical; available for updates) or its resolution,
// or how a run finished (success, warning, failure). A channel subscribes
// to outcomes per kind.
type NotificationOutcome string

// Outcomes.
const (
	OutcomeWarning   NotificationOutcome = "warning"
	OutcomeCritical  NotificationOutcome = "critical"
	OutcomeResolved  NotificationOutcome = "resolved"
	OutcomeAvailable NotificationOutcome = "available"
	OutcomeFailure   NotificationOutcome = "failure"
	OutcomeSuccess   NotificationOutcome = "success"
)

// NotificationOutcomeValues returns every outcome.
func NotificationOutcomeValues() []NotificationOutcome {
	return []NotificationOutcome{OutcomeWarning, OutcomeCritical, OutcomeResolved, OutcomeAvailable, OutcomeFailure, OutcomeSuccess}
}

// Outcomes returns the outcomes a channel can subscribe to for kind k, in
// display order:
//
//   - disks, RAID, temperature, disk space, memory: warning, critical,
//     resolved;
//   - environment offline: critical (offline), resolved (back online);
//   - backups and restores: failure, warning, success;
//   - prune: failure, success;
//   - updates: available (a check found newer images), failure, success
//     (an update run applied them);
//   - other failed jobs: failure, warning (partly failed or interrupted),
//     resolved (the next run succeeded).
func (k NotificationEventKind) Outcomes() []NotificationOutcome {
	switch k {
	case NotifyDiskHealth, NotifyRAID, NotifyTemperature, NotifyDiskSpace, NotifyMemory:
		return []NotificationOutcome{OutcomeWarning, OutcomeCritical, OutcomeResolved}
	case NotifyEnvironmentOffline:
		return []NotificationOutcome{OutcomeCritical, OutcomeResolved}
	case NotifyBackup:
		return []NotificationOutcome{OutcomeFailure, OutcomeWarning, OutcomeSuccess}
	case NotifyPrune:
		return []NotificationOutcome{OutcomeFailure, OutcomeSuccess}
	case NotifyUpdates:
		return []NotificationOutcome{OutcomeAvailable, OutcomeFailure, OutcomeSuccess}
	case NotifyJobFailed:
		return []NotificationOutcome{OutcomeFailure, OutcomeWarning, OutcomeResolved}
	}
	return nil
}

// NotificationSubscriptions are the outcomes a channel sends, per event
// kind (kinds without outcomes are absent).
type NotificationSubscriptions map[NotificationEventKind][]NotificationOutcome

// AllNotificationSubscriptions subscribes to every outcome of every kind
// (a new channel's default).
func AllNotificationSubscriptions() NotificationSubscriptions {
	out := NotificationSubscriptions{}
	for _, k := range NotificationEventKinds() {
		out[k] = k.Outcomes()
	}
	return out
}

// Wants reports whether the subscriptions include outcome o of kind k.
func (s NotificationSubscriptions) Wants(k NotificationEventKind, o NotificationOutcome) bool {
	return slices.Contains(s[k], o)
}

// Kinds returns the subscribed kinds in display order.
func (s NotificationSubscriptions) Kinds() []NotificationEventKind {
	var out []NotificationEventKind
	for _, k := range NotificationEventKinds() {
		if len(s[k]) > 0 {
			out = append(out, k)
		}
	}
	return out
}

// Normalize returns the subscriptions with every kind's outcomes in
// display order, without duplicates, unknown values or empty kinds.
func (s NotificationSubscriptions) Normalize() NotificationSubscriptions {
	out := NotificationSubscriptions{}
	for _, k := range NotificationEventKinds() {
		var os []NotificationOutcome
		for _, o := range k.Outcomes() {
			if slices.Contains(s[k], o) {
				os = append(os, o)
			}
		}
		if len(os) > 0 {
			out[k] = os
		}
	}
	return out
}

// Equal reports whether s and o subscribe to the same outcomes.
func (s NotificationSubscriptions) Equal(o NotificationSubscriptions) bool {
	a, b := s.Normalize(), o.Normalize()
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}
	return true
}

// NotificationResultOK is the LastResult of a successful send; other
// non-empty values are send error classes (NotificationErrorClasses).
const NotificationResultOK = "ok"

// Send error classes: stable, never the service's own error text (which
// can contain the address).
const (
	NotifyErrDNS        = "dns"
	NotifyErrConnect    = "connect"
	NotifyErrTLS        = "tls"
	NotifyErrTimeout    = "timeout"
	NotifyErrAuth       = "auth"
	NotifyErrHTTP4xx    = "http_4xx"
	NotifyErrHTTP5xx    = "http_5xx"
	NotifyErrRedirect   = "redirect"
	NotifyErrRejected   = "rejected"
	NotifyErrInvalidURL = "invalid_url"
)

// NotificationErrorClasses returns every send error class.
func NotificationErrorClasses() []string {
	return []string{NotifyErrDNS, NotifyErrConnect, NotifyErrTLS, NotifyErrTimeout, NotifyErrAuth, NotifyErrHTTP4xx,
		NotifyErrHTTP5xx, NotifyErrRedirect, NotifyErrRejected, NotifyErrInvalidURL}
}

// NotificationChannel is one stored channel (never its address).
type NotificationChannel struct {
	ID   string
	Name string
	// Service is the Shoutrrr service of the address ("discord", "smtp",
	// "generic", ...).
	Service string
	// Target is a non-secret hint of where messages go (a mail or push
	// server's host), empty when every part of the address is secret.
	Target  string
	Enabled bool
	// Subscriptions are the outcomes the channel sends per event kind
	// (at least one).
	Subscriptions NotificationSubscriptions
	// AllEnvironments sends events of every environment, including future
	// ones (EnvironmentIDs is then empty). Otherwise only events of
	// EnvironmentIDs are sent; an empty list (its environments were all
	// removed) sends none: a filter never widens to every environment.
	AllEnvironments bool
	EnvironmentIDs  []string
	// AddressFingerprint is a keyed fingerprint of the address (changes
	// with it, never reveals it); AddressVersion increases with every
	// address change.
	AddressFingerprint string
	AddressVersion     int
	AddressUpdatedAt   time.Time
	// LastResult is "" (never sent), NotificationResultOK or an error class.
	LastResult    string
	LastAttemptAt *time.Time
	LastSuccessAt *time.Time
	Revision      int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Wants reports whether the channel sends outcome o of events of kind
// about the environment (an empty environmentID: an event of no
// environment): it is enabled, subscribed to the outcome, and sends every
// environment's events or lists this one.
func (c NotificationChannel) Wants(kind NotificationEventKind, o NotificationOutcome, environmentID string) bool {
	if !c.Enabled || !c.Subscriptions.Wants(kind, o) {
		return false
	}
	return c.AllEnvironments || environmentID == "" || slices.Contains(c.EnvironmentIDs, environmentID)
}

// NotificationChannelInput creates a channel.
type NotificationChannelInput struct {
	Name string
	// Address is the Shoutrrr URL (a secret).
	Address string
	Enabled bool
	// Subscriptions nil means every outcome of every kind.
	Subscriptions NotificationSubscriptions
	// AllEnvironments or at least one of EnvironmentIDs (an explicit
	// choice, never inferred from an empty list).
	AllEnvironments bool
	EnvironmentIDs  []string
}

// NotificationChannelPatch edits a channel; nil fields stay unchanged.
type NotificationChannelPatch struct {
	Name          *string
	Enabled       *bool
	Subscriptions *NotificationSubscriptions
	// AllEnvironments true clears EnvironmentIDs; false needs at least one
	// environment (given, or already in the filter).
	AllEnvironments *bool
	EnvironmentIDs  *[]string
	// Address replaces the address (re-sealed, version bumped).
	Address *string
}

// NotificationTone colors a message: the strip of a Discord embed or a
// Slack attachment, an email's accent, a push message's priority.
type NotificationTone string

// Tones.
const (
	ToneCritical NotificationTone = "critical"
	ToneWarning  NotificationTone = "warning"
	ToneSuccess  NotificationTone = "success"
	ToneInfo     NotificationTone = "info"
)

// NotificationField is one labelled value of a message (an embed field;
// a line "Name: value" where a service has no fields). Inline fields sit
// side by side where the service can.
type NotificationField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

// NotificationMessage is one message sent through a channel. Services
// render what they can: Discord an embed (tone color, fields, link,
// footer, time), Slack colored attachments, email HTML, push services a
// title, priority and link, the rest plain text.
type NotificationMessage struct {
	Title string
	Body  string
	// URL links to the page in Docker Manager the message is about
	// (optional).
	URL    string
	Tone   NotificationTone
	Fields []NotificationField
	// Footer is a short line under the message (the instance name).
	Footer string
	// Time is when the event happened (zero: not shown).
	Time time.Time
}

// Notification channel errors.
var (
	ErrNotificationChannelNotFound  = errors.New("notification channel not found")
	ErrNotificationChannelNameTaken = errors.New("notification channel name taken")
)
