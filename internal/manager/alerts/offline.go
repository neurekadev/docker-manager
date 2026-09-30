package alerts

import (
	"context"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Offline alerts: an active environment offline longer than the grace
// period (Options.OfflineGrace) raises a critical alert; it resolves when
// the environment is back online. The grace runs from the environment's
// last connection change or from the manager's start, whichever is later:
// every environment is marked offline when the manager starts (no agent
// session survives a restart), so a restart raises nothing by itself. An
// archived (or removed) environment's alerts, of every kind, end silently.
// Nothing is evaluated while the manager moves (its agents are refused).

func offlineKey(env string) string { return "environment_offline/" + env }

// EvaluateOffline raises and resolves the offline alerts of every
// environment and ends the alerts of archived ones. It returns when the
// next environment's grace period ends (zero: none is waiting).
func (s *Service) EvaluateOffline(ctx context.Context) (time.Time, error) {
	if s.locked() {
		return time.Time{}, nil
	}
	now := s.now()
	var next time.Time
	err := s.inTx(ctx, func(ctx context.Context, tx bun.Tx) ([]domain.Alert, error) {
		var out []domain.Alert
		var err error
		out, next, err = evaluateOffline(ctx, tx, now, s.startedAt, s.opts.OfflineGrace)
		return out, err
	})
	return next, err
}

func evaluateOffline(ctx context.Context, db bun.IDB, now, startedAt time.Time, grace time.Duration) ([]domain.Alert, time.Time, error) {
	envs, err := store.ListEnvironments(ctx, db, domain.EnvironmentFilter{})
	if err != nil {
		return nil, time.Time{}, err
	}
	firing, err := store.FiringAlerts(ctx, db, "", "")
	if err != nil {
		return nil, time.Time{}, err
	}
	byEnv := map[string][]domain.Alert{}
	for _, a := range firing {
		if a.EnvironmentID != "" {
			byEnv[a.EnvironmentID] = append(byEnv[a.EnvironmentID], a)
		}
	}
	var out []domain.Alert
	var next time.Time
	active := map[string]bool{}
	for _, e := range envs {
		if e.Status != domain.EnvironmentActive {
			continue
		}
		active[e.ID] = true
		key := offlineKey(e.ID)
		if e.AgentID == "" {
			// Detached on purpose (its agent was removed): not a problem.
			if out, err = collect(out)(resolveKey(ctx, db, key, domain.AlertResolvedRemoved, now)); err != nil {
				return nil, time.Time{}, err
			}
			continue
		}
		if e.Online {
			if out, err = collect(out)(resolveKey(ctx, db, key, domain.AlertResolvedFixed, now)); err != nil {
				return nil, time.Time{}, err
			}
			continue
		}
		since := e.CreatedAt
		if e.ConnectionChangedAt != nil {
			since = *e.ConnectionChangedAt
		}
		base := since
		if startedAt.After(base) {
			base = startedAt
		}
		if due := base.Add(grace); now.Before(due) {
			if next.IsZero() || due.Before(next) {
				next = due
			}
			continue
		}
		o := Observation{
			Key: key, Kind: domain.NotifyEnvironmentOffline, Severity: domain.AlertCritical, EnvironmentID: e.ID,
			ResourceType: domain.AlertResourceEnvironment, ResourceID: e.ID, Title: e.Name + " is offline",
			Facts: map[string]string{"since": since.UTC().Format(time.RFC3339)}, Fingerprint: domain.Fingerprint("offline"),
		}
		if out, err = collect(out)(raise(ctx, db, o, now)); err != nil {
			return nil, time.Time{}, err
		}
	}
	// Archived or removed environments: every alert ends, silently.
	for env, as := range byEnv {
		if active[env] {
			continue
		}
		for _, a := range as {
			if out, err = collect(out)(resolve(ctx, db, a, domain.AlertResolvedArchived, now)); err != nil {
				return nil, time.Time{}, err
			}
		}
	}
	return out, next, nil
}
