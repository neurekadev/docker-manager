package engine

import (
	"context"
	"net/netip"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

// ListVolumes lists volumes, optionally filtered by labels ("key" or
// "key=value").
func (c *Client) ListVolumes(ctx context.Context, labels ...string) ([]Volume, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	f := client.Filters{}
	if len(labels) > 0 {
		f.Add("label", labels...)
	}
	res, err := c.api.VolumeList(ctx, client.VolumeListOptions{Filters: f})
	if err != nil {
		return nil, wrap("volume.list", err)
	}
	out := make([]Volume, 0, len(res.Items))
	for _, v := range res.Items {
		out = append(out, volumeFrom(v))
	}
	return out, nil
}

// InspectVolume returns a volume.
func (c *Client) InspectVolume(ctx context.Context, name string) (Volume, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	res, err := c.api.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if err != nil {
		return Volume{}, wrap("volume.inspect", err)
	}
	return volumeFrom(res.Volume), nil
}

// CreateVolume creates a volume (idempotent in the Engine for an existing
// name with the same driver).
func (c *Client) CreateVolume(ctx context.Context, spec VolumeSpec) (Volume, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	res, err := c.api.VolumeCreate(ctx, client.VolumeCreateOptions{
		Name: spec.Name, Driver: spec.Driver, DriverOpts: spec.DriverOpts, Labels: spec.Labels,
	})
	if err != nil {
		return Volume{}, wrap("volume.create", err)
	}
	return volumeFrom(res.Volume), nil
}

// RemoveVolume removes a volume; CodeConflict when it is in use.
func (c *Client) RemoveVolume(ctx context.Context, name string, force bool) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	_, err := c.api.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: force})
	return wrap("volume.remove", err)
}

func volumeFrom(v volume.Volume) Volume {
	return Volume{
		Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, Scope: v.Scope,
		CreatedAt: parseTime(v.CreatedAt), Labels: v.Labels, Options: v.Options,
	}
}

// ListNetworks lists networks, optionally filtered by labels.
func (c *Client) ListNetworks(ctx context.Context, labels ...string) ([]Network, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	f := client.Filters{}
	if len(labels) > 0 {
		f.Add("label", labels...)
	}
	res, err := c.api.NetworkList(ctx, client.NetworkListOptions{Filters: f})
	if err != nil {
		return nil, wrap("network.list", err)
	}
	out := make([]Network, 0, len(res.Items))
	for _, n := range res.Items {
		out = append(out, networkFrom(n.Network))
	}
	return out, nil
}

// InspectNetwork returns a network and its attached containers.
func (c *Client) InspectNetwork(ctx context.Context, idOrName string) (Network, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	res, err := c.api.NetworkInspect(ctx, idOrName, client.NetworkInspectOptions{})
	if err != nil {
		return Network{}, wrap("network.inspect", err)
	}
	n := networkFrom(res.Network.Network)
	n.Containers = map[string]EndpointInfo{}
	for id, ep := range res.Network.Containers {
		n.Containers[id] = EndpointInfo{
			IPAddress:   prefixAddr(ep.IPv4Address),
			IPv6Address: prefixAddr(ep.IPv6Address),
			MacAddress:  ep.MacAddress.String(),
		}
	}
	return n, nil
}

// CreateNetwork creates a network and returns its ID. An existing network
// with the same name is a conflict on every Engine: API 1.44+ Engines refuse
// duplicates themselves, older ones (Docker 24) would create a second
// network with the same name, so the adapter checks first.
func (c *Client) CreateNetwork(ctx context.Context, spec NetworkSpec) (string, error) {
	const op = "network.create"
	ctx, cancel := c.bound(ctx)
	defer cancel()
	existing, err := c.api.NetworkList(ctx, client.NetworkListOptions{Filters: client.Filters{}.Add("name", spec.Name)})
	if err != nil {
		return "", wrap(op, err)
	}
	for _, n := range existing.Items {
		if n.Name == spec.Name {
			return "", newError(op, CodeConflict, "network with name %s already exists", spec.Name)
		}
	}
	res, err := c.api.NetworkCreate(ctx, spec.Name, client.NetworkCreateOptions{
		Driver: spec.Driver, Internal: spec.Internal, Attachable: spec.Attachable, Labels: spec.Labels, Options: spec.Options,
	})
	if err != nil {
		return "", wrap(op, err)
	}
	return res.ID, nil
}

// RemoveNetwork removes a network; CodeConflict while containers use it.
func (c *Client) RemoveNetwork(ctx context.Context, idOrName string) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	_, err := c.api.NetworkRemove(ctx, idOrName, client.NetworkRemoveOptions{})
	return wrap("network.remove", err)
}

// ConnectNetwork attaches a container to a network with optional aliases.
func (c *Client) ConnectNetwork(ctx context.Context, netID, containerID string, aliases ...string) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	o := client.NetworkConnectOptions{Container: containerID}
	if len(aliases) > 0 {
		o.EndpointConfig = &network.EndpointSettings{Aliases: aliases}
	}
	_, err := c.api.NetworkConnect(ctx, netID, o)
	return wrap("network.connect", err)
}

// DisconnectNetwork detaches a container from a network.
func (c *Client) DisconnectNetwork(ctx context.Context, netID, containerID string, force bool) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	_, err := c.api.NetworkDisconnect(ctx, netID, client.NetworkDisconnectOptions{Container: containerID, Force: force})
	return wrap("network.disconnect", err)
}

func networkFrom(n network.Network) Network {
	out := Network{
		ID: n.ID, Name: n.Name, Driver: n.Driver, Scope: n.Scope, Internal: n.Internal,
		Attachable: n.Attachable, EnableIPv6: n.EnableIPv6, Created: n.Created.UTC(), Labels: n.Labels,
	}
	for _, cfg := range n.IPAM.Config {
		if cfg.Subnet.IsValid() {
			out.Subnets = append(out.Subnets, cfg.Subnet.String())
		}
		if cfg.Gateway.IsValid() {
			out.Gateways = append(out.Gateways, cfg.Gateway.String())
		}
	}
	return out
}

func prefixAddr(p netip.Prefix) string {
	if !p.IsValid() {
		return ""
	}
	return p.Addr().String()
}
