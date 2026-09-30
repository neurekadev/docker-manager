// Package notify owns the manager's notification channels (#142): the
// owner-administered outgoing destinations (Discord, Slack, Microsoft
// Teams, Telegram, email, ntfy, Gotify, Pushover, Matrix, a generic
// webhook, or any other Shoutrrr service) and sending a message through
// one of them.
//
// A channel's address (its Shoutrrr URL) carries its credentials. It is
// sealed at rest ("notification_channels/<id>/url") and leaves this
// package only as the owner's explicit reveal (Reveal, owner with a recent
// step-up) and inside one send: never in a domain value, log, audit
// record, job input, error or stored result. Sends are reduced to stable
// error classes (classify.go).
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/secrets"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Limits.
const (
	MaxNameLen         = 100
	MaxEnvironments    = 100
	TestInterval       = 5 * time.Second
	auditTargetType    = "notification_channel"
	testMessageTitle   = "Docker Manager test message"
	testMessageBodyFmt = "This is a test message from Docker Manager. If you can read it, the channel %q works."
)

// OwnerGuard enforces owner-only administration (auth.Service).
type OwnerGuard interface {
	RequireOwner(ctx context.Context, recent bool) (string, error)
}

// Options configures the service.
type Options struct {
	DB      *bun.DB
	Keyring *secrets.Keyring
	Clock   clock.Clock
	Logger  *slog.Logger
	// Guard enforces owner-only administration; nil refuses it.
	Guard OwnerGuard
	// Timeout bounds one send (default DefaultTimeout).
	Timeout time.Duration
	// PublicURL is the manager's address; test messages link to it.
	PublicURL string
}

// Service manages notification channels.
type Service struct {
	opts Options
	db   *bun.DB

	mu        sync.Mutex
	lastTests map[string]time.Time
}

// New creates the service.
func New(opts Options) (*Service, error) {
	if opts.DB == nil || opts.Keyring == nil {
		return nil, errors.New("notify: DB and Keyring are required")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	return &Service{opts: opts, db: opts.DB, lastTests: map[string]time.Time{}}, nil
}

// TestRateLimitedError refuses a test sent too soon after the previous one
// of the same channel.
type TestRateLimitedError struct {
	RetryAfter time.Duration
}

func (e *TestRateLimitedError) Error() string {
	return fmt.Sprintf("notify: wait %s before testing this channel again", e.RetryAfter)
}

func fieldErr(field, message string) error { return &domain.FieldError{Field: field, Message: message} }

func sealContext(id string) string { return "notification_channels/" + id + "/url" }

func (s *Service) owner(ctx context.Context, recent bool) error {
	if s.opts.Guard == nil {
		return domain.ErrForbidden
	}
	_, err := s.opts.Guard.RequireOwner(ctx, recent)
	return err
}

// List returns channels in creation order (never their addresses).
func (s *Service) List(ctx context.Context, afterID string, limit int) ([]domain.NotificationChannel, error) {
	return store.ListNotificationChannels(ctx, s.db, afterID, limit)
}

// Get returns one channel (never its address).
func (s *Service) Get(ctx context.Context, id string) (domain.NotificationChannel, error) {
	return store.GetNotificationChannel(ctx, s.db, id)
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxNameLen || strings.ContainsFunc(name, unicode.IsControl) {
		return "", fieldErr("name", fmt.Sprintf("must be 1 to %d characters without control characters", MaxNameLen))
	}
	return name, nil
}

// validAddress checks an address without sending anything and returns its
// service. The error never repeats the address.
func (s *Service) validAddress(address string) (string, error) {
	service, err := validate(address, s.opts.Timeout)
	switch {
	case errors.Is(err, errUnknownService):
		return "", fieldErr("address", "Shoutrrr has no service of this name")
	case err != nil:
		return "", fieldErr("address", "not a valid address for this service")
	}
	return service, nil
}

// validKinds returns the event kinds in their canonical order (nil: all).
func validKinds(kinds []domain.NotificationEventKind) ([]domain.NotificationEventKind, error) {
	if kinds == nil {
		return domain.NotificationEventKinds(), nil
	}
	want := map[domain.NotificationEventKind]bool{}
	for _, k := range kinds {
		if !k.Valid() {
			return nil, fieldErr("eventKinds", "unknown event kind "+string(k))
		}
		want[k] = true
	}
	if len(want) == 0 {
		return nil, fieldErr("eventKinds", "choose at least one kind of event")
	}
	out := make([]domain.NotificationEventKind, 0, len(want))
	for _, k := range domain.NotificationEventKinds() {
		if want[k] {
			out = append(out, k)
		}
	}
	return out, nil
}

// validEnvironments checks an environment filter (existing, active
// environments) and returns it de-duplicated ([] for every environment).
func (s *Service) validEnvironments(ctx context.Context, envIDs []string) ([]string, error) {
	if len(envIDs) > MaxEnvironments {
		return nil, fieldErr("environmentIds", fmt.Sprintf("at most %d environments", MaxEnvironments))
	}
	seen := map[string]bool{}
	out := []string{}
	for _, id := range envIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		env, err := store.GetEnvironment(ctx, s.db, id)
		if errors.Is(err, domain.ErrEnvironmentNotFound) || (err == nil && env.Status != domain.EnvironmentActive) {
			return nil, fieldErr("environmentIds", "no such active environment")
		}
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// Create stores a new channel (owner, recent step-up).
func (s *Service) Create(ctx context.Context, in domain.NotificationChannelInput) (domain.NotificationChannel, error) {
	if err := s.owner(ctx, true); err != nil {
		return domain.NotificationChannel{}, err
	}
	name, err := validName(in.Name)
	if err != nil {
		return domain.NotificationChannel{}, err
	}
	service, err := s.validAddress(in.Address)
	if err != nil {
		return domain.NotificationChannel{}, err
	}
	kinds, err := validKinds(in.EventKinds)
	if err != nil {
		return domain.NotificationChannel{}, err
	}
	envs, err := s.validEnvironments(ctx, in.EnvironmentIDs)
	if err != nil {
		return domain.NotificationChannel{}, err
	}
	now := s.opts.Clock.Now().UTC()
	c := domain.NotificationChannel{
		ID: ids.New(), Name: name, Service: service, Target: targetOf(in.Address), Enabled: in.Enabled, EventKinds: kinds,
		SendResolved: in.SendResolved, EnvironmentIDs: envs, AddressVersion: 1, AddressUpdatedAt: now, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	sealed, err := s.opts.Keyring.Seal([]byte(in.Address), sealContext(c.ID))
	if err != nil {
		return domain.NotificationChannel{}, err
	}
	c.AddressFingerprint = s.opts.Keyring.Fingerprint([]byte(in.Address), sealContext(c.ID))
	if err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return store.InsertNotificationChannel(ctx, tx, &c, sealed)
	}); err != nil {
		return domain.NotificationChannel{}, err
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: auditTargetType, ID: c.ID})
	audit.SetDetail(ctx, "service", c.Service)
	return c, nil
}

