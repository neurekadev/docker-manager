package alerts

import (
	"context"
	"fmt"
	"slices"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Notifications and alert thresholds as the API reads and changes them.
// Callers check the permission: notifications are filtered by job.read on
// their job; the thresholds are the owner's (Settings → Notifications).

// Notifications returns notifications matching f, newest first, before
// beforeID.
func (s *Service) Notifications(ctx context.Context, f domain.NotificationFilter, beforeID string, limit int) ([]domain.Notification, error) {
	return store.ListNotifications(ctx, s.db, f, beforeID, limit)
}

// EnvironmentName names an environment ("" when unknown).
func (s *Service) EnvironmentName(ctx context.Context, id string) string {
	return environmentName(ctx, s.db, id)
}

// Settings returns the alert thresholds.
func (s *Service) Settings(ctx context.Context) (domain.AlertSettings, error) {
	return store.GetAlertSettings(ctx, s.db)
}

func fieldErr(field, message string) error { return &domain.FieldError{Field: field, Message: message} }

// validPair checks a warning and critical level (0: off): each within
// 0..maxValue, and warning below critical when both are on.
func validPair(field string, warning, critical, maxValue int) error {
	for _, v := range []struct {
		name  string
		value int
	}{{field + "Warning", warning}, {field + "Critical", critical}} {
		if v.value < 0 || v.value > maxValue {
			return fieldErr(v.name, fmt.Sprintf("must be 0 (off) to %d", maxValue))
		}
	}
	if warning > 0 && critical > 0 && warning >= critical {
		return fieldErr(field+"Warning", "must be below the critical level")
	}
	return nil
}

// validThresholds checks complete thresholds (prefix names the fields:
// "" for the defaults, "overrides[i]." for an environment's).
func validThresholds(prefix string, t domain.AlertThresholds) error {
	if err := validPair(prefix+"temperature", t.TemperatureWarning, t.TemperatureCritical, domain.MaxTemperatureThreshold); err != nil {
		return err
	}
	if err := validPair(prefix+"diskSpace", t.DiskSpaceWarning, t.DiskSpaceCritical, domain.MaxPercentThreshold); err != nil {
		return err
	}
	return validPair(prefix+"memory", t.MemoryWarning, t.MemoryCritical, domain.MaxPercentThreshold)
}

// UpdateSettings replaces the thresholds and overrides when the settings
// are still at revision (domain.ErrRevisionConflict otherwise). Every
// override names an existing environment once; with its defaults applied
// each must be valid. The change is audited (the values, no secrets).
func (s *Service) UpdateSettings(ctx context.Context, revision int64, next domain.AlertSettings) (domain.AlertSettings, error) {
	if err := validThresholds("", next.Thresholds); err != nil {
		return domain.AlertSettings{}, err
	}
	ids := make([]string, 0, len(next.Overrides))
	for i, o := range next.Overrides {
		if slices.Contains(ids, o.EnvironmentID) {
			return domain.AlertSettings{}, fieldErr(fmt.Sprintf("overrides[%d].environmentId", i), "this environment has an override already")
		}
		ids = append(ids, o.EnvironmentID)
		if err := validThresholds(fmt.Sprintf("overrides[%d].", i), o.Apply(next.Thresholds)); err != nil {
			return domain.AlertSettings{}, err
		}
	}
	envs, err := store.GetEnvironmentsByID(ctx, s.db, ids)
	if err != nil {
		return domain.AlertSettings{}, err
	}
	if len(envs) != len(ids) {
		for i, id := range ids {
			if !slices.ContainsFunc(envs, func(e domain.Environment) bool { return e.ID == id }) {
				return domain.AlertSettings{}, fieldErr(fmt.Sprintf("overrides[%d].environmentId", i), "no such environment")
			}
		}
	}
	slices.SortFunc(next.Overrides, func(a, b domain.AlertThresholdOverride) int {
		switch {
		case a.EnvironmentID < b.EnvironmentID:
			return -1
		case a.EnvironmentID > b.EnvironmentID:
			return 1
		}
		return 0
	})
	cur, err := store.GetAlertSettings(ctx, s.db)
	if err != nil {
		return domain.AlertSettings{}, err
	}
	now := s.now()
	if err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return store.ReplaceAlertSettings(ctx, tx, revision, next, now)
	}); err != nil {
		return domain.AlertSettings{}, err
	}
	audit.SetDiff(ctx, settingsView(cur), settingsView(next))
	s.Wake()
	return store.GetAlertSettings(ctx, s.db)
}

// settingsView is the audited form of the thresholds.
func settingsView(set domain.AlertSettings) map[string]any {
	view := func(t domain.AlertThresholds) map[string]any {
		return map[string]any{"temperatureWarning": t.TemperatureWarning, "temperatureCritical": t.TemperatureCritical,
			"diskSpaceWarning": t.DiskSpaceWarning, "diskSpaceCritical": t.DiskSpaceCritical,
			"memoryWarning": t.MemoryWarning, "memoryCritical": t.MemoryCritical}
	}
	overrides := map[string]any{}
	for _, o := range set.Overrides {
		overrides[o.EnvironmentID] = view(o.Apply(set.Thresholds))
	}
	return map[string]any{"thresholds": view(set.Thresholds), "overrides": overrides}
}
