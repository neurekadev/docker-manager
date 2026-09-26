package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/compose"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/protect"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// preview answers migration.preview: facts about the source stack or
// volume, or what the destination would collide with. It changes nothing.
func (s *Service) preview(ctx context.Context, raw json.RawMessage) (any, error) {
	in, err := decode[protocol.MigrationPreviewInput](raw)
	if err != nil {
		return nil, err
	}
	if err := in.Validate(); err != nil {
		return nil, invalid(err)
	}
	if in.Role == protocol.RoleSource {
		f, err := s.previewSource(ctx, *in.Source)
		if err != nil {
			return nil, err
		}
		return protocol.MigrationPreviewOutput{Source: f}, nil
	}
	f, err := s.previewDestination(ctx, *in.Destination)
	if err != nil {
		return nil, err
	}
	return protocol.MigrationPreviewOutput{Destination: f}, nil
}

// budget bounds the data scans of one preview answer (nil: no scan).
type budget struct{ ctx context.Context }

func platformOf(id engine.Identity) string {
	os := id.OS
	if os == "" {
		os = "linux"
	}
	return os + "/" + id.Arch
}

func (s *Service) previewSource(ctx context.Context, q protocol.MigrationSourceQuery) (*protocol.MigrationSourceFacts, error) {
	eng, err := s.engine()
	if err != nil {
		return nil, err
	}
	set, cs, err := s.protectedSet(ctx, eng)
	if err != nil {
		return nil, err
	}
	out := &protocol.MigrationSourceFacts{Platform: platformOf(eng.Identity())}
	// One measuring budget for the whole answer (the request has a
	// deadline): data beyond it is reported as a lower bound.
	var measure *budget
	if q.Measure {
		mctx, cancel := context.WithTimeout(ctx, s.opts.MeasureTimeout)
		defer cancel()
		measure = &budget{ctx: mctx}
	}
	if q.Volume != "" {
		v, err := s.volumeFacts(ctx, eng, set, cs, q.Volume, measure)
		if err != nil {
			return nil, err
		}
		if !v.Exists {
			return nil, fail(protocol.CodeNotFound, "volume %s does not exist", q.Volume)
		}
		out.Volume = &v
		return out, nil
	}
	pf, err := s.projectFacts(ctx, eng, set, cs, *q.Stack, measure)
	if err != nil {
		return nil, err
	}
	out.Project = pf
	return out, nil
}

func (s *Service) projectFacts(ctx context.Context, eng engine.Engine, set *protect.Set, cs []engine.Container,
	ref protocol.ProjectRef, measure *budget) (*protocol.MigrationProjectFacts, error) {
	pfs, dir, err := s.openProject(ref)
	if err != nil {
		return nil, err
	}
	defer func() { _ = pfs.Close() }()
	p, err := compose.LoadProject(ctx, projectSpec(pfs, ref, filepath.FromSlash(dir)))
	if err != nil {
		return nil, fail(protocol.CodeInvalidArgument, "the project does not load: %s", err.Error())
	}
	res := p.Resources()
	f := &protocol.MigrationProjectFacts{Name: p.Name, Dir: dir, Services: []protocol.MigrationServiceFacts{},
		Volumes: []protocol.MigrationVolumeFacts{}, Networks: []protocol.MigrationNetworkFacts{}, Binds: []protocol.ComposeBind{}}
	if pr := set.Project(p.Name); pr != nil {
		f.Protected, f.ProtectionReason = true, pr.Reason
	}
	containers, err := lifecycle.ProjectContainers(ctx, eng, p.Name)
	if err != nil {
		return nil, engineError(err)
	}
	running := map[string]bool{}
	for _, c := range containers {
		if c.State == "running" {
			running[c.Labels[lifecycle.ComposeServiceLabel]] = true
		}
	}
	byName := map[string]compose.ServiceResources{}
	for _, sr := range res.Services {
		byName[sr.Name] = sr
	}
	for _, svc := range p.Services {
		sr := byName[svc.Name]
		sf := protocol.MigrationServiceFacts{Name: svc.Name, Image: svc.Image, Build: svc.Build, ContainerNames: sr.ContainerNames,
			Devices: sr.Devices, Running: running[svc.Name]}
		for _, pd := range sr.Ports {
			sf.Ports = append(sf.Ports, protocol.MigrationPort{HostIP: pd.HostIP, Published: pd.Published, Target: pd.Target, Protocol: pd.Protocol})
		}
		if img, err := eng.InspectImage(ctx, svc.Image); err == nil {
			sf.ImageID, sf.ImageSize, sf.RepoDigests = img.ID, img.Size, img.RepoDigests
			sf.ImagePlatform = img.OS + "/" + img.Architecture
			if img.Variant != "" {
				sf.ImagePlatform += "/" + img.Variant
			}
			sf.ImageProtected = set.Image(img.ID) != nil
		}
		f.Services = append(f.Services, sf)
	}
	named := map[string]bool{}
	for _, v := range res.Volumes {
		vm := measure
		if v.External {
			vm = nil
		}
		vf, err := s.volumeFacts(ctx, eng, set, cs, v.Name, vm)
		if err != nil {
			return nil, err
		}
		vf.Key, vf.External = v.Key, v.External
		if vf.Driver == "" {
			vf.Driver = v.Driver
		}
		if v.DriverOpts && vf.Supported {
			vf.Supported = false
			vf.Reason = "the volume has driver options: its data does not live in the volume directory (only its definition is migrated)"
		}
		named[v.Name] = true
		f.Volumes = append(f.Volumes, vf)
	}
	// Anonymous volumes of the project's containers.
	seen := map[string]bool{}
	for _, c := range containers {
		for _, m := range c.Mounts {
			if m.Type != "volume" || m.Name == "" || named[m.Name] || seen[m.Name] {
				continue
			}
			seen[m.Name] = true
			vf, err := s.volumeFacts(ctx, eng, set, cs, m.Name, measure)
			if err != nil {
				return nil, err
			}
			vf.Anonymous = true
			f.Volumes = append(f.Volumes, vf)
		}
	}
	for _, n := range res.Networks {
		f.Networks = append(f.Networks, protocol.MigrationNetworkFacts{Key: n.Key, Name: n.Name, External: n.External, Driver: n.Driver})
	}
	for _, b := range p.Binds {
		src := filepath.ToSlash(b.Source)
		cb := protocol.ComposeBind{Service: b.Service, Source: src, Target: b.Target, ReadOnly: b.ReadOnly}
		if rel, ok := strings.CutPrefix(src, strings.TrimSuffix(dir, "/")+"/"); ok && protocol.ValidRelativePath(rel) {
			cb.RelPath = rel
		} else if path.Clean(src) == path.Clean(dir) {
			cb.RelPath = "."
		} else {
			cb.External = true
		}
		f.Binds = append(f.Binds, cb)
	}
	for _, w := range p.Warnings {
		f.Warnings = append(f.Warnings, protocol.ComposeIssue{Code: protocol.IssueInvalidProject, Message: w})
	}
	if measure != nil {
		f.DirEntries, f.DirBytes, f.DirSkipped, f.DirTruncated = MeasureTree(measure.ctx, pfs, s.opts.MeasureEntries)
	}
	return f, nil
}

