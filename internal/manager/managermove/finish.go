package managermove

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/backups"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// FinishArrival completes a move applied at this start (the marker of kind
// move written by waiting mode): the sessions, API tokens and agents of
// the copy are kept (this manager is the same instance), the arrived move
// records the old manager's address and keeps the move code sealed for
// the confirmation, system.move is audited and the marker is removed.
// Repeatable until the marker is gone. The confirmation runs in Run.
func (s *Service) FinishArrival(ctx context.Context, mk *backups.RestoreMarker) error {
	if mk == nil || mk.Kind != backups.RestoreKindMove {
		return nil
	}
	if _, err := s.opts.Keyring.Open(mk.SealedMoveCode, SealContext(mk.MoveID)); err != nil {
		return fmt.Errorf("the staged move code does not open with the moved secret key: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := store.GetManagerMove(ctx, s.db, mk.MoveID)
	if err != nil {
		return err
	}
	if m.State != domain.MoveArrived {
		return fmt.Errorf("the moved database records move %s as %s, not arrived", m.ID, m.State)
	}
	now := s.now()
	m.SourceURL, m.UpdatedAt = mk.SourceURL, now
	if m.ArrivedAt == nil {
		m.ArrivedAt = &now
	}
	if err := store.UpdateManagerMove(ctx, s.db, &m, domain.MoveArrived); err != nil {
		return err
	}
	if m.ConfirmedAt == nil {
		if err := store.SetManagerMoveSealedCode(ctx, s.db, m.ID, mk.SealedMoveCode); err != nil {
			return err
		}
	}
	host := ""
	if u, err := url.Parse(mk.SourceURL); err == nil {
		host = u.Host
	}
	if s.opts.Audit != nil {
		if err := s.opts.Audit.Record(ctx, domain.AuditEvent{Category: domain.AuditSystem, Action: "system.move", Actor: audit.ServiceActor(),
			Outcome: domain.AuditSuccess, JobID: mk.JobID, Targets: []domain.AuditTarget{{Type: auditTargetType, ID: m.ID}},
			Details: map[string]any{"moveId": m.ID, "sourceHost": host, "generation": s.opts.Instance.Generation, "fromVersion": mk.App.Version,
				"schema": mk.SchemaLatest}}); err != nil {
			return err
		}
	}
	s.log.Warn("this manager now runs the moved instance: sessions, API tokens and agents were kept; confirming the move to the old manager",
		"move_id", m.ID, "source_host", host, "generation", s.opts.Instance.Generation)
	if err := backups.FinishedRestore(s.opts.DataDir); err != nil {
		return err
	}
	s.confirmMu.Lock()
	s.nextConfirm, s.confirmBackoff = s.opts.Clock.Now(), 0
	s.confirmMu.Unlock()
	s.notify()
	return nil
}

// StartConfirming schedules the confirmation of an arrived, unconfirmed
// move (at start, also after a restart that interrupted it).
func (s *Service) StartConfirming(ctx context.Context) error {
	a, found, err := store.LatestArrivedManagerMove(ctx, s.db)
	if err != nil || !found || a.ConfirmedAt != nil {
		return err
	}
	sealed, err := store.ManagerMoveSealedCode(ctx, s.db, a.ID)
	if err != nil || sealed == "" {
		return err
	}
	s.confirmMu.Lock()
	s.nextConfirm, s.confirmBackoff = s.opts.Clock.Now(), 0
	s.confirmMu.Unlock()
	s.notify()
	return nil
}

// Confirmation outcome classes (ManagerMove.ConfirmError).
const (
	ConfirmUnreachable  = "unreachable"
	ConfirmCodeInvalid  = "code_invalid"
	ConfirmStateRefused = "state_refused"
	ConfirmClockSkew    = "clock_skew"
)

// confirmDue calls the old manager's confirmation when it is due. It
// stops once the old manager answered for good: confirmed, or refused
// the code or the state (it was resumed, or the address now reaches this
// manager); other failures retry with backoff.
func (s *Service) confirmDue(ctx context.Context) {
	s.confirmMu.Lock()
	next := s.nextConfirm
	s.confirmMu.Unlock()
	if next.IsZero() || s.opts.Clock.Now().Before(next) {
		return
	}
	done, err := s.ConfirmOnce(ctx)
	if err != nil && ctx.Err() == nil {
		s.log.Error("could not confirm the move to the old manager", "error", err)
	}
	s.confirmMu.Lock()
	defer s.confirmMu.Unlock()
	if done {
		s.nextConfirm, s.confirmBackoff = time.Time{}, 0
		return
	}
	s.confirmBackoff = min(max(s.confirmBackoff*2, confirmBackoffMin), confirmBackoffMax)
	s.nextConfirm = s.opts.Clock.Now().Add(s.confirmBackoff)
}

// ConfirmOnce makes one confirmation attempt and records its outcome on
// the arrived move. done is true when nothing is left to try.
func (s *Service) ConfirmOnce(ctx context.Context) (done bool, err error) {
	a, found, err := store.LatestArrivedManagerMove(ctx, s.db)
	if err != nil {
		return false, err
	}
	if !found || a.ConfirmedAt != nil || a.ConfirmAcknowledgedAt != nil {
		return true, nil
	}
	sealed, err := store.ManagerMoveSealedCode(ctx, s.db, a.ID)
	if err != nil {
		return false, err
	}
	if sealed == "" {
		return true, nil
	}
	code, err := s.opts.Keyring.Open(sealed, SealContext(a.ID))
	if err != nil {
		return true, fmt.Errorf("open the sealed move code: %w", err)
	}
	class, terminal := s.callConfirm(ctx, a.SourceURL, string(code))
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Read the move again: the owner may have acknowledged meanwhile.
	if a, err = store.GetManagerMove(ctx, s.db, a.ID); err != nil {
		return false, err
	}
	if a.ConfirmAcknowledgedAt != nil {
		terminal = true
	}
	now := s.now()
	a.ConfirmAttempts++
	a.LastConfirmAt, a.ConfirmError, a.UpdatedAt = &now, class, now
	if class == "" {
		a.ConfirmedAt = &now
	}
	if err := store.UpdateManagerMove(ctx, s.db, &a, domain.MoveArrived); err != nil {
		return false, err
	}
	s.publish(a.ID)
	switch {
	case class == "":
		if err := store.SetManagerMoveSealedCode(ctx, s.db, a.ID, ""); err != nil {
			return true, err
		}
		s.log.Info("the old manager confirmed the move", "move_id", a.ID)
		return true, nil
	case terminal:
		if err := store.SetManagerMoveSealedCode(ctx, s.db, a.ID, ""); err != nil {
			return true, err
		}
		s.log.Warn("the old manager refused the move confirmation; check that it is not running the instance any more",
			"move_id", a.ID, "class", class)
		return true, nil
	}
	s.log.Warn("the old manager did not confirm the move yet; retrying", "move_id", a.ID, "class", class, "attempts", a.ConfirmAttempts)
	return false, nil
}

// callConfirm posts the confirmation, signed with the code. class is ""
// on success; terminal reports an answer that retrying cannot change.
func (s *Service) callConfirm(ctx context.Context, source, code string) (class string, terminal bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(source, "/")+ConfirmPath, nil)
	if err != nil {
		return ConfirmUnreachable, true
	}
	auth, err := SignRequest(code, http.MethodPost, ConfirmPath, s.opts.Clock.Now())
	if err != nil {
		return ConfirmCodeInvalid, true
	}
	req.Header.Set("Authorization", auth)
	resp, err := s.http.Do(req)
	if err != nil {
		return ConfirmUnreachable, false
	}
	defer func() { _ = resp.Body.Close() }()
	var e apiError
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
	switch {
	case resp.StatusCode == http.StatusOK:
		return "", true
	case resp.StatusCode == http.StatusUnauthorized && e.Code == "move_clock_skew":
		return ConfirmClockSkew, false
	case resp.StatusCode == http.StatusUnauthorized:
		return ConfirmCodeInvalid, true
	case resp.StatusCode == http.StatusConflict:
		return ConfirmStateRefused, true
	}
	return "http_" + strconv.Itoa(resp.StatusCode), false
}

// AcknowledgeConfirmation records the owner's statement (recent step-up)
// that the old manager is stopped or no longer uses this instance,
// although it never confirmed the move (it refused, or cannot be
// reached): "Move complete" counts the confirmation as done and the
// confirmation stops (the sealed move code is forgotten). Allowed for the
// arrived, unconfirmed move after at least one failed attempt
// (domain.ErrManagerMoveState otherwise, domain.ErrManagerMoveNotFound
// without an arrived move); repeating it is harmless.
func (s *Service) AcknowledgeConfirmation(ctx context.Context) (View, error) {
	if _, err := s.requireOwner(ctx, true); err != nil {
		return View{}, err
	}
	s.mu.Lock()
	a, err := s.acknowledge(ctx)
	s.mu.Unlock()
	if err != nil {
		return View{}, err
	}
	s.confirmMu.Lock()
	s.nextConfirm, s.confirmBackoff = time.Time{}, 0
	s.confirmMu.Unlock()
	audit.AddTarget(ctx, domain.AuditTarget{Type: auditTargetType, ID: a.ID})
	audit.SetDetail(ctx, "confirmError", a.ConfirmError)
	audit.SetDetail(ctx, "confirmAttemptCount", a.ConfirmAttempts)
	c, err := s.complete(ctx, a)
	if err != nil {
		return View{}, err
	}
	return View{Move: a, Complete: &c}, nil
}

// acknowledge marks the arrived move's confirmation acknowledged (under mu).
func (s *Service) acknowledge(ctx context.Context) (domain.ManagerMove, error) {
	a, found, err := store.LatestArrivedManagerMove(ctx, s.db)
	if err != nil {
		return a, err
	}
	if !found {
		return a, domain.ErrManagerMoveNotFound
	}
	if a.ConfirmAcknowledgedAt != nil {
		return a, nil
	}
	if a.ConfirmedAt != nil || a.ConfirmError == "" {
		return a, domain.ErrManagerMoveState
	}
	now := s.now()
	a.ConfirmAcknowledgedAt, a.UpdatedAt = &now, now
	if err := store.UpdateManagerMove(ctx, s.db, &a, domain.MoveArrived); err != nil {
		return a, err
	}
	if err := store.SetManagerMoveSealedCode(ctx, s.db, a.ID, ""); err != nil {
		return a, err
	}
	s.publish(a.ID)
	s.log.Warn("the owner acknowledged that the old manager no longer runs the instance; the move confirmation stops",
		"move_id", a.ID, "class", a.ConfirmError)
	return a, nil
}
