package engine

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/agent/engine/enginetest"
	"github.com/neurekadev/dockyard/internal/testutil"
)

func connect(t *testing.T, fake *enginetest.Engine) *Client {
	t.Helper()
	c, err := Connect(testutil.Context(t), Options{Host: fake.Host, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestConnectNegotiatesAPIVersion(t *testing.T) {
	for _, tc := range []struct {
		engineAPI, want string
	}{
		{"1.44", "1.44"}, // Docker 25: the client downgrades
		{"1.51", "1.51"},
		{"1.56", "1.56"},
		{"1.60", "1.56"}, // newer Engine: the client's maximum
	} {
		t.Run(tc.engineAPI, func(t *testing.T) {
			fake := enginetest.Start(t, enginetest.Options{APIVersion: tc.engineAPI})
			fake.Handle(http.MethodGet, "/containers/json", func(w http.ResponseWriter, _ *http.Request) {
				enginetest.JSON(w, http.StatusOK, []any{})
			})
			c := connect(t, fake)
			id := c.Identity()
			if id.NegotiatedAPIVersion != tc.want || id.APIVersion != tc.engineAPI {
				t.Fatalf("negotiated %s (engine %s), want %s", id.NegotiatedAPIVersion, id.APIVersion, tc.want)
			}
			if _, err := c.ListContainers(testutil.Context(t), ContainerFilter{}); err != nil {
				t.Fatal(err)
			}
			reqs := fake.Find(http.MethodGet, "/containers/json")
			if len(reqs) != 1 || reqs[0].APIVersion != tc.want {
				t.Fatalf("requests used API %v, want %s", reqs, tc.want)
			}
			for _, name := range []string{CapContainers, CapImageBuild, CapBuildGit, CapExec, CapCompose} {
				if !id.Supports(name) {
					t.Errorf("capability %s not supported on API %s", name, tc.engineAPI)
				}
			}
		})
	}
}

func TestConnectRefusesOldEngines(t *testing.T) {
	for _, api := range []string{"1.43", "1.41", "1.38"} { // Docker 24, 20.10, 18.09
		t.Run(api, func(t *testing.T) {
			fake := enginetest.Start(t, enginetest.Options{APIVersion: api})
			_, err := Connect(testutil.Context(t), Options{Host: fake.Host})
			if !errors.Is(err, ErrUnsupportedAPIVersion) {
				t.Fatalf("Connect to API %s = %v, want unsupported_api_version", api, err)
			}
			if !strings.Contains(err.Error(), "requires API "+MinSupportedAPIVersion+" or newer (Docker Engine 25.0 or later)") {
				t.Errorf("error lacks the minimum: %v", err)
			}
		})
	}
}

func TestConnectUnavailable(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(testutil.Context(t), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	_, err = Connect(testutil.Context(t), Options{Host: "tcp://" + addr})
	if CodeOf(err) != CodeEngineUnavailable {
		t.Fatalf("Connect to a closed port = %v (%s), want engine_unavailable", err, CodeOf(err))
	}
	if _, err := Connect(testutil.Context(t), Options{}); CodeOf(err) != CodeInvalidArgument {
		t.Fatalf("Connect without host = %v", err)
	}
}

func TestIdentity(t *testing.T) {
	fake := enginetest.Start(t, enginetest.Options{
		APIVersion:      "1.44",
		SecurityOptions: []string{"name=seccomp,profile=builtin", "name=rootless", "name=cgroupns"},
		DockerRootDir:   "/home/user/.local/share/docker",
	})
	id := connect(t, fake).Identity()
	if id.EngineID != "FAKE:ENGINE:ID" || id.Version != "29.8.1" || id.OS != "linux" || id.Arch != "amd64" {
		t.Errorf("identity %+v", id)
	}
	if !id.Rootless || id.DockerDesktop || id.DockerRootDir != "/home/user/.local/share/docker" {
		t.Errorf("rootless=%v desktop=%v root=%s", id.Rootless, id.DockerDesktop, id.DockerRootDir)
	}
	if strings.Join(id.SecurityOptions, ",") != "seccomp,rootless,cgroupns" {
		t.Errorf("security options %v", id.SecurityOptions)
	}

	desktop := enginetest.Start(t, enginetest.Options{PlatformName: "Docker Desktop 4.49.0 (208003)", OperatingSystem: "Docker Desktop"})
	if !connect(t, desktop).Identity().DockerDesktop {
		t.Error("Docker Desktop not detected")
	}
}

func TestErrorMapping(t *testing.T) {
	fake := enginetest.Start(t, enginetest.Options{})
	fake.Handle(http.MethodGet, "/containers/missing/json", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.Error(w, http.StatusNotFound, "No such container: missing")
	})
	fake.Handle(http.MethodPost, "/containers/busy/start", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.Error(w, http.StatusConflict, "container is paused")
	})
	fake.Handle(http.MethodDelete, "/volumes/inuse", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.Error(w, http.StatusConflict, "volume is in use")
	})
	fake.Handle(http.MethodPost, "/containers/create", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.Error(w, http.StatusBadRequest, "invalid reference format")
	})
	fake.Handle(http.MethodPost, "/containers/x/pause", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.Error(w, http.StatusInternalServerError, "cgroup failure")
	})
	c := connect(t, fake)
	ctx := testutil.Context(t)
	_, err := c.InspectContainer(ctx, "missing")
	check := func(err error, want Code, op string) {
		t.Helper()
		var e *Error
		if !errors.As(err, &e) || e.Code != want || e.Op != op {
			t.Errorf("%v: code %s, want %s/%s", err, CodeOf(err), want, op)
		}
	}
	check(err, CodeNotFound, "container.inspect")
	if !errors.Is(err, ErrNotFound) {
		t.Error("errors.Is(ErrNotFound) = false")
	}
	check(c.StartContainer(ctx, "busy"), CodeConflict, "container.start")
	check(c.RemoveVolume(ctx, "inuse", false), CodeConflict, "volume.remove")
	_, _, err = c.CreateContainer(ctx, ContainerSpec{Image: "bad image"})
	check(err, CodeInvalidArgument, "container.create")
	check(c.PauseContainer(ctx, "x"), CodeEngineError, "container.pause")
	_, _, err = c.CreateContainer(ctx, ContainerSpec{Image: "x", Mounts: []MountSpec{{Type: "npipe"}}})
	check(err, CodeInvalidArgument, "container.create")

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	check(c.StartContainer(canceled, "busy"), CodeCanceled, "container.start")
}

