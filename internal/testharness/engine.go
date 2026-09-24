//go:build integration

package testharness

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// DefaultInsecureRegistries lets DinD Engines pull over plain HTTP from
// fixtures on private addresses (the registry fixture and its fault proxy).
var DefaultInsecureRegistries = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8"}

// EngineOptions configures StartEngine.
type EngineOptions struct {
	// Version is the matrix entry to start; zero means SelectEngine().
	Version EngineVersion
	// Network attaches the Engine; nil creates a new one.
	Network *Network
	// Alias is the Engine's name on the network (default "engine").
	Alias string
	// InsecureRegistries are added to DefaultInsecureRegistries
	// (host:port or CIDR), e.g. the registry fixture's EngineAddress.
	InsecureRegistries []string
	// HostAccessPorts exposes ports of the test process to the Engine as
	// host.testcontainers.internal:<port> (e.g. the registry fault proxy).
	HostAccessPorts []int
}

// Engine is a running Docker-in-Docker Engine.
type Engine struct {
	Version EngineVersion
	// Host is a DOCKER_HOST value for the Moby client in the test process
	// (tcp://127.0.0.1:<port>, plain HTTP).
	Host string
	// Alias and InternalHost address the Engine from other containers on
	// the network, e.g. an agent container (tcp://<alias>:2375).
	Alias        string
	InternalHost string
	Network      *Network
	Container    *testcontainers.DockerContainer
}

// StartEngine starts a privileged docker:<version>-dind Engine and waits
// until its API answers. It is removed when the test ends.
func StartEngine(t testing.TB, opts EngineOptions) *Engine {
	t.Helper()
	if opts.Version.Image == "" {
		v, err := SelectEngine()
		if err != nil {
			t.Fatal(err)
		}
		opts.Version = v
	}
	if opts.Network == nil {
		opts.Network = NewNetwork(t)
	}
	if opts.Alias == "" {
		opts.Alias = "engine"
	}
	// Arguments starting with "-" are appended to the entrypoint's default
	// dockerd flags (unix socket plus tcp://0.0.0.0:2375 without TLS).
	args := []string{"--tls=false"}
	for _, r := range append(append([]string{}, DefaultInsecureRegistries...), opts.InsecureRegistries...) {
		args = append(args, "--insecure-registry="+r)
	}
	copts := []testcontainers.ContainerCustomizer{
		testcontainers.WithEnv(map[string]string{"DOCKER_TLS_CERTDIR": ""}),
		testcontainers.WithCmd(args...),
		testcontainers.WithExposedPorts("2375/tcp"),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) { hc.Privileged = true }),
		testcontainers.WithLabels(map[string]string{"dev.neureka.dockyard.test": "engine"}),
		opts.Network.option(opts.Alias),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/_ping").WithPort("2375/tcp").WithStartupTimeout(3 * time.Minute)),
	}
	if len(opts.HostAccessPorts) > 0 {
		copts = append(copts, testcontainers.WithHostPortAccess(opts.HostAccessPorts...))
	}
	c := run(t, opts.Version.Image, copts...)
	return &Engine{
		Version:      opts.Version,
		Host:         "tcp://" + endpoint(t, c, "2375/tcp"),
		Alias:        opts.Alias,
		InternalHost: "tcp://" + opts.Alias + ":2375",
		Network:      opts.Network,
		Container:    c,
	}
}

// StartEngines starts n Engines of the same version concurrently on one
// network (aliases engine-1 … engine-n) for multi-host tests.
func StartEngines(t testing.TB, n int, opts EngineOptions) []*Engine {
	t.Helper()
	if opts.Version.Image == "" {
		v, err := SelectEngine()
		if err != nil {
			t.Fatal(err)
		}
		opts.Version = v
	}
	if opts.Network == nil {
		opts.Network = NewNetwork(t)
	}
	engines := make([]*Engine, n)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []string
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o := opts
			o.Alias = fmt.Sprintf("engine-%d", i+1)
			// StartEngine calls t.Fatal on failure, which must not run off
			// the test goroutine; record failures via a sub-recorder.
			rec := &fatalRecorder{TB: t}
			func() {
				defer func() {
					if r := recover(); r != nil && r != errFatal {
						panic(r)
					}
				}()
				engines[i] = StartEngine(rec, o)
			}()
			if rec.msg != "" {
				mu.Lock()
				failures = append(failures, o.Alias+": "+rec.msg)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(failures) > 0 {
		t.Fatalf("start engines: %v", failures)
	}
	return engines
}

// Client returns a Moby client for the Engine. API-version negotiation is
// on by default in the Moby client (the mode the agent adapter uses, #21).
// It is closed at test end.
func (e *Engine) Client(t testing.TB) *client.Client {
	t.Helper()
	cli, err := client.New(client.WithHost(e.Host))
	if err != nil {
		t.Fatalf("moby client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// Exec runs a command inside the DinD container (the Engine's own network
// namespace, where dockerd resolves registry and Git hosts).
func (e *Engine) Exec(ctx context.Context, cmd ...string) (int, string, error) {
	return execIn(ctx, e.Container, cmd...)
}

var errFatal = fmt.Errorf("testharness: fatal in helper goroutine")

// fatalRecorder turns Fatal calls on a non-test goroutine into a recorded
// message plus a panic(errFatal) that the caller recovers.
type fatalRecorder struct {
	testing.TB
	msg string
}

func (r *fatalRecorder) Fatal(args ...any) { r.msg = fmt.Sprint(args...); panic(errFatal) }

func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.msg = fmt.Sprintf(format, args...)
	panic(errFatal)
}
