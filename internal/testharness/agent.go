//go:build integration

package testharness

import (
	"bytes"
	"context"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// EnvAgentImage names a locally built agent image (the extended workflow
// builds deploy/docker/agent.Dockerfile as dockyard-agent:test) that
// integration tests run inside DinD Engines (#2, #28).
const EnvAgentImage = "DOCKYARD_TEST_AGENT_IMAGE"

// AgentImage returns the agent image under test. Outside CI the test is
// skipped when the variable is unset; in CI (CI=true) it fails, so a
// missing image can never pass silently.
func AgentImage(t testing.TB) string {
	t.Helper()
	img := os.Getenv(EnvAgentImage)
	if img == "" {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s is not set: the workflow must build the agent image first", EnvAgentImage)
		}
		t.Skipf("%s not set (build deploy/docker/agent.Dockerfile and export it to run this test)", EnvAgentImage)
	}
	return img
}

// LoadWorkload loads the test workload image (WorkloadImage) into e.
func (e *Engine) LoadWorkload(t testing.TB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()
	bin, err := BuildWorkload(ctx, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := WorkloadArchive(bin, runtime.GOARCH, WorkloadImage)
	if err != nil {
		t.Fatal(err)
	}
	e.loadArchive(ctx, t, bytes.NewReader(archive))
}

// LoadHostImage copies ref from the Engine that runs the fixtures (the CI
// runner's Docker) into e, e.g. the locally built agent image.
func (e *Engine) LoadHostImage(t testing.TB, ref string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()
	host, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	rc, err := host.ImageSave(ctx, []string{ref})
	if err != nil {
		t.Fatalf("save %s from the host Engine: %v", ref, err)
	}
	defer rc.Close()
	e.loadArchive(ctx, t, rc)
}

func (e *Engine) loadArchive(ctx context.Context, t testing.TB, r io.Reader) {
	t.Helper()
	cli := e.Client(t)
	res, err := cli.ImageLoad(ctx, r, client.ImageLoadWithQuiet(true))
	if err != nil {
		t.Fatalf("image load: %v", err)
	}
	defer res.Close()
	out, _ := io.ReadAll(res)
	if strings.Contains(string(out), `"errorDetail"`) {
		t.Fatalf("image load: %s", out)
	}
}

// AgentOptions configures StartAgent.
type AgentOptions struct {
	Image string
	Name  string
	// Env is added to DOCKYARD_MANAGER_URL=https://manager.invalid.
	Env []string
	// Mounts replace the default mounts (Docker socket and the
	// identical-path volume directory, as in deploy/compose, #28).
	Mounts []mount.Mount
	// HostConfig lets a test adjust the container further.
	HostConfig func(*container.HostConfig)
}

// DockerSocket is the Engine socket inside a DinD Engine.
const DockerSocket = "/var/run/docker.sock"

// DefaultAgentMounts are the agent's mounts of deploy/compose/compose.yaml:
// the Docker socket and Docker's volume directory at its identical path.
func DefaultAgentMounts(dockerRoot string) []mount.Mount {
	return []mount.Mount{
		{Type: mount.TypeBind, Source: DockerSocket, Target: DockerSocket},
		{Type: mount.TypeBind, Source: dockerRoot + "/volumes", Target: dockerRoot + "/volumes"},
	}
}

// StartAgent runs the agent image inside e and returns the container ID.
// The container is removed when the test ends.
func (e *Engine) StartAgent(t testing.TB, o AgentOptions) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cli := e.Client(t)
	if o.Mounts == nil {
		o.Mounts = DefaultAgentMounts("/var/lib/docker")
	}
	hc := &container.HostConfig{Mounts: o.Mounts}
	if o.HostConfig != nil {
		o.HostConfig(hc)
	}
	res, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: o.Name,
		Config: &container.Config{
			Image: o.Image,
			Env:   append([]string{"DOCKYARD_MANAGER_URL=https://manager.invalid", "DOCKYARD_LOG_FORMAT=json"}, o.Env...),
		},
		HostConfig: hc,
	})
	if err != nil {
		t.Fatalf("create agent container: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if t.Failed() {
			t.Logf("agent logs:\n%s", e.Logs(ctx, t, res.ID))
		}
		_, _ = cli.ContainerRemove(ctx, res.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	})
	if _, err := cli.ContainerStart(ctx, res.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start agent container: %v", err)
	}
	return res.ID
}

