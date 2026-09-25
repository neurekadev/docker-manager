package auth

import (
	"context"
	"log/slog"
	"sync"

	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/requestinfo"
)

// hub tracks the contexts of in-flight authenticated requests (including
// SSE and WebSocket streams) per user, made with a browser session or an
// API token (#31), so ending a user's sessions or revoking a token also
// ends the open streams immediately.
type hub struct {
	mu   sync.Mutex
	next uint64
	subs map[string]map[uint64]hubEntry
}

type hubEntry struct {
	// epoch is the session epoch of a session request.
	epoch int64
	// tokenID is set for API-token requests (epoch is unused then).
	tokenID string
	cancel  context.CancelCauseFunc
}

func newHub() *hub { return &hub{subs: map[string]map[uint64]hubEntry{}} }

// register tracks cancel for userID's request made with session epoch.
func (h *hub) register(userID string, epoch int64, cancel context.CancelCauseFunc) func() {
	return h.add(userID, hubEntry{epoch: epoch, cancel: cancel})
}

// registerToken tracks cancel for a request made with API token tokenID
// of userID.
func (h *hub) registerToken(userID, tokenID string, cancel context.CancelCauseFunc) func() {
	return h.add(userID, hubEntry{tokenID: tokenID, cancel: cancel})
}

func (h *hub) add(userID string, e hubEntry) func() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	id := h.next
	if h.subs[userID] == nil {
		h.subs[userID] = map[uint64]hubEntry{}
	}
	h.subs[userID][id] = e
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.subs[userID], id)
		if len(h.subs[userID]) == 0 {
			delete(h.subs, userID)
		}
	}
}

// revoke cancels userID's session requests whose epoch differs from keep
// (all of them when keep is -1): their sessions ended
// (authz.ErrSessionEnded). API-token requests are not affected.
func (h *hub) revoke(userID string, keep int64) { h.revokeCause(userID, keep, authz.ErrSessionEnded) }

// revokeCause cancels like revoke with the given cause, which streams
// report to the client (authz.CloseReason).
func (h *hub) revokeCause(userID string, keep int64, cause error) {
	h.cancelWhere(cause, func(uid string, e hubEntry) bool {
		return uid == userID && e.tokenID == "" && (keep < 0 || e.epoch != keep)
	})
}

// revokeUser cancels every request of userID: sessions and API tokens.
func (h *hub) revokeUser(userID string, cause error) {
	h.cancelWhere(cause, func(uid string, _ hubEntry) bool { return uid == userID })
}

// revokeTokens cancels the requests made with the given API tokens.
func (h *hub) revokeTokens(tokenIDs []string, cause error) {
	set := make(map[string]bool, len(tokenIDs))
	for _, id := range tokenIDs {
		set[id] = true
	}
	h.cancelWhere(cause, func(_ string, e hubEntry) bool { return e.tokenID != "" && set[e.tokenID] })
}

// revokeAllTokens cancels every API-token request.
func (h *hub) revokeAllTokens(cause error) {
	h.cancelWhere(cause, func(_ string, e hubEntry) bool { return e.tokenID != "" })
}

func (h *hub) cancelWhere(cause error, match func(userID string, e hubEntry) bool) {
	h.mu.Lock()
	var cancels []context.CancelCauseFunc
	for uid, m := range h.subs {
		for id, e := range m {
			if match(uid, e) {
				cancels = append(cancels, e.cancel)
				delete(m, id)
			}
		}
		if len(m) == 0 {
			delete(h.subs, uid)
		}
	}
	h.mu.Unlock()
	for _, c := range cancels {
		c(cause)
	}
}

// snapshot returns the users with open session requests and the API
// tokens with open requests (token ID -> user).
func (h *hub) snapshot() (sessions map[string]struct{}, tokens map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sessions, tokens = map[string]struct{}{}, map[string]string{}
	for uid, m := range h.subs {
		for _, e := range m {
			if e.tokenID != "" {
				tokens[e.tokenID] = uid
			} else {
				sessions[uid] = struct{}{}
			}
		}
	}
	return sessions, tokens
}

func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.subs {
		n += len(m)
	}
	return n
}

// Audit event outcomes.
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
)

// AuditEvent is one security event (#30 defines the full record shape;
// the recorder adds time, client IP, user agent and request ID). It never
// carries secrets: no passwords, codes, seeds or tokens.
type AuditEvent struct {
	// Action is a stable key such as "auth.sign_in" or "user.disable".
	Action  string
	Outcome string
	// ActorUserID is the signed-in user (empty for anonymous attempts and
	// the owner-recovery CLI).
	ActorUserID string
	// TargetType/TargetID name the affected resource (user, invitation, ...).
	TargetType string
	TargetID   string
	// Reason is a short, non-sensitive failure class.
	Reason string
}

// Auditor records security events. #30 provides the append-only recorder;
// until then LogAuditor writes them to the structured log.
type Auditor interface {
	Record(ctx context.Context, e AuditEvent)
}

// LogAuditor writes audit events to the log.
type LogAuditor struct{ Logger *slog.Logger }

// Record implements Auditor.
func (a LogAuditor) Record(ctx context.Context, e AuditEvent) {
	l := a.Logger
	if l == nil {
		l = logging.FromContext(ctx)
	}
	attrs := []any{
		slog.String("action", e.Action), slog.String("outcome", e.Outcome),
		slog.String("request_id", logging.RequestID(ctx)),
	}
	if ip := requestinfo.ClientIP(ctx); ip.IsValid() {
		attrs = append(attrs, slog.String("client_ip", ip.String()))
	}
	for k, v := range map[string]string{"actor_user_id": e.ActorUserID, "target_type": e.TargetType, "target_id": e.TargetID, "reason": e.Reason} {
		if v != "" {
			attrs = append(attrs, slog.String(k, v))
		}
	}
	l.InfoContext(ctx, "audit", attrs...)
}
