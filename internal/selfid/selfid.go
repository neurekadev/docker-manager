// Package selfid finds the ID of the Docker container the current process
// runs in (#32: the agent protects its own container, the manager tells its
// agents which container is the co-located manager). It only reads the
// process's own /proc files; it never talks to an Engine.
//
// Docker bind-mounts /etc/hostname, /etc/hosts and /etc/resolv.conf from
// <DockerRootDir>/containers/<id>/, so the container ID appears in
// /proc/self/mountinfo on cgroup v1 and v2 alike; /proc/self/cgroup
// (cgroup v1, or v2 with a private cgroup namespace off) is the fallback.
package selfid

import (
	"bufio"
	"io"
	"os"
	"regexp"
)

var (
	mountRE  = regexp.MustCompile(`/containers/([0-9a-f]{64})/(hostname|hosts|resolv\.conf)`)
	cgroupRE = regexp.MustCompile(`(?:docker[-/]|/)([0-9a-f]{64})(?:\.scope)?\s*$`)
)

// FromMountinfo returns the container ID named by a mountinfo listing, or
// "" when the process does not run in a Docker container.
func FromMountinfo(r io.Reader) string {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if m := mountRE.FindStringSubmatch(sc.Text()); m != nil {
			return m[1]
		}
	}
	return ""
}

// FromCgroup returns the container ID named by a /proc/self/cgroup
// listing, or "".
func FromCgroup(r io.Reader) string {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if m := cgroupRE.FindStringSubmatch(sc.Text()); m != nil {
			return m[1]
		}
	}
	return ""
}

// Detect returns the ID of the container the process runs in, or "" (not
// in a container, or not detectable). The caller verifies the ID against
// its Engine before trusting it.
func Detect() string {
	for _, f := range []struct {
		path  string
		parse func(io.Reader) string
	}{{"/proc/self/mountinfo", FromMountinfo}, {"/proc/self/cgroup", FromCgroup}} {
		file, err := os.Open(f.path)
		if err != nil {
			continue
		}
		id := f.parse(file)
		_ = file.Close()
		if id != "" {
			return id
		}
	}
	return ""
}
