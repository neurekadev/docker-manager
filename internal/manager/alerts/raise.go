package alerts

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Observation is a problem as an evaluator sees it now.
type Observation struct {
	Key           string
	Kind          domain.NotificationEventKind
	Severity      domain.AlertSeverity
	EnvironmentID string
	ResourceType  string
	ResourceID    string
	JobKind       domain.JobKind
	Targets       []domain.JobTarget
	Title         string
	Facts         map[string]string
	// Fingerprint is the set of problem tokens (domain.Fingerprint).
	Fingerprint string
}

// raise records o: a new alert fires (and is sent), or the firing one of
// its key is updated. It is sent again (and a dismissal cleared) only
// when it got worse: a higher severity or a problem token it did not have
// (a new failing attribute, a new failed member, a new image digest).
// Anything else users see (title, facts, progress) updates quietly; an
// unchanged observation only stamps last_seen_at. It returns the alert
// when users see a change.
func raise(ctx context.Context, db bun.IDB, o Observation, now time.Time) (*domain.Alert, error) {
	cur, found, err := store.FiringAlert(ctx, db, o.Key)
	if err != nil {
		return nil, err
	}
	if o.Facts == nil {
		o.Facts = map[string]string{}
	}
	if !found {
		a := domain.Alert{
			ID: ids.New(), DedupeKey: o.Key, Kind: o.Kind, Severity: o.Severity, State: domain.AlertFiring,
			EnvironmentID: o.EnvironmentID, ResourceType: o.ResourceType, ResourceID: o.ResourceID, JobKind: o.JobKind,
			Targets: o.Targets, Title: o.Title, Facts: o.Facts, Fingerprint: o.Fingerprint, StartedAt: now, UpdatedAt: now,
			LastSeenAt: now, Revision: 1,
		}
		if err := store.InsertAlert(ctx, db, &a); err != nil {
			return nil, err
		}
		if err := enqueue(ctx, db, a, domain.AlertEventFiring, now); err != nil {
			return nil, err
		}
		return &a, nil
	}
	worse := o.Severity.Rank() > cur.Severity.Rank() || domain.NewTokens(cur.Fingerprint, o.Fingerprint)
	next := cur
	next.Severity, next.Title, next.Facts, next.Fingerprint = o.Severity, o.Title, o.Facts, o.Fingerprint
	next.ResourceType, next.ResourceID, next.JobKind, next.Targets = o.ResourceType, o.ResourceID, o.JobKind, o.Targets
	next.LastSeenAt = now
	if worse {
		next.Escalation = cur.Escalation + 1
		next.DismissedAt, next.DismissedBy, next.DismissedByName = nil, "", ""
	}
	if !worse && sameView(cur, next) {
		return nil, store.TouchAlerts(ctx, db, []string{cur.ID}, now)
	}
	next.UpdatedAt, next.Revision = now, cur.Revision+1
	if err := store.UpdateAlert(ctx, db, &next, cur.Revision); err != nil {
		return nil, err
	}
	if worse {
		if err := enqueue(ctx, db, next, domain.AlertEventWorse, now); err != nil {
			return nil, err
		}
	}
	return &next, nil
}

// sameView reports whether nothing users see differs between a and b.
func sameView(a, b domain.Alert) bool {
	return a.Severity == b.Severity && a.Title == b.Title && maps.Equal(a.Facts, b.Facts) && a.Fingerprint == b.Fingerprint &&
		a.ResourceType == b.ResourceType && a.ResourceID == b.ResourceID && a.JobKind == b.JobKind &&
		slices.Equal(a.Targets, b.Targets) && (a.DismissedAt == nil) == (b.DismissedAt == nil)
}

// resolve ends a firing alert. Only AlertResolvedFixed is announced, to
// the channels that were told about the alert (a message of it was sent)
// and send resolved problems (a failed job sent as its area's kind: to
// every told channel, with an outcome it was told); a message of the alert not sent yet is
// dropped instead, with its resolution (nothing to report).
func resolve(ctx context.Context, db bun.IDB, a domain.Alert, resolution string, now time.Time) (*domain.Alert, error) {
	if a.State != domain.AlertFiring {
		return nil, nil
	}
	next := a
	next.State, next.Resolution, next.ResolvedAt = domain.AlertResolved, resolution, &now
	next.UpdatedAt, next.Revision = now, a.Revision+1
	if err := store.UpdateAlert(ctx, db, &next, a.Revision); err != nil {
		return nil, err
	}
	// Messages of this alert still waiting: a problem that went away
	// before anyone heard of it is not reported at all.
	ds, err := store.AlertDeliveries(ctx, db, a.ID)
	if err != nil {
		return nil, err
	}
	// told: the channels that were sent a message of this alert, with the
	// outcomes they were sent (a failed job sent as its area's kind: its
	// resolution goes out with one of them, see write).
	told := map[string][]domain.NotificationOutcome{}
	var drop []domain.AlertDelivery
	for _, d := range ds {
		switch {
		case d.Event == domain.AlertEventResolved:
		case d.State == domain.DeliverySent:
			told[d.ChannelID] = append(told[d.ChannelID], d.Outcome)
		case d.State == domain.DeliveryPending:
			d.State, d.UpdatedAt = domain.DeliveryDropped, now
			drop = append(drop, d)
		}
	}
	if err := store.SetAlertDeliveries(ctx, db, drop); err != nil {
		return nil, err
	}
	if resolution == domain.AlertResolvedFixed && len(told) > 0 {
		if err := enqueueTo(ctx, db, next, domain.AlertEventResolved, now, told); err != nil {
			return nil, err
		}
	}
	return &next, nil
}

