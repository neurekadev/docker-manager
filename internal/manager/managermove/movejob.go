package managermove

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/migrations"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// manager.move (Move everything): an environment migration of every
// movable stack from the environment next to this manager to the new
// server's environment, run with the owner's principal and waited for;
// then the move is ready and the waiting manager gets the handoff. When
// the migration does not complete, the move goes back to open and the
// job fails with the migration's guidance; Move everything again moves
// what is left.

const moveKind = jobspec.ManagerMove

// Job error classes of manager.move.
const (
	// ClassMoveEnded: the move was cancelled or expired meanwhile.
	ClassMoveEnded = "manager_move_ended"
	// ClassAppsBlocked: the environment migration's preview has blockers.
	ClassAppsBlocked = "manager_move_apps_blocked"
	// ClassAppsNotMoved: the environment migration did not complete.
	ClassAppsNotMoved = "manager_move_apps_not_moved"
)

// moveJobInput is manager.move's input (never the code).
type moveJobInput struct {
	MoveID string `json:"moveId"`
}

// moveJobOutput is what the migrate step recorded.
type moveJobOutput struct {
	MigrationID string `json:"migrationId,omitempty"`
	// NoStacks: nothing to move (no environment next to the manager, or
	// no movable stack in it).
	NoStacks bool `json:"noStacks,omitempty"`
}

// StartRun starts manager.move (Move everything; owner, recent step-up):
// allowed for an open or ready move whose new server's agent is connected
// and whose waiting manager checked in (domain.ErrManagerMoveNewServerMissing
// otherwise). One at a time: a moving move answers
// domain.ErrManagerMoveState.
func (s *Service) StartRun(ctx context.Context) (domain.Job, error) {
	if _, err := s.requireOwner(ctx, true); err != nil {
		return domain.Job{}, err
	}
	p, ok := authz.PrincipalFrom(ctx)
	if !ok {
		return domain.Job{}, domain.ErrNotAuthenticated
	}
	if s.opts.Jobs == nil {
		return domain.Job{}, jobs.ErrManagerMoved
	}
	active, found, err := store.ActiveManagerMove(ctx, s.db)
	if err != nil {
		return domain.Job{}, err
	}
	if !found {
		return domain.Job{}, domain.ErrManagerMoveNotFound
	}
	active = s.resolveTarget(ctx, active)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.expireDue(ctx); err != nil {
		return domain.Job{}, err
	}
	m, err := store.GetManagerMove(ctx, s.db, active.ID)
	if err != nil {
		return domain.Job{}, err
	}
	if m.State != domain.MoveOpen && m.State != domain.MoveReady {
		return domain.Job{}, domain.ErrManagerMoveState
	}
	if m.TargetEnvironmentID == "" || !s.online(m.TargetEnvironmentID) || m.CheckedInAt == nil ||
		s.opts.Clock.Now().Sub(*m.CheckedInAt) > CheckInFresh {
		return domain.Job{}, domain.ErrManagerMoveNewServerMissing
	}
	if m.SourceEnvironmentID == "" {
		if e, ok := s.colocatedEnvironment(ctx); ok && e.ID != m.TargetEnvironmentID {
			m.SourceEnvironmentID = e.ID
		}
	}
	// The state changes before the job exists: its first step checks it.
	from := m.State
	m.State, m.ReadyAt, m.MoveJobID, m.MigrationID, m.UpdatedAt = domain.MoveMoving, nil, "", "", s.now()
	if err := store.UpdateManagerMove(ctx, s.db, &m, from); err != nil {
		return domain.Job{}, err
	}
	j, _, err := s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: moveKind, Principal: p,
		Targets: []domain.JobTarget{{Type: domain.TargetManager, ID: "instance"}}, Input: moveJobInput{MoveID: m.ID}})
	if err != nil {
		m.State, m.UpdatedAt = from, s.now()
		if uerr := store.UpdateManagerMove(ctx, s.db, &m, domain.MoveMoving); uerr != nil {
			s.log.Error("could not put the move back after manager.move was refused", "move_id", m.ID, "error", uerr)
		}
		return domain.Job{}, err
	}
	m.MoveJobID, m.UpdatedAt = j.ID, s.now()
	if err := store.UpdateManagerMove(ctx, s.db, &m, domain.MoveMoving); err != nil && !errors.Is(err, domain.ErrManagerMoveState) {
		return j, err
	}
	s.publish(m.ID)
	audit.AddTarget(ctx, domain.AuditTarget{Type: auditTargetType, ID: m.ID})
	audit.SetDetail(ctx, "sourceEnvironmentId", m.SourceEnvironmentID)
	audit.SetDetail(ctx, "targetEnvironmentId", m.TargetEnvironmentID)
	return j, nil
}

