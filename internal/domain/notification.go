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
// kind (a run that finished: a backup, a restore, a prune, an update).
// updates is both: its alerts say updates are available, its
// notifications that an update run finished. A failed scheduled or API
// token job is sent as the kind of its area (JobEventKind: backups,
// restores, prune, image updates); job_failed holds the others.
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
	NotifyRestore            NotificationEventKind = "restore"
	NotifyPrune              NotificationEventKind = "prune"
	NotifyUpdates            NotificationEventKind = "updates"
	NotifyJobFailed          NotificationEventKind = "job_failed"
)

// NotificationEventKinds returns every event kind in display order.
func NotificationEventKinds() []NotificationEventKind {
	return []NotificationEventKind{NotifyDiskHealth, NotifyRAID, NotifyTemperature, NotifyDiskSpace, NotifyMemory,
		NotifyEnvironmentOffline, NotifyBackup, NotifyRestore, NotifyPrune, NotifyUpdates, NotifyJobFailed}
}

// AlertKinds returns the kinds alerts are raised with, in display order
// (backup: backups paused without a Primary repository, #246).
func AlertKinds() []NotificationEventKind {
	return []NotificationEventKind{NotifyDiskHealth, NotifyRAID, NotifyTemperature, NotifyDiskSpace, NotifyMemory,
		NotifyEnvironmentOffline, NotifyBackup, NotifyUpdates, NotifyJobFailed}
}

// NotificationKinds returns the kinds of notifications (finished runs),
// in display order.
func NotificationKinds() []NotificationEventKind {
	return []NotificationEventKind{NotifyBackup, NotifyRestore, NotifyPrune, NotifyUpdates}
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
//   - backups: failure, warning, success;
//   - restores, prune: failure, success;
//   - updates: available (a check found newer images), failure, success
//     (an update run applied them);
//   - other jobs: failure, warning (partly failed or interrupted),
//     resolved (the next run succeeded).
//
// A failed job of an area is sent with its area's outcomes
// (Alert.SentAs).
func (k NotificationEventKind) Outcomes() []NotificationOutcome {
	switch k {
	case NotifyDiskHealth, NotifyRAID, NotifyTemperature, NotifyDiskSpace, NotifyMemory:
		return []NotificationOutcome{OutcomeWarning, OutcomeCritical, OutcomeResolved}
	case NotifyEnvironmentOffline:
		return []NotificationOutcome{OutcomeCritical, OutcomeResolved}
	case NotifyBackup:
		return []NotificationOutcome{OutcomeFailure, OutcomeWarning, OutcomeSuccess}
	case NotifyRestore, NotifyPrune:
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

// NotificationField is one labeled value of a message (an embed field;
// a line "Name: value" where a service has no fields). Inline fields sit
// side by side where the service can.
type NotificationField struct {
	Name string `json:"name"`
	// Value is the field as plain text (with Items: one entry per line).
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
	// Link is the page Value names: a path in Docker Manager in a
	// snapshot, an absolute URL in a message (none without a public URL).
	Link string `json:"link,omitempty"`
	// Items make the field a list (each entry linked and with its change
	// where the service can show it).
	Items []NotificationItem `json:"items,omitempty"`
}

// NotificationItem is one entry of a field's list: a name, the page it
// links to (like NotificationField.Link) and an optional change shown as
// code, "From → To" (image digests).
type NotificationItem struct {
	Text string `json:"text"`
	Link string `json:"link,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// Plain is the entry as plain text ("web: 1a2b → 3c4d").
func (it NotificationItem) Plain() string {
	switch {
	case it.From != "" && it.To != "":
		return it.Text + ": " + it.From + " → " + it.To
	case it.To != "":
		return it.Text + ": " + it.To
	}
	return it.Text
}

// NotificationMessage is one message sent through a channel. Services
// render what they can: Discord an embed (tone color, status line,
// fields, links, footer, time), Slack colored attachments, email HTML,
// push services a title, priority and link, the rest plain text.
type NotificationMessage struct {
	// Label is the short status line above the title: the event's kind
	// and outcome as "What to Send" names them ("Disk Health ·
	// Critical").
	Label string
	Title string
	Body  string
	// Tag is the short name emails put in brackets before the subject:
	// the one environment the message is about ("[homelab] Disk
	// /dev/sda is failing"), "Test" for a test message; "" for none.
	Tag string
	// URL links to the page in Docker Manager the message is about
	// (optional).
	URL    string
	Tone   NotificationTone
	Fields []NotificationField
	// Footer is a short line under the message (the instance name, beside
	// Docker Manager's logo where the service shows one).
	Footer string
	// Time is when the event happened (zero: not shown).
	Time time.Time
}

// Notification channel errors.
var (
	ErrNotificationChannelNotFound  = errors.New("notification channel not found")
	ErrNotificationChannelNameTaken = errors.New("notification channel name taken")
)
