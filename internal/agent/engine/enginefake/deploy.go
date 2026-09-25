package enginefake

import "github.com/neurekadev/dockyard/internal/agent/engine"

// Deployment is DockYard deployed on a fake Engine like the deploy
// examples (deploy/caddy): Compose project "dockyard" with the manager,
// the co-located agent and a reverse proxy, their volumes and network; or,
// for a remote host, only the agent (project "dockyard-agent").
type Deployment struct {
	AgentID, ManagerID, ProxyID          string
	AgentImage, ManagerImage, ProxyImage string
	// Volumes and networks by role.
	ManagerData, AgentState, Stacks, ProxyData, Network string
}

// Deploy adds DockYard to the Engine: with manager (the co-located
// deployment of deploy/caddy) or agent only (deploy/remote-agent).
func (e *Engine) Deploy(withManager bool) Deployment {
	project := "dockyard-agent"
	if withManager {
		project = "dockyard"
	}
	d := Deployment{Stacks: "dockyard_stacks", AgentState: project + "_dockyard_agent_state", Network: project + "_default"}
	d.AgentImage = e.AddImage("ghcr.io/neurekadev/dockyard-agent:edge")
	e.AddNetwork(d.Network, map[string]string{"com.docker.compose.project": project})
	labels := func(service, role string) map[string]string {
		l := map[string]string{"com.docker.compose.project": project, "com.docker.compose.service": service,
			"com.docker.compose.project.working_dir": "/opt/dockyard"}
		if role != "" {
			l["dev.neureka.dockyard.role"] = role
		}
		return l
	}
	d.AgentID = e.AddContainer(engine.ContainerSpec{Name: project + "-dockyard-agent-1", Image: "ghcr.io/neurekadev/dockyard-agent:edge",
		NetworkMode: d.Network, Labels: labels("dockyard-agent", "agent"), Mounts: []engine.MountSpec{
			{Type: "bind", Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
			{Type: "bind", Source: "/var/lib/docker/volumes", Target: "/var/lib/docker/volumes"},
			{Type: "volume", Source: d.Stacks, Target: "/var/lib/docker/volumes/dockyard_stacks/_data"},
			{Type: "volume", Source: d.AgentState, Target: "/var/lib/dockyard-agent"},
		}}, true)
	if !withManager {
		return d
	}
	d.ManagerData, d.ProxyData = project+"_dockyard_data", project+"_caddy_data"
	d.ManagerImage = e.AddImage("ghcr.io/neurekadev/dockyard-manager:edge")
	d.ProxyImage = e.AddImage("caddy:2.11.4-alpine")
	d.ManagerID = e.AddContainer(engine.ContainerSpec{Name: project + "-dockyard-manager-1", Image: "ghcr.io/neurekadev/dockyard-manager:edge",
		NetworkMode: d.Network, Labels: labels("dockyard-manager", "manager"),
		Mounts: []engine.MountSpec{{Type: "volume", Source: d.ManagerData, Target: "/var/lib/dockyard"}}}, true)
	d.ProxyID = e.AddContainer(engine.ContainerSpec{Name: project + "-caddy-1", Image: "caddy:2.11.4-alpine", NetworkMode: d.Network,
		Labels: labels("caddy", ""), Mounts: []engine.MountSpec{{Type: "volume", Source: d.ProxyData, Target: "/data"}},
		Ports: []engine.PortBinding{{ContainerPort: 443, HostPort: 443}}}, true)
	return d
}