// Update edits a channel. Changing its address needs a recent step-up;
// other settings need the owner.
func (s *Service) Update(ctx context.Context, id string, revision int64, p domain.NotificationChannelPatch) (domain.NotificationChannel, error) {
	if err := s.owner(ctx, p.Address != nil); err != nil {
		return domain.NotificationChannel{}, err
	}
	cur, err := store.GetNotificationChannel(ctx, s.db, id)
	if err != nil {
		return domain.NotificationChannel{}, err
	}
	if cur.Revision != revision {
		return domain.NotificationChannel{}, domain.ErrRevisionMismatch
	}
	next := cur
	if p.Name != nil {
		if next.Name, err = validName(*p.Name); err != nil {
			return domain.NotificationChannel{}, err
		}
	}
	if p.Enabled != nil {
		next.Enabled = *p.Enabled
	}
	if p.EventKinds != nil {
		kinds := *p.EventKinds
		if kinds == nil {
			kinds = []domain.NotificationEventKind{}
		}
		if next.EventKinds, err = validKinds(kinds); err != nil {
			return domain.NotificationChannel{}, err
		}
	}
	if p.SendResolved != nil {
		next.SendResolved = *p.SendResolved
	}
	if p.EnvironmentIDs != nil {
		if next.EnvironmentIDs, err = s.validEnvironments(ctx, *p.EnvironmentIDs); err != nil {
			return domain.NotificationChannel{}, err
		}
	}
	now := s.opts.Clock.Now().UTC()
	sealed := ""
	if p.Address != nil {
		service, err := s.validAddress(*p.Address)
		if err != nil {
			return domain.NotificationChannel{}, err
		}
		if sealed, err = s.opts.Keyring.Seal([]byte(*p.Address), sealContext(cur.ID)); err != nil {
			return domain.NotificationChannel{}, err
		}
		next.Service, next.Target = service, targetOf(*p.Address)
		next.AddressFingerprint = s.opts.Keyring.Fingerprint([]byte(*p.Address), sealContext(cur.ID))
		next.AddressVersion, next.AddressUpdatedAt = cur.AddressVersion+1, now
		// A new address has not been tried yet.
		next.LastResult, next.LastAttemptAt, next.LastSuccessAt = "", nil, nil
	}
	next.Revision, next.UpdatedAt = cur.Revision+1, now
	if err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return store.UpdateNotificationChannel(ctx, tx, &next, sealed, revision)
	}); err != nil {
		return domain.NotificationChannel{}, err
	}
	audit.SetDetail(ctx, "service", next.Service)
	audit.SetDiff(ctx, auditView(cur), auditView(next))
	return next, nil
}

