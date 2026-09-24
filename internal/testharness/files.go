//go:build integration

package testharness

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// PutFiles writes files (relative path -> content) below the directory a
// mount exposes inside the DinD Engine (a named volume or a bind path on the
// Engine's host), through a short-lived workload container. exec lists
// paths that get mode 0755. LoadWorkload must have run.
func (e *Engine) PutFiles(t testing.TB, m mount.Mount, files map[string][]byte, exec ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cli := e.Client(t)
	m.Target = "/target"
	res, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: WorkloadImage, Cmd: []string{"echo", "files"}},
		HostConfig: &container.HostConfig{Mounts: []mount.Mount{m}},
	})
	if err != nil {
		t.Fatalf("create file helper: %v", err)
	}
	defer func() {
		_, _ = cli.ContainerRemove(context.Background(), res.ID, client.ContainerRemoveOptions{Force: true})
	}()
	archive, err := TarFiles(files, exec...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.CopyToContainer(ctx, res.ID, client.CopyToContainerOptions{DestinationPath: "/target", Content: bytes.NewReader(archive)}); err != nil {
		t.Fatalf("copy files into %s: %v", m.Source, err)
	}
}

// ImageRun describes a one-shot container for RunInImage.
type ImageRun struct {
	Image      string
	Entrypoint []string
	Cmd        []string
	Env        []string
	Mounts     []mount.Mount
	// Files are copied into the container's filesystem before it starts
	// (absolute path -> content); Exec lists the executable ones.
	Files map[string][]byte
	Exec  []string
}

// RunInImage runs a container to completion inside e and returns its exit
// code and combined output.
func (e *Engine) RunInImage(t testing.TB, r ImageRun) (int64, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cli := e.Client(t)
	res, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: r.Image, Entrypoint: r.Entrypoint, Cmd: r.Cmd, Env: r.Env},
		HostConfig: &container.HostConfig{Mounts: r.Mounts},
	})
	if err != nil {
		t.Fatalf("create %s: %v", r.Image, err)
	}
	defer func() {
		_, _ = cli.ContainerRemove(context.Background(), res.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if len(r.Files) > 0 {
		files := map[string][]byte{}
		var exec []string
		for p, b := range r.Files {
			files[strings.TrimPrefix(p, "/")] = b
		}
		for _, p := range r.Exec {
			exec = append(exec, strings.TrimPrefix(p, "/"))
		}
		archive, err := TarFiles(files, exec...)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cli.CopyToContainer(ctx, res.ID, client.CopyToContainerOptions{DestinationPath: "/", Content: bytes.NewReader(archive)}); err != nil {
			t.Fatalf("copy files into the container: %v", err)
		}
	}
	wait := cli.ContainerWait(ctx, res.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if _, err := cli.ContainerStart(ctx, res.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start %s: %v", r.Image, err)
	}
	var code int64
	select {
	case w := <-wait.Result:
		code = w.StatusCode
	case err := <-wait.Error:
		t.Fatalf("wait %s: %v", r.Image, err)
	}
	return code, e.Logs(ctx, t, res.ID)
}
