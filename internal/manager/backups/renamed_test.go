package backups

import (
	"testing"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
)

// TestCurrentVolumeFollowsARename (#7): a snapshot taken before the stack
// was renamed restores its project volumes into the new project's
// volumes; other volumes keep their names.
func TestCurrentVolumeFollowsARename(t *testing.T) {
	sn := domain.BackupSnapshot{Kind: backup.MemberStack, StackName: "app"}
	renamed := &domain.Stack{Name: "shop"}
	cases := []struct {
		st                 *domain.Stack
		v, name, proj, key string
	}{
		{renamed, "app_data", "shop_data", "shop", "data"},
		{&domain.Stack{Name: "app"}, "app_data", "app_data", "app", "data"},
		{nil, "app_data", "app_data", "app", "data"},
		{renamed, "shared-media", "shared-media", "", ""},
	}
	for _, tc := range cases {
		name, proj, key := currentVolume(sn, tc.st, tc.v)
		if name != tc.name || proj != tc.proj || key != tc.key {
			t.Errorf("currentVolume(%s) = %s %s %s, want %s %s %s", tc.v, name, proj, key, tc.name, tc.proj, tc.key)
		}
	}
	vol := domain.BackupSnapshot{Kind: backup.MemberVolume, Volume: "app_data"}
	if name, _, _ := currentVolume(vol, renamed, "app_data"); name != "app_data" {
		t.Errorf("volume backup renamed to %s", name)
	}
	if p := volumeDataPath([]string{"/stacks/app", "/var/lib/docker/volumes/app_data/_data"}, "app_data"); p != "/var/lib/docker/volumes/app_data/_data" {
		t.Errorf("data path %q", p)
	}
}
