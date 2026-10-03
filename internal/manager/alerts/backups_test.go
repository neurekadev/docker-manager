package alerts

import (
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// TestBackupsPausedAlert (#246): backups on without a Primary repository
// raise one critical alert (raised again it stays one), sent as a backup
// failure and linking to the Backups page; a Primary resolves it.
func TestBackupsPausedAlert(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	for range 2 {
		if err := f.svc.BackupsPaused(f.ctx, true); err != nil {
			t.Fatal(err)
		}
	}
	a := f.one()
	if a.Kind != domain.NotifyBackup || a.Severity != domain.AlertCritical || a.ResourceType != domain.AlertResourceBackupSettings ||
		Link(a) != "/backups" || Detail(a) == "" {
		t.Fatalf("%+v", a)
	}
	if got := f.dispatch(); len(got) != 1 || got[0].msg.URL != "https://docker.example.com/backups" {
		t.Fatalf("sent %+v", got)
	}
	if err := f.svc.BackupsPaused(f.ctx, false); err != nil {
		t.Fatal(err)
	}
	if len(f.firing()) != 0 {
		t.Errorf("still firing %+v", f.firing())
	}
	// The channel told of it hears that it is over.
	if p := f.pending(); len(p) != 1 || p[0].Event != domain.AlertEventResolved {
		t.Errorf("resolution messages %+v", p)
	}
	// Nothing to resolve: nothing happens.
	if err := f.svc.BackupsPaused(f.ctx, false); err != nil {
		t.Fatal(err)
	}
}
