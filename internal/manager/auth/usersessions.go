package auth

import (
	"context"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/auth/sessions"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// Signed-in devices (#16): every browser session has a device record
// (user_sessions) that its owner, and the instance owner, can list and
// sign out one by one.

// pendingStay returns the "Stay signed in" choice of the pending sign-in.
func (s *Service) pendingStay(ctx context.Context) bool {
	return s.kit.Sessions.GetBool(ctx, kPendingStay)
}

// ListMySessions lists the caller's signed-in devices, most recently
// active first; the caller's own is marked Current.
func (s *Service) ListMySessions(ctx context.Context) ([]domain.UserSession, error) {
	cur, err := s.session(ctx, false)
	if err != nil {
		return nil, err
	}
	return s.listSessions(ctx, cur.user.ID, cur.session.ID)
}

// RevokeMySession signs out one of the caller's devices and closes its
// open streams. Signing out the caller's own device signs the caller out.
func (s *Service) RevokeMySession(ctx context.Context, id string) error {
	cur, err := s.session(ctx, false)
	if err != nil {
		return err
	}
	if err := store.DeleteUserSession(ctx, s.db, id, cur.user.ID); err != nil {
		return err
	}
	s.record(ctx, "auth.session_revoke", OutcomeSuccess, cur.user.ID, "session", id, "")
	s.endDevices(ctx, []string{id})
	if id == cur.session.ID {
		return s.kit.Sessions.Destroy(ctx)
	}
	return nil
}

// RevokeMyOtherSessions signs out every device of the caller except the
// current one and returns how many it signed out.
func (s *Service) RevokeMyOtherSessions(ctx context.Context) (int, error) {
	cur, err := s.session(ctx, false)
	if err != nil {
		return 0, err
	}
	ids, err := store.DeleteOtherUserSessions(ctx, s.db, cur.user.ID, cur.session.ID)
	if err != nil {
		return 0, err
	}
	audit.SetDetail(ctx, "sessionCount", len(ids))
	s.record(ctx, "auth.session_revoke_others", OutcomeSuccess, cur.user.ID, "user", cur.user.ID, "")
	s.endDevices(ctx, ids)
	return len(ids), nil
}

// ListUserSessions lists an account's signed-in devices (owner).
func (s *Service) ListUserSessions(ctx context.Context, userID string) ([]domain.UserSession, error) {
	cur, err := s.requireOwner(ctx, false)
	if err != nil {
		return nil, err
	}
	if _, err := store.GetUser(ctx, s.db, userID); err != nil {
		return nil, err
	}
	return s.listSessions(ctx, userID, cur.session.ID)
}

// RevokeUserSession signs out one device of an account (owner) and closes
// its open streams.
func (s *Service) RevokeUserSession(ctx context.Context, userID, id string) error {
	cur, err := s.requireOwner(ctx, false)
	if err != nil {
		return err
	}
	if _, err := store.GetUser(ctx, s.db, userID); err != nil {
		return err
	}
	if err := store.DeleteUserSession(ctx, s.db, id, userID); err != nil {
		return err
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: "user", ID: userID})
	s.record(ctx, "user.session_revoke", OutcomeSuccess, cur.user.ID, "session", id, "")
	s.endDevices(ctx, []string{id})
	if id == cur.session.ID {
		return s.kit.Sessions.Destroy(ctx)
	}
	return nil
}

// listSessions returns userID's live devices with their expiry times,
// marking currentID. Devices that ended but were not swept yet are left
// out.
func (s *Service) listSessions(ctx context.Context, userID, currentID string) ([]domain.UserSession, error) {
	all, err := store.ListUserSessions(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := make([]domain.UserSession, 0, len(all))
	for _, sess := range all {
		s.fill(&sess)
		if !now.Before(sess.IdleExpiresAt) {
			continue
		}
		sess.Current = sess.ID == currentID
		out = append(out, sess)
	}
	return out, nil
}

// endDevices finishes signing out devices whose records were deleted: it
// deletes their stored sessions (they could no longer authenticate anyway)
// and closes their open requests, except the calling request itself.
func (s *Service) endDevices(ctx context.Context, ids []string) {
	if len(ids) == 0 {
		return
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	sm := s.kit.Sessions
	current := sm.GetString(ctx, kSessionID)
	_, err := sessions.RevokeWhere(context.WithoutCancel(ctx), sm, s.kit.SessionStore, func(sctx context.Context) bool {
		sid := sm.GetString(sctx, kSessionID)
		// The caller's own session is destroyed through its request (the
		// response then clears the cookie).
		return set[sid] && sid != current
	})
	if err != nil {
		s.log.Warn("delete signed-out sessions", "error", err)
	}
	s.hub.revokeSessions(ids, authz.ErrSessionEnded, hubEntryFrom(ctx))
}
