package api

import (
	"testing"

	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// TestVolumeComposeLabels: the labels a stack's Compose file gives a
// volume that Docker kept off it reach the full view next to the volume's
// own labels, never the minimal one.
func TestVolumeComposeLabels(t *testing.T) {
	v := protocol.VolumeInfo{Name: "media_data", Labels: map[string]string{protocol.ComposeProjectLabel: "media"},
		ComposeLabels: map[string]string{protocol.LabelBackupExclude: "true"}}
	full := newVolume("e1", v, authz.View{Level: authz.Full}, nil)
	if full.ComposeLabels[protocol.LabelBackupExclude] != "true" || full.Labels[protocol.ComposeProjectLabel] != "media" ||
		len(full.Labels) != 1 {
		t.Errorf("full view = %+v", full)
	}
	if minimal := newVolume("e1", v, authz.View{Level: authz.Minimal}, nil); minimal.ComposeLabels != nil || minimal.Labels != nil {
		t.Errorf("minimal view = %+v", minimal)
	}
}
