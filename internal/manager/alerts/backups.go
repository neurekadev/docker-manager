package alerts

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// Backups without a Primary repository (#246): while backups are on and
// no Primary repository is set (its removal promoted no Secondary), every
// run is refused. A critical alert says so until a Primary is chosen or
// backups are turned off.

const backupsPausedKey = "backup/no_primary"

// BackupsPaused raises (paused) or resolves the alert of backups that
// cannot run for want of a Primary repository.
func (s *Service) BackupsPaused(ctx context.Context, paused bool) error {
	if s.locked() {
		return nil
	}
	now := s.now()
	return s.inTx(ctx, func(ctx context.Context, tx bun.Tx) ([]domain.Alert, error) {
		if !paused {
			return collect(nil)(resolveKey(ctx, tx, backupsPausedKey, domain.AlertResolvedFixed, now))
		}
		o := Observation{Key: backupsPausedKey, Kind: domain.NotifyBackup, Severity: domain.AlertCritical,
			ResourceType: domain.AlertResourceBackupSettings, Title: "Backups are paused: no Primary repository",
			Fingerprint: domain.Fingerprint("no_primary")}
		return collect(nil)(raise(ctx, tx, o, now))
	})
}
