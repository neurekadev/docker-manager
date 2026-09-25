// Package protection is the one place that decides what DockYard may do to
// its own containers, images, volumes and networks (#32). The agent
// identifies them (internal/agent/protect) and annotates its inventory with
// protocol.Protection; the manager and the agent both call Check before any
// destructive operation (the agent refuses regardless of the manager), and
// every feature that selects Docker objects in bulk — prune (#14),
// automatic updates (#20), backup and restore shutdown plans (#10), bulk
// selections and migrations (#35) — drops protected objects with Excluded /
// Filter. The instance owner cannot override a refusal in v1: host-level
// Docker access is the escape hatch.
package protection

import "github.com/neurekadev/dockyard/internal/protocol"

// Roles of protected objects (protocol.Protection.Role).
const (
	// RoleAgent is a DockYard agent container (Self: the connected one).
	RoleAgent = "agent"
	// RoleManager is a DockYard manager container (Self: this instance's,
	// matched through manager.identity).
	RoleManager = "manager"
	// RoleProject is another container of DockYard's own Compose project
	// (e.g. the reverse proxy of the deploy examples).
	RoleProject = "dockyard_project"
	// RoleImage is an image a DockYard container runs.
	RoleImage = "dockyard_image"
	// RoleManagerData is the manager's data volume.
	RoleManagerData = "manager_data"
	// RoleAgentState is the agent's state volume (its credential).
	RoleAgentState = "agent_state"
	// RoleStacks is the stacks volume (#28).
	RoleStacks = "stacks"
	// RoleVolume is another volume mounted into a DockYard container (for
	// example a local backup repository, #10) or of DockYard's project.
	RoleVolume = "dockyard_volume"
	// RoleNetwork is a network of DockYard's containers or project.
	RoleNetwork = "dockyard_network"
)

// Action is an operation on a Docker object.
type Action string

// Actions checked by Check.
const (
	Start   Action = "start"
	Unpause Action = "unpause"
	Stop    Action = "stop"
	Pause   Action = "pause"
	Restart Action = "restart"
	Remove  Action = "remove"
	// Update covers in-place changes and recreation.
	Update Action = "update"
	// Mount is mounting a volume into a new container.
	Mount Action = "mount"
	// Stack actions on DockYard's own Compose project (#7): deploy, down,
	// stop.
	Deploy Action = "deploy"
	Down   Action = "down"
)

// Stable codes of refusals (API error codes and job error classes).
const (
	CodeProtected            = "protected"
	CodeConfirmationRequired = "confirmation_required"
)

// Refusal is a refused operation on a protected object. It is a
// jobexec.ClassedError (class = Code).
type Refusal struct {
	Code   string
	Reason string
	Action Action
}

func (r *Refusal) Error() string { return r.Reason }

// ErrorClass implements jobexec.ClassedError.
func (r *Refusal) ErrorClass() string { return r.Code }

// Recovery implements jobexec.ClassedError.
func (r *Refusal) Recovery() string {
	if r.Code == CodeConfirmationRequired {
		return "Confirm the restart: the DockYard UI and API disconnect until the manager is back."
	}
	return "DockYard does not change its own containers, images and volumes; use Docker on the host if you really need to."
}

// Check decides whether action may run on an object with protection p
// (nil: not protected). confirmed is the caller's explicit confirmation of
// a restart that interrupts DockYard (the co-located manager).
func Check(p *protocol.Protection, action Action, confirmed bool) error {
	if p == nil {
		return nil
	}
	switch action {
	case Start, Unpause:
		return nil
	case Restart:
		if p.RestartAllowed {
			if confirmed {
				return nil
			}
			return &Refusal{Code: CodeConfirmationRequired, Action: action,
				Reason: "restarting this container interrupts DockYard (" + p.Reason + "); confirm the restart to continue"}
		}
	}
	return &Refusal{Code: CodeProtected, Action: action, Reason: "refused to " + string(action) + " a protected DockYard resource: " + p.Reason}
}

// Excluded reports whether an object must be left out of prune candidates,
// automatic update policies, backup/restore shutdown plans, bulk
// selections and migrations.
func Excluded(p *protocol.Protection) bool { return p != nil }

// Exclusion is an item Filter dropped, with the reason to show.
type Exclusion[T any] struct {
	Item   T
	Reason string
}

// Filter splits items into those a bulk operation may touch and the
// protected ones.
func Filter[T any](items []T, protectionOf func(T) *protocol.Protection) (kept []T, excluded []Exclusion[T]) {
	for _, it := range items {
		if p := protectionOf(it); p != nil {
			excluded = append(excluded, Exclusion[T]{Item: it, Reason: p.Reason})
			continue
		}
		kept = append(kept, it)
	}
	return kept, excluded
}

// RestartAllowed reports whether a role may be restarted after an explicit
// confirmation. The connected agent never restarts itself through a job
// (it would kill the job's executor).
func RestartAllowed(role string, self bool) bool {
	switch role {
	case RoleManager, RoleProject:
		return true
	case RoleAgent:
		return !self
	}
	return false
}
