// Package protect identifies Docker Manager's own resources on the agent's Engine
// (#32): the connected agent's container (self-inspection,
// internal/selfid), a co-located manager (its container ID from
// manager.identity, else the dev.neureka.docker-manager.role label of the deploy
// examples), other Docker Manager containers, Docker Manager's own Compose project,
// their images, the manager data and agent state volumes, the stacks
// volume (#28), other volumes mounted into Docker Manager containers (local
// backup repositories, #10) and their networks. The decisions are in
// internal/protection; the agent's executors call them before every
// destructive step, whatever the manager decided.
package protect

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"sync"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/protection"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Default in-container paths of the documented compose.yaml's volumes.
const (
	ManagerDataDir = "/var/lib/docker-manager"
	AgentStateDir  = "/var/lib/docker-agent"
)

// Options configures a Guard.
type Options struct {
	// SelfContainerID is the ID of the agent's own container (selfid.Detect
	// at startup; "" when not in a container).
	SelfContainerID string
	// StacksVolume is the stacks volume name (#28).
	StacksVolume string
	Logger       *slog.Logger
}

// Guard knows the agent's identity and the manager's, and computes the
// protected set of the Engine.
type Guard struct {
	opts Options
	log  *slog.Logger

	mu               sync.Mutex
	managerInstance  string
	managerContainer string
}

// New returns a Guard.
func New(opts Options) *Guard {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Guard{opts: opts, log: log.With("component", "protect")}
}

// SelfContainerID returns the agent's own container ID ("" if unknown).
func (g *Guard) SelfContainerID() string { return g.opts.SelfContainerID }

// SetManager records the manager's identity (manager.identity).
func (g *Guard) SetManager(instanceID, containerID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.managerInstance, g.managerContainer = instanceID, containerID
}

// Manager returns the recorded manager identity.
func (g *Guard) Manager() (instanceID, containerID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.managerInstance, g.managerContainer
}

var containerIDRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ManagerIdentityHandler serves manager.identity with the Engine returned by
// eng (nil while disconnected).
func (g *Guard) ManagerIdentityHandler(eng func() engine.Engine) session.RequestHandler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in protocol.ManagerIdentityInput
		if err := json.Unmarshal(raw, &in); err != nil || len(in.InstanceID) > 128 || (in.ContainerID != "" && !containerIDRE.MatchString(in.ContainerID)) {
			return nil, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: "malformed manager identity"}
		}
		g.SetManager(in.InstanceID, in.ContainerID)
		out := protocol.ManagerIdentityOutput{}
		if e := eng(); e != nil && in.ContainerID != "" {
			if _, err := e.InspectContainer(ctx, in.ContainerID); err == nil {
				out.Colocated = true
			}
		}
		g.log.Info("manager identity received", "manager_instance_id", in.InstanceID, "colocated", out.Colocated)
		return out, nil
	}
}

// Set is the protected objects of one Engine at one moment.
type Set struct {
	containers map[string]*protocol.Protection // by container ID
	images     map[string]*protocol.Protection // by image ID
	volumes    map[string]*protocol.Protection // by name
	networks   map[string]*protocol.Protection // by ID and by name
	projects   map[string]*protocol.Protection // Docker Manager's Compose projects
	// DockerRootDir is the Engine's data root (binding it or an ancestor
	// into a container would expose Docker Manager's volumes).
	DockerRootDir string
}

func lookup(m map[string]*protocol.Protection, k string) *protocol.Protection {
	if p, ok := m[k]; ok {
		c := *p
		return &c
	}
	return nil
}

// Container returns the protection of a container (nil: not protected).
func (s *Set) Container(id string) *protocol.Protection {
	if s == nil {
		return nil
	}
	return lookup(s.containers, id)
}

// Image returns the protection of an image ID.
func (s *Set) Image(id string) *protocol.Protection {
	if s == nil {
		return nil
	}
	return lookup(s.images, id)
}

// Volume returns the protection of a volume (by name, or as part of
// Docker Manager's Compose project by its labels).
func (s *Set) Volume(name string, labels map[string]string) *protocol.Protection {
	if s == nil {
		return nil
	}
	if p := lookup(s.volumes, name); p != nil {
		return p
	}
	return s.projectObject(protection.RoleVolume, labels)
}

// Network returns the protection of a network (by ID or name, or as part
// of Docker Manager's Compose project by its labels).
func (s *Set) Network(id, name string, labels map[string]string) *protocol.Protection {
	if s == nil {
		return nil
	}
	if p := lookup(s.networks, id); p != nil {
		return p
	}
	if p := lookup(s.networks, name); p != nil {
		return p
	}
	return s.projectObject(protection.RoleNetwork, labels)
}

// Project returns the protection of a Compose project (Docker Manager's own).
func (s *Set) Project(name string) *protocol.Protection {
	if s == nil {
		return nil
	}
	return lookup(s.projects, name)
}

