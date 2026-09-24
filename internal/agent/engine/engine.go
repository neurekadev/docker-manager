// Package engine is the agent's adapter over the official Moby Engine Go
// SDK (github.com/moby/moby/client, #21). It is the only package that talks
// to Docker Engine directly; the Compose SDK adapter (internal/agent/compose)
// is the only other SDK boundary.
//
// Rules:
//   - No SDK type crosses the package boundary: callers use the
//     domain-neutral types in types.go and get *Error values with stable
//     Codes (errors.go).
//   - API version negotiation happens once at Connect; Engines older than
//     MinSupportedAPIVersion are refused with CodeUnsupportedAPIVersion.
//   - Every request is bounded by the caller's context; non-streaming calls
//     additionally get Options.RequestTimeout. Streams (events, logs, stats,
//     exec, pull, build) live exactly as long as their context and are
//     closed when it ends or the callback returns an error.
//   - Registry credentials are encoded per operation in memory and never
//     written to disk or logs.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/client"
	"github.com/moby/moby/client/pkg/versions"

	"github.com/neurekadev/dockyard/internal/buildinfo"
)

// MinSupportedAPIVersion is DockYard's minimum Engine API version (Docker
// Engine 24.0, API 1.43), chosen from the Engine matrix evidence recorded in
// docs/support-matrix.md (#21, #25 Q2). The Moby client itself accepts API
// 1.40 and newer.
const MinSupportedAPIVersion = "1.43"

// DefaultRequestTimeout bounds non-streaming Engine requests.
const DefaultRequestTimeout = 60 * time.Second

// Options configures Connect.
type Options struct {
	// Host is the Engine endpoint (unix:///var/run/docker.sock or tcp://host:port).
	Host string
	// RequestTimeout bounds non-streaming requests (default DefaultRequestTimeout).
	RequestTimeout time.Duration
	Logger         *slog.Logger
}

// Client is a connected Engine. It is safe for concurrent use.
type Client struct {
	api      *client.Client
	opts     Options
	identity Identity
	log      *slog.Logger
}

// Connect creates the SDK client, negotiates the API version, checks it
// against MinSupportedAPIVersion and loads the Engine identity.
func Connect(ctx context.Context, opts Options) (*Client, error) {
	const op = "engine.connect"
	if opts.Host == "" {
		return nil, newError(op, CodeInvalidArgument, "no Engine host configured")
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = DefaultRequestTimeout
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	api, err := client.New(
		client.WithHost(opts.Host),
		client.WithUserAgent("dockyard-agent/"+buildinfo.Get().Version),
	)
	if err != nil {
		return nil, newError(op, CodeInvalidArgument, "invalid Engine host %q: %v", opts.Host, err)
	}
	c := &Client{api: api, opts: opts, log: opts.Logger}
	if err := c.negotiate(ctx); err != nil {
		_ = api.Close()
		return nil, err
	}
	id, err := c.loadIdentity(ctx)
	if err != nil {
		_ = api.Close()
		return nil, err
	}
	c.identity = id
	return c, nil
}

// negotiate pings the Engine with API version negotiation. The SDK would
// otherwise negotiate lazily and silently fall back to its maximum version
// when negotiation fails, so it is forced here and failures are explicit.
func (c *Client) negotiate(ctx context.Context) error {
	const op = "engine.negotiate"
	ctx, cancel := c.bound(ctx)
	defer cancel()
	ping, err := c.api.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true, ForceNegotiate: true})
	if err != nil {
		if ping.APIVersion != "" && versions.LessThan(ping.APIVersion, client.MinAPIVersion) {
			return newError(op, CodeUnsupportedAPIVersion,
				"Docker Engine API %s is not supported: DockYard requires API %s or newer (Docker Engine 24.0+)",
				ping.APIVersion, MinSupportedAPIVersion)
		}
		return wrap(op, err)
	}
	if ping.APIVersion == "" {
		return newError(op, CodeUnsupportedAPIVersion, "the Engine did not report an API version (too old or not a Docker Engine)")
	}
	if versions.LessThan(ping.APIVersion, MinSupportedAPIVersion) {
		return newError(op, CodeUnsupportedAPIVersion,
			"Docker Engine API %s is not supported: DockYard requires API %s or newer (Docker Engine 24.0+)",
			ping.APIVersion, MinSupportedAPIVersion)
	}
	if ping.OSType != "" && ping.OSType != "linux" {
		return newError(op, CodeUnsupported, "%s Engines are not supported; DockYard manages Linux Engines", ping.OSType)
	}
	return nil
}

