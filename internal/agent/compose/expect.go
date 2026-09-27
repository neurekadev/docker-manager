package compose

import (
	"maps"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
)

// Expectation is what Compose creates a service's containers with, for
// comparing a definition with running containers (#7 import by copy:
// variables a tool supplied from outside the files would otherwise be
// lost silently). Env holds resolved values: compare them in memory only;
// never log, journal or send them.
type Expectation struct {
	Image string
	// Env are the service's variables that have a value.
	Env map[string]string
	// Labels are the labels the definition sets (not Compose's own).
	Labels map[string]string
	// Command and Entrypoint are nil when the definition keeps the image's.
	Command    []string
	Entrypoint []string
	User       string
	WorkingDir string
	// Ports are the published ports with a fixed host port; Partial is set
	// when the definition also publishes ranges or random host ports.
	Ports   []ExpectedPort
	Partial bool
	// Mounts are bind mounts (Source: host path) and named volumes
	// (Source: volume name) by container path.
	Mounts map[string]ExpectedMount
}

// ExpectedPort is a published port.
type ExpectedPort struct {
	Target    uint16
	Published uint16
	Protocol  string
	HostIP    string
}

// ExpectedMount is a bind or named-volume mount.
type ExpectedMount struct {
	Type   string // "bind" or "volume"
	Source string
}

// Expected returns what Compose creates the named service's containers
// with (false: not a service of the loaded project).
func (p *Project) Expected(service string) (Expectation, bool) {
	if p == nil || p.model == nil {
		return Expectation{}, false
	}
	s, ok := p.model.Services[service]
	if !ok {
		return Expectation{}, false
	}
	e := Expectation{Image: api.GetImageNameOrDefault(s, p.model.Name), Env: map[string]string{}, Labels: maps.Clone(map[string]string(s.Labels)),
		Command: s.Command, Entrypoint: s.Entrypoint, User: s.User, WorkingDir: s.WorkingDir, Mounts: map[string]ExpectedMount{}}
	if e.Labels == nil {
		e.Labels = map[string]string{}
	}
	for k, v := range s.Environment {
		if v != nil {
			e.Env[k] = *v
		}
	}
	for _, pc := range s.Ports {
		pub, err := strconv.ParseUint(pc.Published, 10, 16)
		if pc.Published == "" || strings.Contains(pc.Published, "-") || err != nil || pub == 0 {
			e.Partial = true
			continue
		}
		proto := pc.Protocol
		if proto == "" {
			proto = "tcp"
		}
		e.Ports = append(e.Ports, ExpectedPort{Target: uint16(pc.Target), Published: uint16(pub), Protocol: proto, HostIP: pc.HostIP}) //nolint:gosec // container ports fit 16 bits
	}
	for _, v := range s.Volumes {
		switch {
		case v.Type == types.VolumeTypeBind:
			e.Mounts[v.Target] = ExpectedMount{Type: "bind", Source: v.Source}
		case v.Type == types.VolumeTypeVolume && v.Source != "":
			name := p.model.Volumes[v.Source].Name
			if name == "" {
				name = p.model.Name + "_" + v.Source
			}
			e.Mounts[v.Target] = ExpectedMount{Type: "volume", Source: name}
		}
	}
	return e, true
}
