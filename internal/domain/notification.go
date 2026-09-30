package domain

import (
	"errors"
	"time"
)

// Notification channels (#142): outgoing destinations (Discord, Slack,
// email, ntfy, a generic webhook, ... through Shoutrrr) the owner
// configures once. The channel's address (a Shoutrrr URL) holds its
// credentials (webhook tokens, passwords): it is sealed at rest, never
// logged, audited or returned by list and get, and only the owner can
// reveal it again. Each channel also stores what it is subscribed to
// (event kinds, environments, whether resolved problems are sent too).

// NotificationEventKind is a kind of event a channel can be subscribed to.
type NotificationEventKind string

// Event kinds.
const (
	NotifyDiskHealth         NotificationEventKind = "disk_health"
	NotifyRAID               NotificationEventKind = "raid"
	NotifyEnvironmentOffline NotificationEventKind = "environment_offline"
	NotifyJobFailed          NotificationEventKind = "job_failed"
	NotifyUpdatesAvailable   NotificationEventKind = "updates_available"
)

// NotificationEventKinds returns every event kind in display order (the
// default subscription of a new channel).
func NotificationEventKinds() []NotificationEventKind {
	return []NotificationEventKind{NotifyDiskHealth, NotifyRAID, NotifyEnvironmentOffline, NotifyJobFailed, NotifyUpdatesAvailable}
}

// Valid reports whether k is a known event kind.
func (k NotificationEventKind) Valid() bool {
	for _, v := range NotificationEventKinds() {
		if k == v {
			return true
		}
	}
	return false
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
	// EventKinds are the subscribed event kinds (never empty).
	EventKinds []NotificationEventKind
	// SendResolved also sends a message when a problem is resolved.
	SendResolved bool
	// EnvironmentIDs limits the channel to these environments; empty means
	// every environment, including future ones.
	EnvironmentIDs []string
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

// Wants reports whether the channel sends events of kind about the
// environment (an empty environmentID: an event of no environment): it is
// enabled, subscribed to the kind, and not limited to other environments.
func (c NotificationChannel) Wants(kind NotificationEventKind, environmentID string) bool {
	if !c.Enabled {
		return false
	}
	subscribed := false
	for _, k := range c.EventKinds {
		if k == kind {
			subscribed = true
			break
		}
	}
	if !subscribed {
		return false
	}
	if len(c.EnvironmentIDs) == 0 || environmentID == "" {
		return true
	}
	for _, id := range c.EnvironmentIDs {
		if id == environmentID {
			return true
		}
	}
	return false
}

// NotificationChannelInput creates a channel.
type NotificationChannelInput struct {
	Name string
	// Address is the Shoutrrr URL (a secret).
	Address string
	Enabled bool
	// EventKinds nil means every kind.
	EventKinds     []NotificationEventKind
	SendResolved   bool
	EnvironmentIDs []string
}

// NotificationChannelPatch edits a channel; nil fields stay unchanged.
type NotificationChannelPatch struct {
	Name           *string
	Enabled        *bool
	EventKinds     *[]NotificationEventKind
	SendResolved   *bool
	EnvironmentIDs *[]string
	// Address replaces the address (re-sealed, version bumped).
	Address *string
}

// NotificationMessage is one message sent through a channel.
type NotificationMessage struct {
	Title string
	Body  string
	// URL links to the page in Docker Manager the message is about
	// (optional).
	URL string
}

// Notification channel errors.
var (
	ErrNotificationChannelNotFound  = errors.New("notification channel not found")
	ErrNotificationChannelNameTaken = errors.New("notification channel name taken")
)