func (s *Set) projectObject(role string, labels map[string]string) *protocol.Protection {
	project := labels[protocol.ComposeProjectLabel]
	if project == "" || s.projects[project] == nil {
		return nil
	}
	return prot(role, fmt.Sprintf("part of Docker Manager's own Compose project %q", project), false)
}

func prot(role, reason string, self bool) *protocol.Protection {
	return &protocol.Protection{Role: role, Reason: reason, Self: self, RestartAllowed: protection.RestartAllowed(role, self)}
}

func name(c engine.Container) string {
	if len(c.Names) > 0 {
		return c.Names[0]
	}
	return c.ID[:min(12, len(c.ID))]
}

// Identify computes the protected set from the container list cs (all
// containers) and the Engine (inspections of Docker Manager's containers for
// their networks).
func (g *Guard) Identify(ctx context.Context, eng engine.Engine, cs []engine.Container) *Set {
	s := &Set{containers: map[string]*protocol.Protection{}, images: map[string]*protocol.Protection{},
		volumes: map[string]*protocol.Protection{}, networks: map[string]*protocol.Protection{}, projects: map[string]*protocol.Protection{},
		DockerRootDir: eng.Identity().DockerRootDir}
	self := g.opts.SelfContainerID
	_, manager := g.Manager()
	var ours []engine.Container
	for _, c := range cs {
		var p *protocol.Protection
		switch {
		case self != "" && c.ID == self:
			p = prot(protection.RoleAgent, "the Docker Agent connected to this environment: stopping or removing it cuts Docker Manager off from this host", true)
		case manager != "" && c.ID == manager:
			p = prot(protection.RoleManager, "the Docker Manager of this installation: the UI and API run in it", true)
		case c.Labels[protocol.LabelRole] == "agent":
			p = prot(protection.RoleAgent, fmt.Sprintf("Docker Agent container %s", name(c)), false)
		case c.Labels[protocol.LabelRole] == "manager":
			p = prot(protection.RoleManager, fmt.Sprintf("Docker Manager container %s (%s=manager)", name(c), protocol.LabelRole), false)
		case c.Labels[protocol.LabelRole] == "self-update":
			p = prot(protection.RoleAgent, fmt.Sprintf("Docker Agent self-update helper %s: it is recreating the agent", name(c)), false)
		}
		if p != nil {
			s.containers[c.ID] = p
			ours = append(ours, c)
			if project := c.Labels[protocol.ComposeProjectLabel]; project != "" {
				s.projects[project] = prot(protection.RoleProject, fmt.Sprintf("Docker Manager's own Compose project %q", project), false)
			}
		}
	}
	// The rest of Docker Manager's Compose project (e.g. its reverse proxy).
	for _, c := range cs {
		if _, done := s.containers[c.ID]; done {
			continue
		}
		if project := c.Labels[protocol.ComposeProjectLabel]; project != "" && s.projects[project] != nil {
			s.containers[c.ID] = prot(protection.RoleProject, fmt.Sprintf("container %s of Docker Manager's own Compose project %q", name(c), project), false)
			ours = append(ours, c)
		}
	}
	if g.opts.StacksVolume != "" {
		s.volumes[g.opts.StacksVolume] = prot(protection.RoleStacks, "the Docker Manager stacks volume: every stack's project files", false)
	}
	for _, c := range ours {
		role := s.containers[c.ID].Role
		if c.ImageID != "" && s.images[c.ImageID] == nil {
			s.images[c.ImageID] = prot(protection.RoleImage, fmt.Sprintf("the image of Docker Manager container %s", name(c)), false)
		}
		for _, m := range c.Mounts {
			if m.Type != "volume" || m.Name == "" || s.volumes[m.Name] != nil {
				continue
			}
			switch {
			case role == protection.RoleManager && m.Destination == ManagerDataDir:
				s.volumes[m.Name] = prot(protection.RoleManagerData, "the Docker Manager's data volume (database and secret key)", false)
			case role == protection.RoleAgent && m.Destination == AgentStateDir:
				s.volumes[m.Name] = prot(protection.RoleAgentState, "the Docker Agent's state volume (its credential and job journal)", false)
			default:
				s.volumes[m.Name] = prot(protection.RoleVolume, fmt.Sprintf("mounted into Docker Manager container %s (for example a local backup repository)", name(c)), false)
			}
		}
		d, err := eng.InspectContainer(ctx, c.ID)
		if err != nil {
			g.log.Debug("could not inspect a Docker Manager container", "container_id", c.ID, "error", err)
			continue
		}
		for _, n := range slices.Sorted(maps.Keys(d.Networks)) {
			if slices.Contains(protocol.BuiltinNetworks, n) {
				continue
			}
			p := prot(protection.RoleNetwork, fmt.Sprintf("a network of Docker Manager container %s", name(c)), false)
			if s.networks[n] == nil {
				s.networks[n] = p
			}
			if id := d.Networks[n].NetworkID; id != "" && s.networks[id] == nil {
				s.networks[id] = p
			}
		}
	}
	return s
}
