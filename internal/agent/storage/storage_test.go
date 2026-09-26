package storage

import (
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine/enginetest"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

const selfID = "4f2c9a1b8d7e6f5a4b3c2d1e0f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a"

// env is a fake host: a temporary Docker data root, a fake /proc and a
// scripted Engine that reports the agent container's mounts.
type env struct {
	t         *testing.T
	root      string // Docker data root (slash path)
	stacks    string
	proc      string
	dockerenv string
	fake      *enginetest.Engine
	mounts    []map[string]any
	driver    string
	volOpts   map[string]string
	hostname  string
}

func slash(p string) string { return filepath.ToSlash(p) }

func newEnv(t *testing.T, opts enginetest.Options) *env {
	t.Helper()
	tmp := t.TempDir()
	e := &env{t: t, root: slash(filepath.Join(tmp, "docker")), proc: filepath.Join(tmp, "proc"), dockerenv: filepath.Join(tmp, ".dockerenv"), driver: "local"}
	e.stacks = e.root + "/volumes/docker-manager_stacks/_data"
	if err := os.MkdirAll(filepath.FromSlash(e.stacks), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(e.proc, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.writeProc("mountinfo", "1203 1180 0:95 / / rw,relatime - overlay overlay rw\n"+
		"1215 1203 259:1 /var/lib/docker/containers/"+selfID+"/hostname /etc/hostname rw,relatime - ext4 /dev/root rw\n")
	e.writeProc("cgroup", "0::/\n")
	if err := os.WriteFile(e.dockerenv, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if opts.DockerRootDir == "" {
		opts.DockerRootDir = e.root
	}
	e.fake = enginetest.Start(t, opts)
	e.fake.Handle(http.MethodGet, "/volumes/docker-manager_stacks", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusOK, map[string]any{"Name": "docker-manager_stacks", "Driver": e.driver, "Mountpoint": e.stacks, "Scope": "local", "Options": e.volOpts})
	})
	e.fake.Handle(http.MethodGet, "/containers/[0-9a-f]+/json", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/json")
		if !strings.HasPrefix(selfID, id) {
			enginetest.Error(w, http.StatusNotFound, "No such container: "+id)
			return
		}
		enginetest.JSON(w, http.StatusOK, map[string]any{"Id": selfID, "Name": "/docker-agent", "Mounts": e.mounts})
	})
	e.mountBind(e.root+"/volumes", e.root+"/volumes", true)
	return e
}

func (e *env) writeProc(name, content string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.proc, "self", name), []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) mountBind(src, dst string, rw bool) {
	e.mounts = append(e.mounts, map[string]any{"Type": "bind", "Source": src, "Destination": dst, "RW": rw})
}

func (e *env) verify(roots ...string) Result {
	e.t.Helper()
	eng, err := engine.Connect(testutil.Context(e.t), engine.Options{Host: e.fake.Host})
	if err != nil {
		e.t.Fatal(err)
	}
	defer eng.Close()
	host := e.hostname
	return Verify(testutil.Context(e.t), Options{
		Engine: eng, StacksVolume: "docker-manager_stacks", StackRoots: roots,
		ProcDir: e.proc, DockerEnvFile: e.dockerenv,
		Hostname: func() (string, error) { return host, nil },
	})
}

func codes(r Result) string {
	var out []string
	for _, d := range r.Diagnostics {
		out = append(out, d.Code)
	}
	return strings.Join(out, ",")
}

func TestIdenticalPathLayout(t *testing.T) {
	e := newEnv(t, enginetest.Options{})
	r := e.verify()
	if !r.StacksOK() || !r.VolumesOK() || len(r.Diagnostics) != 0 || !r.Containerized || r.SelfContainerID != selfID {
		t.Fatalf("result %+v", r)
	}
	if r.StacksDir != e.stacks || r.VolumesDir != e.root+"/volumes" {
		t.Errorf("dirs %s %s", r.StacksDir, r.VolumesDir)
	}
	if err := r.Allows(e.stacks + "/shop"); err != nil {
		t.Errorf("stack in the stacks volume refused: %v", err)
	}
	var d Diagnostic
	if err := r.Allows("/home/user/app"); !errors.As(err, &d) || d.Code != CodeOutsideRoots {
		t.Errorf("stack outside every root: %v", err)
	}
	// The write check leaves nothing behind.
	entries, _ := os.ReadDir(filepath.FromSlash(e.stacks))
	if len(entries) != 0 {
		t.Errorf("write check left %v", entries)
	}
	// The deploy examples' extra volume mount of the stacks volume at its
	// own mountpoint is equally fine.
	e.mounts = append(e.mounts, map[string]any{"Type": "volume", "Name": "docker-manager_stacks", "Source": e.stacks, "Destination": e.stacks, "RW": true})
	if r := e.verify(); !r.StacksOK() {
		t.Errorf("with the stacks volume mount: %s", codes(r))
	}
}

