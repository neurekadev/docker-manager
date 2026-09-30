package backups

import (
	"testing"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/restic"
)

func TestClassifySnapshots(t *testing.T) {
	env := backup.EnvironmentScope("e1")
	cases := []struct {
		tags  []string
		scope string
		want  string
		item  string
	}{
		{[]string{backup.TagDockerManager, backup.SetTag("s1"), backup.ItemTag(backup.VolumeItem("media"))}, env, SnapshotVolume, "volume/media"},
		{[]string{backup.TagDockerManager, backup.SetTag("s1"), backup.ItemTag(backup.StackItem("st"))}, env, SnapshotStack, "stack/st"},
		{[]string{backup.TagManagerState, backup.SetTag("s1"), backup.ItemTag(backup.ItemManagerState)}, backup.ScopeManager, SnapshotManagerState, backup.ItemManagerState},
		{[]string{backup.TagManifest, backup.SetTag("s1")}, backup.ScopeManager, SnapshotSetManifest, ""},
		{[]string{backup.TagManifest, backup.SetTag("s1")}, env, SnapshotHostManifest, ""},
		{[]string{"nightly"}, env, SnapshotForeign, ""},
	}
	for _, c := range cases {
		got := classify(restic.Snapshot{ID: "x", Tags: c.tags}, c.scope)
		if got.Class != c.want || got.Item != c.item {
			t.Errorf("classify(%v, %s) = %s %q, want %s %q", c.tags, c.scope, got.Class, got.Item, c.want, c.item)
		}
	}
}
