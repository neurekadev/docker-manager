//go:build integration

package testharness

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// RegistryOptions configures StartRegistry.
type RegistryOptions struct {
	// Network attaches the registry (alias "registry"); nil creates one.
	Network *Network
	// User and Password for htpasswd auth; random when empty.
	User, Password string
}

// Registry is a distribution registry with htpasswd auth, fronted by a
// FaultProxy served from the test process.
type Registry struct {
	User, Password string
	// Direct is the registry's URL from the test process, bypassing the
	// proxy (http://127.0.0.1:<port>).
	Direct string
	// InternalAddress is host:port on the network, bypassing the proxy.
	InternalAddress string
	// Proxy injects faults; ProxyURL is its URL from the test process.
	Proxy    *FaultProxy
	ProxyURL string
	// ProxyPort must be passed to EngineOptions.HostAccessPorts; Engines
	// then reach the proxy at EngineAddress
	// (host.testcontainers.internal:<ProxyPort>).
	ProxyPort     int
	EngineAddress string
	Network       *Network
	Container     *testcontainers.DockerContainer
}

// StartRegistry starts the registry fixture and its fault proxy.
func StartRegistry(t testing.TB, opts RegistryOptions) *Registry {
	t.Helper()
	if opts.Network == nil {
		opts.Network = NewNetwork(t)
	}
	if opts.User == "" {
		opts.User = "dockyard"
	}
	if opts.Password == "" {
		opts.Password = RandomSecret("reg-", 12)
	}
	line, err := HtpasswdLine(opts.User, opts.Password)
	if err != nil {
		t.Fatal(err)
	}
	c := run(t, RegistryImage,
		testcontainers.WithEnv(map[string]string{ //nolint:gosec // G101: configuration keys, not credentials
			"REGISTRY_AUTH":                   "htpasswd",
			"REGISTRY_AUTH_HTPASSWD_REALM":    "dockyard-test",
			"REGISTRY_AUTH_HTPASSWD_PATH":     "/auth/htpasswd",
			"REGISTRY_STORAGE_DELETE_ENABLED": "true",
		}),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader:            strings.NewReader(line + "\n"),
			ContainerFilePath: "/auth/htpasswd",
			FileMode:          0o644,
		}),
		testcontainers.WithExposedPorts("5000/tcp"),
		opts.Network.option("registry"),
		// /v2/ answers 401 once the registry serves requests.
		testcontainers.WithWaitStrategy(wait.ForHTTP("/v2/").WithPort("5000/tcp").
			WithStatusCodeMatcher(func(s int) bool { return s == http.StatusUnauthorized }).
			WithStartupTimeout(2*time.Minute)),
	)
	r := &Registry{
		User:            opts.User,
		Password:        opts.Password,
		Direct:          "http://" + endpoint(t, c, "5000/tcp"),
		InternalAddress: "registry:5000",
		Network:         opts.Network,
		Container:       c,
	}
	r.Proxy, err = NewFaultProxy(r.Direct)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: r.Proxy, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	r.ProxyPort = ln.Addr().(*net.TCPAddr).Port
	r.ProxyURL = "http://127.0.0.1:" + strconv.Itoa(r.ProxyPort)
	r.EngineAddress = testcontainers.HostInternal + ":" + strconv.Itoa(r.ProxyPort)
	return r
}

// EngineOptions returns options for an Engine that pulls from this
// registry through the fault proxy.
func (r *Registry) EngineOptions() EngineOptions {
	return EngineOptions{
		Network:            r.Network,
		InsecureRegistries: []string{r.EngineAddress, r.InternalAddress},
		HostAccessPorts:    []int{r.ProxyPort},
	}
}

// Auth returns the X-Registry-Auth value for pulls through EngineAddress.
func (r *Registry) Auth() string { return RegistryAuth(r.User, r.Password, r.EngineAddress) }
