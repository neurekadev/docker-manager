// Package regauth turns the registry credentials of a job command (#19,
// protocol.CommandSecrets) into the Engine adapter's per-operation
// engine.RegistryAuth values. Executors call it inside a step and pass the
// result straight to engine.PullOptions.Auth, engine.BuildSpec.RegistryAuth
// or the Compose adapter; nothing is stored, logged or written to a Docker
// config.
package regauth

import (
	"errors"
	"fmt"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/imageref"
	"code.neureka.dev/docker-manager/docker-manager/internal/logging"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// ErrMissing means the job input names a registry connection for the
// image's registry but the command carries no credential for it. Executors
// must fail instead of pulling anonymously.
var ErrMissing = errors.New("regauth: the command carries no credential for the image's registry")

func toAuth(c protocol.RegistryCredential) engine.RegistryAuth {
	addr := c.ServerAddress
	if addr == "" {
		addr = imageref.ServerAddress(c.Host)
	}
	return engine.RegistryAuth{ServerAddress: addr, Username: c.Username, Password: logging.Secret(c.Secret)}
}

// ForReference returns the credential for the registry of reference, or
// nil for anonymous access when the command carries none for that host.
// With required set (the job input named a connection for this image) a
// missing credential is ErrMissing.
func ForReference(s *protocol.CommandSecrets, reference string, required bool) (*engine.RegistryAuth, error) {
	ref, err := imageref.Parse(reference)
	if err != nil {
		return nil, fmt.Errorf("regauth: %w", err)
	}
	if s != nil {
		for _, c := range s.Registries {
			if c.Host == ref.Host {
				a := toAuth(c)
				return &a, nil
			}
		}
	}
	if required {
		return nil, ErrMissing
	}
	return nil, nil
}

// All returns every registry credential of the command, for builds (the
// BuildKit session answers credential requests per registry host).
func All(s *protocol.CommandSecrets) []engine.RegistryAuth {
	if s == nil {
		return nil
	}
	out := make([]engine.RegistryAuth, 0, len(s.Registries))
	for _, c := range s.Registries {
		out = append(out, toAuth(c))
	}
	return out
}
