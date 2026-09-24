//go:build integration

package storage_test

import (
	"bytes"
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/storage"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// Storage layout tests (#28), in the compose-fixtures job: the real agent
// image runs inside a DinD Engine with the identical-path mount; a stack in
// the stacks volume using ./data, env_file and a local build context is
// deployed by the agent's storage check + Compose adapter (the stackdriver
// fixture, run inside the agent image with the agent's mounts), and the
// Engine's own view proves it used the correct host paths.

const stacksVolume = "dockyard_stacks"

type layout struct {
	t        *testing.T
	e        *testharness.Engine
	eng      *engine.Client
	root     string // Engine data root
	agentImg string
	driver   []byte
}

func newLayout(t *testing.T, dataRoot string) *layout {
	t.Helper()
	img := testharness.AgentImage(t)
	e := testharness.StartEngine(t, testharness.EngineOptions{DataRoot: dataRoot})
	e.LoadWorkload(t)
	e.LoadHostImage(t, img)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	eng, err := engine.Connect(ctx, engine.Options{Host: e.Host, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if _, err := eng.CreateVolume(ctx, engine.VolumeSpec{Name: stacksVolume}); err != nil {
		t.Fatal(err)
	}
	driver, err := testharness.BuildFixture(ctx, "stackdriver", runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	root := dataRoot
	if root == "" {
		root = "/var/lib/docker"
	}
	if got := eng.Identity().DockerRootDir; got != root {
		t.Fatalf("Engine data root %s, want %s", got, root)
	}
	return &layout{t: t, e: e, eng: eng, root: root, agentImg: img, driver: driver}
}

func (l *layout) stacksDir() string { return l.root + "/volumes/" + stacksVolume + "/_data" }

// agentMounts are deploy/*/compose.yaml's agent mounts for this data root:
// the socket, Docker's volume directory at its identical path and the
// stacks volume at its own mountpoint.
func (l *layout) agentMounts() []mount.Mount {
	return append(testharness.DefaultAgentMounts(l.root),
		mount.Mount{Type: mount.TypeVolume, Source: stacksVolume, Target: l.stacksDir()})
}

// stackFiles is a stack using a relative bind mount, an env file and a
// local build context.
func (l *layout) stackFiles(greeting string) map[string][]byte {
	l.t.Helper()
	bin, err := testharness.BuildWorkload(l.t.Context(), runtime.GOARCH)
	if err != nil {
		l.t.Fatal(err)
	}
	return map[string][]byte{
		"compose.yaml": []byte(`services:
  web:
    build: ./app
    command: ["write", "/data/written.txt", "written by the stack"]
    env_file: [web.env]
    volumes:
      - ./data:/data
`),
		"web.env":           []byte("GREETING=" + greeting + "\n"),
		"data/seed.txt":     []byte("seed from the stacks volume\n"),
		"app/Dockerfile":    []byte("FROM scratch\nCOPY workload /workload\nENTRYPOINT [\"/workload\"]\n"),
		"app/workload":      bin,
		"app/.dockerignore": []byte("*.tmp\n"),
	}
}

func prefixed(prefix string, files map[string][]byte) (map[string][]byte, []string) {
	out := map[string][]byte{}
	for k, v := range files {
		out[prefix+"/"+k] = v
	}
	return out, []string{prefix + "/app/workload"}
}

type report struct {
	Step        string               `json:"step"`
	StacksOK    bool                 `json:"stacksOk"`
	StacksDir   string               `json:"stacksDir"`
	SelfID      string               `json:"selfContainerId"`
	Diagnostics []storage.Diagnostic `json:"diagnostics"`
	Dir         string               `json:"dir"`
	Code        string               `json:"code"`
	Error       string               `json:"error"`
}

// deploy runs the stackdriver inside the agent image with mounts and
// returns its exit code and reports by step.
func (l *layout) deploy(mounts []mount.Mount, env []string, args ...string) (int64, map[string]report) {
	l.t.Helper()
	code, out := l.e.RunInImage(l.t, testharness.ImageRun{
		Image: l.agentImg, Entrypoint: []string{"/stackdriver"}, Cmd: args, Env: env, Mounts: mounts,
		Files: map[string][]byte{"/stackdriver": l.driver}, Exec: []string{"/stackdriver"},
	})
	l.t.Logf("stackdriver %v (exit %d):\n%s", args, code, out)
	reports := map[string]report{}
	for _, line := range strings.Split(out, "\n") {
		var r report
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &r) == nil && r.Step != "" {
			reports[r.Step] = r
		}
	}
	return code, reports
}

func (l *layout) execOut(id string, cmd ...string) string {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(l.t.Context(), time.Minute)
	defer cancel()
	eid, err := l.eng.CreateExec(ctx, id, engine.ExecSpec{Cmd: cmd})
	if err != nil {
		l.t.Fatal(err)
	}
	var out bytes.Buffer
	if err := l.eng.AttachExec(ctx, eid, engine.ExecIO{Stdout: &out, Stderr: &out}); err != nil {
		l.t.Fatal(err)
	}
	return strings.TrimSpace(out.String())
}

// assertDeployed checks the Engine's own view of the deployed stack.
func (l *layout) assertDeployed(project, projectDir, greeting string, dataMount mount.Mount) {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(l.t.Context(), 2*time.Minute)
	defer cancel()
	d, err := l.eng.InspectContainer(ctx, project+"-web-1")
	if err != nil {
		l.t.Fatalf("stack container: %v", err)
	}
	if !d.State.Running {
		l.t.Fatalf("stack container not running: %+v", d.State)
	}
	var bind *engine.Mount
	for i, m := range d.Mounts {
		if m.Destination == "/data" {
			bind = &d.Mounts[i]
		}
	}
	// The Engine resolved ./data to the real host path in the stack root.
	if bind == nil || bind.Type != "bind" || bind.Source != projectDir+"/data" {
		l.t.Fatalf("./data mounted as %+v, want host path %s/data", bind, projectDir)
	}
	// ...where the pre-existing file is (a wrong path would be an empty
	// directory the Engine created).
	if got := l.execOut(d.ID, "/workload", "cat", "/data/seed.txt"); got != "seed from the stacks volume" {
		l.t.Errorf("seed file in the container: %q", got)
	}
	// env_file was read from the project directory.
	if got := l.execOut(d.ID, "/workload", "env", "GREETING"); got != greeting {
		l.t.Errorf("GREETING = %q, want %q (env_file)", got, greeting)
	}
	// The local build context was built.
	if _, err := l.eng.InspectImage(ctx, project+"-web"); err != nil {
		l.t.Errorf("image built from the local context: %v", err)
	}
	// What the service wrote landed in the stack root on the host.
	out := l.e.RunWorkload(l.t, "cat /stackroot/"+strings.TrimPrefix(projectDir, rootOf(dataMount))+"/data/written.txt",
		func(hc *container.HostConfig) {
			m := dataMount
			m.Target = "/stackroot"
			hc.Mounts = []mount.Mount{m}
		})
	if !strings.Contains(out, "written by the stack") {
		l.t.Errorf("file written by the stack: %q", out)
	}
}

// rootOf returns the directory a helper mount exposes, for path arithmetic.
func rootOf(m mount.Mount) string { return m.Target }

func (l *layout) volumeRootMount() mount.Mount {
	return mount.Mount{Type: mount.TypeVolume, Source: stacksVolume, Target: l.stacksDir()}
}

func (l *layout) runDeployment(t *testing.T) {
	files, exec := prefixed("shop", l.stackFiles("hello from web.env"))
	l.e.PutFiles(t, mount.Mount{Type: mount.TypeVolume, Source: stacksVolume}, files, exec...)
	code, reports := l.deploy(l.agentMounts(), nil, "shop")
	v := reports["verify"]
	if !v.StacksOK || v.StacksDir != l.stacksDir() || v.SelfID == "" || len(v.Diagnostics) != 0 {
		t.Fatalf("storage check inside the agent image: %+v", v)
	}
	if code != 0 || reports["up"].Error != "" {
		t.Fatalf("deploy failed: exit %d, %+v, %+v", code, reports["load"], reports["up"])
	}
	l.assertDeployed("shop", l.stacksDir()+"/shop", "hello from web.env", l.volumeRootMount())
}

func TestComposeStacksVolumeDefaultDataRoot(t *testing.T) {
	newLayout(t, "").runDeployment(t)
}

func TestComposeStacksVolumeCustomDataRoot(t *testing.T) {
	newLayout(t, "/srv/docker-data").runDeployment(t)
}

// A misconfigured mount is detected at agent startup with a diagnostic and
// no deploy runs.
func TestComposeMisconfiguredMountRefused(t *testing.T) {
	l := newLayout(t, "/srv/docker-data")
	files, exec := prefixed("shop", l.stackFiles("unused"))
	l.e.PutFiles(t, mount.Mount{Type: mount.TypeVolume, Source: stacksVolume}, files, exec...)
	ctx := t.Context()
	// The default example's host directory exists but is not this Engine's.
	if code, out, err := l.e.Exec(ctx, "mkdir", "-p", "/var/lib/docker/volumes"); err != nil || code != 0 {
		t.Fatalf("mkdir: %d %s %v", code, out, err)
	}

	cases := []struct {
		name   string
		mounts []mount.Mount
		code   string
	}{
		// The default example mount on a custom data-root Engine.
		{"default mount on a custom data root", testharness.DefaultAgentMounts("/var/lib/docker"), storage.CodeMountMissing},
		// Docker's volume directory mounted from somewhere else.
		{"volume directory from another path", []mount.Mount{
			{Type: mount.TypeBind, Source: testharness.DockerSocket, Target: testharness.DockerSocket},
			{Type: mount.TypeBind, Source: "/srv", Target: "/srv/docker-data/volumes"},
		}, storage.CodePathMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, reports := l.deploy(tc.mounts, nil, "shop", l.stacksDir()+"/shop")
			v := reports["verify"]
			if v.StacksOK || len(v.Diagnostics) == 0 || v.Diagnostics[0].Code != tc.code {
				t.Fatalf("verify: %+v", v)
			}
			if code != 3 || reports["load"].Code != tc.code {
				t.Fatalf("deploy not refused with %s: exit %d, %+v", tc.code, code, reports["load"])
			}
			list, err := l.eng.ListContainers(ctx, engine.ContainerFilter{All: true, Labels: []string{"com.docker.compose.project=shop"}})
			if err != nil || len(list) != 0 {
				t.Fatalf("containers of the refused stack: %+v, %v", list, err)
			}
		})
	}

	// The real agent binary reports the diagnostic at startup and keeps
	// running (healthy) for everything else.
	id := l.e.StartAgent(t, testharness.AgentOptions{Image: l.agentImg, Name: "misconfigured-agent", Mounts: testharness.DefaultAgentMounts("/var/lib/docker")})
	logs := l.e.WaitLog(t, id, `"code":"`+storage.CodeMountMissing+`"`, 3*time.Minute)
	if !strings.Contains(logs, "stack operations disabled") {
		t.Errorf("agent log lacks the refusal:\n%s", logs)
	}
	l.e.WaitHealthy(t, id, 3*time.Minute)
}

// The real agent image verifies a correct layout at startup.
func TestComposeAgentVerifiesStorageAtStartup(t *testing.T) {
	l := newLayout(t, "/srv/docker-data")
	id := l.e.StartAgent(t, testharness.AgentOptions{Image: l.agentImg, Name: "dockyard-agent", Mounts: l.agentMounts()})
	logs := l.e.WaitLog(t, id, "storage layout verified", 3*time.Minute)
	if !strings.Contains(logs, `"stacks_dir":"`+l.stacksDir()+`"`) || strings.Contains(logs, "stack operations disabled") {
		t.Errorf("agent log:\n%s", logs)
	}
	l.e.WaitHealthy(t, id, 3*time.Minute)
}

// Additional stack roots (DOCKYARD_STACK_ROOTS) are identical-path bind
// mounts verified the same way.
func TestComposeStackRoots(t *testing.T) {
	l := newLayout(t, "")
	ctx := t.Context()
	for _, dir := range []string{"/opt/stacks", "/srv/elsewhere"} {
		if code, out, err := l.e.Exec(ctx, "mkdir", "-p", dir); err != nil || code != 0 {
			t.Fatalf("mkdir %s: %d %s %v", dir, code, out, err)
		}
	}
	files, exec := prefixed("blog", l.stackFiles("hello from a stack root"))
	rootMount := mount.Mount{Type: mount.TypeBind, Source: "/opt/stacks"}
	l.e.PutFiles(t, rootMount, files, exec...)
	env := []string{"DOCKYARD_STACK_ROOTS=/opt/stacks"}

	good := append(l.agentMounts(), mount.Mount{Type: mount.TypeBind, Source: "/opt/stacks", Target: "/opt/stacks"})
	code, reports := l.deploy(good, env, "blog", "/opt/stacks/blog")
	if code != 0 || !reports["verify"].StacksOK {
		t.Fatalf("deploy from a verified stack root: exit %d %+v %+v", code, reports["verify"], reports["load"])
	}
	rootMount.Target = "/opt/stacks"
	l.assertDeployed("blog", "/opt/stacks/blog", "hello from a stack root", rootMount)

	bad := append(l.agentMounts(), mount.Mount{Type: mount.TypeBind, Source: "/srv/elsewhere", Target: "/opt/stacks"})
	code, reports = l.deploy(bad, env, "blog2", "/opt/stacks/blog")
	if code != 3 || reports["load"].Code != storage.CodeRootMismatch {
		t.Fatalf("mismatched stack root: exit %d %+v", code, reports["load"])
	}
	if !reports["verify"].StacksOK {
		t.Error("a bad extra root must not disable the stacks volume")
	}
}
