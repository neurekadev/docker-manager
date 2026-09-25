package app

import (
	"context"
	"log/slog"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/buildinfo"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/diagnostics"
	"github.com/neurekadev/dockyard/internal/manager/server/sse"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// withLogRing tees the manager's logger into an in-memory ring of recent
// lines for the support bundle (#34), unless opts already has one.
func withLogRing(opts Options) Options {
	if opts.LogRing != nil {
		return opts
	}
	opts.LogRing = logging.NewRing(0)
	opts.Logger = slog.New(logging.Tee(opts.Logger.Handler(), opts.LogRing.Handler(opts.Config.LogLevel)))
	return opts
}

// newDiagnostics builds the metrics and support bundle service (#34).
func (m *Manager) newDiagnostics() (*diagnostics.Service, error) {
	cfg := m.opts.Config
	settings := []diagnostics.Setting{}
	for _, s := range cfg.Settings() {
		settings = append(settings, diagnostics.Setting{Name: s.Name, Value: s.Value})
	}
	hub := m.agents.Hub()
	return diagnostics.New(diagnostics.Options{
		DB: m.db, Clock: m.opts.Clock, Logger: m.opts.Logger.With("component", "diagnostics"), Build: buildinfo.Get(),
		MetricsEnabled: cfg.MetricsEnabled, Settings: settings,
		DatabasePath: cfg.DatabasePath(), MetricsPath: cfg.MetricsPath(), SnapshotDir: cfg.SnapshotDir(),
		Migrations: m.opts.Migrations, Environments: m.agents, Audit: m.audit,
		Sessions: func() []string {
			var ids []string
			for _, s := range hub.Sessions() {
				ids = append(ids, s.EnvironmentID)
			}
			return ids
		},
		BusSubscribers: m.events.Subscribers,
		SSEStreams:     sse.Open,
		Inventory: func(envID string) (protocol.EngineInventory, bool) {
			inv, ok := m.observe.Inventory(envID)
			return inv.EngineInventory, ok
		},
		Logs: m.opts.LogRing,
	})
}

// Operability (#34): environment removal and diagnostics wiring.

// AuditEnvironmentRulesRemoved records the permission rules removed
// because their environment was archived.
const AuditEnvironmentRulesRemoved = "environment.permission_rules_remove"

// maxAuditedRules bounds the rule list in the audit record's details (the
// count is always complete).
const maxAuditedRules = 200

// archiveEnvironment is the agents service's archive hook: in the archive
// transaction it removes the permission rules scoped to the environment
// and records them in the audit trail; the affected users' requests and
// streams end once the transaction committed.
func (m *Manager) archiveEnvironment(ctx context.Context, tx bun.Tx, env domain.Environment) (func(), error) {
	removed, users, err := m.perms.ForgetEnvironment(ctx, tx, env.ID)
	if err != nil || len(removed) == 0 {
		return nil, err
	}
	rules := make([]map[string]any, 0, min(len(removed), maxAuditedRules))
	for _, r := range removed[:min(len(removed), maxAuditedRules)] {
		rule := map[string]any{"subject": r.SubjectKind, "subjectId": r.SubjectID, "capability": r.Rule.Capability,
			"effect": string(r.Rule.Effect), "scope": r.Rule.Scope.Kind}
		if r.Rule.Scope.Kind == domain.ScopeKindResource {
			rule["resourceType"], rule["resourceId"] = r.Rule.Scope.ResourceType, r.Rule.Scope.ResourceID
		}
		rules = append(rules, rule)
	}
	if err := m.audit.RecordTx(ctx, tx, domain.AuditEvent{Action: AuditEnvironmentRulesRemoved, Actor: audit.ActorFromContext(ctx),
		EnvironmentID: env.ID, Targets: []domain.AuditTarget{{Type: "environment", ID: env.ID, EnvironmentID: env.ID}},
		Details: map[string]any{"count": len(removed), "rules": rules}}); err != nil {
		return nil, err
	}
	audit.SetDetail(ctx, "permissionRulesRemoved", len(removed))
	m.opts.Logger.Info("permission rules scoped to an archived environment removed", "environment_id", env.ID, "count", len(removed))
	return func() { m.perms.AccessChanged(ctx, users) }, nil
}
