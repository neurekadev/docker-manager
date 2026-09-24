//go:build integration

package testharness

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/network"
)

// setupTimeout bounds fixture startup (image pulls included).
const setupTimeout = 5 * time.Minute

// Network is a user-defined Docker network shared by fixtures and Engines.
// Containers on it resolve each other by alias; so does the dockerd of a
// DinD Engine attached to it (image pulls, Git build contexts).
type Network struct {
	*testcontainers.DockerNetwork
}

// NewNetwork creates a network removed when the test ends.
func NewNetwork(t testing.TB) *Network {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	nw, err := network.New(ctx)
	testcontainers.CleanupNetwork(t, nw)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	return &Network{DockerNetwork: nw}
}

func (n *Network) option(aliases ...string) testcontainers.CustomizeRequestOption {
	return network.WithNetwork(aliases, n.DockerNetwork)
}

// run starts a container and registers its cleanup.
func run(t testing.TB, image string, opts ...testcontainers.ContainerCustomizer) *testcontainers.DockerContainer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()
	c, err := testcontainers.Run(ctx, image, opts...)
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		if c != nil {
			if logs, lerr := c.Logs(ctx); lerr == nil {
				b, _ := io.ReadAll(io.LimitReader(logs, 64<<10))
				_ = logs.Close()
				t.Logf("%s logs:\n%s", image, b)
			}
		}
		t.Fatalf("start %s: %v", image, err)
	}
	return c
}

// endpoint returns host:port of a container port as reachable from the test
// process.
func endpoint(t testing.TB, c *testcontainers.DockerContainer, port string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ep, err := c.PortEndpoint(ctx, port, "")
	if err != nil {
		t.Fatalf("endpoint %s: %v", port, err)
	}
	// Docker Desktop and some CI hosts report "localhost"; IPv4 avoids
	// surprises with ::1 not being forwarded.
	return strings.Replace(ep, "localhost:", "127.0.0.1:", 1)
}

// containerIP returns the container's IP address on the network.
func containerIP(t testing.TB, c *testcontainers.DockerContainer, n *Network) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ins, err := c.Inspect(ctx)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if es, ok := ins.NetworkSettings.Networks[n.Name]; ok && es.IPAddress.IsValid() {
		return es.IPAddress.String()
	}
	t.Fatalf("container has no address on network %s", n.Name)
	return ""
}

// execIn runs cmd in c and returns the exit code and combined output.
func execIn(ctx context.Context, c *testcontainers.DockerContainer, cmd ...string) (int, string, error) {
	code, r, err := c.Exec(ctx, cmd, tcexec.Multiplexed())
	if err != nil {
		return code, "", fmt.Errorf("exec %v: %w", cmd, err)
	}
	var out bytes.Buffer
	if r != nil {
		_, _ = io.Copy(&out, r)
	}
	return code, out.String(), nil
}

// dialIPv4Loopback dials 127.0.0.1 for "localhost": published container
// ports are bound on IPv4, while localhost may resolve to ::1 first.
func dialIPv4Loopback(ctx context.Context, netw, addr string) (net.Conn, error) {
	if host, port, err := net.SplitHostPort(addr); err == nil && host == "localhost" {
		addr = net.JoinHostPort("127.0.0.1", port)
	}
	var d net.Dialer
	return d.DialContext(ctx, netw, addr)
}
