package engine

import (
	"context"
	"io"
	"time"
)

// Engine is the agent-owned interface over the Docker Engine. *Client
// implements it with the Moby SDK; agent code depends on this interface so
// it can be faked in Docker-free tests.
type Engine interface {
	Identity() Identity
	Refresh(ctx context.Context) (Identity, error)
	Ping(ctx context.Context) error
	Close() error

	ListContainers(ctx context.Context, f ContainerFilter) ([]Container, error)
	InspectContainer(ctx context.Context, id string) (ContainerDetails, error)
	CreateContainer(ctx context.Context, spec ContainerSpec) (id string, warnings []string, err error)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string, timeout *time.Duration) error
	RestartContainer(ctx context.Context, id string, timeout *time.Duration) error
	PauseContainer(ctx context.Context, id string) error
	UnpauseContainer(ctx context.Context, id string) error
	KillContainer(ctx context.Context, id, signal string) error
	RemoveContainer(ctx context.Context, id string, o RemoveOptions) error
	UpdateContainer(ctx context.Context, id string, u ContainerUpdate) ([]string, error)
	WaitContainer(ctx context.Context, id string) (int64, error)

	ListImages(ctx context.Context, all bool) ([]Image, error)
	InspectImage(ctx context.Context, ref string) (ImageDetails, error)
	PullImage(ctx context.Context, ref string, o PullOptions) (PullResult, error)
	TagImage(ctx context.Context, source, target string) error
	RemoveImage(ctx context.Context, ref string, force, pruneChildren bool) ([]DeletedImage, error)
	LoadImage(ctx context.Context, archive io.Reader) error
	Build(ctx context.Context, spec BuildSpec) (BuildResult, error)

	ListVolumes(ctx context.Context, labels ...string) ([]Volume, error)
	InspectVolume(ctx context.Context, name string) (Volume, error)
	CreateVolume(ctx context.Context, spec VolumeSpec) (Volume, error)
	RemoveVolume(ctx context.Context, name string, force bool) error

	ListNetworks(ctx context.Context, labels ...string) ([]Network, error)
	InspectNetwork(ctx context.Context, idOrName string) (Network, error)
	CreateNetwork(ctx context.Context, spec NetworkSpec) (string, error)
	RemoveNetwork(ctx context.Context, idOrName string) error
	ConnectNetwork(ctx context.Context, netID, containerID string, aliases ...string) error
	DisconnectNetwork(ctx context.Context, netID, containerID string, force bool) error

	Events(ctx context.Context, f EventFilter, fn func(Event) error) error
	Logs(ctx context.Context, id string, o LogOptions, fn func(LogEntry) error) error
	Stats(ctx context.Context, id string, stream bool, fn func(Stats) error) error

	CreateExec(ctx context.Context, containerID string, spec ExecSpec) (string, error)
	AttachExec(ctx context.Context, execID string, stdio ExecIO) error
	ResizeExec(ctx context.Context, execID string, height, width uint) error
	InspectExec(ctx context.Context, execID string) (ExecStatus, error)
}

var _ Engine = (*Client)(nil)
