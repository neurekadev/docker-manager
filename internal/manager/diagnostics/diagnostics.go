// Package diagnostics serves DockYard's own operability data (#34): the
// optional Prometheus-format metrics of the manager's internals (off by
// default, DOCKYARD_METRICS_ENABLED, capability system.metrics.read) and the
// owner-only support bundle (versions, redacted configuration,
// support-matrix checks, recent logs, agent states, audit chain
// verification, job queue summary). Neither ever contains a secret: they
// are assembled from allowlisted fields, and the recent log lines pass a
// secret scrubber on top of the logging rules.
package diagnostics

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"

	"github.com/neurekadev/dockyard/internal/buildinfo"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Setting is one configuration value as the operator set it (never a
// secret value: secrets are files whose paths are listed).
type Setting struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Environments reads environments and agents (*agents.Service).
type Environments interface {
	ListEnvironments(ctx context.Context, f domain.EnvironmentFilter) ([]domain.Environment, error)
	ListAgents(ctx context.Context, f domain.AgentFilter) ([]domain.Agent, error)
}

// AuditVerifier verifies the audit chain (*audit.Log).
type AuditVerifier interface {
	Verify(ctx context.Context) (audit.VerifyReport, error)
}

// Inventory is the last Engine inventory of an environment (#5).
type Inventory func(environmentID string) (protocol.EngineInventory, bool)

// Options configures the service.
type Options struct {
	DB     *bun.DB
	Clock  clock.Clock
	Logger *slog.Logger
	Build  buildinfo.Info
	// MetricsEnabled turns the metrics endpoint on (DOCKYARD_METRICS_ENABLED).
	MetricsEnabled bool
	// Settings is the configuration as shown in the support bundle.
	Settings []Setting
	// DatabasePath, MetricsPath and SnapshotDir locate the database files.
	DatabasePath, MetricsPath, SnapshotDir string
	// Migrations is the migration set the manager applied.
	Migrations   *migrate.Migrations
	Environments Environments
	// Sessions returns the connected agent sessions' environment IDs.
	Sessions func() []string
	// BusSubscribers returns the internal event bus's subscriber count.
	BusSubscribers func() int
	// SSEStreams returns the number of open event streams.
	SSEStreams func() int64
	Audit      AuditVerifier
	Inventory  Inventory
	// Logs holds the recent manager log lines.
	Logs *logging.Ring
}

// Service is the diagnostics service.
type Service struct{ o Options }

// New returns the service.
func New(o Options) (*Service, error) {
	if o.DB == nil || o.Environments == nil || o.Audit == nil {
		return nil, errors.New("diagnostics: DB, Environments and Audit are required")
	}
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Service{o: o}, nil
}

// MetricsEnabled reports whether the metrics endpoint is on.
func (s *Service) MetricsEnabled() bool { return s.o.MetricsEnabled }

// fileSize is the size of a SQLite database including its WAL (0 when
// absent).
func fileSize(path string) int64 {
	if path == "" {
		return 0
	}
	var n int64
	for _, suffix := range []string{"", "-wal"} {
		if st, err := os.Stat(path + suffix); err == nil {
			n += st.Size()
		}
	}
	return n
}
