package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/notify"
)

// Notification channels (#142): owner-administered outgoing destinations
// (Shoutrrr URLs). The address is a secret: list and get never return it;
// the owner reads it again only through the audited reveal operation. The
// flows live in internal/manager/notify; this file is the transport
// contract.

const tagNotifications = "Notifications"

// capNotificationChannelManage is the owner-only catalog key of channel
// administration (only the owner holds it).
const capNotificationChannelManage = "notification_channel.manage"

// NotificationService is the notification channel service as seen by the
// API (implemented by *notify.Service). Management methods enforce
// owner-only access and step-up from the caller's session.
type NotificationService interface {
	List(ctx context.Context, afterID string, limit int) ([]domain.NotificationChannel, error)
	Get(ctx context.Context, id string) (domain.NotificationChannel, error)
	Create(ctx context.Context, in domain.NotificationChannelInput) (domain.NotificationChannel, error)
	Update(ctx context.Context, id string, revision int64, p domain.NotificationChannelPatch) (domain.NotificationChannel, error)
	Delete(ctx context.Context, id string, revision int64) error
	Reveal(ctx context.Context, id string) (string, error)
	Test(ctx context.Context, id string) (notify.Result, error)
}

// NotificationAddress describes the stored address without revealing it.
type NotificationAddress struct {
	Fingerprint string    `json:"fingerprint" example:"fp_3f2a9c0d1e4b5a67" doc:"Keyed fingerprint of the address: changes when the address changes, never reveals it."`
	Version     int       `json:"version" doc:"Increases with every address change."`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// NotificationChannel is a stored channel. Its address is never part of
// it (GET …/address reveals it to the owner).
type NotificationChannel struct {
	ID      string `json:"id"`
	Name    string `json:"name" example:"Ops on Discord"`
	Service string `json:"service" example:"discord" doc:"The Shoutrrr service of the address (discord, slack, teams, telegram, smtp, ntfy, gotify, pushover, matrix, generic, ...)."`
	Target  string `json:"target,omitempty" example:"mail.example.com" doc:"Where messages go when that is not secret: the host of a mail, push or chat server or of a generic webhook. Absent for services whose address holds only tokens."`
	Enabled bool   `json:"enabled"`
	// Subscription (what the channel sends).
	EventKinds     []string            `json:"eventKinds" enum:"disk_health,raid,environment_offline,job_failed,updates_available" doc:"The kinds of events the channel sends."`
	SendResolved   bool                `json:"sendResolved" doc:"Also send a message when a problem is resolved."`
	EnvironmentIDs []string            `json:"environmentIds" doc:"Limits the channel to these environments; empty means every environment, including future ones."`
	Address        NotificationAddress `json:"address"`
	LastResult     string              `json:"lastResult,omitempty" enum:"ok,dns,connect,tls,timeout,auth,http_4xx,http_5xx,redirect,rejected,invalid_url" doc:"Outcome of the last send or test: ok or an error class; absent before the first one (and after an address change)."`
	LastAttemptAt  *time.Time          `json:"lastAttemptAt,omitempty"`
	LastSuccessAt  *time.Time          `json:"lastSuccessAt,omitempty"`
	Revision       int64               `json:"revision" doc:"Edit revision (the ETag)."`
	CreatedAt      time.Time           `json:"createdAt"`
	UpdatedAt      time.Time           `json:"updatedAt"`
}

func newNotificationChannel(c domain.NotificationChannel) NotificationChannel {
	kinds := make([]string, 0, len(c.EventKinds))
	for _, k := range c.EventKinds {
		kinds = append(kinds, string(k))
	}
	envs := c.EnvironmentIDs
	if envs == nil {
		envs = []string{}
	}
	return NotificationChannel{
		ID: c.ID, Name: c.Name, Service: c.Service, Target: c.Target, Enabled: c.Enabled, EventKinds: kinds,
		SendResolved: c.SendResolved, EnvironmentIDs: envs,
		Address:    NotificationAddress{Fingerprint: c.AddressFingerprint, Version: c.AddressVersion, UpdatedAt: c.AddressUpdatedAt},
		LastResult: c.LastResult, LastAttemptAt: c.LastAttemptAt, LastSuccessAt: c.LastSuccessAt, Revision: c.Revision,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

func eventKinds(in []string) []domain.NotificationEventKind {
	if in == nil {
		return nil
	}
	out := make([]domain.NotificationEventKind, 0, len(in))
	for _, k := range in {
		out = append(out, domain.NotificationEventKind(k))
	}
	return out
}

type notificationsAPI struct {
	svc   NotificationService
	authz authz.Authorizer
}

// admin returns the service for an owner-only operation. The owner check
// runs before any lookup, so other callers cannot probe which channel IDs
// exist; the service enforces it again (with step-up where needed).
func (h *notificationsAPI) admin(ctx context.Context) (NotificationService, error) {
	if h.svc == nil {
		return nil, Unavailable(CodeUnavailable, "notification channels are not available")
	}
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	if !c.Can(capNotificationChannelManage, authz.Instance()).Allowed {
		return nil, Forbidden("only the instance owner may administer notification channels")
	}
	return h.svc, nil
}

func notificationError(err error) error {
	var rl *notify.TestRateLimitedError
	switch {
	case errors.As(err, &rl):
		secs := int((rl.RetryAfter + time.Second - 1) / time.Second)
		return NewError(http.StatusTooManyRequests, CodeNotificationTestRateLimited,
			"a test message was sent to this channel moments ago; wait a few seconds and try again").
			WithHeader("Retry-After", strconv.Itoa(max(secs, 1)))
	case errors.Is(err, domain.ErrNotificationChannelNotFound):
		return NotFound("notification channel not found")
	case errors.Is(err, domain.ErrNotificationChannelNameTaken):
		return Conflict(CodeNotificationChannelNameTaken, "another notification channel already uses this name")
	}
	return identityError(err)
}

// --- inputs and outputs ---

type notificationChannelIDInput struct {
	ChannelID string `path:"channelId" maxLength:"64" doc:"Notification channel ID."`
}

type notificationChannelOutput struct {
	ETagHeader
	Body NotificationChannel
}

type notificationChannelListOutput struct{ Body Page[NotificationChannel] }

type createNotificationChannelInput struct {
	Body struct {
		Name           string   `json:"name" minLength:"1" maxLength:"100" example:"Ops on Discord"`
		Address        string   `json:"address" minLength:"1" maxLength:"4096" writeOnly:"true" example:"discord://token@webhookid" doc:"The Shoutrrr URL of the destination. A secret: never returned by list or get, never logged or audited."`
		Enabled        *bool    `json:"enabled,omitempty" default:"true"`
		EventKinds     []string `json:"eventKinds,omitempty" maxItems:"16" enum:"disk_health,raid,environment_offline,job_failed,updates_available" doc:"Default: every kind."`
		SendResolved   *bool    `json:"sendResolved,omitempty" default:"true"`
		EnvironmentIDs []string `json:"environmentIds,omitempty" maxItems:"100" doc:"Default (empty): every environment, including future ones."`
	}
}

type updateNotificationChannelInput struct {
	ChannelID string `path:"channelId" maxLength:"64" doc:"Notification channel ID."`
	IfMatchParam
	Body struct {
		Name           *string  `json:"name,omitempty" minLength:"1" maxLength:"100" example:"Ops on Discord"`
		Enabled        *bool    `json:"enabled,omitempty"`
		EventKinds     []string `json:"eventKinds,omitempty" maxItems:"16" enum:"disk_health,raid,environment_offline,job_failed,updates_available" doc:"Replaces the kinds (at least one)."`
		SendResolved   *bool    `json:"sendResolved,omitempty"`
		EnvironmentIDs []string `json:"environmentIds,omitempty" maxItems:"100" doc:"Replaces the environment filter; an empty list means every environment."`
		Address        *string  `json:"address,omitempty" minLength:"1" maxLength:"4096" writeOnly:"true" doc:"A new Shoutrrr URL (needs a recent step-up; resets the last result)."`
	}
}

type deleteNotificationChannelInput struct {
	ChannelID string `path:"channelId" maxLength:"64" doc:"Notification channel ID."`
	IfMatchParam
}

// NotificationChannelAddress is a revealed address (owner only).
type NotificationChannelAddress struct {
	Address string `json:"address" example:"discord://token@webhookid" doc:"The channel's Shoutrrr URL."`
}

type notificationAddressOutput struct{ Body NotificationChannelAddress }

// NotificationChannelTest is the outcome of a test message. A failed
// delivery is a successful test run (ok false, errorClass set).
type NotificationChannelTest struct {
	OK         bool      `json:"ok"`
	ErrorClass string    `json:"errorClass,omitempty" example:"auth" enum:"dns,connect,tls,timeout,auth,http_4xx,http_5xx,redirect,rejected,invalid_url"`
	Message    string    `json:"message,omitempty" example:"The service refused the credentials. Check the token, password or webhook address." doc:"What went wrong and what to check, in words (never contains the address)."`
	SentAt     time.Time `json:"sentAt"`
}

type notificationTestOutput struct{ Body NotificationChannelTest }

func notificationETag(c NotificationChannel) ETagHeader {
	return ETagHeader{ETag: RevisionETag(c.Revision)}
}

// --- handlers ---

func (h *notificationsAPI) list(ctx context.Context, in *struct{ PageParams }) (*notificationChannelListOutput, error) {
	svc, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	fp := QueryFingerprint("notification-channels")
	var after agentCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fp, &after); err != nil {
			return nil, err
		}
	}
	items, next, err := ScanPage(ctx, Scan[domain.NotificationChannel]{
		Limit: in.PageLimit(), After: after.ID,
		Fetch: func(ctx context.Context, afterID string, n int) ([]domain.NotificationChannel, error) {
			return svc.List(ctx, afterID, n)
		},
		Position: func(c domain.NotificationChannel) string { return c.ID },
		Visible:  func(domain.NotificationChannel) bool { return true },
	})
	if err != nil {
		return nil, Internal(err)
	}
	out := make([]NotificationChannel, 0, len(items))
	for _, c := range items {
		out = append(out, newNotificationChannel(c))
	}
	cursor, err := nextCursor(fp, next)
	if err != nil {
		return nil, err
	}
	return &notificationChannelListOutput{Body: NewPage(out, cursor, nil)}, nil
}

func (h *notificationsAPI) get(ctx context.Context, in *notificationChannelIDInput) (*notificationChannelOutput, error) {
	svc, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	c, err := svc.Get(ctx, in.ChannelID)
	if err != nil {
		return nil, notificationError(err)
	}
	body := newNotificationChannel(c)
	return &notificationChannelOutput{ETagHeader: notificationETag(body), Body: body}, nil
}

func (h *notificationsAPI) create(ctx context.Context, in *createNotificationChannelInput) (*notificationChannelOutput, error) {
	svc, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	b := in.Body
	enabled, resolved := true, true
	if b.Enabled != nil {
		enabled = *b.Enabled
	}
	if b.SendResolved != nil {
		resolved = *b.SendResolved
	}
	c, err := svc.Create(ctx, domain.NotificationChannelInput{Name: b.Name, Address: b.Address, Enabled: enabled,
		EventKinds: eventKinds(b.EventKinds), SendResolved: resolved, EnvironmentIDs: b.EnvironmentIDs})
	if err != nil {
		return nil, notificationError(err)
	}
	body := newNotificationChannel(c)
	return &notificationChannelOutput{ETagHeader: notificationETag(body), Body: body}, nil
}

// current loads a channel for an If-Match edit.
func (h *notificationsAPI) current(ctx context.Context, svc NotificationService, id string, im IfMatchParam) (domain.NotificationChannel, error) {
	c, err := svc.Get(ctx, id)
	if err != nil {
		return c, notificationError(err)
	}
	if err := im.CheckIfMatch(RevisionETag(c.Revision)); err != nil {
		return c, err
	}
	return c, nil
}

func (h *notificationsAPI) staleOr(ctx context.Context, svc NotificationService, id string, err error) error {
	if !errors.Is(err, domain.ErrRevisionMismatch) {
		return notificationError(err)
	}
	cur, gerr := svc.Get(ctx, id)
	if gerr != nil {
		return notificationError(gerr)
	}
	return stale(cur.Revision)
}

func (h *notificationsAPI) update(ctx context.Context, in *updateNotificationChannelInput) (*notificationChannelOutput, error) {
	svc, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := h.current(ctx, svc, in.ChannelID, in.IfMatchParam)
	if err != nil {
		return nil, err
	}
	b := in.Body
	p := domain.NotificationChannelPatch{Name: b.Name, Enabled: b.Enabled, SendResolved: b.SendResolved, Address: b.Address}
	if b.EventKinds != nil {
		kinds := eventKinds(b.EventKinds)
		p.EventKinds = &kinds
	}
	if b.EnvironmentIDs != nil {
		envs := b.EnvironmentIDs
		p.EnvironmentIDs = &envs
	}
	c, err := svc.Update(ctx, cur.ID, cur.Revision, p)
	if err != nil {
		return nil, h.staleOr(ctx, svc, cur.ID, err)
	}
	body := newNotificationChannel(c)
	return &notificationChannelOutput{ETagHeader: notificationETag(body), Body: body}, nil
}

func (h *notificationsAPI) remove(ctx context.Context, in *deleteNotificationChannelInput) (*struct{}, error) {
	svc, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := h.current(ctx, svc, in.ChannelID, in.IfMatchParam)
	if err != nil {
		return nil, err
	}
	if err := svc.Delete(ctx, cur.ID, cur.Revision); err != nil {
		return nil, h.staleOr(ctx, svc, cur.ID, err)
	}
	return nil, nil
}

func (h *notificationsAPI) reveal(ctx context.Context, in *notificationChannelIDInput) (*notificationAddressOutput, error) {
	svc, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	addr, err := svc.Reveal(ctx, in.ChannelID)
	if err != nil {
		return nil, notificationError(err)
	}
	return &notificationAddressOutput{Body: NotificationChannelAddress{Address: addr}}, nil
}

func (h *notificationsAPI) test(ctx context.Context, in *notificationChannelIDInput) (*notificationTestOutput, error) {
	svc, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := svc.Test(ctx, in.ChannelID)
	if err != nil {
		return nil, notificationError(err)
	}
	return &notificationTestOutput{Body: NotificationChannelTest{OK: r.OK, ErrorClass: r.ErrorClass, Message: r.Message, SentAt: r.At}}, nil
}

func registerNotifications(a huma.API, deps Deps) {
	h := &notificationsAPI{svc: deps.Notifications, authz: authz.OrDenyAll(deps.Authorizer)}
	stepUp := " Requires a recent step-up (403 step_up_required)."
	path := BasePath + "/notification-channels"

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-notification-channels", Method: http.MethodGet, Path: path,
			Summary:     "List notification channels",
			Description: "Notification channels in creation order, with their subscription and last result. Addresses are never returned. " + ownerOnly,
			Tags:        []string{tagNotifications}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, h.list)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-notification-channel", Method: http.MethodPost, Path: path,
			Summary: "Add a notification channel", DefaultStatus: http.StatusCreated,
			Description: "Stores a destination by its Shoutrrr URL, sealed with the manager's secret-protection key. The address is " +
				"checked without sending anything (422 body.address). Defaults: enabled, every event kind, every environment, " +
				"resolved problems sent too. 409 notification_channel_name_taken." + stepUp + " " + ownerOnly,
			Tags: []string{tagNotifications}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, h.create)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-notification-channel", Method: http.MethodGet, Path: path + "/{channelId}",
			Summary:     "Get a notification channel",
			Description: "One channel with its ETag. Never contains the address. " + ownerOnly,
			Tags:        []string{tagNotifications}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusNotFound},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, h.get)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-notification-channel", Method: http.MethodPatch, Path: path + "/{channelId}",
			Summary: "Update a notification channel",
			Description: "Edits the name, whether it is enabled, its subscription (event kinds, environments, resolved problems) or " +
				"its address. A new address is checked like on creation, needs a recent step-up (403 step_up_required) and resets " +
				"the last result. Requires If-Match. 409 notification_channel_name_taken. " + ownerOnly,
			Tags: []string{tagNotifications}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusPreconditionFailed,
				http.StatusPreconditionRequired, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, h.update)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-notification-channel", Method: http.MethodDelete, Path: path + "/{channelId}",
			Summary: "Delete a notification channel", DefaultStatus: http.StatusNoContent,
			Description: "Removes the channel and its address. Requires If-Match. " + ownerOnly,
			Tags:        []string{tagNotifications}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusPreconditionFailed, http.StatusPreconditionRequired},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, h.remove)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-notification-channel-address", Method: http.MethodGet, Path: path + "/{channelId}/address",
			Summary: "Reveal the address of a notification channel",
			Description: "Returns the channel's Shoutrrr URL so the owner can view or edit it. Every reveal is audited (never the value)." +
				stepUp + " " + ownerOnly,
			Tags: []string{tagNotifications}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusNotFound},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance, Audit: AuditAlways, AuditAction: "notification_channel.reveal",
	}, h.reveal)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-notification-channel-test", Method: http.MethodPost, Path: path + "/{channelId}/tests",
			Summary: "Send a test message", DefaultStatus: http.StatusOK,
			Description: "Sends a test message through the channel now (also while it is off) and records the result as its last " +
				"result. A failed delivery is reported in the body (ok false, errorClass, message in words), never with the " +
				"service's own error text. At most one test per channel every 5 seconds (429 notification_test_rate_limited). " +
				ownerOnly,
			Tags: []string{tagNotifications}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusTooManyRequests},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance, AuditAction: "notification_channel.test",
	}, h.test)
}
