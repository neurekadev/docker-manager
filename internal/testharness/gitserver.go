//go:build integration

package testharness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// GitServerOptions configures StartGitServer.
type GitServerOptions struct {
	// Network attaches the server (alias "gitserver"); nil creates one.
	Network *Network
	// User and Password protect every repository whose name starts with
	// "private"; random when empty.
	User, Password string
}

// GitServer serves the repositories seeded from test/fixtures/git over
// dumb HTTP (Caddy file server). Repositories named private* require basic
// auth; the others are public.
type GitServer struct {
	User, Password string
	Repos          []string
	// URL is the base URL from the test process (http://127.0.0.1:<port>).
	URL string
	// InternalURL is the base URL on the network (http://gitserver), which
	// the dockerd of an Engine on the same network resolves, e.g. for Git
	// build contexts (#33).
	InternalURL string
	// IP is the server's address on the network, for clients that cannot
	// use the network's DNS (containers nested inside a DinD Engine).
	IP        string
	Network   *Network
	Container *testcontainers.DockerContainer
}

// StartGitServer seeds the fixture repositories and serves them.
func StartGitServer(t testing.TB, opts GitServerOptions) *GitServer {
	t.Helper()
	if opts.Network == nil {
		opts.Network = NewNetwork(t)
	}
	if opts.User == "" {
		opts.User = "git-user"
	}
	if opts.Password == "" {
		opts.Password = RandomSecret("git-", 12)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// CopyDirToContainer keeps the directory's base name: <tmp>/git lands
	// at /srv/git.
	root := filepath.Join(t.TempDir(), "git")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	repos, err := SeedGitFixtures(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := BcryptHash(opts.Password)
	if err != nil {
		t.Fatal(err)
	}
	caddyfile := fmt.Sprintf(`{
	admin off
	auto_https off
}

:80 {
	root * /srv/git
	@private path_regexp ^/private[^/]*\.git(/.*)?$
	basic_auth @private {
		%s %s
	}
	file_server
}
`, opts.User, hash)
	c := run(t, CaddyImage,
		testcontainers.WithFiles(
			testcontainers.ContainerFile{Reader: strings.NewReader(caddyfile), ContainerFilePath: "/etc/caddy/Caddyfile", FileMode: 0o644},
			testcontainers.ContainerFile{HostFilePath: root, ContainerFilePath: "/srv/git", FileMode: 0o755},
		),
		testcontainers.WithExposedPorts("80/tcp"),
		opts.Network.option("gitserver"),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/public.git/HEAD").WithPort("80/tcp").WithStartupTimeout(2*time.Minute)),
	)
	return &GitServer{
		User:        opts.User,
		Password:    opts.Password,
		Repos:       repos,
		URL:         "http://" + endpoint(t, c, "80/tcp"),
		InternalURL: "http://gitserver",
		IP:          containerIP(t, c, opts.Network),
		Network:     opts.Network,
		Container:   c,
	}
}

// RepoURL returns the clone URL of repo relative to base (URL, InternalURL
// or "http://"+IP), with credentials embedded when withAuth is set.
func (g *GitServer) RepoURL(base, repo string, withAuth bool) string {
	u := strings.TrimSuffix(base, "/") + "/" + repo + ".git"
	if withAuth {
		u = strings.Replace(u, "://", "://"+g.User+":"+g.Password+"@", 1)
	}
	return u
}