func TestCustomDataRootNeedsItsOwnMount(t *testing.T) {
	// The Engine uses a custom data root, but the agent only has the
	// default /var/lib/docker/volumes mount.
	custom := slash(filepath.Join(t.TempDir(), "srv-docker"))
	e2 := newEnv(t, enginetest.Options{DockerRootDir: custom})
	e2.root, e2.stacks = custom, custom+"/volumes/docker-manager_stacks/_data"
	if err := os.MkdirAll(filepath.FromSlash(e2.stacks), 0o755); err != nil {
		t.Fatal(err)
	}
	e2.mounts = nil
	e2.mountBind("/var/lib/docker/volumes", "/var/lib/docker/volumes", true)
	r := e2.verify()
	if r.StacksOK() || codes(r) != CodeMountMissing+","+CodeMountMissing {
		t.Fatalf("custom data root without its mount: %s", codes(r))
	}
	if !strings.Contains(r.Diagnostics[0].Message, custom+"/volumes:"+custom+"/volumes") {
		t.Errorf("diagnostic lacks the fix: %s", r.Diagnostics[0].Message)
	}
	var d Diagnostic
	if err := r.Allows(e2.stacks + "/shop"); !errors.As(err, &d) || d.Code != CodeMountMissing {
		t.Errorf("deploy allowed despite the missing mount: %v", err)
	}
	// With the matching identical mount it passes.
	e2.mounts = nil
	e2.mountBind(custom+"/volumes", custom+"/volumes", true)
	if r := e2.verify(); !r.StacksOK() || !r.VolumesOK() {
		t.Errorf("custom data root with its identical mount: %s", codes(r))
	}
}

func TestMountedFromAnotherPath(t *testing.T) {
	e := newEnv(t, enginetest.Options{})
	e.mounts = nil
	e.mountBind("/srv/elsewhere", e.root+"/volumes", true)
	r := e.verify()
	if codes(r) != CodePathMismatch+","+CodePathMismatch || !strings.Contains(r.Diagnostics[0].Message, "/srv/elsewhere/docker-manager_stacks/_data") {
		t.Fatalf("mismatch: %s %+v", codes(r), r.Diagnostics)
	}
}

func TestReadOnlyMount(t *testing.T) {
	e := newEnv(t, enginetest.Options{})
	e.mounts = nil
	e.mountBind(e.root+"/volumes", e.root+"/volumes", false)
	if r := e.verify(); codes(r) != CodeReadOnly+","+CodeReadOnly {
		t.Fatalf("read-only: %s", codes(r))
	}
}

func TestStackRoots(t *testing.T) {
	e := newEnv(t, enginetest.Options{})
	good := slash(filepath.Join(t.TempDir(), "opt-stacks"))
	bad := slash(filepath.Join(t.TempDir(), "srv-stacks"))
	for _, d := range []string{good, bad} {
		if err := os.MkdirAll(filepath.FromSlash(d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.mountBind(good, good, true)
	e.mountBind("/host/other", bad, true)
	r := e.verify(good, bad, "/not/mounted")
	if !r.StacksOK() || codes(r) != CodeRootMismatch+","+CodeRootMismatch {
		t.Fatalf("roots: %s", codes(r))
	}
	if err := r.Allows(good + "/app"); err != nil {
		t.Errorf("verified root refused: %v", err)
	}
	var d Diagnostic
	if err := r.Allows(bad + "/app"); !errors.As(err, &d) || d.Code != CodeRootMismatch || d.Path != bad {
		t.Errorf("mismatched root: %v", err)
	}
	if err := r.Allows("/not/mounted/app"); !errors.As(err, &d) || d.Path != "/not/mounted" {
		t.Errorf("unmounted root: %v", err)
	}
}

func TestUnsupportedEngines(t *testing.T) {
	for name, tc := range map[string]struct {
		opts enginetest.Options
		code string
	}{
		"rootless": {enginetest.Options{SecurityOptions: []string{"name=seccomp,profile=builtin", "name=rootless"}}, CodeRootlessEngine},
		"desktop":  {enginetest.Options{PlatformName: "Docker Desktop 4.49.0 (208003)"}, CodeDockerDesktop},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, tc.opts)
			r := e.verify("/opt/stacks")
			if codes(r) != tc.code || r.StacksOK() {
				t.Fatalf("%s: %s", name, codes(r))
			}
			var d Diagnostic
			if err := r.Allows(e.stacks + "/app"); !errors.As(err, &d) || d.Code != tc.code {
				t.Errorf("Allows = %v", err)
			}
			if err := r.Allows("/opt/stacks/app"); !errors.As(err, &d) || d.Code != CodeUnverified && d.Code != tc.code {
				t.Errorf("Allows(root) = %v", err)
			}
		})
	}
}

