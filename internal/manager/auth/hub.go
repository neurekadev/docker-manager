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
// SSE and WebSocket streams) per user, so ending a user's sessions also
// ends their open streams immediately.
type hub struct {
	mu   sync.Mutex
	next uint64
	subs map[string]map[uint64]hubEntry
}

type hubEntry struct {
	epoch  int64
	cancel context.CancelCauseFunc
}

func newHub() *hub { return &hub{subs: map[string]map[uint64]hubEntry{}} }

// register tracks cancel for userID's request made with session epoch.
func (h *hub) register(userID string, epoch int64, cancel context.CancelCauseFunc) func() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	id := h.next
	if h.subs[userID] == nil {
		h.subs[userID] = map[uint64]hubEntry{}
	}
	h.subs[userID][id] = hubEntry{epoch: epoch, cancel: cancel}
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.subs[userID], id)
		if len(h.subs[userID]) == 0 {
			delete(h.subs, userID)
		}
	}
}

// revoke cancels userID's requests whose epoch differs from keep (all of
// them when keep is -1): their sessions ended (authz.ErrSessionEnded).
func (h *hub) revoke(userID string, keep int64) { h.revokeCause(userID, keep, authz.ErrSessionEnded) }

// revokeCause cancels like revoke with the given cause, which streams
// report to the client (authz.CloseReason).
func (h *hub) revokeCause(userID string, keep int64, cause error) {
	h.mu.Lock()
	var cancels []context.CancelCauseFunc
	for id, e := range h.subs[userID] {
		if keep < 0 || e.epoch != keep {
			cancels = append(cancels, e.cancel)
			delete(h.subs[userID], id)
		}
	}
	if len(h.subs[userID]) == 0 {
		delete(h.subs, userID)
	}
	h.mu.Unlock()
	for _, c := range cancels {
		c(cause)
	}
}

func (h *hub) snapshot() map[string]struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]struct{}, len(h.subs))
	for id := range h.subs {
		out[id] = struct{}{}
	}
	return out
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
