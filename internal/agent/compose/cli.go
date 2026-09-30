package compose

import (
	"io"
	"strings"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/config/configfile"
	clitypes "github.com/docker/cli/cli/config/types"
	"github.com/docker/cli/cli/context/docker"
	"github.com/docker/cli/cli/context/store"
	"github.com/docker/cli/cli/streams"
	"github.com/moby/moby/client"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
)

// dockerHubConfigKey is the Docker config key for Docker Hub credentials.
const dockerHubConfigKey = "https://index.docker.io/v1/"

// memoryCLI implements the docker/cli command.Cli the Compose SDK is built
// on, entirely in memory (#2, #19):
//
//   - ConfigFile is a fresh configfile per operation holding only that
//     operation's registry credentials. It has no file name (Save fails),
//     no credsStore and no credHelpers, so the SDK never writes a Docker
//     config and never executes a docker-credential-* helper.
//   - Nothing is read from ~/.docker or DOCKER_CONFIG: the standard
//     DockerCli (Initialize/LoadDefaultConfigFile) is never used.
//   - BuildKitEnabled reports false so Compose never looks up or executes
//     the buildx CLI plugin; Docker Manager builds images itself through the
//     Engine's BuildKit before calling the SDK (build.go).
//   - Telemetry providers are no-ops.
type memoryCLI struct {
	api      client.APIClient
	host     string
	cfg      *configfile.ConfigFile
	in       *streams.In
	out, err *streams.Out
	info     command.ServerInfo
}

var _ command.Cli = (*memoryCLI)(nil)

func newMemoryCLI(api client.APIClient, host string, osType string, auths []engine.RegistryAuth, out io.Writer) *memoryCLI {
	cfg := configfile.New("")
	cfg.AuthConfigs = map[string]clitypes.AuthConfig{}
	for _, a := range auths {
		key := authConfigKey(a.ServerAddress)
		cfg.AuthConfigs[key] = clitypes.AuthConfig{
			Username:      a.Username,
			Password:      string(a.Password),
			IdentityToken: string(a.IdentityToken),
			ServerAddress: key,
		}
	}
	return &memoryCLI{
		api:  api,
		host: host,
		cfg:  cfg,
		in:   streams.NewIn(io.NopCloser(strings.NewReader(""))),
		out:  streams.NewOut(out),
		err:  streams.NewOut(out),
		info: command.ServerInfo{OSType: osType},
	}
}

// authConfigKey maps a registry address to the key the Docker config (and
// thus the Compose SDK) looks credentials up by.
func authConfigKey(addr string) string {
	h := engine.RegistryHost(addr)
	if h == "registry-1.docker.io" {
		return dockerHubConfigKey
	}
	return h
}

func (c *memoryCLI) Client() client.APIClient             { return c.api }
func (c *memoryCLI) In() *streams.In                      { return c.in }
func (c *memoryCLI) Out() *streams.Out                    { return c.out }
func (c *memoryCLI) Err() *streams.Out                    { return c.err }
func (c *memoryCLI) SetIn(in *streams.In)                 { c.in = in }
func (c *memoryCLI) ConfigFile() *configfile.ConfigFile   { return c.cfg }
func (c *memoryCLI) ServerInfo() command.ServerInfo       { return c.info }
func (c *memoryCLI) CurrentVersion() string               { return c.api.ClientVersion() }
func (c *memoryCLI) BuildKitEnabled() (bool, error)       { return false, nil }
func (c *memoryCLI) CurrentContext() string               { return "default" }
func (c *memoryCLI) Resource() *resource.Resource         { return resource.Empty() }
func (c *memoryCLI) TracerProvider() trace.TracerProvider { return tracenoop.NewTracerProvider() }
func (c *memoryCLI) MeterProvider() metric.MeterProvider  { return metricnoop.NewMeterProvider() }
func (c *memoryCLI) ContextStore() store.Store            { return nil }
func (c *memoryCLI) DockerEndpoint() docker.Endpoint {
	return docker.Endpoint{EndpointMeta: docker.EndpointMeta{Host: c.host}}
}