func TestStacksVolumeProblems(t *testing.T) {
	e := newEnv(t, enginetest.Options{})
	e.driver = "rclone"
	if r := e.verify(); codes(r) != CodeStacksVolumeRemote {
		t.Errorf("plugin driver: %s", codes(r))
	}
	e2 := newEnv(t, enginetest.Options{})
	e2.fake.Handle(http.MethodGet, "/volumes/docker-manager_stacks", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.Error(w, http.StatusNotFound, "get docker-manager_stacks: no such volume")
	})
	r := e2.verify()
	if codes(r) != CodeStacksVolumeMissing || !strings.Contains(r.Diagnostics[0].Message, "DOCKER_AGENT_STACKS_VOLUME") {
		t.Errorf("missing volume: %s", codes(r))
	}
}

func TestFindsSelf(t *testing.T) {
	// cgroup v1 path.
	e := newEnv(t, enginetest.Options{})
	e.writeProc("mountinfo", "1 0 0:1 / / rw - overlay overlay rw\n")
	e.writeProc("cgroup", "12:memory:/docker/"+selfID+"\n")
	if r := e.verify(); r.SelfContainerID != selfID || !r.StacksOK() {
		t.Errorf("cgroup: %+v", r)
	}
	// Hostname fallback (Docker's default hostname is the ID prefix).
	e.writeProc("cgroup", "0::/\n")
	e.hostname = selfID[:12]
	if r := e.verify(); r.SelfContainerID != selfID {
		t.Errorf("hostname fallback: %+v", r)
	}
	// A custom hostname in a container: the agent cannot find itself.
	e.hostname = "docker-agent"
	if r := e.verify(); codes(r) != CodeSelfUnknown || r.StacksOK() {
		t.Errorf("unknown self: %s", codes(r))
	}
	// Not in a container at all: host paths are the agent's own paths.
	if err := os.Remove(e.dockerenv); err != nil {
		t.Fatal(err)
	}
	r := e.verify()
	if r.Containerized || !r.StacksOK() {
		t.Errorf("on the host: %+v %s", r, codes(r))
	}
	// ...and a missing directory is reported.
	if err := os.RemoveAll(filepath.FromSlash(e.stacks)); err != nil {
		t.Fatal(err)
	}
	if r := e.verify(); !strings.Contains(codes(r), CodeNotVisible) {
		t.Errorf("missing directory: %s", codes(r))
	}
}

func TestMountinfoIDs(t *testing.T) {
	other := strings.Repeat("a", 64)
	got := mountinfoIDs("100 1 8:1 /var/lib/docker/containers/" + selfID + "/resolv.conf /etc/resolv.conf rw - ext4 /dev/sda1 rw\n" +
		"101 1 8:1 /data/docker/containers/" + selfID + "/hostname /etc/hostname rw - ext4 /dev/sda1 rw\n" +
		"102 1 8:1 /srv/containers/" + other + "x/y /y rw - ext4 /dev/sda1 rw\n" +
		"bad line\n")
	if strings.Join(got, ",") != selfID+","+other {
		t.Errorf("ids %v", got)
	}
}

func TestVolumeAccess(t *testing.T) {
	e := newEnv(t, enginetest.Options{})
	r := e.verify()
	local := engine.Volume{Name: "db", Driver: "local", Mountpoint: e.root + "/volumes/db/_data"}
	if a := r.AccessFor(local); !a.Supported {
		t.Errorf("local volume: %+v", a)
	}
	for name, v := range map[string]engine.Volume{
		"plugin":  {Name: "s3", Driver: "rexray/s3fs", Mountpoint: "/var/lib/docker/plugins/x/rootfs"},
		"nfs":     {Name: "nfs", Driver: "local", Mountpoint: e.root + "/volumes/nfs/_data", Options: map[string]string{"type": "nfs", "o": "addr=10.0.0.2,rw"}},
		"cifs":    {Name: "smb", Driver: "local", Mountpoint: e.root + "/volumes/smb/_data", Options: map[string]string{"type": "cifs"}},
		"outside": {Name: "odd", Driver: "local", Mountpoint: "/mnt/elsewhere/_data"},
	} {
		if a := r.AccessFor(v); a.Supported || a.Reason == "" {
			t.Errorf("%s: %+v", name, a)
		}
	}
	e.mounts = nil
	if a := e.verify().AccessFor(local); a.Supported {
		t.Error("volume access without the volume directory mount")
	}
	if path.Base(local.Mountpoint) != "_data" {
		t.Fatal("fixture")
	}
}
