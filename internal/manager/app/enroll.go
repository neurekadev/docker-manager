package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/neurekadev/dockyard/internal/buildinfo"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/db/migrations"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/agents"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/config"
	"github.com/neurekadev/dockyard/internal/manager/secrets"
	"github.com/neurekadev/dockyard/internal/manager/store"
)

// ErrNotInitialized means the data directory has no database yet.
var ErrNotInitialized = errors.New("the manager has not initialized its data directory yet; start it once first")

// CreateEnrollment creates an agent enrollment token directly in the
// manager's database, for `dockyard-manager enrollment create` run inside
// the manager container (docker compose exec). It is the headless way to
// enroll agents before the UI and owner accounts exist (#16): whoever can
// exec into the manager container already controls its data volume. The
// running manager picks the enrollment up (it reads enrollments from the
// database). A database with pending migrations is refused.
func CreateEnrollment(ctx context.Context, cfg config.Config, log *slog.Logger, spec domain.EnrollmentSpec) (domain.CreatedEnrollment, error) {
	if _, err := os.Stat(cfg.DatabasePath()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.CreatedEnrollment{}, ErrNotInitialized
		}
		return domain.CreatedEnrollment{}, err
	}
	db, err := store.Open(ctx, cfg.DatabasePath())
	if err != nil {
		return domain.CreatedEnrollment{}, err
	}
	defer func() { _ = db.Close() }()
	_, pending, err := store.Status(ctx, db, migrations.Migrations)
	if err != nil {
		return domain.CreatedEnrollment{}, err
	}
	if len(pending) > 0 {
		return domain.CreatedEnrollment{}, fmt.Errorf("the database has %d pending migrations; start the manager (it applies them) first", len(pending))
	}
	key, err := secrets.LoadKeyFile(cfg.SecretKeyFile)
	if err != nil {
		return domain.CreatedEnrollment{}, err
	}
	svc, err := agents.New(agents.Options{DB: db, Clock: clock.Real(), Logger: log, Keyring: secrets.NewKeyring(key),
		ManagerVersion: buildinfo.Get().Version, PublicURL: cfg.PublicURL})
	if err != nil {
		return domain.CreatedEnrollment{}, err
	}
	created, err := svc.CreateEnrollment(ctx, spec)
	if err != nil {
		return created, err
	}
	// The API records this as its capability (agent.enroll); the CLI runs
	// as the operator of the manager container, recorded as the service.
	trail, err := audit.New(audit.Options{DB: db, Clock: clock.Real(), Logger: log})
	if err == nil {
		err = trail.Record(ctx, domain.AuditEvent{Action: "agent.enroll", Actor: audit.ServiceActor(),
			Targets: []domain.AuditTarget{{Type: "agent_enrollment", ID: created.Enrollment.ID}},
			Details: map[string]any{"via": "cli", "intent": string(created.Enrollment.Intent)}})
	}
	if err != nil {
		return created, fmt.Errorf("record the enrollment in the audit log: %w", err)
	}
	return created, nil
}
