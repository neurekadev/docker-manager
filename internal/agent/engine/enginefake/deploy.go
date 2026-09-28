package enginefake

import "code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"

// Deployment is Docker Manager deployed on a fake Engine like the documented
// compose.yaml plus a reverse proxy: Compose project "docker-manager" with the manager,
// the co-located agent and a reverse proxy, their volumes (docker-manager_data,
// docker-manager_agent, docker-manager_stacks) and network; or, for a remote host, only
// the agent (the documented remote-host compose.yaml, also project "docker-manager").
type Deployment struct {
	AgentID, ManagerID, ProxyID          string
	AgentImage, ManagerImage, ProxyImage string
	// Volumes and networks by role.
	ManagerData, AgentState, Stacks, ProxyData, Network string
}

// Deploy adds Docker Manager to the Engine: with manager (the co-located
// deployment) or agent only (a remote host).
func (e *Engine) Deploy(withManager bool) Deployment {
	const project = "docker-manager"
	d := Deployment{Stacks: project + "_stacks", AgentState: project + "_agent", Network: project}
	d.AgentImage = e.AddImage("code.neureka.dev/docker-manager/docker-agent:edge")
	e.AddNetwork(d.Network, map[string]string{"com.docker.compose.project": project})
	labels := func(service, role string) map[string]string {
		l := map[string]string{"com.docker.compose.project": project, "com.docker.compose.service": service,
			"com.docker.compose.project.working_dir": "/opt/docker-manager"}
		if role != "" {
			l["dev.neureka.docker-manager.role"] = role
		}
		return l
	}
	d.AgentID = e.AddContainer(engine.ContainerSpec{Name: project + "-docker-agent-1", Image: "code.neureka.dev/docker-manager/docker-agent:edge",
		NetworkMode: d.Network, Labels: labels("docker-agent", "agent"), Mounts: []engine.MountSpec{
			{Type: "bind", Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
			{Type: "bind", Source: "/var/lib/docker/volumes", Target: "/var/lib/docker/volumes"},
			{Type: "volume", Source: d.Stacks, Target: "/var/lib/docker/volumes/docker-manager_stacks/_data"},
			{Type: "volume", Source: d.AgentState, Target: "/var/lib/docker-agent"},
		}}, true)
	if !withManager {
		return d
	}
	d.ManagerData, d.ProxyData = project+"_data", project+"_caddy_data"
	d.ManagerImage = e.AddImage("code.neureka.dev/docker-manager/docker-manager:edge")
	d.ProxyImage = e.AddImage("caddy:2.11.4-alpine")
	d.ManagerID = e.AddContainer(engine.ContainerSpec{Name: project + "-docker-manager-1", Image: "code.neureka.dev/docker-manager/docker-manager:edge",
		NetworkMode: d.Network, Labels: labels("docker-manager", "manager"),
		Mounts: []engine.MountSpec{{Type: "volume", Source: d.ManagerData, Target: "/var/lib/docker-manager"}}}, true)
	d.ProxyID = e.AddContainer(engine.ContainerSpec{Name: project + "-caddy-1", Image: "caddy:2.11.4-alpine", NetworkMode: d.Network,
		Labels: labels("caddy", ""), Mounts: []engine.MountSpec{{Type: "volume", Source: d.ProxyData, Target: "/data"}},
		Ports: []engine.PortBinding{{ContainerPort: 443, HostPort: 443}}}, true)
	return d
}
