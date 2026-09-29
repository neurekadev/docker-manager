package app

import (
	"context"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/buildinfo"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/backups"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/managermove"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// startMoves creates the manager-move service
// (docs/internal/architecture/manager-move.md). It runs before the job
// engine's recovery: manager.receive and its finish hook must be
// registered by then.
func (m *Manager) startMoves() error {
	cfg, log := m.opts.Config, m.opts.Logger
	var err error
	m.moves, err = managermove.New(managermove.Options{
		DB: m.db, Keyring: m.keyring, Clock: m.opts.Clock, Logger: log.With("component", "managermove"), Lock: m.moveLock,
		Jobs: m.jobs, Guard: m.identity, Audit: m.audit, Instance: m.instance, DataDir: cfg.DataDir, PublicURL: cfg.PublicURL,
		LocalDevelopment: cfg.LocalDevelopment, Build: buildinfo.Get(), HTTPClient: m.opts.MoveHTTPClient, RequestRestart: m.RequestRestart,
		Migrations: func(ctx context.Context) ([]string, error) {
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
