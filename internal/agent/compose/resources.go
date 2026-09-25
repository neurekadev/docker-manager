package compose

import (
	"sort"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
)

// Resources summarizes what a loaded project creates on an Engine, for
// environment migration previews (#35): its named volumes and networks,
// and per service the containers, published ports and devices. SDK types
// stay inside this package.
type Resources struct {
	Volumes  []VolumeDef
	Networks []NetworkDef
	Services []ServiceResources
}

// VolumeDef is a top-level volume of the project.
type VolumeDef struct {
	// Key is the Compose key; Name the Engine volume name.
	Key      string
	Name     string
	External bool
	Driver   string
	// DriverOpts reports whether driver options are set (e.g. a local
	// volume bound to a host path or NFS: its data lives elsewhere).
	DriverOpts bool
}

// NetworkDef is a top-level network of the project.
type NetworkDef struct {
	Key      string
	Name     string
	External bool
	Driver   string
}

// PortDef is a published port.
type PortDef struct {
	HostIP string
	// Published is the host port (0: ephemeral).
	Published uint16
	Target    uint16
	Protocol  string
}

// ServiceResources are a service's Engine-visible names and host bindings.
type ServiceResources struct {
	Name string
	// ContainerNames are the containers Compose creates (container_name,
	// or <project>-<service>-<n> for each replica).
	ContainerNames []string
	Ports          []PortDef
	// Devices are the host device paths mapped into the service.
	Devices []string
	// Volumes are the named volume keys the service mounts; Anonymous
	// counts its anonymous volume mounts.
	Volumes   []string
	Anonymous int
}

// maxPortRange bounds the ports expanded from one published range.
const maxPortRange = 1024

// Resources returns the project's resource summary (sorted).
func (p *Project) Resources() Resources {
	var r Resources
	m := p.model
	if m == nil {
		return r
	}
	for key, v := range m.Volumes {
		r.Volumes = append(r.Volumes, VolumeDef{Key: key, Name: volumeName(m.Name, key, v), External: bool(v.External),
			Driver: driverOr(v.Driver), DriverOpts: len(v.DriverOpts) > 0})
	}
	sort.Slice(r.Volumes, func(i, j int) bool { return r.Volumes[i].Key < r.Volumes[j].Key })
	for key, n := range m.Networks {
		name := n.Name
		if name == "" {
			name = m.Name + "_" + key
		}
		r.Networks = append(r.Networks, NetworkDef{Key: key, Name: name, External: bool(n.External), Driver: n.Driver})
	}
	sort.Slice(r.Networks, func(i, j int) bool { return r.Networks[i].Key < r.Networks[j].Key })
	for _, name := range m.ServiceNames() {
		s := m.Services[name]
		sr := ServiceResources{Name: name}
		if s.ContainerName != "" {
			sr.ContainerNames = []string{s.ContainerName}
		} else {
			for i := 1; i <= max(s.GetScale(), 1); i++ {
				sr.ContainerNames = append(sr.ContainerNames, strings.Join([]string{m.Name, name, strconv.Itoa(i)}, api.Separator))
			}
		}
		for _, pc := range s.Ports {
			sr.Ports = append(sr.Ports, portDefs(pc)...)
		}
		for _, d := range s.Devices {
			sr.Devices = append(sr.Devices, d.Source)
		}
		for _, v := range s.Volumes {
			if v.Type != types.VolumeTypeVolume {
				continue
			}
			if v.Source == "" {
				sr.Anonymous++
			} else {
				sr.Volumes = append(sr.Volumes, v.Source)
			}
		}
		r.Services = append(r.Services, sr)
	}
	return r
}

func driverOr(d string) string {
	if d == "" {
		return "local"
	}
	return d
}

// volumeName is the Engine name of a top-level volume (the loader
// normally resolves it; the fallback mirrors Compose's naming).
func volumeName(project, key string, v types.VolumeConfig) string {
	if v.Name != "" {
		return v.Name
	}
	if v.External {
		return key
	}
	return project + "_" + key
}

// portDefs expands a published port (a single port, a range or empty).
func portDefs(pc types.ServicePortConfig) []PortDef {
	proto := pc.Protocol
	if proto == "" {
		proto = "tcp"
	}
	target := uint16(min(pc.Target, 65535)) //nolint:gosec // bounded
	pub := strings.TrimSpace(pc.Published)
	if pub == "" {
		return []PortDef{{HostIP: pc.HostIP, Target: target, Protocol: proto}}
	}
	lo, hi, ok := strings.Cut(pub, "-")
	if !ok {
		hi = lo
	}
	a, err1 := strconv.ParseUint(lo, 10, 16)
	b, err2 := strconv.ParseUint(hi, 10, 16)
	if err1 != nil || err2 != nil || b < a {
		return nil
	}
	var out []PortDef
	for port := a; port <= b && len(out) < maxPortRange; port++ {
		p := uint16(port) //nolint:gosec // port <= b <= 65535 (ParseUint bitSize 16)
		out = append(out, PortDef{HostIP: pc.HostIP, Published: p, Target: target, Protocol: proto})
	}
	return out
}
