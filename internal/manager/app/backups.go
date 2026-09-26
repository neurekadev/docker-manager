package app

import (
	"context"
	"errors"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/buildinfo"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/backups"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/maintenance"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/permissions"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/restic"
)

// finishRestore completes a manager-state restore applied at this startup
// (#24; decisions in docs/architecture/backups.md, "Fresh-manager
// import"): no session of the snapshot is revived, every restored API
// token is revoked (reason restore), every restored agent credential is
// revoked (environments wait for a reattach enrollment, #34), and the
// backup repository and index are brought up to date. Each step can be
// repeated: the marker is removed only after all of them succeeded.
func (m *Manager) finishRestore(ctx context.Context) error {
	cfg, log := m.opts.Config, m.opts.Logger
	mk, err := backups.AppliedRestore(cfg.DataDir)
	if err != nil || mk == nil {
		return err
	}
	sessions, err := store.DeleteAllSessions(ctx, m.db)
	if err != nil {
		return err
	}
	if _, err := store.BumpAllSessionEpochs(ctx, m.db, m.opts.Clock.Now()); err != nil {
		return err
	}
	tokens, err := m.identity.RevokeAllAPITokens(ctx, domain.RevokedRestore)
	if err != nil {
		return err
	}
	agentsRevoked, err := m.agents.RevokeAllAgents(ctx, "restore")
	if err != nil {
		return err
	}
	done, err := m.backups.CompleteRestore(ctx, mk)
	if err != nil {
		return err
	}
	if err := m.audit.Record(ctx, domain.AuditEvent{Category: domain.AuditSystem, Action: "system.restore", Actor: audit.ServiceActor(),
		Outcome: domain.AuditSuccess, JobID: mk.JobID, Targets: []domain.AuditTarget{{Type: "backup_set", ID: mk.SetID}},
		Details: map[string]any{"sessionsDeleted": sessions, "apiTokenCount": tokens, "agentsRevoked": agentsRevoked,
			"repositoryRelocated": done.Relocated, "s3KeyPairReplaced": done.CredentialsReplaced, "keyAdopted": done.KeyAdopted,
			"snapshotsAdded": done.SnapshotsAdded, "setsAdded": done.SetsAdded, "setsUpdated": done.SetsUpdated,
			"fromVersion": mk.App.Version, "schema": mk.SchemaLatest}}); err != nil {
		return err
	}
	log.Warn("manager state restored from a backup: sessions, API tokens and agent credentials were revoked; "+
		"re-attach every environment with an enrollment (intent reattach:<environmentId>)",
		"set_id", mk.SetID, "api_tokens_revoked", tokens, "agents_revoked", agentsRevoked, "snapshots_added", done.SnapshotsAdded)
	return backups.FinishedRestore(cfg.DataDir)
}

// startBackups creates the backup service (#10, #24) and places its
// resources in the authorization graph (#17). It runs before the job
// engine's recovery: the manager-executed backup kinds and their finish
// hooks must be registered by then.
func (m *Manager) startBackups(ctx context.Context) error {
	cfg, log := m.opts.Config, m.opts.Logger
	opener := m.opts.Restic
	if opener == nil {
		opener = &restic.Runner{Binary: cfg.ResticBinary, CacheDir: cfg.ResticCacheDir(), TempDir: cfg.ResticTempDir(),
			Logger: log.With("component", "restic")}
	}
	var err error
	m.backups, err = backups.New(backups.Options{
		DB: m.db, Keyring: m.keyring, Clock: m.opts.Clock, Logger: log.With("component", "backups"), Jobs: m.jobs,
		Scheduler: m.sched, Agents: m.agents.Hub(), Environments: m.agents, Stacks: m.stacks, Guard: m.identity, Audit: m.audit,
		InstanceID: m.instance.ID, Restic: opener, DataDir: cfg.DataDir, DatabasePath: cfg.DatabasePath(), MetricsPath: cfg.MetricsPath(),
		SecretKeyFile: cfg.SecretKeyFile, LocalRoots: cfg.BackupLocalRoots, HTTPClient: m.opts.BackupHTTPClient,
		ForgetResource: m.perms.ForgetResource, Build: buildinfo.Get(),
		MetricsSnapshot: func(ctx context.Context, dst string) error {
			if m.metrics == nil {
				return errors.New("the metrics database is not open")
			}
			_, err := m.metrics.DB().ExecContext(ctx, "VACUUM INTO ?", dst)
			return err
		},
		Migrations: func(ctx context.Context) ([]string, error) {
			applied, _, err := store.Status(ctx, m.db, m.opts.Migrations)
			return applied, err
		},
		KnownMigrations: func() []string {
			var out []string
			for _, mig := range m.opts.Migrations.Sorted() {
				out = append(out, mig.Name)
			}
			return out
		},
		CheckSchema: func(ctx context.Context, db bun.IDB) error {
			_, _, err := store.Status(ctx, db, m.opts.Migrations)
			return err
		},
		RequestRestart: m.RequestRestart,
	})
	if err != nil {
		return err
	}
	// Docker maintenance (#14) never prunes volumes a backup policy selects.
	if m.maint != nil {
		m.maint.SetBackupReferences(func(ctx context.Context, environmentID string) ([]maintenance.BackupRef, error) {
			refs, err := m.backups.VolumeReferences(ctx, environmentID)
			if err != nil {
				return nil, err
			}
			out := make([]maintenance.BackupRef, 0, len(refs))
			for _, r := range refs {
				out = append(out, maintenance.BackupRef{Kind: "volume", Name: r.Volume, Reason: "selected by backup policy " + r.PolicyName})
			}
			return out, nil
		})
	}
	m.perms.RegisterLocator(catalog.TypeBackupRepository, permissions.LocatorFunc(func(ctx context.Context, ref authz.ResourceRef) (permissions.Location, error) {
		_, err := m.backups.GetRepository(ctx, ref.ID)
		if errors.Is(err, domain.ErrBackupRepositoryNotFound) {
			return permissions.Location{}, nil
		}
		if err != nil {
			return permissions.Location{}, err
		}
		return permissions.Location{Found: true, Parents: []authz.ResourceRef{}}, nil
	}))
	m.perms.RegisterLocator(catalog.TypeBackupPolicy, permissions.LocatorFunc(func(ctx context.Context, ref authz.ResourceRef) (permissions.Location, error) {
		_, err := m.backups.GetPolicy(ctx, ref.ID)
		if errors.Is(err, domain.ErrBackupPolicyNotFound) {
			return permissions.Location{}, nil
		}
		if err != nil {
			return permissions.Location{}, err
		}
		return permissions.Location{Found: true, Parents: []authz.ResourceRef{}}, nil
	}))
	m.perms.RegisterLocator(catalog.TypeBackup, permissions.LocatorFunc(func(ctx context.Context, ref authz.ResourceRef) (permissions.Location, error) {
		sn, err := m.backups.GetSnapshot(ctx, ref.ID)
		if errors.Is(err, domain.ErrBackupNotFound) {
			return permissions.Location{}, nil
		}
		if err != nil {
			return permissions.Location{}, err
		}
		return permissions.Location{Found: true, Parents: []authz.ResourceRef{{Type: catalog.TypeBackupRepository, ID: sn.RepositoryID}}}, nil
	}))
	return nil
}
