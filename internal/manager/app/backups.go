package app

import (
	"context"
	"errors"

	"github.com/neurekadev/dockyard/internal/buildinfo"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/manager/backups"
	"github.com/neurekadev/dockyard/internal/manager/maintenance"
	"github.com/neurekadev/dockyard/internal/manager/permissions"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/restic"
)

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
