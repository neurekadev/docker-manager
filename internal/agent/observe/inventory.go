package observe

import (
	"context"
	"encoding/json"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// EngineInfo serves the engine.info request: the Engine identity, capacity
// and Docker object counts. Counts that cannot be read are -1; host paths
// (the Docker root dir) are never included.
func (s *Sampler) EngineInfo(ctx context.Context, _ json.RawMessage) (any, error) {
	eng := s.opts.Engine()
	if eng == nil {
		return nil, &session.HandlerError{Code: protocol.CodeEngineUnavailable, Message: "the Docker Engine is not connected", Retryable: true}
	}
	return Inventory(ctx, eng, s.opts.Clock.Now()), nil
}

// Inventory reads the Engine inventory.
func Inventory(ctx context.Context, eng EngineAPI, now time.Time) protocol.EngineInventory {
	id := eng.Identity()
	inv := protocol.EngineInventory{
		EngineID: id.EngineID, Hostname: id.Name, Version: id.Version, APIVersion: id.APIVersion, MinAPIVersion: id.MinAPIVersion,
		NegotiatedAPIVersion: id.NegotiatedAPIVersion, OS: id.OS, Arch: id.Arch, OperatingSystem: id.OperatingSystem,
		KernelVersion: id.KernelVersion, StorageDriver: id.StorageDriver, CgroupVersion: id.CgroupVersion,
		CPUs: max(id.NCPU, 0), MemoryBytes: max(id.MemTotal, 0), Rootless: id.Rootless, DockerDesktop: id.DockerDesktop,
		Containers: -1, ContainersRunning: -1, ContainersPaused: -1, ContainersStopped: -1, Images: -1, Volumes: -1, Networks: -1,
		CollectedAt: now.UTC(),
	}
	if cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true}); err == nil {
		inv.Containers, inv.ContainersRunning, inv.ContainersPaused, inv.ContainersStopped = len(cs), 0, 0, 0
		// Only "running" counts as running: a restarting container is
		// between crashes, so it counts as stopped like created or dead.
		for _, c := range cs {
			switch c.State {
			case "running":
				inv.ContainersRunning++
			case "paused":
				inv.ContainersPaused++
			default:
				inv.ContainersStopped++
			}
		}
	}
	if im, err := eng.ListImages(ctx, false); err == nil {
		inv.Images = len(im)
	}
	if vs, err := eng.ListVolumes(ctx); err == nil {
		inv.Volumes = len(vs)
	}
	if ns, err := eng.ListNetworks(ctx); err == nil {
		inv.Networks = len(ns)
	}
	return inv
}