// volumeFacts describes a source volume (existence, support, protection,
// users and, with a measuring context, its size).
func (s *Service) volumeFacts(ctx context.Context, eng engine.Engine, set *protect.Set, cs []engine.Container,
	name string, measure *budget) (protocol.MigrationVolumeFacts, error) {
	vf := protocol.MigrationVolumeFacts{Name: name}
	v, err := eng.InspectVolume(ctx, name)
	if engine.IsCode(err, engine.CodeNotFound) {
		vf.Reason = "the volume does not exist on the source"
		return vf, nil
	}
	if err != nil {
		return vf, engineError(err)
	}
	vf.Exists, vf.Driver = true, v.Driver
	vf.Labels = map[string]string{}
	for k, val := range v.Labels {
		if !strings.HasPrefix(k, protocol.LabelPrefix) {
			vf.Labels[k] = val
		}
	}
	if p := set.Volume(v.Name, v.Labels); p != nil {
		vf.Protected, vf.Reason = true, p.Reason
	}
	if res, err := s.storage(); err == nil && !vf.Protected {
		acc := res.AccessFor(v)
		vf.Supported, vf.Reason = acc.Supported, acc.Reason
		if acc.Supported && len(v.Options) > 0 {
			vf.Supported = false
			vf.Reason = "the volume has driver options: its data does not live in the volume directory (only its definition is migrated)"
		}
	} else if err != nil {
		vf.Reason = "the storage layout has not been verified yet"
	}
	for _, c := range cs {
		for _, m := range c.Mounts {
			if m.Type == "volume" && m.Name == name {
				vf.UsedBy = append(vf.UsedBy, protocol.MigrationContainerUse{Name: strings.TrimPrefix(firstName(c), "/"),
					Running: c.State == "running", Project: c.Labels[protocol.ComposeProjectLabel]})
			}
		}
	}
	if measure != nil && vf.Supported {
		if vfs, err := s.openVolume(v, set); err == nil {
			vf.Entries, vf.Bytes, _, vf.Truncated = MeasureTree(measure.ctx, vfs, s.opts.MeasureEntries)
			_ = vfs.Close()
		}
	}
	return vf, nil
}