// Logs returns a container's combined output.
func (e *Engine) Logs(ctx context.Context, t testing.TB, id string) string {
	t.Helper()
	rc, err := e.Client(t).ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return "logs unavailable: " + err.Error()
	}
	defer rc.Close()
	var out bytes.Buffer
	_, _ = stdcopy.StdCopy(&out, &out, rc)
	return out.String()
}

// WaitLog polls a container's output until it contains substr (setup
// polling with a deadline, not an assertion on time).
func (e *Engine) WaitLog(t testing.TB, id, substr string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		logs := e.Logs(ctx, t, id)
		cancel()
		if strings.Contains(logs, substr) {
			return logs
		}
		if time.Now().After(deadline) {
			t.Fatalf("container %s did not log %q within %s:\n%s", id[:12], substr, timeout, logs)
		}
		time.Sleep(time.Second)
	}
}

// WaitHealthy polls until the container's health status is healthy.
func (e *Engine) WaitHealthy(t testing.TB, id string, timeout time.Duration) {
	t.Helper()
	cli := e.Client(t)
	deadline := time.Now().Add(timeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		res, err := cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		st := res.Container.State
		if st.Health != nil && st.Health.Status == container.Healthy {
			return
		}
		if !st.Running {
			t.Fatalf("container %s exited (code %d)", id[:12], st.ExitCode)
		}
		if time.Now().After(deadline) {
			status := "no health check"
			if st.Health != nil {
				status = string(st.Health.Status)
			}
			t.Fatalf("container %s not healthy within %s (status %s)", id[:12], timeout, status)
		}
		time.Sleep(2 * time.Second)
	}
}

// Listeners runs the workload in the network namespace of container id and
// returns the listening sockets it finds in /proc/net (nil when none).
func (e *Engine) Listeners(t testing.TB, id string) []string {
	t.Helper()
	out := e.RunWorkload(t, "listeners", func(hc *container.HostConfig) {
		hc.NetworkMode = container.NetworkMode("container:" + id)
	})
	out = strings.TrimSpace(out)
	if out == "none" {
		return nil
	}
	return strings.Split(out, "\n")
}

// StartWorkload starts a long-running workload container with args and
// returns its ID (removed when the test ends).
func (e *Engine) StartWorkload(t testing.TB, args string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cli := e.Client(t)
	res, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: WorkloadImage, Cmd: strings.Fields(args)},
	})
	if err != nil {
		t.Fatalf("create workload: %v", err)
	}
	t.Cleanup(func() {
		_, _ = cli.ContainerRemove(context.Background(), res.ID, client.ContainerRemoveOptions{Force: true})
	})
	if _, err := cli.ContainerStart(ctx, res.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start workload: %v", err)
	}
	return res.ID
}

// RunWorkload runs the workload image once with args and returns its
// output; it fails the test on a non-zero exit. LoadWorkload must have run.
func (e *Engine) RunWorkload(t testing.TB, args string, hostConfig func(*container.HostConfig)) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cli := e.Client(t)
	hc := &container.HostConfig{}
	if hostConfig != nil {
		hostConfig(hc)
	}
	res, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: WorkloadImage, Cmd: strings.Fields(args)},
		HostConfig: hc,
	})
	if err != nil {
		t.Fatalf("create workload: %v", err)
	}
	defer func() {
		_, _ = cli.ContainerRemove(context.Background(), res.ID, client.ContainerRemoveOptions{Force: true})
	}()
	wait := cli.ContainerWait(ctx, res.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if _, err := cli.ContainerStart(ctx, res.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start workload: %v", err)
	}
	var code int64
	select {
	case r := <-wait.Result:
		code = r.StatusCode
	case err := <-wait.Error:
		t.Fatalf("wait workload: %v", err)
	}
	out := e.Logs(ctx, t, res.ID)
	if code != 0 {
		t.Fatalf("workload %q exited %d: %s", args, code, out)
	}
	return out
}