func TestClassifyRegistryMessages(t *testing.T) {
	for msg, want := range map[string]Code{
		"toomanyrequests: You have reached your pull rate limit":                            CodeRateLimited,
		"Head \"https://r/v2/x/manifests/1\": 429 Too Many Requests":                        CodeRateLimited,
		"unauthorized: authentication required":                                             CodeUnauthorized,
		"Head \"http://r/v2/x/manifests/1\": no basic auth credentials":                     CodeUnauthorized,
		"pull access denied for x, repository does not exist or may require 'docker login'": CodeUnauthorized,
		"denied: requested access to the resource is denied":                                CodeForbidden,
		"manifest unknown: manifest unknown":                                                CodeNotFound,
		"received unexpected HTTP status: 503 Service Unavailable":                          CodeRegistryUnavailable,
		"something else": "",
	} {
		if got := classifyRegistryMessage(msg); got != want {
			t.Errorf("%q -> %q, want %q", msg, got, want)
		}
	}
}

func TestNormalizeRegistryHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://index.docker.io/v1/":        "registry-1.docker.io",
		"docker.io":                          "registry-1.docker.io",
		"GHCR.io":                            "ghcr.io",
		"https://registry:5000/v2/":          "registry:5000",
		"host.testcontainers.internal:41234": "host.testcontainers.internal:41234",
	} {
		if got := RegistryHost(in); got != want {
			t.Errorf("RegistryHost(%q) = %q, want %q", in, got, want)
		}
	}
}
