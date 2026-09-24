package engine

import (
	"context"
	"strings"

	"github.com/docker/cli/cli/config/types"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/session/auth/authprovider"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// dockerHubHost is the registry host BuildKit asks credentials for when
// pulling from Docker Hub.
const dockerHubHost = "registry-1.docker.io"

// RegistryHost normalizes a registry address for credential matching:
// scheme and path are dropped, the host is lower-cased and every Docker Hub
// alias maps to registry-1.docker.io.
func RegistryHost(addr string) string {
	h := strings.ToLower(strings.TrimSpace(addr))
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexByte(h, '/'); i >= 0 {
		h = h[:i]
	}
	switch h {
	case "docker.io", "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		return dockerHubHost
	}
	return h
}

// newAuthProvider returns a BuildKit session attachable that serves the
// given credentials from memory. It wraps BuildKit's Docker auth provider
// (which performs token exchanges for the daemon) but disables the
// client-side token authority, whose key seeds BuildKit would otherwise
// persist in the Docker config directory: nothing touches disk.
func newAuthProvider(auths []RegistryAuth) session.Attachable {
	byHost := make(map[string]RegistryAuth, len(auths))
	for _, a := range auths {
		byHost[RegistryHost(a.ServerAddress)] = a
	}
	inner := authprovider.NewDockerAuthProvider(authprovider.DockerAuthProviderConfig{
		AuthConfigProvider: func(_ context.Context, host string, _ []string, _ authprovider.ExpireCachedAuthCheck) (types.AuthConfig, error) {
			a, ok := byHost[RegistryHost(host)]
			if !ok {
				return types.AuthConfig{}, nil // anonymous
			}
			return types.AuthConfig{
				Username:      a.Username,
				Password:      string(a.Password),
				IdentityToken: string(a.IdentityToken),
				ServerAddress: host,
			}, nil
		},
	})
	return &memoryAuth{AuthServer: inner.(auth.AuthServer)}
}

type memoryAuth struct {
	auth.AuthServer
}

func (a *memoryAuth) Register(s *grpc.Server) { auth.RegisterAuthServer(s, a) }

func (a *memoryAuth) GetTokenAuthority(context.Context, *auth.GetTokenAuthorityRequest) (*auth.GetTokenAuthorityResponse, error) {
	return nil, status.Error(codes.Unavailable, "client side tokens disabled")
}

func (a *memoryAuth) VerifyTokenAuthority(context.Context, *auth.VerifyTokenAuthorityRequest) (*auth.VerifyTokenAuthorityResponse, error) {
	return nil, status.Error(codes.Unavailable, "client side tokens disabled")
}
