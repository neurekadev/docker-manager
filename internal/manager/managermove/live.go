package managermove

import (
	"context"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Live updates (docs/internal/architecture/manager-move.md, "Live
// updates"): the move's pages follow the live stream instead of polling.
// The service publishes events.ManagerMoveUpdated (the owner's move: its
// ID only) whenever the move changes here, and events.ManagerMoveLockChanged
// (no ID, every signed-in user) when the lock the session reports
// changes. What changes elsewhere (the new server's agent enrolling or
// going online or offline, the old server's environment being archived,
// Move everything's progress) is heard on the bus and republished for the
// current move (followBus). A check-in that stops counting after
// CheckInFresh is announced by the Run loop (announceStaleCheckIn).

// publish announces that move id changed (nil-safe without a bus).
func (s *Service) publish(id string) {
	if s.opts.Bus == nil || id == "" {
		return
	}
	s.opts.Bus.Publish(events.Event{Type: events.ManagerMoveUpdated, ResourceType: events.ResourceManagerMove, ResourceID: id})
}

// publishLock announces that the move lock every session reads changed.
func (s *Service) publishLock() {
	if s.opts.Bus == nil {
		return
	}
	s.opts.Bus.Publish(events.Event{Type: events.ManagerMoveLockChanged, ResourceType: events.ResourceManagerMoveLock, ResourceID: "instance"})
}

// lockShown is the lock state GET /auth/session reports for a move state
// (LockStatus).
func lockShown(st domain.ManagerMoveState) string {
	switch st {
	case domain.MoveDraining, domain.MoveHandedOff:
		return LockMoving
	case domain.MoveConfirmed:
		return LockMoved
	}
	return LockNone
}

// published announces a change of move m from state from: the move, and
// the lock when the session's lock state changed with it.
func (s *Service) published(m domain.ManagerMove, from domain.ManagerMoveState) {
	s.publish(m.ID)
	if lockShown(from) != lockShown(m.State) {
		s.publishLock()
	}
}

// followBuffer is the bus subscription's buffer (the events it follows
// are rare; a dropped one is covered by the next).
const followBuffer = 64

// followed are the bus events that change what the move's view shows.
func followed(e events.Event) bool {
	switch e.Type {
	case events.EnvironmentOnline, events.EnvironmentOffline, events.EnvironmentArchived, events.EnvironmentReattached,
		events.EnrollmentUsed, events.EnrollmentRevoked, events.AgentEnrolled:
		return true
	case events.JobUpdated:
		return e.Job != nil && e.Job.Kind == moveKind
	}
	return false
}

// followBus republishes the current move when something its view shows
// changed elsewhere, until ctx ends.
func (s *Service) followBus(ctx context.Context) {
	if s.opts.Bus == nil {
		return
	}
	sub := s.opts.Bus.Subscribe(followBuffer, followed)
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-sub.C():
			if id := s.shownMove(ctx); id != "" {
				s.publish(id)
			}
			if e.Type == events.EnvironmentOnline {
				s.notify() // an agent of the arrived move may need its next step (secure.go)
			}
		}
	}
}

// shownMove is the ID of the move GET /manager/move shows: the open or
// in-progress move, else the move this manager arrived by ("" none).
func (s *Service) shownMove(ctx context.Context) string {
	if m, found, err := store.ActiveManagerMove(ctx, s.db); err == nil && found {
		return m.ID
	}
	if a, found, err := store.LatestArrivedManagerMove(ctx, s.db); err == nil && found {
		return a.ID
	}
	return ""
}

// checkInStaleAt is when the active move's last check-in stops counting
// (newServer.managerCheckedIn turns false); zero without a check-in.
func checkInStaleAt(m domain.ManagerMove) time.Time {
	if m.CheckedInAt == nil || !expirable(m.State) {
		return time.Time{}
	}
	return m.CheckedInAt.Add(CheckInFresh)
}

// checkInFresh reports whether a check-in at t still counts now.
func (s *Service) checkInFresh(t *time.Time) bool {
	return t != nil && s.opts.Clock.Now().Sub(*t) <= CheckInFresh
}

// announceStaleCheckIn publishes the move once its last check-in stopped
// counting (the new manager stopped asking), so the owner's page shows it
// without polling.
func (s *Service) announceStaleCheckIn(ctx context.Context) {
	m, found, err := store.ActiveManagerMove(ctx, s.db)
	if err != nil || !found {
		return
	}
	at := checkInStaleAt(m)
	if at.IsZero() || s.opts.Clock.Now().Sub(at) <= 0 {
		return
	}
	s.staleMu.Lock()
	seen := s.staleAnnounced.Equal(at)
	s.staleAnnounced = at
	s.staleMu.Unlock()
	if !seen {
		s.publish(m.ID)
	}
}
