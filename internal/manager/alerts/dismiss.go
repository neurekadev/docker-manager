package alerts

import (
	"context"
	"errors"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Reading and dismissing alerts. A dismissal is instance-wide: the alert
// leaves the bell, the dashboard and the environment's notice for
// everyone but stays in the Alerts list (Dismissed) until it resolves; it
// opens again (and is sent again) when it gets worse. Who dismissed it and
// when is kept on the alert; the API operation is audited. Callers check
// the permission (alert.dismiss, scoped like the alert's source).

// Get returns one alert.
func (s *Service) Get(ctx context.Context, id string) (domain.Alert, error) {
	return store.GetAlert(ctx, s.db, id)
}

// List returns alerts matching f, newest first, before beforeID.
func (s *Service) List(ctx context.Context, f domain.AlertFilter, beforeID string, limit int) ([]domain.Alert, error) {
	return store.ListAlerts(ctx, s.db, f, beforeID, limit)
}

// userName is how a dismissal names its user (the display name, else the
// username).
func userName(ctx context.Context, db bun.IDB, userID string) string {
	u, err := store.GetUser(ctx, db, userID)
	if err != nil {
		return ""
	}
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

// Dismiss dismisses a firing alert for everyone. An alert already
// dismissed is returned unchanged; a resolved one is refused
// (domain.ErrAlertNotFiring).
func (s *Service) Dismiss(ctx context.Context, id, userID string) (domain.Alert, error) {
	out, err := s.DismissMany(ctx, []string{id}, userID, true)
	if err != nil {
		return domain.Alert{}, err
	}
	return out[0], nil
}

// DismissMany dismisses the given firing alerts for everyone and returns
// them (resolved ones are skipped unless strict, which refuses them with
// domain.ErrAlertNotFiring; unknown ones are domain.ErrAlertNotFound when
// strict, else skipped).
func (s *Service) DismissMany(ctx context.Context, ids []string, userID string, strict bool) ([]domain.Alert, error) {
	now := s.now()
	var out []domain.Alert
	err := s.inTx(ctx, func(ctx context.Context, tx bun.Tx) ([]domain.Alert, error) {
		name := userName(ctx, tx, userID)
		var changed []domain.Alert
		for _, id := range ids {
			a, err := store.GetAlert(ctx, tx, id)
			if err != nil {
				if strict || !errors.Is(err, domain.ErrAlertNotFound) {
					return nil, err
				}
				continue
			}
			if a.State != domain.AlertFiring {
				if strict {
					return nil, domain.ErrAlertNotFiring
				}
				continue
			}
			if a.DismissedAt != nil {
				out = append(out, a)
				continue
			}
			next := a
			next.DismissedAt, next.DismissedBy, next.DismissedByName = &now, userID, name
			next.UpdatedAt, next.Revision = now, a.Revision+1
			if err := store.UpdateAlert(ctx, tx, &next, a.Revision); err != nil {
				return nil, err
			}
			out = append(out, next)
			changed = append(changed, next)
		}
		return changed, nil
	})
	return out, err
}
