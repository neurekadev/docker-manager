package volumelabels

import (
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/protocol"
)

var created = time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)

// TestRecordKeepsOnlyMissingDockerManagerLabels: a deploy records the
// Docker Manager labels the Compose file declares with a value the volume
// lacks; labels the volume carries already, and Docker's or anyone
// else's, are never recorded.
func TestRecordKeepsOnlyMissingDockerManagerLabels(t *testing.T) {
	s := New(t.TempDir())
	err := s.Record("media", []Volume{
		{Name: "media_data", Created: created, Actual: map[string]string{"com.docker.compose.project": "media"},
			Declared: map[string]string{protocol.LabelBackupExclude: "true", "tier": "cold"}},
		{Name: "media_cache", Created: created, Actual: map[string]string{protocol.LabelBackupExclude: "true"},
			Declared: map[string]string{protocol.LabelBackupExclude: "true"}},
		{Name: "media_tmp", Created: created, Actual: map[string]string{protocol.LabelMaintenanceExclude: "true"},
			Declared: map[string]string{protocol.LabelMaintenanceExclude: "false"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Compose("media_data", created); !maps.Equal(got, map[string]string{protocol.LabelBackupExclude: "true"}) {
		t.Errorf("media_data = %v", got)
	}
	if got := s.Compose("media_cache", created); got != nil {
		t.Errorf("a label the volume carries was recorded: %v", got)
	}
	// A value changed in the Compose file wins over the volume's.
	if got := s.Compose("media_tmp", created); got[protocol.LabelMaintenanceExclude] != "false" {
		t.Errorf("media_tmp = %v", got)
	}
}

// TestComposeLabelsFollowTheVolume: a volume created again (another
// creation time) no longer gets the old entry; the next deploy of the
// project replaces its entries, and a removed stack's are forgotten.
func TestComposeLabelsFollowTheVolume(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	vol := func(name string) Volume {
		return Volume{Name: name, Created: created, Declared: map[string]string{protocol.LabelBackupExclude: "true"}}
	}
	if err := s.Record("a", []Volume{vol("a_data"), vol("a_logs")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Record("b", []Volume{vol("b_data")}); err != nil {
		t.Fatal(err)
	}
	if s.Compose("a_data", created.Add(time.Second)) != nil {
		t.Error("an entry applied to a volume created again")
	}
	if err := s.Record("a", []Volume{vol("a_data")}); err != nil {
		t.Fatal(err)
	}
	if s.Compose("a_logs", created) != nil || s.Compose("a_data", created) == nil || s.Compose("b_data", created) == nil {
		t.Error("the redeploy did not replace only its project's entries")
	}
	// The file survives a restart.
	again := New(dir)
	if again.Compose("a_data", created) == nil || again.Compose("b_data", created) == nil {
		t.Error("entries lost on reopen")
	}
	if err := again.Forget("b"); err != nil {
		t.Fatal(err)
	}
	if again.Compose("b_data", created) != nil || again.Compose("a_data", created) == nil {
		t.Error("Forget dropped the wrong entries")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != FileName {
		t.Errorf("state files = %v", entries)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
		t.Fatal(err)
	}
}

func TestEffectiveAndNilStore(t *testing.T) {
	own := map[string]string{"a": "1", protocol.LabelBackupExclude: "false"}
	got := Effective(own, map[string]string{protocol.LabelBackupExclude: "true"})
	if !protocol.BackupExcluded(got) || got["a"] != "1" || own[protocol.LabelBackupExclude] != "false" {
		t.Errorf("effective = %v, own = %v", got, own)
	}
	if got := Effective(nil, map[string]string{protocol.LabelBackupExclude: "true"}); !protocol.BackupExcluded(got) {
		t.Errorf("effective of an unlabeled volume = %v", got)
	}
	var s *Store
	if err := s.Record("x", []Volume{{Name: "v", Declared: map[string]string{protocol.LabelBackupExclude: "true"}}}); err != nil ||
		s.Compose("v", time.Time{}) != nil {
		t.Error("a nil store kept something")
	}
}
