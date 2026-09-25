package stacks

import (
	"context"
	"errors"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/store"
)

// Place moves a stack record to placement p inside db (the caller's
// transaction): the cut-over of an environment migration and its rollback
// (#35). The stack keeps its ID, revisions and display metadata; stack- and
// service-scoped permission rules follow its ID. A stack with the same
// project name or directory in the destination fails with
// domain.ErrStackNameTaken. Call Published after the transaction commits.
func (s *Service) Place(ctx context.Context, db bun.IDB, stackID string, p domain.StackPlacement) (domain.Stack, error) {
	st, err := store.GetStack(ctx, db, stackID)
	if err != nil {
		return st, err
	}
	p.Apply(&st)
	st.Revision++
	st.UpdatedAt = s.now()
	if err := store.UpdateStack(ctx, db, &st); err != nil {
		if errors.Is(err, domain.ErrStackNameTaken) {
			return st, domain.ErrStackNameTaken
		}
		return st, err
	}
	return st, nil
}

// Published announces a stack changed by a caller's transaction (Place).
func (s *Service) Published(st domain.Stack, attrs map[string]string) {
	s.publish(EventUpdated, st, attrs)
}

// FindByName returns the stack managing a Compose project in an environment
// (domain.ErrStackNotFound when none does).
func (s *Service) FindByName(ctx context.Context, environmentID, name string) (domain.Stack, error) {
	return store.FindStackByName(ctx, s.db, environmentID, name)
}
