package app

import (
	"context"
	"strings"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/buildinfo"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/agents"
	"github.com/neurekadev/docker-manager/internal/manager/backups"
	"github.com/neurekadev/docker-manager/internal/manager/managermove"
	"github.com/neurekadev/docker-manager/internal/manager/movelock"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// decideWaiting starts waiting mode (docs/internal/architecture/
// manager-move.md) when DOCKER_MANAGER_MOVE_FROM and
// DOCKER_MANAGER_MOVE_CODE are set on an empty data directory: no owner
// and no environment. The lock closes everything but the move status and
// refuses agents. With data, the variables are ignored with a warning.
func (m *Manager) decideWaiting(ctx context.Context) (*managermove.WaitingConfig, error) {
	cfg, log := m.opts.Config, m.opts.Logger
	if !cfg.Move.Set() {
		return nil, nil
	}
	_, hasOwner, err := store.OwnerID(ctx, m.db)
	if err != nil {
		return nil, err
	}
	envs, err := store.ListEnvironments(ctx, m.db, domain.EnvironmentFilter{Limit: 1})
	if err != nil {
		return nil, err
	}
	if hasOwner || len(envs) > 0 {
		log.Warn("DOCKER_MANAGER_MOVE_FROM and DOCKER_MANAGER_MOVE_CODE are set, but this Docker Manager already has an owner or " +
			"environments: they are ignored; remove them from .env")
		return nil, nil
	}
	m.moveLock.Set(movelock.Waiting)
	log.Warn("waiting for the move from the old Docker Manager: every route but the move status is closed until the move is done",
		"from", cfg.Move.From.String())
	return &managermove.WaitingConfig{From: cfg.Move.From, Code: string(cfg.Move.Code)}, nil
}

// startMoves creates the manager-move service
// (docs/internal/architecture/manager-move.md). It runs before the job
// engine's recovery: manager.move and its finish hook must be registered
// by then.
func (m *Manager) startMoves(waiting *managermove.WaitingConfig) error {
	cfg, log := m.opts.Config, m.opts.Logger
	proxies := make([]string, 0, len(cfg.TrustedProxies))
	for _, p := range cfg.TrustedProxies {
		proxies = append(proxies, p.String())
	}
	var err error
	m.moves, err = managermove.New(managermove.Options{
		DB: m.db, Keyring: m.keyring, Clock: m.opts.Clock, Logger: log.With("component", "managermove"), Lock: m.moveLock,
		Jobs: m.jobs, Guard: m.identity, Audit: m.audit, Instance: m.instance, DataDir: cfg.DataDir, PublicURL: cfg.PublicURL,
		TrustedProxies: strings.Join(proxies, ","), Build: buildinfo.Get(), HTTPClient: m.opts.MoveHTTPClient,
		RequestRestart: m.RequestRestart, Enrollments: m.agents, Hub: m.agents.Hub(), Migrations: m.migrations,
		Render: func(in managermove.RenderInput) managermove.Files {
			compose, env := agents.MoveFiles(agents.MoveFilesInput{PublicURL: in.PublicURL, TrustedProxies: strings.Join(proxies, ","),
				OldManagerURL: in.OldManagerURL, MoveCode: in.MoveCode, EnrollmentToken: in.EnrollmentToken, EnvironmentName: in.EnvironmentName})
			return managermove.Files{ComposeYAML: compose, Env: env}
		},
		Waiting: waiting, MoveVariablesSet: cfg.Move.Set(), Bus: m.events,
		SchemaMigrations: func(ctx context.Context) ([]string, error) {
			applied, _, err := store.Status(ctx, m.db, m.opts.Migrations)
			return applied, err
		},
		CheckSchema: func(ctx context.Context, db bun.IDB) error {
			_, _, err := store.Status(ctx, db, m.opts.Migrations)
			return err
		},
	})
	return err
}

// finishMove completes a move applied at this startup (a restore of kind
// move): unlike a backup restore nothing is revoked — this manager is the
// same instance, so the copy's sessions, API tokens and agent credentials
// stay valid — the arrival is recorded (system.move) and the old manager
// is confirmed in the background (Serve). A move already finished at an
// earlier start resumes its confirmation.
func (m *Manager) finishMove(ctx context.Context) error {
	mk, err := backups.AppliedRestore(m.opts.Config.DataDir)
	if err != nil {
		return err
	}
	if mk != nil && mk.Kind == backups.RestoreKindMove {
		if err := m.moves.FinishArrival(ctx, mk); err != nil {
			return err
		}
	}
	return m.moves.StartConfirming(ctx)
}

// Moves returns the manager-move service.
func (m *Manager) Moves() *managermove.Service { return m.moves }
