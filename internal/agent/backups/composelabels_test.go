package backups

import (
	"path/filepath"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/volumelabels"
	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestBackupHonorsComposeLabels: a backup exclude label the stack's
// Compose file declares on a volume created before it (Docker never
// relabels a volume; the deploy recorded it) leaves the volume out like a
// label on the volume itself, until the volume is created again.
func TestBackupHonorsComposeLabels(t *testing.T) {
	e := newEnv(t)
	ctx := testutil.Context(t)
	store := volumelabels.New(t.TempDir())
	e.svc.opts.VolumeLabels = store
	v, err := e.eng.InspectVolume(ctx, "app_dbdata")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record("app", []volumelabels.Volume{{Name: v.Name, Created: v.CreatedAt, Actual: v.Labels,
		Declared: map[string]string{protocol.LabelBackupExclude: "true"}}}); err != nil {
		t.Fatal(err)
	}
	p := e.svc.plan(ctx, stackItem(protocol.BackupRules{}), false)
	if p.err != nil {
		t.Fatal(p.err)
	}
	if s, _ := sourceState(p, protocol.SourceVolume, "app_dbdata"); s.State != protocol.SourceExcluded || s.Reason != composeLabeledReason {
		t.Errorf("volume labeled in the Compose file: %+v", s)
	}

	// Recorded for an older volume of that name: not this one.
	if err := store.Record("app", []volumelabels.Volume{{Name: v.Name, Created: v.CreatedAt.Add(-1), Actual: v.Labels,
		Declared: map[string]string{protocol.LabelBackupExclude: "true"}}}); err != nil {
		t.Fatal(err)
	}
	p = e.svc.plan(ctx, stackItem(protocol.BackupRules{}), false)
	if s, _ := sourceState(p, protocol.SourceVolume, "app_dbdata"); s.State != protocol.SourceIncluded {
		t.Errorf("a record of an older volume applied: %+v", s)
	}
}

// TestBackupComposeFalseWinsOverVolumeLabel: a volume created with the
// backup exclude label is backed up again once its stack's Compose file
// declares "false" (recorded at the deploy), like the policy UI shows.
func TestBackupComposeFalseWinsOverVolumeLabel(t *testing.T) {
	e := newEnv(t)
	ctx := testutil.Context(t)
	e.eng.AddVolume("cache", map[string]string{protocol.LabelBackupExclude: "true"})
	mp := filepath.Join(e.volumes, "cache", "_data")
	write(t, filepath.Join(mp, "x"), "x")
	e.eng.SetVolumeMountpoint("cache", filepath.ToSlash(mp))
	v, err := e.eng.InspectVolume(ctx, "cache")
	if err != nil {
		t.Fatal(err)
	}
	item := protocol.BackupItem{Kind: backup.MemberVolume, Volume: "cache"}
	if s, _ := sourceState(e.svc.plan(ctx, item, false), protocol.SourceVolume, "cache"); s.Reason != labeledVolumeReason {
		t.Fatalf("labeled volume: %+v", s)
	}
	store := volumelabels.New(t.TempDir())
	e.svc.opts.VolumeLabels = store
	if err := store.Record("app", []volumelabels.Volume{{Name: v.Name, Created: v.CreatedAt, Actual: v.Labels,
		Declared: map[string]string{protocol.LabelBackupExclude: "false"}}}); err != nil {
		t.Fatal(err)
	}
	if s, _ := sourceState(e.svc.plan(ctx, item, false), protocol.SourceVolume, "cache"); s.State != protocol.SourceIncluded {
		t.Errorf("volume the Compose file sets to false: %+v", s)
	}
}
