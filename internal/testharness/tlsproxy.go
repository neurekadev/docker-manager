//go:build integration

package testharness

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TLSProxyOptions configures StartTLSProxy.
type TLSProxyOptions struct {
	// Network attaches the proxy (alias "proxy"); nil creates one.
	Network *Network
	// Upstream is host:port as reachable from the proxy container: a
	// container alias on the network, or host.testcontainers.internal:<p>
	// together with HostAccessPorts for a server in the test process.
	Upstream        string
	HostAccessPorts []int
}

// TLSProxy is Caddy terminating TLS with its internal CA for "localhost"
// and reverse-proxying everything (including SSE and WebSocket, unbuffered)
// to Upstream — the same shape as deploy/compose (#27).
type TLSProxy struct {
	// URL is https://localhost:<port> from the test process.
	URL string
	// RootCAPEM is Caddy's local root certificate.
	RootCAPEM []byte
	// Client trusts RootCAPEM (no InsecureSkipVerify).
	Client    *http.Client
	Network   *Network
	Container *testcontainers.DockerContainer
}

const caddyRootCA = "/data/caddy/pki/authorities/local/root.crt"

// StartTLSProxy starts the proxy and waits until it serves TLS with a
// certificate chaining to its root.
func StartTLSProxy(t testing.TB, opts TLSProxyOptions) *TLSProxy {
	t.Helper()
	if opts.Upstream == "" {
		t.Fatal("TLSProxyOptions.Upstream is required")
	}
	if opts.Network == nil {
		opts.Network = NewNetwork(t)
	}
	caddyfile := fmt.Sprintf(`{
	admin off
	local_certs
	skip_install_trust
}

https://localhost {
	tls internal
	reverse_proxy %s {
		flush_interval -1
	}
}
`, opts.Upstream)
	copts := []testcontainers.ContainerCustomizer{
		testcontainers.WithFiles(testcontainers.ContainerFile{Reader: strings.NewReader(caddyfile), ContainerFilePath: "/etc/caddy/Caddyfile", FileMode: 0o644}),
		testcontainers.WithExposedPorts("443/tcp"),
		opts.Network.option("proxy"),
		testcontainers.WithWaitStrategy(wait.ForExec([]string{"test", "-s", caddyRootCA}).WithStartupTimeout(2 * time.Minute)),
	}
	if len(opts.HostAccessPorts) > 0 {
		copts = append(copts, testcontainers.WithHostPortAccess(opts.HostAccessPorts...))
	}
	c := run(t, CaddyImage, copts...)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rc, err := c.CopyFileFromContainer(ctx, caddyRootCA)
	if err != nil {
		t.Fatalf("copy Caddy root CA: %v", err)
	}
	pemBytes, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatalf("Caddy root CA is not PEM: %q", pemBytes)
	}
	port := endpoint(t, c, "443/tcp")
	_, p, _ := strings.Cut(port, ":")
	tp := &TLSProxy{
		URL:       "https://localhost:" + p,
		RootCAPEM: pemBytes,
		Network:   opts.Network,
		Container: c,
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12}
	// "localhost" may resolve to ::1 first; the published port is IPv4.
	tr.DialContext = dialIPv4Loopback
	tp.Client = &http.Client{Transport: tr}

	// The leaf certificate is issued asynchronously after startup: wait for
	// a verified TLS handshake (setup polling, not an assertion).
	deadline := time.Now().Add(90 * time.Second)
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodHead, tp.URL+"/", nil)
		resp, err := tp.Client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("TLS proxy not ready: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	return tp
}
