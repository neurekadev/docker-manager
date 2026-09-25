package selfid

import (
	"strings"
	"testing"
)

const id = "4b825dc642cb6eb9a060e54bf8d69288fbee4904b825dc642cb6eb9a060e54bf"

func TestFromMountinfo(t *testing.T) {
	cases := map[string]string{
		// Docker (default data root), cgroup v2.
		"1405 1404 0:120 / / rw,relatime master:500 - overlay overlay rw,lowerdir=/var/lib/docker/overlay2/l/ABC\n" +
			"1411 1405 8:1 /var/lib/docker/containers/" + id + "/resolv.conf /etc/resolv.conf rw,relatime - ext4 /dev/sda1 rw\n" +
			"1412 1405 8:1 /var/lib/docker/containers/" + id + "/hostname /etc/hostname rw,relatime - ext4 /dev/sda1 rw\n": id,
		// Custom data root.
		"77 60 0:31 /srv/docker/containers/" + id + "/hosts /etc/hosts rw - btrfs /dev/sdb rw\n": id,
		// A volume whose name looks like a container path is not a match.
		"80 60 8:1 /var/lib/docker/volumes/containers/_data /data rw - ext4 /dev/sda1 rw\n": "",
		// Not in a container.
		"22 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw\n": "",
	}
	for in, want := range cases {
		if got := FromMountinfo(strings.NewReader(in)); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestFromCgroup(t *testing.T) {
	for in, want := range map[string]string{
		"12:pids:/docker/" + id + "\n11:memory:/docker/" + id + "\n": id,
		"0::/system.slice/docker-" + id + ".scope\n":                 id,
		"0::/\n": "",
		"0::/user.slice/user-1000.slice/session-2.scope\n": "",
	} {
		if got := FromCgroup(strings.NewReader(in)); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	// Detect never fails (it may find nothing outside Linux containers).
	_ = Detect()
}
