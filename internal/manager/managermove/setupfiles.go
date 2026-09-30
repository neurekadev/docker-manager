package managermove

import (
	"context"
	"errors"
	"strings"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authsep"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// New setup files (docs/internal/architecture/manager-move.md, "New setup
// files"): the files are shown once, and the new server's enrollment token
// lives 24 hours. When the owner lost the files (a reload) or the token
// expired before the new server was set up, NewSetupFiles issues them
// again instead of making the owner cancel and start over.

// setupFilesAllowed: new files before the new manager was handed the
// state. open is the usual case; ready is safe too (the next handoff
// request must carry the new code). moving is refused: the new server's
// agent restarts with the new .env and would cut off the app that is
// moving; draining and later are refused: the handoff has begun.
func setupFilesAllowed(st domain.ManagerMoveState) bool {
	return st == domain.MoveOpen || st == domain.MoveReady
}

// NewSetupFiles issues the new server's files again (owner, recent
// step-up) with a new move code (same move; the old code stops working at
// once, so a new manager still started with the old .env is refused with
// move_code_invalid) and, unless the new server's agent already enrolled,
// a new enrollment token (the old one is revoked; same environment name).
// When the agent already enrolled its environment is kept: the new .env
// has no enrollment token and no environment name, and the agent keeps
// its credential in its volume (replace the .env in the same folder and
// run docker compose up -d). The last check-in is forgotten: the new
// manager must check in with the new code. The move's expiry does not
// change. Allowed in open and ready (setupFilesAllowed; else
// domain.ErrManagerMoveState); domain.ErrManagerMoveNotFound without a
// move.
func (s *Service) NewSetupFiles(ctx context.Context) (Created, error) {
	uid, err := s.requireOwner(ctx, true)
	if err != nil {
		return Created{}, err
	}
	if s.opts.Enrollments == nil || s.opts.Render == nil {
		return Created{}, errors.New("managermove: moving needs the agents service")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.expireDue(ctx); err != nil {
		return Created{}, err
	}
	m, found, err := store.ActiveManagerMove(ctx, s.db)
	if err != nil {
		return Created{}, err
	}
	if !found {
		return Created{}, domain.ErrManagerMoveNotFound
	}
	if !setupFilesAllowed(m.State) {
		return Created{}, domain.ErrManagerMoveState
	}
	from, previous := m.State, m.EnrollmentID
	name := hostOf(m.NewServerAddress)
	if m.EnrollmentID != "" {
		if en, err := s.opts.Enrollments.GetEnrollment(ctx, m.EnrollmentID); err == nil && en.EnvironmentName != "" {
			name = en.EnvironmentName
		}
	}
	if m.TargetEnvironmentID == "" && m.EnrollmentID != "" {
		// Revoke the old token unless it was used (then it is kept, and
		// its environment with it). A used token is not revoked: the
		// answer names its agent.
		if en, err := s.opts.Enrollments.RevokeEnrollment(ctx, m.EnrollmentID); err == nil && en.AgentID != "" {
			if a, err := s.opts.Enrollments.GetAgent(ctx, en.AgentID); err == nil {
				m.TargetEnvironmentID = a.EnvironmentID
			}
		}
	}
	keep := false
	if m.TargetEnvironmentID != "" {
		e, err := store.GetEnvironment(ctx, s.db, m.TargetEnvironmentID)
		switch {
		case err == nil && e.Status == domain.EnvironmentActive:
			keep, name = true, e.Name
		case err == nil, errors.Is(err, domain.ErrEnvironmentNotFound):
			// Removed or archived since: the agent enrolls again.
			m.TargetEnvironmentID = ""
		default:
			return Created{}, err
		}
	}
	token := ""
	if !keep {
		en, err := s.opts.Enrollments.CreateEnrollment(ctx, domain.EnrollmentSpec{Intent: domain.IntentNew, EnvironmentName: name,
			TTL: EnrollmentLifetime, CreatedBy: uid})
		if err != nil {
			return Created{}, err
		}
		m.EnrollmentID, token = en.Enrollment.ID, en.Token
	}
	minted, err := authsep.MintMoveCode(m.ID)
	if err != nil {
		return Created{}, s.undoEnrollment(ctx, keep, m.EnrollmentID, err)
	}
	sealed, err := s.opts.Keyring.Seal([]byte(minted.Token), SealContext(m.ID))
	if err != nil {
		return Created{}, s.undoEnrollment(ctx, keep, m.EnrollmentID, err)
	}
	now := s.now()
	m.CheckedInAt, m.HandoffAddress, m.UpdatedAt = nil, "", now
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := store.UpdateManagerMove(ctx, tx, &m, from); err != nil {
			return err
		}
		return store.SetManagerMoveSealedCode(ctx, tx, m.ID, sealed)
	})
	if err != nil {
		return Created{}, s.undoEnrollment(ctx, keep, m.EnrollmentID, err)
	}
	public := ""
	if s.opts.PublicURL != nil {
		public = strings.TrimSuffix(s.opts.PublicURL.String(), "/")
	}
	in := RenderInput{PublicURL: public, OldManagerURL: serverURL(m.ThisServerAddress), MoveCode: minted.Token}
	if !keep {
		in.EnrollmentToken, in.EnvironmentName = token, name
	}
	files := s.opts.Render(in)
	audit.AddTarget(ctx, domain.AuditTarget{Type: auditTargetType, ID: m.ID})
	audit.SetDetail(ctx, "state", string(m.State))
	audit.SetDetail(ctx, "enrollmentId", m.EnrollmentID)
	audit.SetDetail(ctx, "previousEnrollmentId", previous)
	audit.SetDetail(ctx, "agentEnrolled", keep)
	s.log.Info("new setup files for the move's new server: the previous move code no longer works", "move_id", m.ID,
		"agent_enrolled", keep)
	s.publish(m.ID)
	s.notify()
	return Created{Move: m, Files: files, StatusURL: serverURL(m.NewServerAddress), AgentEnrolled: keep}, nil
}

// undoEnrollment revokes an enrollment NewSetupFiles created before it
// failed, and returns err.
func (s *Service) undoEnrollment(ctx context.Context, kept bool, id string, err error) error {
	if !kept && id != "" {
		_, _ = s.opts.Enrollments.RevokeEnrollment(ctx, id)
	}
	return err
}