func (s *Service) moveExecutor() jobexec.Executor {
	return jobexec.Executor{Kind: moveKind, Steps: map[string]jobexec.StepFunc{
		"migrate": s.stepMigrate,
		"ready":   s.stepReady,
	}}
}

// principalOf is the principal a job runs with (its initiator).
func principalOf(j domain.Job) authz.Principal {
	switch j.Origin {
	case domain.OriginAPIToken:
		return authz.Principal{Kind: authz.KindAPIToken, UserID: j.InitiatorUserID, TokenID: j.InitiatorTokenID}
	case domain.OriginManual:
		return authz.Principal{Kind: authz.KindUser, UserID: j.InitiatorUserID}
	}
	return authz.Service()
}

// movingMove reads the move of a manager.move step; it must still be
// moving.
func (s *Service) movingMove(ctx context.Context, sc *jobexec.StepContext) (domain.ManagerMove, error) {
	var in moveJobInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return domain.ManagerMove{}, err
	}
	m, err := store.GetManagerMove(ctx, s.db, in.MoveID)
	if err != nil {
		return domain.ManagerMove{}, err
	}
	if m.State != domain.MoveMoving {
		return m, refuse(ClassMoveEnded, "the move is "+string(m.State)+", no longer moving",
			"Nothing more moves. Start the move again from Settings → Move to a new server if you still want to move.")
	}
	return m, nil
}

// stepMigrate starts the environment migration (once: a resumed step
// finds its ID in the output) and waits for it.
func (s *Service) stepMigrate(ctx context.Context, sc *jobexec.StepContext) error {
	m, err := s.movingMove(ctx, sc)
	if err != nil {
		return err
	}
	var out moveJobOutput
	_ = json.Unmarshal(sc.Output(), &out)
	if out.MigrationID == "" && !out.NoStacks {
		if out, err = s.startMigration(ctx, sc, m); err != nil {
			return err
		}
		if err := sc.SetOutput(ctx, out); err != nil {
			return err
		}
	}
	if out.NoStacks {
		sc.Progress(ctx, 90, "no app to move")
		return nil
	}
	sc.Progress(ctx, 10, "moving the apps to the new server")
	done, err := s.waitMigration(ctx, sc, out.MigrationID)
	if err != nil {
		return err
	}
	if done.State != domain.JobSucceeded {
		recovery := done.Recovery
		if recovery == "" {
			recovery = "See the environment migration for the cause."
		}
		return refuse(ClassAppsNotMoved, fmt.Sprintf("the environment migration ended %s (%s)", done.State, done.ErrorClass),
			recovery+" Then press Move everything again: it moves what is left. Docker Manager itself has not moved.")
	}
	return nil
}

// startMigration starts the environment migration of every movable stack
// from the environment next to this manager to the new server's.
func (s *Service) startMigration(ctx context.Context, sc *jobexec.StepContext, m domain.ManagerMove) (moveJobOutput, error) {
	if m.SourceEnvironmentID == "" || s.opts.Migrations == nil {
		return moveJobOutput{NoStacks: true}, nil
	}
	j, err := s.opts.Jobs.Get(ctx, sc.JobID)
	if err != nil {
		return moveJobOutput{}, err
	}
	mj, _, err := s.opts.Migrations.StartEnvironment(ctx, principalOf(j), m.SourceEnvironmentID,
		migrations.EnvironmentRequest{TargetEnvironmentID: m.TargetEnvironmentID, IdempotencyKey: "manager-move-" + sc.JobID})
	var blocked *migrations.EnvironmentBlockedError
	switch {
	case errors.As(err, &blocked):
		if onlyNoStacks(blocked.Plan) {
			return moveJobOutput{NoStacks: true}, nil
		}
		return moveJobOutput{}, refuse(ClassAppsBlocked, "the apps cannot move: "+blockerSummary(blocked.Plan),
			"Open Environments → the old server → Migrate environment to the new server's environment to see what blocks it, fix it, "+
				"then press Move everything again.")
	case err != nil:
		return moveJobOutput{}, err
	}
	s.mu.Lock()
	cur, gerr := store.GetManagerMove(ctx, s.db, m.ID)
	if gerr == nil && cur.State == domain.MoveMoving {
		cur.MigrationID, cur.UpdatedAt = mj.ID, s.now()
		if gerr = store.UpdateManagerMove(ctx, s.db, &cur, domain.MoveMoving); gerr == nil {
			s.publish(cur.ID)
		}
	}
	s.mu.Unlock()
	if gerr != nil {
		s.log.Warn("could not record the move's environment migration", "move_id", m.ID, "migration_id", mj.ID, "error", gerr)
	}
	return moveJobOutput{MigrationID: mj.ID}, nil
}

