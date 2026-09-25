package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/db/migrations"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/auth"
	"github.com/neurekadev/dockyard/internal/manager/config"
	"github.com/neurekadev/dockyard/internal/manager/store"
)

// OwnerRecovery is the owner lockout break-glass (#16), run inside the
// manager container with access to the data volume:
//
//	docker exec dockyard-manager dockyard-manager owner-recovery
//
// It issues a one-time owner-recovery code (valid auth.OwnerRecoveryTTL),
// signs out every owner session (the running manager rejects them at the
// next request and closes open streams within auth.StreamSweepInterval) and
// records an audit event. Redeeming the code sets a new owner password and
// removes the owner's TOTP, passkeys and recovery codes. Anyone who can run
// it already controls the data volume.
func OwnerRecovery(ctx context.Context, cfg config.Config, log *slog.Logger, clk clock.Clock) (domain.IssuedCode, error) {
	if clk == nil {
		clk = clock.Real()
	}
	path := cfg.DatabasePath()
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.IssuedCode{}, fmt.Errorf("no DockYard database at %s (check %s)", path, config.EnvDataDir)
		}
		return domain.IssuedCode{}, err
	}
	db, err := store.Open(ctx, path)
	if err != nil {
		return domain.IssuedCode{}, err
	}
	defer func() { _ = db.Close() }()
	_, pending, err := store.Status(ctx, db, migrations.Migrations)
	if err != nil {
		return domain.IssuedCode{}, err
	}
	if len(pending) > 0 {
		return domain.IssuedCode{}, fmt.Errorf("the database has %d pending migrations; start this manager version once before recovering the owner", len(pending))
	}
	trail, err := audit.New(audit.Options{DB: db, Clock: clk, Logger: log})
	if err != nil {
		return domain.IssuedCode{}, err
	}
	return auth.IssueOwnerRecovery(ctx, db, auth.TrailAuditor{Recorder: trail, Logger: log}, clk.Now(), cfg.PublicURL)
}