// auditView is the audited settings of a channel (never the address or
// anything derived from it but the service and its version).
func auditView(c domain.NotificationChannel) map[string]any {
	kinds := make([]string, 0, len(c.EventKinds))
	for _, k := range c.EventKinds {
		kinds = append(kinds, string(k))
	}
	return map[string]any{
		"name": c.Name, "service": c.Service, "enabled": c.Enabled, "eventKinds": kinds, "sendResolved": c.SendResolved,
		"environmentIds": append([]string{}, c.EnvironmentIDs...), "addressVersion": c.AddressVersion,
	}
}

// Delete removes a channel (owner).
func (s *Service) Delete(ctx context.Context, id string, revision int64) error {
	if err := s.owner(ctx, false); err != nil {
		return err
	}
	cur, err := store.GetNotificationChannel(ctx, s.db, id)
	if err != nil {
		return err
	}
	if err := store.DeleteNotificationChannel(ctx, s.db, id, revision); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.lastTests, id)
	s.mu.Unlock()
	audit.SetDetail(ctx, "service", cur.Service)
	return nil
}

// address opens a channel's address for one use.
func (s *Service) address(ctx context.Context, id string) (logging.Secret, error) {
	sealed, _, err := store.NotificationChannelSecret(ctx, s.db, id)
	if err != nil {
		return "", err
	}
	pt, err := s.opts.Keyring.Open(sealed, sealContext(id))
	if err != nil {
		return "", fmt.Errorf("notify: open the address of notification channel %s: %w", id, err)
	}
	return logging.Secret(pt), nil
}

// Reveal returns a channel's address to the owner (recent step-up). The
// API audits every reveal (never the value).
func (s *Service) Reveal(ctx context.Context, id string) (string, error) {
	if err := s.owner(ctx, true); err != nil {
		return "", err
	}
	c, err := store.GetNotificationChannel(ctx, s.db, id)
	if err != nil {
		return "", err
	}
	addr, err := s.address(ctx, id)
	if err != nil {
		return "", err
	}
	audit.SetDetail(ctx, "service", c.Service)
	return string(addr), nil
}

// Result is the outcome of a send.
type Result struct {
	OK bool
	// ErrorClass and Message describe a failure (domain.NotifyErr*
	// classes; the message is a sentence without the address).
	ErrorClass string
	Message    string
	At         time.Time
}

// Test sends a test message through a channel (owner), enabled or not, at
// most once every TestInterval per channel, and records the result.
func (s *Service) Test(ctx context.Context, id string) (Result, error) {
	if err := s.owner(ctx, false); err != nil {
		return Result{}, err
	}
	c, err := store.GetNotificationChannel(ctx, s.db, id)
	if err != nil {
		return Result{}, err
	}
	now := s.opts.Clock.Now()
	s.mu.Lock()
	if last, ok := s.lastTests[id]; ok && now.Sub(last) < TestInterval {
		s.mu.Unlock()
		return Result{}, &TestRateLimitedError{RetryAfter: TestInterval - now.Sub(last)}
	}
	s.lastTests[id] = now
	s.mu.Unlock()
	msg := domain.NotificationMessage{Title: testMessageTitle, Body: fmt.Sprintf(testMessageBodyFmt, c.Name), URL: s.opts.PublicURL}
	res, err := s.send(ctx, c, msg)
	if err != nil {
		return Result{}, err
	}
	result := domain.NotificationResultOK
	if !res.OK {
		result = res.ErrorClass
	}
	audit.SetDetail(ctx, "service", c.Service)
	audit.SetDetail(ctx, "result", result)
	return res, nil
}

// Send delivers a message through a channel and records the result (for
// alerts; callers choose the channels, e.g. with
// domain.NotificationChannel.Wants). A failed delivery is a Result with
// OK false, not an error.
func (s *Service) Send(ctx context.Context, channelID string, msg domain.NotificationMessage) (Result, error) {
	c, err := store.GetNotificationChannel(ctx, s.db, channelID)
	if err != nil {
		return Result{}, err
	}
	return s.send(ctx, c, msg)
}

func (s *Service) send(ctx context.Context, c domain.NotificationChannel, msg domain.NotificationMessage) (Result, error) {
	addr, err := s.address(ctx, c.ID)
	if err != nil {
		return Result{}, err
	}
	class := deliver(ctx, string(addr), msg, s.opts.Timeout)
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	at := s.opts.Clock.Now().UTC()
	res := Result{OK: class == "", ErrorClass: class, Message: Message(class), At: at}
	result := domain.NotificationResultOK
	if !res.OK {
		result = class
		s.opts.Logger.Warn("a notification could not be sent", "notification_channel_id", c.ID, "service", c.Service,
			"error_class", class)
	}
	if err := store.RecordNotificationResult(context.WithoutCancel(ctx), s.db, c.ID, at, result, res.OK); err != nil {
		s.opts.Logger.Warn("could not record a notification result", "notification_channel_id", c.ID, "error", err)
	}
	return res, nil
}