func (s *Service) previewDestination(ctx context.Context, q protocol.MigrationDestinationQuery) (*protocol.MigrationDestinationFacts, error) {
	eng, err := s.engine()
	if err != nil {
		return nil, err
	}
	f := &protocol.MigrationDestinationFacts{Platform: platformOf(eng.Identity()), StacksFree: -1, VolumesFree: -1}
	res, _ := s.storage()
	if res != nil {
		f.StacksOK, f.VolumesOK = res.StacksOK(), res.VolumesOK()
		if f.StacksOK {
			f.StacksFree = s.opts.FreeBytes(res.StacksDir)
		}
		if f.VolumesOK {
			f.VolumesFree = s.opts.FreeBytes(res.VolumesDir)
		}
		if !f.StacksOK || !f.VolumesOK {
			var msgs []string
			for _, d := range res.Diagnostics {
				msgs = append(msgs, d.Message)
			}
			f.Reason = strings.Join(msgs, "; ")
		}
	} else {
		f.Reason = "the storage layout has not been verified yet"
	}
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return nil, engineError(err)
	}
	names := map[string]bool{}
	for _, c := range cs {
		for _, n := range c.Names {
			names[strings.TrimPrefix(n, "/")] = true
		}
		if q.ProjectName != "" && c.Labels[protocol.ComposeProjectLabel] == q.ProjectName {
			f.ProjectContainers = append(f.ProjectContainers, strings.TrimPrefix(firstName(c), "/"))
		}
	}
	for _, n := range q.ContainerNames {
		if names[n] {
			f.Containers = append(f.Containers, n)
		}
	}
	vols, err := eng.ListVolumes(ctx)
	if err != nil {
		return nil, engineError(err)
	}
	volLabels := map[string]map[string]string{}
	for _, v := range vols {
		volLabels[v.Name] = v.Labels
	}
	for _, n := range q.Volumes {
		if l, ok := volLabels[n]; ok {
			f.Volumes = append(f.Volumes, protocol.MigrationExistingVolume{Name: n, Migration: l[protocol.LabelMigration]})
		}
	}
	for _, n := range q.ExternalVolumes {
		if _, ok := volLabels[n]; !ok {
			f.MissingVolumes = append(f.MissingVolumes, n)
		}
	}
	nets, err := eng.ListNetworks(ctx)
	if err != nil {
		return nil, engineError(err)
	}
	netNames := map[string]bool{}
	for _, n := range nets {
		netNames[n.Name] = true
	}
	for _, n := range q.Networks {
		if netNames[n] {
			f.Networks = append(f.Networks, n)
		}
	}
	for _, n := range q.ExternalNetworks {
		if !netNames[n] {
			f.MissingNetworks = append(f.MissingNetworks, n)
		}
	}
	f.PortConflicts = portConflicts(cs, q.Ports)
	for _, ref := range q.Images {
		if _, err := eng.InspectImage(ctx, ref); err == nil {
			f.ImagesPresent = append(f.ImagesPresent, ref)
		}
	}
	if f.StacksOK {
		sfs, _, err := s.openStacks()
		if err == nil {
			if q.Dir != "" {
				if _, err := sfs.Lstat(q.Dir); err == nil {
					f.DirExists = true
				}
			}
			if ids, err := sfs.ReadDir(protocol.MigrationStagingDir); err == nil {
				for _, id := range ids {
					if protocol.ValidMigrationID(id) {
						f.Staged = append(f.Staged, id)
					}
				}
			}
			_ = sfs.Close()
		}
	}
	return f, nil
}

// portConflicts lists the wanted published ports running containers hold.
func portConflicts(cs []engine.Container, want []protocol.MigrationPort) []protocol.MigrationPortConflict {
	var out []protocol.MigrationPortConflict
	seen := map[string]bool{}
	for _, w := range want {
		if w.Published == 0 {
			continue
		}
		for _, c := range cs {
			if c.State != "running" {
				continue
			}
			for _, p := range c.Ports {
				if p.PublicPort != w.Published || !strings.EqualFold(orTCP(p.Protocol), orTCP(w.Protocol)) || !ipOverlap(p.HostIP, w.HostIP) {
					continue
				}
				name := strings.TrimPrefix(firstName(c), "/")
				key := fmt.Sprintf("%s/%d/%s", w.Protocol, w.Published, name)
				if !seen[key] {
					seen[key] = true
					out = append(out, protocol.MigrationPortConflict{Port: w, Container: name})
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b protocol.MigrationPortConflict) int {
		if a.Port.Published != b.Port.Published {
			return int(a.Port.Published) - int(b.Port.Published)
		}
		return strings.Compare(a.Container, b.Container)
	})
	return out
}

func orTCP(p string) string {
	if p == "" {
		return "tcp"
	}
	return p
}

// ipOverlap reports whether two host IPs of published ports can collide
// (a wildcard address collides with every address).
func ipOverlap(a, b string) bool {
	wild := func(ip string) bool { return ip == "" || ip == "0.0.0.0" || ip == "::" || ip == "[::]" }
	return wild(a) || wild(b) || a == b
}
