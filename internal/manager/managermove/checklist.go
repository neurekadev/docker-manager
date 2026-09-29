package managermove

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Checklist actions of an environment after the move.
const (
	// ActionSetManagerURL: the agent dialed another address than the
	// public URL (it reaches the locked old manager): set
	// DOCKER_AGENT_MANAGER_URL to the public URL.
	ActionSetManagerURL = "set_manager_url"
	// ActionMigrate: the old server's environment still holds stacks:
	// migrate them to the new server's environment.
	ActionMigrate = "migrate"
	// ActionArchive: the old server's environment is empty: archive it.
	ActionArchive = "archive"
)

// Checklist is the finish checklist of an arrived move.
type Checklist struct {
	// NewEnvironmentAdded: an environment was enrolled after the arrival
	// (the new server's own agent).
	NewEnvironmentAdded bool
	// Environments are the environments with something left to do.
	Environments []ChecklistEnvironment
}

// ChecklistEnvironment is one environment to act on.
type ChecklistEnvironment struct {
	ID         string
	Name       string
	Online     bool
	ManagerURL string
	StackCount int
	Actions    []string
}

// checklist builds the finish checklist: environments whose agent last
// dialed another manager address than the public URL (capabilities
// transport.managerUrl) must be pointed at it; those are the old
// server's co-located environments, which then are migrated (stacks
// left) or archived (empty).
func (s *Service) checklist(ctx context.Context, arrived domain.ManagerMove) (Checklist, error) {
	envs, err := store.ListEnvironments(ctx, s.db, domain.EnvironmentFilter{Statuses: []domain.EnvironmentStatus{domain.EnvironmentActive}})
	if err != nil {
		return Checklist{}, err
	}
	out := Checklist{Environments: []ChecklistEnvironment{}}
	for _, e := range envs {
		if arrived.ArrivedAt != nil && e.CreatedAt.After(*arrived.ArrivedAt) {
			out.NewEnvironmentAdded = true
			continue
		}
		if e.AgentID == "" {
			continue
		}
		a, err := store.GetAgent(ctx, s.db, e.AgentID)
		if errors.Is(err, domain.ErrAgentNotFound) {
			continue
		}
		if err != nil {
			return Checklist{}, err
		}
		managerURL := capabilitiesManagerURL(a.Capabilities)
		if managerURL == "" || SameOrigin(managerURL, s.opts.PublicURL) {
			continue
		}
		stacks, err := store.ListStacks(ctx, s.db, domain.StackFilter{EnvironmentID: e.ID})
		if err != nil {
			return Checklist{}, err
		}
		c := ChecklistEnvironment{ID: e.ID, Name: e.Name, Online: e.Online, ManagerURL: managerURL, StackCount: len(stacks),
			Actions: []string{ActionSetManagerURL}}
		if len(stacks) > 0 {
			c.Actions = append(c.Actions, ActionMigrate)
		} else {
			c.Actions = append(c.Actions, ActionArchive)
		}
		out.Environments = append(out.Environments, c)
	}
	return out, nil
}

// capabilitiesManagerURL reads transport.managerUrl of an agent's last
// capabilities ("" unknown).
func capabilitiesManagerURL(raw string) string {
	if raw == "" {
		return ""
	}
	var c protocol.CapabilitiesPayload
	if json.Unmarshal([]byte(raw), &c) != nil {
		return ""
	}
	return c.Transport.ManagerURL
}

// SameOrigin reports whether raw addresses the public URL's origin
// (scheme, host and port; default ports normalized).
func SameOrigin(raw string, public *url.URL) bool {
	u, err := url.Parse(raw)
	if err != nil || public == nil {
		return false
	}
	return strings.EqualFold(u.Scheme, public.Scheme) && hostPort(u) == hostPort(public)
}

func hostPort(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}