func (c *Client) loadIdentity(ctx context.Context) (Identity, error) {
	const op = "engine.identity"
	ctx, cancel := c.bound(ctx)
	defer cancel()
	v, err := c.api.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return Identity{}, wrap(op, err)
	}
	info, err := c.api.Info(ctx, client.InfoOptions{})
	if err != nil {
		return Identity{}, wrap(op, err)
	}
	in := info.Info
	id := Identity{
		EngineID:             in.ID,
		Name:                 in.Name,
		Version:              v.Version,
		APIVersion:           v.APIVersion,
		MinAPIVersion:        v.MinAPIVersion,
		NegotiatedAPIVersion: c.api.ClientVersion(),
		OS:                   in.OSType,
		Arch:                 normalizeArch(v.Arch, in.Architecture),
		OperatingSystem:      in.OperatingSystem,
		KernelVersion:        in.KernelVersion,
		DockerRootDir:        in.DockerRootDir,
		StorageDriver:        in.Driver,
		CgroupVersion:        in.CgroupVersion,
		NCPU:                 in.NCPU,
		MemTotal:             in.MemTotal,
		SecurityOptions:      securityOptionNames(in.SecurityOptions),
		DockerDesktop: strings.Contains(v.Platform.Name, "Docker Desktop") ||
			in.OperatingSystem == "Docker Desktop" ||
			slices.ContainsFunc(in.Labels, func(l string) bool { return strings.HasPrefix(l, "com.docker.desktop.") }),
	}
	id.Rootless = slices.Contains(id.SecurityOptions, "rootless")
	id.Capabilities = capabilities(id)
	return id, nil
}

// Identity returns the Engine identity loaded at Connect.
func (c *Client) Identity() Identity { return c.identity }

// Refresh reloads the identity (e.g. after an Engine restart).
func (c *Client) Refresh(ctx context.Context) (Identity, error) {
	if err := c.negotiate(ctx); err != nil {
		return Identity{}, err
	}
	id, err := c.loadIdentity(ctx)
	if err != nil {
		return Identity{}, err
	}
	c.identity = id
	return id, nil
}

// Ping checks that the Engine answers.
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	_, err := c.api.Ping(ctx, client.PingOptions{})
	return wrap("engine.ping", err)
}

// Close releases the client's connections. Streams still running end with
// their contexts; Close does not wait for them.
func (c *Client) Close() error {
	if c == nil || c.api == nil {
		return nil
	}
	return c.api.Close()
}

// bound applies RequestTimeout to a non-streaming request.
func (c *Client) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.opts.RequestTimeout)
}

// boundExtra applies RequestTimeout plus extra (e.g. a stop grace period).
func (c *Client) boundExtra(ctx context.Context, extra time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.opts.RequestTimeout+extra)
}

// Capability names: the planned v1 operations (#21).
const (
	CapContainers   = "container.crud"
	CapContainerOps = "container.lifecycle"
	CapImages       = "image.crud"
	CapImagePull    = "image.pull"
	CapImageBuild   = "image.build"
	CapBuildGit     = "image.build.git"
	CapVolumes      = "volume.crud"
	CapNetworks     = "network.crud"
	CapEvents       = "events.stream"
	CapLogs         = "logs.stream"
	CapStats        = "stats.stream"
	CapExec         = "exec"
	CapCompose      = "compose.lifecycle"
)

// capabilities evaluates the v1 operations for an Engine that passed the
// minimum-version check. BuildKit through the Engine API needs API 1.39+
// (always true above the minimum) and a Linux Engine.
func capabilities(id Identity) []Capability {
	all := []string{CapContainers, CapContainerOps, CapImages, CapImagePull, CapImageBuild, CapBuildGit,
		CapVolumes, CapNetworks, CapEvents, CapLogs, CapStats, CapExec, CapCompose}
	out := make([]Capability, 0, len(all))
	for _, name := range all {
		c := Capability{Name: name, Supported: true}
		switch {
		case id.OS != "linux":
			c.Supported, c.Reason = false, "only Linux Engines are supported"
		case versions.LessThan(id.NegotiatedAPIVersion, MinSupportedAPIVersion):
			c.Supported, c.Reason = false, fmt.Sprintf("Engine API %s is below the minimum %s", id.NegotiatedAPIVersion, MinSupportedAPIVersion)
		}
		out = append(out, c)
	}
	return out
}

// normalizeArch prefers the Engine's Go architecture name and falls back to
// the uname machine name.
func normalizeArch(goArch, unameArch string) string {
	if goArch != "" {
		return goArch
	}
	switch unameArch {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	}
	return unameArch
}

// securityOptionNames turns "name=seccomp,profile=builtin" into "seccomp".
func securityOptionNames(opts []string) []string {
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		for kv := range strings.SplitSeq(o, ",") {
			if name, ok := strings.CutPrefix(kv, "name="); ok {
				out = append(out, name)
			}
		}
	}
	return out
}

// IsCode reports whether err carries code.
func IsCode(err error, code Code) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}
