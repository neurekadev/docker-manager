//go:build integration

package engine_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// Engine adapter tests against every Engine of test/matrix/engines.json
// (engine-matrix job, DOCKYARD_TEST_ENGINE). Each v1 operation is a
// subtest; the job summary lists them per Engine and docs/support-matrix.md
// records the results (#21, #25 Q2).

func ctxFor(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), d)
	t.Cleanup(cancel)
	return ctx
}

func connectTo(t *testing.T, e *testharness.Engine) *engine.Client {
	t.Helper()
	c, err := engine.Connect(ctxFor(t, time.Minute), engine.Options{Host: e.Host, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatalf("connect to Engine %s: %v", e.Version.Version, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestEngineAdapterNegotiatesAndIdentifies(t *testing.T) {
	want, err := testharness.SelectEngine()
	if err != nil {
		t.Fatal(err)
	}
	e := testharness.StartEngine(t, testharness.EngineOptions{Version: want})
	c := connectTo(t, e)
	id := c.Identity()
	t.Logf("Engine %s API %s negotiated %s (%s/%s) root %s", id.Version, id.APIVersion, id.NegotiatedAPIVersion, id.OS, id.Arch, id.DockerRootDir)
	if id.Version != want.Version || id.APIVersion != want.APIVersion {
		t.Errorf("identity %s/%s, matrix says %s/%s", id.Version, id.APIVersion, want.Version, want.APIVersion)
	}
	// The client downgrades to the Engine's API when the Engine is older.
	if exp := testharness.MinVersion("1.56", want.APIVersion); id.NegotiatedAPIVersion != exp {
		t.Errorf("negotiated API %s, want %s", id.NegotiatedAPIVersion, exp)
	}
	if id.OS != "linux" || id.Arch != runtime.GOARCH || id.DockerRootDir != "/var/lib/docker" || id.EngineID == "" {
		t.Errorf("identity %+v", id)
	}
	if id.Rootless || id.DockerDesktop {
		t.Errorf("DinD reported as rootless=%v desktop=%v", id.Rootless, id.DockerDesktop)
	}
	for _, capa := range id.Capabilities {
		if !capa.Supported {
			t.Errorf("capability %s unsupported: %s", capa.Name, capa.Reason)
		}
	}
	if err := c.Ping(ctxFor(t, time.Minute)); err != nil {
		t.Fatal(err)
	}
}

// operations shares one Engine across the operation subtests.
type operations struct {
	e   *testharness.Engine
	c   *engine.Client
	reg *testharness.Registry
	git *testharness.GitServer
}

func TestEngineOperations(t *testing.T) {
	nw := testharness.NewNetwork(t)
	reg := testharness.StartRegistry(t, testharness.RegistryOptions{Network: nw})
	git := testharness.StartGitServer(t, testharness.GitServerOptions{Network: nw})
	eopts := reg.EngineOptions()
	e := testharness.StartEngine(t, eopts)
	e.LoadWorkload(t)
	o := &operations{e: e, c: connectTo(t, e), reg: reg, git: git}

	t.Run("container.crud", o.containers)
	t.Run("container.lifecycle", o.lifecycle)
	t.Run("image.pull.auth", o.pull)
	t.Run("image.crud", o.images)
	t.Run("volume.crud", o.volumes)
	t.Run("network.crud", o.networks)
	t.Run("events.stream", o.events)
	t.Run("logs.stream", o.logs)
	t.Run("stats.stream", o.stats)
	t.Run("exec", o.exec)
	t.Run("image.build.local", o.buildLocal)
	t.Run("image.build.git", o.buildGit)
	t.Run("image.build.private_base", o.buildPrivateBase)
}

func (o *operations) run(t *testing.T, name string, spec engine.ContainerSpec) string {
	t.Helper()
	ctx := ctxFor(t, 2*time.Minute)
	spec.Name = name
	if spec.Image == "" {
		spec.Image = testharness.WorkloadImage
	}
	id, _, err := o.c.CreateContainer(ctx, spec)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = o.c.RemoveContainer(context.Background(), id, engine.RemoveOptions{Force: true, Volumes: true})
	})
	if err := o.c.StartContainer(ctx, id); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	return id
}

func (o *operations) containers(t *testing.T) {
	ctx := ctxFor(t, 3*time.Minute)
	stop := 3 * time.Second
	spec := engine.ContainerSpec{
		Name: "crud", Image: testharness.WorkloadImage, Cmd: []string{"serve", "up"},
		Env: []string{"A=1"}, Labels: map[string]string{"dev.neureka.dockyard.test": "crud"},
		Mounts:        []engine.MountSpec{{Type: "volume", Source: "crud-data", Target: "/data"}, {Type: "tmpfs", Target: "/scratch"}},
		Ports:         []engine.PortBinding{{ContainerPort: 8080, HostIP: "127.0.0.1"}},
		RestartPolicy: "unless-stopped", StopTimeout: &stop,
		Healthcheck: &engine.HealthcheckSpec{Test: []string{"CMD", "/workload", "check", "/workload"}, Interval: time.Second, Retries: 3},
	}
	id, warnings, err := o.c.CreateContainer(ctx, spec)
	if err != nil {
		t.Fatalf("create: %v (warnings %v)", err, warnings)
	}
	defer func() {
		_ = o.c.RemoveContainer(context.Background(), id, engine.RemoveOptions{Force: true, Volumes: true})
	}()
	if _, _, err := o.c.CreateContainer(ctx, spec); !errors.Is(err, engine.ErrConflict) {
		t.Errorf("duplicate name: %v (%s), want conflict", err, engine.CodeOf(err))
	}
	if err := o.c.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	d, err := o.c.InspectContainer(ctx, "crud")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != id || d.Name != "crud" || !d.State.Running || d.RestartPolicy != "unless-stopped" || d.Labels["dev.neureka.dockyard.test"] != "crud" {
		t.Errorf("inspect %+v", d)
	}
	var sawVolume bool
	for _, m := range d.Mounts {
		if m.Type == "volume" && m.Name == "crud-data" && m.Destination == "/data" && strings.HasPrefix(m.Source, "/var/lib/docker/volumes/crud-data") {
			sawVolume = true
		}
	}
	if !sawVolume {
		t.Errorf("mounts %+v", d.Mounts)
	}
	if len(d.Ports) != 1 || d.Ports[0].PrivatePort != 8080 || d.Ports[0].PublicPort == 0 {
		t.Errorf("ports %+v", d.Ports)
	}
	list, err := o.c.ListContainers(ctx, engine.ContainerFilter{Labels: []string{"dev.neureka.dockyard.test=crud"}})
	if err != nil || len(list) != 1 || list[0].ID != id || list[0].State != "running" {
		t.Fatalf("list %+v, %v", list, err)
	}
	mem := int64(64 << 20)
	if _, err := o.c.UpdateContainer(ctx, id, engine.ContainerUpdate{Resources: &engine.Resources{Memory: mem, MemorySwap: -1}, RestartPolicy: "on-failure"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if d, _ := o.c.InspectContainer(ctx, id); d.RestartPolicy != "on-failure" {
		t.Errorf("restart policy after update %q", d.RestartPolicy)
	}
	if err := o.c.RemoveContainer(ctx, id, engine.RemoveOptions{}); !errors.Is(err, engine.ErrConflict) {
		t.Errorf("remove running container: %v (%s), want conflict", err, engine.CodeOf(err))
	}
	if err := o.c.RemoveContainer(ctx, id, engine.RemoveOptions{Force: true, Volumes: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.c.InspectContainer(ctx, id); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("inspect removed: %v", err)
	}
	if err := o.c.StartContainer(ctx, "does-not-exist"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("start missing: %v (%s)", err, engine.CodeOf(err))
	}
}

func (o *operations) lifecycle(t *testing.T) {
	ctx := ctxFor(t, 3*time.Minute)
	id := o.run(t, "lifecycle", engine.ContainerSpec{Cmd: []string{"serve"}})
	state := func() engine.ContainerState {
		d, err := o.c.InspectContainer(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return d.State
	}
	if err := o.c.PauseContainer(ctx, id); err != nil || !state().Paused {
		t.Fatalf("pause: %v", err)
	}
	if err := o.c.UnpauseContainer(ctx, id); err != nil || state().Paused {
		t.Fatalf("unpause: %v", err)
	}
	before := state().StartedAt
	timeout := 5 * time.Second
	if err := o.c.RestartContainer(ctx, id, &timeout); err != nil {
		t.Fatal(err)
	}
	if st := state(); !st.Running || !st.StartedAt.After(before) {
		t.Errorf("after restart %+v (before %v)", st, before)
	}
	if err := o.c.StopContainer(ctx, id, &timeout); err != nil {
		t.Fatal(err)
	}
	if st := state(); st.Running || st.ExitCode != 0 {
		t.Errorf("after graceful stop %+v", st)
	}
	if err := o.c.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := o.c.KillContainer(ctx, id, "SIGKILL"); err != nil {
		t.Fatal(err)
	}
	code, err := o.c.WaitContainer(ctx, id)
	if err != nil || code != 137 {
		t.Errorf("wait after SIGKILL = %d, %v", code, err)
	}
	oneShot := o.run(t, "oneshot", engine.ContainerSpec{Cmd: []string{"exit", "3"}})
	if code, err := o.c.WaitContainer(ctx, oneShot); err != nil || code != 3 {
		t.Errorf("one-shot exit = %d, %v", code, err)
	}
}

func (o *operations) pull(t *testing.T) {
	ctx := ctxFor(t, 5*time.Minute)
	img, err := testharness.NewTestImage(runtime.GOARCH, "pull fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := testharness.PushOCIImage(ctx, http.DefaultClient, o.reg.Direct, o.reg.User, o.reg.Password, "dockyard/pull", "v1", img); err != nil {
		t.Fatal(err)
	}
	ref := o.reg.EngineAddress + "/dockyard/pull:v1"
	auth := &engine.RegistryAuth{ServerAddress: o.reg.EngineAddress, Username: o.reg.User, Password: logging.Secret(o.reg.Password)}

	if _, err := o.c.PullImage(ctx, ref, engine.PullOptions{}); !errors.Is(err, engine.ErrUnauthorized) {
		t.Errorf("anonymous pull: %v (%s), want unauthorized", err, engine.CodeOf(err))
	}
	wrong := *auth
	wrong.Password = "wrong-password"
	if _, err := o.c.PullImage(ctx, ref, engine.PullOptions{Auth: &wrong}); !errors.Is(err, engine.ErrUnauthorized) {
		t.Errorf("wrong password: %v (%s), want unauthorized", err, engine.CodeOf(err))
	}
	o.reg.Proxy.Reset()
	remove := o.reg.Proxy.Inject(testharness.FaultRule{Path: testharness.RegistryManifestPath, Status: http.StatusTooManyRequests, RetryAfter: "7"})
	_, err = o.c.PullImage(ctx, ref, engine.PullOptions{Auth: auth})
	remove()
	if !errors.Is(err, engine.ErrRateLimited) {
		t.Errorf("injected 429: %v (%s), want rate_limited", err, engine.CodeOf(err))
	}
	remove = o.reg.Proxy.Inject(testharness.FaultRule{Path: testharness.RegistryManifestPath, Status: http.StatusUnauthorized})
	_, err = o.c.PullImage(ctx, ref, engine.PullOptions{Auth: auth})
	remove()
	if !errors.Is(err, engine.ErrUnauthorized) {
		t.Errorf("injected 401: %v (%s), want unauthorized", err, engine.CodeOf(err))
	}
	var progress int
	res, err := o.c.PullImage(ctx, ref, engine.PullOptions{Auth: auth, Progress: func(engine.Progress) { progress++ }})
	if err != nil {
		t.Fatalf("authenticated pull: %v", err)
	}
	if res.Digest != img.Digest || res.ImageID == "" || progress == 0 {
		t.Errorf("pull result %+v (want digest %s), %d progress messages", res, img.Digest, progress)
	}
	if _, err := o.c.PullImage(ctx, o.reg.EngineAddress+"/dockyard/missing:v1", engine.PullOptions{Auth: auth}); engine.CodeOf(err) != engine.CodeNotFound {
		t.Errorf("missing image: %v (%s), want not_found", err, engine.CodeOf(err))
	}
}

func (o *operations) images(t *testing.T) {
	ctx := ctxFor(t, 2*time.Minute)
	d, err := o.c.InspectImage(ctx, testharness.WorkloadImage)
	if err != nil {
		t.Fatal(err)
	}
	if d.OS != "linux" || d.Architecture != runtime.GOARCH || len(d.Entrypoint) != 1 || d.Entrypoint[0] != testharness.WorkloadBinary {
		t.Errorf("inspect %+v", d)
	}
	if err := o.c.TagImage(ctx, testharness.WorkloadImage, "dockyard-test/tagged:v2"); err != nil {
		t.Fatal(err)
	}
	list, err := o.c.ListImages(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, img := range list {
		for _, tag := range img.RepoTags {
			found = found || tag == "dockyard-test/tagged:v2"
		}
	}
	if !found {
		t.Errorf("tag missing from list %+v", list)
	}
	deleted, err := o.c.RemoveImage(ctx, "dockyard-test/tagged:v2", false, false)
	if err != nil || len(deleted) == 0 || deleted[0].Untagged == "" {
		t.Errorf("remove tag: %+v, %v", deleted, err)
	}
	if _, err := o.c.InspectImage(ctx, "dockyard-test/tagged:v2"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("inspect removed tag: %v", err)
	}
	if _, err := o.c.InspectImage(ctx, testharness.WorkloadImage); err != nil {
		t.Errorf("removing a tag removed the image: %v", err)
	}
}

func (o *operations) volumes(t *testing.T) {
	ctx := ctxFor(t, 2*time.Minute)
	v, err := o.c.CreateVolume(ctx, engine.VolumeSpec{Name: "ops-vol", Labels: map[string]string{"dev.neureka.dockyard.test": "vol"}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Driver != "local" || v.Mountpoint != "/var/lib/docker/volumes/ops-vol/_data" {
		t.Errorf("volume %+v", v)
	}
	list, err := o.c.ListVolumes(ctx, "dev.neureka.dockyard.test=vol")
	if err != nil || len(list) != 1 || list[0].Name != "ops-vol" {
		t.Fatalf("list %+v, %v", list, err)
	}
	id := o.run(t, "vol-user", engine.ContainerSpec{Cmd: []string{"write", "/data/f", "x"}, Mounts: []engine.MountSpec{{Type: "volume", Source: "ops-vol", Target: "/data"}}})
	if err := o.c.RemoveVolume(ctx, "ops-vol", false); !errors.Is(err, engine.ErrConflict) {
		t.Errorf("remove in-use volume: %v (%s), want conflict", err, engine.CodeOf(err))
	}
	if err := o.c.RemoveContainer(ctx, id, engine.RemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if err := o.c.RemoveVolume(ctx, "ops-vol", false); err != nil {
		t.Fatal(err)
	}
	if _, err := o.c.InspectVolume(ctx, "ops-vol"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("inspect removed volume: %v", err)
	}
}

func (o *operations) networks(t *testing.T) {
	ctx := ctxFor(t, 2*time.Minute)
	nid, err := o.c.CreateNetwork(ctx, engine.NetworkSpec{Name: "ops-net", Driver: "bridge", Labels: map[string]string{"dev.neureka.dockyard.test": "net"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.c.CreateNetwork(ctx, engine.NetworkSpec{Name: "ops-net"}); !errors.Is(err, engine.ErrConflict) {
		t.Errorf("duplicate network: %v (%s), want conflict", err, engine.CodeOf(err))
	}
	list, err := o.c.ListNetworks(ctx, "dev.neureka.dockyard.test=net")
	if err != nil || len(list) != 1 || list[0].ID != nid || len(list[0].Subnets) == 0 {
		t.Fatalf("list %+v, %v", list, err)
	}
	id := o.run(t, "net-user", engine.ContainerSpec{Cmd: []string{"serve"}})
	if err := o.c.ConnectNetwork(ctx, "ops-net", id, "alias-a"); err != nil {
		t.Fatal(err)
	}
	n, err := o.c.InspectNetwork(ctx, "ops-net")
	if err != nil || n.Containers[id].IPAddress == "" {
		t.Fatalf("inspect %+v, %v", n, err)
	}
	if err := o.c.RemoveNetwork(ctx, "ops-net"); !errors.Is(err, engine.ErrConflict) {
		t.Errorf("remove network in use: %v (%s), want conflict", err, engine.CodeOf(err))
	}
	if err := o.c.DisconnectNetwork(ctx, "ops-net", id, false); err != nil {
		t.Fatal(err)
	}
	if err := o.c.RemoveNetwork(ctx, nid); err != nil {
		t.Fatal(err)
	}
	if _, err := o.c.InspectNetwork(ctx, nid); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("inspect removed network: %v", err)
	}
}

func (o *operations) events(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	type got struct{ actions []string }
	var mu sync.Mutex
	var g got
	done := make(chan error, 1)
	subscribed := time.Now().Add(-time.Second)
	go func() {
		done <- o.c.Events(ctx, engine.EventFilter{Types: []string{"container"}, Labels: []string{"dev.neureka.dockyard.test=events"}, Since: subscribed},
			func(e engine.Event) error {
				mu.Lock()
				defer mu.Unlock()
				g.actions = append(g.actions, e.Action)
				if e.Action == "die" {
					return io.EOF // stop after the container ended
				}
				return nil
			})
	}()
	id := o.run(t, "events", engine.ContainerSpec{Cmd: []string{"exit", "0"}, Labels: map[string]string{"dev.neureka.dockyard.test": "events"}})
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatalf("events stream ended with %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(g.actions, ",")
	if !strings.Contains(joined, "create") || !strings.Contains(joined, "start") || !strings.Contains(joined, "die") {
		t.Errorf("events for %s: %s", id[:12], joined)
	}
	// Cancellation ends a stream with CodeCanceled.
	cctx, ccancel := context.WithCancel(t.Context())
	ccancel()
	if err := o.c.Events(cctx, engine.EventFilter{}, func(engine.Event) error { return nil }); engine.CodeOf(err) != engine.CodeCanceled {
		t.Errorf("canceled stream: %v", err)
	}
}

func (o *operations) logs(t *testing.T) {
	ctx := ctxFor(t, 2*time.Minute)
	id := o.run(t, "logs", engine.ContainerSpec{Cmd: []string{"serve", "out-1", "err:err-1", "out-2"}})
	var entries []engine.LogEntry
	collect := func(e engine.LogEntry) error { entries = append(entries, e); return nil }
	// Wait until the output exists (setup polling, bounded).
	for deadline := time.Now().Add(time.Minute); ; {
		entries = nil
		if err := o.c.Logs(ctx, id, engine.LogOptions{Timestamps: true}, collect); err != nil {
			t.Fatal(err)
		}
		if len(entries) >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	var out, errs []string
	for _, e := range entries {
		if e.Time.IsZero() {
			t.Errorf("entry without timestamp: %+v", e)
		}
		if e.Stream == engine.Stdout {
			out = append(out, strings.TrimSpace(string(e.Data)))
		} else {
			errs = append(errs, strings.TrimSpace(string(e.Data)))
		}
	}
	if strings.Join(out, ",") != "out-1,out-2" || strings.Join(errs, ",") != "err-1" {
		t.Errorf("stdout %v stderr %v", out, errs)
	}
	// Follow a ticking container until the callback has seen three lines.
	tick := o.run(t, "ticker", engine.ContainerSpec{Cmd: []string{"tick", "200"}})
	var n int
	stop := errors.New("seen enough")
	err := o.c.Logs(ctx, tick, engine.LogOptions{Follow: true, Stdout: true}, func(e engine.LogEntry) error {
		if strings.HasPrefix(string(e.Data), "tick ") {
			n++
		}
		if n == 3 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || n != 3 {
		t.Errorf("follow: %v after %d lines", err, n)
	}
	// A follow stream ends with CodeCanceled when its context is canceled.
	fctx, fcancel := context.WithCancel(ctx)
	go func() { time.Sleep(time.Second); fcancel() }()
	if err := o.c.Logs(fctx, tick, engine.LogOptions{Follow: true, TailAll: false, Tail: 1}, func(engine.LogEntry) error { return nil }); engine.CodeOf(err) != engine.CodeCanceled {
		t.Errorf("canceled follow: %v (%s)", err, engine.CodeOf(err))
	}
}

func (o *operations) stats(t *testing.T) {
	ctx := ctxFor(t, 2*time.Minute)
	id := o.run(t, "stats", engine.ContainerSpec{Cmd: []string{"tick", "100"}})
	var one engine.Stats
	if err := o.c.Stats(ctx, id, false, func(s engine.Stats) error { one = s; return nil }); err != nil {
		t.Fatal(err)
	}
	if one.MemoryUsage == 0 || one.MemoryLimit == 0 || one.PIDs == 0 || one.Read.IsZero() {
		t.Errorf("single sample %+v", one)
	}
	var samples []engine.Stats
	enough := errors.New("enough")
	err := o.c.Stats(ctx, id, true, func(s engine.Stats) error {
		samples = append(samples, s)
		if len(samples) == 3 {
			return enough
		}
		return nil
	})
	if !errors.Is(err, enough) || len(samples) != 3 || samples[2].CPUPercent < 0 {
		t.Errorf("stream: %v, %d samples", err, len(samples))
	}
}

func (o *operations) exec(t *testing.T) {
	ctx := ctxFor(t, 2*time.Minute)
	id := o.run(t, "exec", engine.ContainerSpec{Cmd: []string{"serve"}})

	// Plain command, demultiplexed output and exit code.
	eid, err := o.c.CreateExec(ctx, id, engine.ExecSpec{Cmd: []string{"/workload", "echo", "hello", "exec"}})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := o.c.AttachExec(ctx, eid, engine.ExecIO{Stdout: &out, Stderr: &errOut}); err != nil {
		t.Fatal(err)
	}
	if st, err := o.c.InspectExec(ctx, eid); err != nil || st.Running || st.ExitCode != 0 || out.String() != "hello exec\n" {
		t.Errorf("exec: %+v %v out %q err %q", st, err, out.String(), errOut.String())
	}

	// stdin is forwarded and closed at EOF.
	eid, err = o.c.CreateExec(ctx, id, engine.ExecSpec{Cmd: []string{"/workload", "stdin"}, AttachStdin: true})
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := o.c.AttachExec(ctx, eid, engine.ExecIO{Stdin: strings.NewReader("from stdin\n"), Stdout: &out}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "from stdin\n" {
		t.Errorf("stdin exec output %q", out.String())
	}

	// Failing command reports its exit code.
	eid, _ = o.c.CreateExec(ctx, id, engine.ExecSpec{Cmd: []string{"/workload", "exit", "4"}})
	_ = o.c.AttachExec(ctx, eid, engine.ExecIO{})
	if st, _ := o.c.InspectExec(ctx, eid); st.ExitCode != 4 {
		t.Errorf("exit code %d, want 4", st.ExitCode)
	}

	// TTY with initial size and resize.
	eid, err = o.c.CreateExec(ctx, id, engine.ExecSpec{Cmd: []string{"/workload", "ttysize"}, Tty: true, AttachStdin: true, Height: 30, Width: 100})
	if err != nil {
		t.Fatal(err)
	}
	stdinR, stdinW := io.Pipe()
	outR, outW := io.Pipe()
	attached := make(chan error, 1)
	go func() {
		attached <- o.c.AttachExec(ctx, eid, engine.ExecIO{Tty: true, Stdin: stdinR, Stdout: outW})
		_ = outW.Close()
	}()
	lines := bufio.NewScanner(outR)
	next := func() string {
		for lines.Scan() {
			if l := strings.TrimSpace(lines.Text()); strings.HasPrefix(l, "size") {
				return l
			}
		}
		return ""
	}
	if l := next(); l != "size 30 100" {
		t.Errorf("initial TTY size %q", l)
	}
	if err := o.c.ResizeExec(ctx, eid, 40, 120); err != nil {
		t.Fatal(err)
	}
	_, _ = stdinW.Write([]byte("\n"))
	if l := next(); l != "size 40 120" {
		t.Errorf("TTY size after resize %q", l)
	}
	_ = stdinW.Close()
	go func() { _, _ = io.Copy(io.Discard, outR) }()
	if err := <-attached; err != nil {
		t.Errorf("tty attach: %v", err)
	}

	if _, err := o.c.CreateExec(ctx, "missing", engine.ExecSpec{Cmd: []string{"x"}}); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("exec in missing container: %v", err)
	}
}

func workloadContext(t *testing.T, dockerfile string) string {
	t.Helper()
	dir := t.TempDir()
	bin, err := testharness.BuildWorkload(t.Context(), runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"workload": bin, "Dockerfile": []byte(dockerfile), ".dockerignore": []byte("ignored.txt\n"),
		"ignored.txt": []byte("must not be sent"), "hello.txt": []byte("hello from the context\n"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func (o *operations) buildLocal(t *testing.T) {
	ctx := ctxFor(t, 5*time.Minute)
	dir := workloadContext(t, "FROM scratch\nCOPY workload /workload\nCOPY hello.txt /hello.txt\nENTRYPOINT [\"/workload\"]\n")
	var steps []string
	res, err := o.c.Build(ctx, engine.BuildSpec{
		ContextDir: dir, Tags: []string{"dockyard-test/built:local"}, Labels: map[string]string{"built": "local"},
		BuildArgs: map[string]string{"UNUSED": "x"},
		Progress:  func(e engine.BuildEvent) { steps = append(steps, e.Status+" "+e.Step) },
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if res.ImageID == "" || len(steps) == 0 {
		t.Errorf("result %+v, progress %v", res, steps)
	}
	if !strings.Contains(strings.Join(steps, "\n"), "COPY hello.txt") {
		t.Errorf("BuildKit progress lacks the COPY step: %v", steps)
	}
	d, err := o.c.InspectImage(ctx, "dockyard-test/built:local")
	if err != nil || d.Labels["built"] != "local" || d.ID != res.ImageID {
		t.Fatalf("built image %+v, %v", d, err)
	}
	id := o.run(t, "built-local", engine.ContainerSpec{Image: "dockyard-test/built:local", Cmd: []string{"cat", "/hello.txt"}})
	if code, _ := o.c.WaitContainer(ctx, id); code != 0 {
		t.Errorf("built image exited %d", code)
	}
	// .dockerignore applies: ignored.txt is not in the context.
	_, err = o.c.Build(ctx, engine.BuildSpec{ContextDir: dir, DockerfileInline: "FROM scratch\nCOPY ignored.txt /x\n", Tags: []string{"dockyard-test/ignored:1"}})
	if engine.CodeOf(err) != engine.CodeBuildFailed {
		t.Errorf("COPY of an ignored file: %v (%s), want build_failed", err, engine.CodeOf(err))
	}
	// Inline Dockerfile.
	if _, err := o.c.Build(ctx, engine.BuildSpec{ContextDir: dir, DockerfileInline: "FROM scratch\nCOPY hello.txt /inline.txt\n", Tags: []string{"dockyard-test/inline:1"}}); err != nil {
		t.Errorf("inline Dockerfile: %v", err)
	}
	// Cancellation mid-build ends with CodeCanceled.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := o.c.Build(cctx, engine.BuildSpec{ContextDir: dir, Tags: []string{"dockyard-test/canceled:1"}}); engine.CodeOf(err) != engine.CodeCanceled {
		t.Errorf("canceled build: %v (%s)", err, engine.CodeOf(err))
	}
}

func (o *operations) buildGit(t *testing.T) {
	ctx := ctxFor(t, 5*time.Minute)
	url := o.git.RepoURL(o.git.InternalURL, "public", false) + "#" + testharness.GitDefaultBranch
	res, err := o.c.Build(ctx, engine.BuildSpec{RemoteContext: url, Tags: []string{"dockyard-test/git:public"}})
	if err != nil {
		t.Fatalf("Git build from %s: %v", url, err)
	}
	d, err := o.c.InspectImage(ctx, "dockyard-test/git:public")
	if err != nil || d.ID != res.ImageID {
		t.Fatalf("Git-built image %+v, %v", d, err)
	}
	if _, err := o.c.Build(ctx, engine.BuildSpec{RemoteContext: o.git.RepoURL(o.git.InternalURL, "missing", false), Tags: []string{"dockyard-test/git:missing"}}); engine.CodeOf(err) != engine.CodeBuildFailed {
		t.Errorf("missing repository: %v (%s), want build_failed", err, engine.CodeOf(err))
	}
}

func (o *operations) buildPrivateBase(t *testing.T) {
	ctx := ctxFor(t, 5*time.Minute)
	img, err := testharness.NewTestImage(runtime.GOARCH, "private base")
	if err != nil {
		t.Fatal(err)
	}
	if err := testharness.PushOCIImage(ctx, http.DefaultClient, o.reg.Direct, o.reg.User, o.reg.Password, "dockyard/base", "v1", img); err != nil {
		t.Fatal(err)
	}
	o.reg.Proxy.Reset()
	dir := workloadContext(t, "FROM "+o.reg.EngineAddress+"/dockyard/base:v1\nCOPY hello.txt /hello.txt\n")
	spec := engine.BuildSpec{ContextDir: dir, Tags: []string{"dockyard-test/private-base:1"}, Pull: true}
	if _, err := o.c.Build(ctx, spec); engine.CodeOf(err) != engine.CodeBuildFailed {
		t.Errorf("anonymous base image pull: %v (%s), want build_failed", err, engine.CodeOf(err))
	}
	spec.RegistryAuth = []engine.RegistryAuth{{ServerAddress: o.reg.EngineAddress, Username: o.reg.User, Password: logging.Secret(o.reg.Password)}}
	if _, err := o.c.Build(ctx, spec); err != nil {
		t.Fatalf("base image with session credentials: %v", err)
	}
}