// onlyNoStacks: the plan's only blocker is that nothing can move.
func onlyNoStacks(p migrations.EnvironmentPlan) bool {
	if len(p.Stacks) > 0 {
		return false
	}
	for _, b := range p.Blockers {
		if b.Code != migrations.FindingNoStacks {
			return false
		}
	}
	return true
}

// blockerSummary lists a plan's blockers (their messages, bounded).
func blockerSummary(p migrations.EnvironmentPlan) string {
	var msgs []string
	for _, b := range p.Blockers {
		msgs = append(msgs, b.Message)
	}
	for _, st := range p.Stacks {
		for _, b := range st.Plan.Blockers {
			msgs = append(msgs, st.Name+": "+b.Message)
		}
	}
	s := strings.Join(msgs, "; ")
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return s
}

// migrationPoll is how often the step reads the migration besides its
// change notifications.
const migrationPoll = 5 * time.Second

// waitMigration waits for the environment migration job, reporting its
// progress; a cancellation of manager.move cancels it.
func (s *Service) waitMigration(ctx context.Context, sc *jobexec.StepContext, id string) (domain.Job, error) {
	ch, stop := s.opts.Jobs.Subscribe(id)
	defer stop()
	t := s.opts.Clock.NewTicker(migrationPoll)
	defer t.Stop()
	cancelled, last := false, ""
	for {
		j, err := s.opts.Jobs.Get(ctx, id)
		if err != nil {
			return j, err
		}
		if j.State.Terminal() {
			return j, nil
		}
		if !cancelled && sc.CancelRequested() {
			cancelled = true
			if _, err := s.opts.Jobs.Cancel(ctx, id); err != nil {
				s.log.Warn("could not cancel the move's environment migration", "job_id", sc.JobID, "migration_id", id, "error", err)
			}
		}
		moved, total, current := s.migrationProgress(ctx, id)
		if msg := fmt.Sprintf("%d of %d apps moved %s", moved, total, current); msg != last && total > 0 {
			last = msg
			sc.Progress(ctx, 10+80*moved/total, strings.TrimSpace(fmt.Sprintf("%d of %d apps moved", moved, total)))
		}
		select {
		case <-ch:
		case <-t.C():
		case <-ctx.Done():
			return j, ctx.Err()
		}
	}
}

// stepReady makes the move ready: the waiting manager's next request gets
// the handoff.
func (s *Service) stepReady(ctx context.Context, sc *jobexec.StepContext) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.movingMove(ctx, sc)
	if err != nil {
		return err
	}
	now := s.now()
	m.State, m.ReadyAt, m.UpdatedAt = domain.MoveReady, &now, now
	if err := store.UpdateManagerMove(ctx, s.db, &m, domain.MoveMoving); err != nil {
		return err
	}
	s.publish(m.ID)
	s.log.Warn("every app moved: Docker Manager hands itself over when the new manager asks next", "move_id", m.ID)
	return nil
}

// onMoveFinished puts a move whose manager.move did not succeed back to
// open (inside the job's finishing transaction: db only, no service
// lock; the job's own change, followed on the bus, announces the move
// once it is committed).
func (s *Service) onMoveFinished(ctx context.Context, db bun.IDB, j domain.Job) error {
	if j.State == domain.JobSucceeded {
		return nil
	}
	var in moveJobInput
	if json.Unmarshal(j.Input, &in) != nil || in.MoveID == "" {
		return nil
	}
	m, err := store.GetManagerMove(ctx, db, in.MoveID)
	if err != nil || m.State != domain.MoveMoving {
		return nil
	}
	m.State, m.UpdatedAt = domain.MoveOpen, s.now()
	if m.MoveJobID == "" {
		m.MoveJobID = j.ID
	}
	if err := store.UpdateManagerMove(ctx, db, &m, domain.MoveMoving); err != nil && !errors.Is(err, domain.ErrManagerMoveState) {
		return err
	}
	s.log.Warn("the apps did not all move: the move waits for Move everything again", "move_id", m.ID, "job_id", j.ID,
		"state", string(j.State), "class", j.ErrorClass)
	return nil
}