// resolveKey resolves the firing alert of a key, if any.
func resolveKey(ctx context.Context, db bun.IDB, key, resolution string, now time.Time) (*domain.Alert, error) {
	a, found, err := store.FiringAlert(ctx, db, key)
	if err != nil || !found {
		return nil, err
	}
	return resolve(ctx, db, a, resolution, now)
}

func enqueue(ctx context.Context, db bun.IDB, a domain.Alert, event string, now time.Time) error {
	return enqueueTo(ctx, db, a, event, now, nil)
}

// newDelivery is a pending message to a channel with a snapshot of what
// it says now (a message sent later, or again, still says this).
func newDelivery(m snapshot, channelID string, now, due time.Time) domain.AlertDelivery {
	return domain.AlertDelivery{ID: ids.New(), AlertID: m.alertID, NotificationID: m.notificationID, ChannelID: channelID,
		Event: m.event, Kind: m.kind, EnvironmentID: m.environmentID, Severity: m.severity, Outcome: m.outcome, Title: m.title,
		Body: m.body, Fields: m.fields, Link: m.link, State: domain.DeliveryPending, NextAttemptAt: due, CreatedAt: now,
		UpdatedAt: now}
}

// snapshot is what a message says, taken when it is written.
type snapshot struct {
	alertID, notificationID string
	event                   string
	kind                    domain.NotificationEventKind
	environmentID           string
	severity                domain.AlertSeverity
	outcome                 domain.NotificationOutcome
	title, body, link       string
	fields                  []domain.NotificationField
	// toldOutcomes: a channel that does not send outcome may get the
	// message with an outcome it was told before (only's), which it sends.
	toldOutcomes bool
}

// enqueueTo adds a message of a to every channel that wants its outcome
// for its kind and environment; only (when only is not nil) to those
// channels. A failed backup, restore, prune or update run is not sent as
// a failed job: its notification says it (with its own subscription).
// Another failed job of an area (a backup verification, an update check)
// is sent as its area's kind (domain.Alert.SentAs).
func enqueueTo(ctx context.Context, db bun.IDB, a domain.Alert, event string, now time.Time,
	only map[string][]domain.NotificationOutcome) error {
	if a.Kind == domain.NotifyJobFailed && domain.NotificationKindOfJob(a.JobKind) != "" {
		return nil
	}
	m := alertSnapshot(a, event)
	// A failed job sent as its area's kind has no "resolved" outcome: its
	// resolution goes to each told channel with an outcome it was sent
	// (the alert's severity may have changed since: a later run that only
	// partly failed lowers a failure to a warning).
	// So does the end of paused backups (backups have no resolved outcome).
	m.toldOutcomes = event == domain.AlertEventResolved && (m.kind != a.Kind || a.Kind == domain.NotifyBackup)
	return write(ctx, db, m, now, only, func() []domain.NotificationField {
		return alertFields(a, environmentName(ctx, db, a.EnvironmentID), event)
	})
}

// alertSnapshot is what a message of a says about event (without its
// fields, which need the environment's name).
func alertSnapshot(a domain.Alert, event string) snapshot {
	kind, outcome := a.SentAs(event)
	m := snapshot{alertID: a.ID, event: event, kind: kind, environmentID: a.EnvironmentID, severity: a.Severity,
		outcome: outcome, title: a.Title, body: Detail(a), link: Link(a)}
	if event == domain.AlertEventResolved {
		m.body = resolvedDetail(a)
	}
	return m
}

// write adds the message m to every channel that wants it (only: to
// those channels only; with m.toldOutcomes, a channel that does not send
// m's outcome gets it with the first of its told outcomes it sends). A message is never due before the channel's
// earlier ones (a channel waiting out a failed send keeps its order), so
// the dispatcher finds due channels by due time alone. fields is called
// only when some channel wants the message.
func write(ctx context.Context, db bun.IDB, m snapshot, now time.Time, only map[string][]domain.NotificationOutcome,
	fields func() []domain.NotificationField) error {
	channels, err := store.ListNotificationChannels(ctx, db, "", 0)
	if err != nil {
		return err
	}
	var targets []string
	outcomes := map[string]domain.NotificationOutcome{}
	for _, c := range channels {
		told, ok := only[c.ID]
		if only != nil && !ok {
			continue
		}
		outcome, wanted := m.outcome, c.Wants(m.kind, m.outcome, m.environmentID)
		for _, o := range told {
			if wanted || !m.toldOutcomes {
				break
			}
			outcome, wanted = o, c.Wants(m.kind, o, m.environmentID)
		}
		if !wanted {
			continue
		}
		targets = append(targets, c.ID)
		outcomes[c.ID] = outcome
	}
	if len(targets) == 0 {
		return nil
	}
	m.fields = fields()
	// One query for every channel's latest pending due time.
	latest, err := store.LatestAlertAttempts(ctx, db, targets)
	if err != nil {
		return err
	}
	ds := make([]domain.AlertDelivery, 0, len(targets))
	for _, id := range targets {
		due := now.Add(DeliveryDelay)
		if l, ok := latest[id]; ok && l.After(due) {
			due = l
		}
		d := newDelivery(m, id, now, due)
		d.Outcome = outcomes[id]
		ds = append(ds, d)
	}
	return store.InsertAlertDeliveries(ctx, db, ds)
}
