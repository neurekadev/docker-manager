package stacks

import (
	"context"
	"slices"
	"strings"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Drift reasons (the live Engine state differs from DockYard's intent).
const (
	// DriftMissing: an applied service has no containers.
	DriftMissing = "missing"
	// DriftNotRunning: the stack is deployed but the service does not run
	// (a completed one-shot with exit code 0 is not drift).
	DriftNotRunning = "not_running"
	// DriftRunningWhileStopped: the stack was stopped or taken down by
	// DockYard but the service runs.
	DriftRunningWhileStopped = "running_while_stopped"
	// DriftUnexpected: a service runs that the applied definition lacks.
	DriftUnexpected = "unexpected_service"
	// DriftImageChanged: the container runs another image than the one the
	// last deploy applied.
	DriftImageChanged = "image_changed"
)

// Services returns the stack's services with live Engine state when the
// environment is online (and records it), otherwise the last known state.
func (s *Service) Services(ctx context.Context, st domain.Stack) (domain.StackServicesView, error) {
	var live protocol.ComposeServicesOutput
	err := s.call(ctx, st.EnvironmentID, protocol.ReqComposeServices, protocol.ComposeServicesInput{ProjectName: st.Name}, &live)
	switch {
	case err == nil:
		v := buildView(st, containersOf(live.Containers), true)
		now := s.now()
		v.ObservedAt = &now
		if serr := s.storeLive(ctx, st.ID, live.Containers); serr != nil {
			s.log.Warn("could not record the stack's Engine state", "stack_id", st.ID, "error", serr)
		}
		return v, nil
	case isCode(err, domain.StackErrOffline):
		// Offline: the last observed summary, read-only.
		var cs []domain.StackContainer
		for _, es := range st.EngineServices {
			for i := range es.Containers {
				state := "exited"
				if i < es.Running {
					state = "running"
				}
				c := domain.StackContainer{Service: es.Service, State: state}
				if len(es.ImageIDs) > 0 {
					c.ImageID = es.ImageIDs[0]
				}
				cs = append(cs, c)
			}
		}
		v := buildView(st, cs, false)
		v.ObservedAt = st.EngineObservedAt
		return v, nil
	}
	return domain.StackServicesView{}, err
}

// buildView joins the applied definition with the Engine's containers and
// computes drift.
func buildView(st domain.Stack, containers []domain.StackContainer, live bool) domain.StackServicesView {
	v := domain.StackServicesView{Live: live}
	by := map[string][]domain.StackContainer{}
	for _, c := range containers {
		if c.OneOff {
			continue
		}
		by[c.Service] = append(by[c.Service], c)
	}
	names := map[string]bool{}
	for _, d := range st.Services {
		names[d.Name] = true
	}
	for n := range by {
		names[n] = true
	}
	var sorted []string
	for n := range names {
		sorted = append(sorted, n)
	}
	slices.Sort(sorted)
	for _, n := range sorted {
		sv := domain.StackServiceView{Name: n, Meta: st.ServiceMeta[n], Containers: by[n]}
		if sv.Containers == nil {
			sv.Containers = []domain.StackContainer{}
		}
		for i := range st.Services {
			if st.Services[i].Name == n {
				d := st.Services[i]
				sv.Expected = &d
			}
		}
		for i := range st.Images {
			if st.Images[i].Service == n {
				img := st.Images[i]
				sv.Applied = &img
			}
		}
		sv.Status = serviceStatus(sv.Containers)
		sv.Drift = drift(st, sv)
		if len(sv.Drift) > 0 {
			v.Drift = true
		}
		v.Services = append(v.Services, sv)
	}
	if v.Services == nil {
		v.Services = []domain.StackServiceView{}
	}
	return v
}

func serviceStatus(cs []domain.StackContainer) string {
	if len(cs) == 0 {
		return "missing"
	}
	running, created := 0, 0
	for _, c := range cs {
		switch c.State {
		case "running", "restarting", "paused":
			running++
		case "created":
			created++
		}
	}
	switch {
	case running == len(cs):
		return "running"
	case running > 0:
		return "partial"
	case created == len(cs):
		return "created"
	}
	return "exited"
}

// completed reports whether every container exited with code 0 (a finished
// one-shot service).
func completed(cs []domain.StackContainer) bool {
	for _, c := range cs {
		if c.State != "exited" || c.ExitCode != 0 {
			return false
		}
	}
	return len(cs) > 0
}

func drift(st domain.Stack, sv domain.StackServiceView) []string {
	var out []string
	switch st.Status {
	case domain.StackDeployed, domain.StackFailed:
		if sv.Expected != nil && st.Applied != nil {
			switch {
			case len(sv.Containers) == 0:
				out = append(out, DriftMissing)
			case sv.Status != "running" && sv.Status != "partial" && !completed(sv.Containers):
				out = append(out, DriftNotRunning)
			}
		}
		if sv.Expected == nil && len(sv.Containers) > 0 && len(st.Services) > 0 {
			out = append(out, DriftUnexpected)
		}
	case domain.StackStopped, domain.StackDown:
		if sv.Status == "running" || sv.Status == "partial" {
			out = append(out, DriftRunningWhileStopped)
		}
	}
	if sv.Applied != nil && sv.Applied.ImageID != "" {
		for _, c := range sv.Containers {
			if c.ImageID != "" && c.ImageID != sv.Applied.ImageID {
				out = append(out, DriftImageChanged)
				break
			}
		}
	}
	return out
}

// Image eligibility for digest-driven updates (#20).
const (
	EligibleUnknown     = ""
	IneligibleBuildOnly = "build_only"
	IneligiblePinned    = "digest_pinned"
	IneligibleUntagged  = "untagged"
)

// ImageStatus returns the applied images of the last deploy and whether
// each could follow its tag's digest (#20). Services never deployed by
// DockYard report their definition's image only.
func (s *Service) ImageStatus(st domain.Stack) []domain.StackImageView {
	out := []domain.StackImageView{}
	seen := map[string]bool{}
	for _, i := range st.Images {
		seen[i.Service] = true
		out = append(out, imageView(domain.StackImageView{Service: i.Service, Image: i.Image, ImageID: i.ImageID, Digest: i.Digest,
			Platform: i.Platform, Build: i.Build}))
	}
	for _, d := range st.Services {
		if !seen[d.Name] {
			out = append(out, imageView(domain.StackImageView{Service: d.Name, Image: d.Image, Build: d.Build}))
		}
	}
	slices.SortFunc(out, func(a, b domain.StackImageView) int { return strings.Compare(a.Service, b.Service) })
	return out
}

func imageView(v domain.StackImageView) domain.StackImageView {
	switch {
	case v.Build:
		v.Reason = IneligibleBuildOnly
	case strings.Contains(v.Image, "@"):
		v.Reason = IneligiblePinned
	case !hasTag(v.Image):
		v.Reason = IneligibleUntagged
	default:
		v.Eligible = true
	}
	return v
}

// hasTag reports whether a reference names an explicit tag.
func hasTag(ref string) bool {
	slash := strings.LastIndex(ref, "/")
	return strings.LastIndex(ref, ":") > slash
}

// containersOf converts the agent's container list.
func containersOf(in []protocol.StackContainer) []domain.StackContainer {
	out := make([]domain.StackContainer, 0, len(in))
	for _, c := range in {
		dc := domain.StackContainer{ID: c.ID, Name: c.Name, Service: c.Service, Image: c.Image, ImageID: c.ImageID, State: c.State,
			Health: c.Health, ExitCode: c.ExitCode, OneOff: c.OneOff, RestartPolicy: c.RestartPolicy, NanoCPUs: c.Resources.NanoCPUs,
			CPUShares: c.Resources.CPUShares, Memory: c.Resources.Memory, PidsLimit: c.Resources.PidsLimit, CreatedAt: c.CreatedAt,
			StartedAt: c.StartedAt}
		for _, p := range c.Ports {
			dc.Ports = append(dc.Ports, domain.PortMapping{PrivatePort: p.PrivatePort, PublicPort: p.PublicPort, HostIP: p.HostIP, Protocol: p.Protocol})
		}
		out = append(out, dc)
	}
	return out
}
