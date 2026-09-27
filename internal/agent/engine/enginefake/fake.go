// Package enginefake is an in-memory engine.Engine for Docker-free tests of
// the code above the Engine adapter: the agent's resource handlers and
// executors (#6) and, through a real agent session, the manager's routes.
// It keeps containers, images, volumes and networks with the Engine's
// conflict rules (name in use, running container, image/volume/network in
// use, predefined networks) and lets tests inject failures per operation.
// Import it from tests only.
package enginefake

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
)

// Engine is the fake. The zero value is not usable; call New.
type Engine struct {
	mu         sync.Mutex
	identity   engine.Identity
	now        func() time.Time
	seq        int
	containers map[string]*Container
	images     map[string]*Image
	volumes    map[string]*engine.Volume
	networks   map[string]*engine.Network
	failures   map[string][]error
	calls      []string
	pullAuths  []*engine.RegistryAuth
	// volumeRoot is where volume mountpoints live (SetVolumeRoot).
	volumeRoot string
	execs      []ExecInstance
	// paths are the files that exist in a container's filesystem, by
	// container ID (SetPaths; none by default).
	paths map[string]map[string]bool
	// events is the Engine's event log (Docker API operations only, not
	// the Add* seeding helpers' containers); notify is closed and replaced
	// whenever an event is appended.
	events []engine.Event
	notify chan struct{}
	// Prune support (#14): build cache records, volume sizes and container
	// writable-layer sizes.
	buildCache  map[string]*engine.BuildCacheRecord
	volumeSizes map[string]int64
	sizes       map[string]int64
	// remote maps a normalized tag to the digest the registry serves for
	// it (Publish); startHealth sets the health of containers of an image
	// when they start (SetStartHealth).
	remote      map[string]string
	startHealth map[string]string
}

// unlock releases e.mu.
func (e *Engine) unlock() { e.mu.Unlock() }

// ExecInstance is an exec instance created with CreateExec (the fake
// records it; attaching is not supported).
type ExecInstance struct {
	ID          string
	ContainerID string
	Spec        engine.ExecSpec
}

// Container is a fake container.
type Container struct {
	Details engine.ContainerDetails
	Env     []string
	// Command is the list entry's command string.
	Command string
}

// Image is a fake image.
type Image struct {
	engine.ImageDetails
}

var _ engine.Engine = (*Engine)(nil)

// New returns an Engine with the predefined networks (bridge, host, none).
func New(engineID string) *Engine {
	e := &Engine{
		identity: engine.Identity{EngineID: engineID, Name: "fake-" + engineID, Version: "29.8.1", APIVersion: "1.56",
			MinAPIVersion: "1.24", NegotiatedAPIVersion: "1.56", OS: "linux", Arch: "amd64", DockerRootDir: "/var/lib/docker"},
		now:         func() time.Time { return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) },
		containers:  map[string]*Container{},
		images:      map[string]*Image{},
		volumes:     map[string]*engine.Volume{},
		networks:    map[string]*engine.Network{},
		failures:    map[string][]error{},
		buildCache:  map[string]*engine.BuildCacheRecord{},
		volumeSizes: map[string]int64{},
		sizes:       map[string]int64{},
		remote:      map[string]string{},
		startHealth: map[string]string{},
	}
	for _, n := range []string{"bridge", "host", "none"} {
		id := e.newID("net-" + n)
		e.networks[id] = &engine.Network{ID: id, Name: n, Driver: map[string]string{"bridge": "bridge", "host": "host", "none": "null"}[n],
			Scope: "local", Created: e.now(), Labels: map[string]string{}}
	}
	return e
}

func (e *Engine) newID(seed string) string {
	e.seq++
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%d", e.identity.EngineID, seed, e.seq)))
	return hex.EncodeToString(sum[:])
}

// SetVolumeRoot places the mountpoints of volumes created from now on
// below root (default /var/lib/docker/volumes): tests that give the
// volumes real or in-memory directories (#35) use an absolute path of
// their own.
func (e *Engine) SetVolumeRoot(root string) {
	e.mu.Lock()
	defer e.unlock()
	e.volumeRoot = strings.TrimSuffix(root, "/")
}

// volumeDir is a volume's mountpoint. Callers hold e.mu.
func (e *Engine) volumeDir(name string) string {
	root := e.volumeRoot
	if root == "" {
		root = "/var/lib/docker/volumes"
	}
	return root + "/" + name + "/_data"
}

// Fail makes the next call of op (e.g. "container.start", "image.pull")
// return err; several calls queue several failures.
func (e *Engine) Fail(op string, err error) {
	e.mu.Lock()
	defer e.unlock()
	e.failures[op] = append(e.failures[op], err)
}

// Err builds an adapter error with a stable code.
func Err(op string, code engine.Code, format string, args ...any) error {
	return &engine.Error{Op: op, Code: code, Message: fmt.Sprintf(format, args...)}
}

// Calls returns the operations called so far, in order.
func (e *Engine) Calls() []string {
	e.mu.Lock()
	defer e.unlock()
	return slices.Clone(e.calls)
}

// call records op and returns an injected failure. Callers hold e.mu.
func (e *Engine) call(op string) error {
	e.calls = append(e.calls, op)
	if q := e.failures[op]; len(q) > 0 {
		e.failures[op] = q[1:]
		return q[0]
	}
	return nil
}

// AddImage adds an image with the given tags and returns its ID.
func (e *Engine) AddImage(tags ...string) string {
	e.mu.Lock()
	defer e.unlock()
	return e.addImage(tags, nil)
}

// AddImageDetails adds an image with explicit details (e.g. a locally
// built image without repository digests, or another architecture) and
// returns its ID (generated when d.ID is empty).
func (e *Engine) AddImageDetails(d engine.ImageDetails) string {
	e.mu.Lock()
	defer e.unlock()
	if d.ID == "" {
		d.ID = "sha256:" + e.newID("img-"+strings.Join(d.RepoTags, ","))
	}
	if d.OS == "" {
		d.OS = "linux"
	}
	if d.Created.IsZero() {
		d.Created = e.now()
	}
	e.images[d.ID] = &Image{d}
	return d.ID
}

// SetPlatform changes the Engine's reported OS and architecture.
func (e *Engine) SetPlatform(os, arch string) {
	e.mu.Lock()
	defer e.unlock()
	e.identity.OS, e.identity.Arch = os, arch
}

// AddLabeledImage adds an image with labels.
func (e *Engine) AddLabeledImage(labels map[string]string, tags ...string) string {
	e.mu.Lock()
	defer e.unlock()
	return e.addImage(tags, labels)
}

func (e *Engine) addImage(tags []string, labels map[string]string) string {
	id := "sha256:" + e.newID("img-"+strings.Join(tags, ","))
	var digests []string
	for _, t := range tags {
		repo, _, _ := strings.Cut(t, ":")
		sum := sha256.Sum256([]byte(id + t))
		digests = append(digests, repo+"@sha256:"+hex.EncodeToString(sum[:]))
	}
	e.images[id] = &Image{engine.ImageDetails{ID: id, RepoTags: slices.Clone(tags), RepoDigests: digests, Created: e.now(),
		Size: 1 << 20, OS: "linux", Architecture: "amd64", Labels: labels}}
	return id
}

// AddContainer adds a container from spec directly (as if created by
// another tool) and returns its ID. The image is added when missing.
func (e *Engine) AddContainer(spec engine.ContainerSpec, running bool) string {
	e.mu.Lock()
	defer e.unlock()
	if _, ok := e.findImage(spec.Image); !ok {
		e.addImage([]string{normalizeRef(spec.Image)}, nil)
	}
	id, err := e.create(spec)
	if err != nil {
		panic(err)
	}
	if running {
		c := e.containers[id]
		c.Details.State = engine.ContainerState{Status: "running", Running: true, Pid: 42, StartedAt: e.now()}
	}
	return id
}

// Container returns a copy of a container by ID or name.
func (e *Engine) Container(idOrName string) (Container, bool) {
	e.mu.Lock()
	defer e.unlock()
	c, ok := e.findContainer(idOrName)
	if !ok {
		return Container{}, false
	}
	return *c, true
}

// PullAuths returns the credential of every successful pull call so far
// (nil for anonymous pulls), in order.
func (e *Engine) PullAuths() []*engine.RegistryAuth {
	e.mu.Lock()
	defer e.unlock()
	return slices.Clone(e.pullAuths)
}

// Images returns every image's tags keyed by ID.
func (e *Engine) Images() map[string][]string {
	e.mu.Lock()
	defer e.unlock()
	out := map[string][]string{}
	for id, im := range e.images {
		out[id] = slices.Clone(im.RepoTags)
	}
	return out
}

// Identity implements engine.Engine.
func (e *Engine) Identity() engine.Identity {
	e.mu.Lock()
	defer e.unlock()
	return e.identity
}

// Refresh implements engine.Engine.
func (e *Engine) Refresh(context.Context) (engine.Identity, error) { return e.Identity(), nil }

// Ping implements engine.Engine.
func (e *Engine) Ping(context.Context) error {
	e.mu.Lock()
	defer e.unlock()
	return e.call("ping")
}

// Close implements engine.Engine.
func (e *Engine) Close() error { return nil }

func normalizeRef(ref string) string {
	if strings.Contains(ref, "@") {
		return ref
	}
	last := ref[strings.LastIndex(ref, "/")+1:]
	if !strings.Contains(last, ":") {
		return ref + ":latest"
	}
	return ref
}

func (e *Engine) findContainer(idOrName string) (*Container, bool) {
	if c, ok := e.containers[idOrName]; ok {
		return c, true
	}
	name := strings.TrimPrefix(idOrName, "/")
	var prefix []*Container
	for id, c := range e.containers {
		if c.Details.Name == name {
			return c, true
		}
		if len(idOrName) >= 4 && strings.HasPrefix(id, idOrName) {
			prefix = append(prefix, c)
		}
	}
	if len(prefix) == 1 {
		return prefix[0], true
	}
	return nil, false
}

func (e *Engine) findImage(ref string) (*Image, bool) {
	if im, ok := e.images[ref]; ok {
		return im, true
	}
	if !strings.HasPrefix(ref, "sha256:") {
		if im, ok := e.images["sha256:"+ref]; ok {
			return im, true
		}
	}
	short := strings.TrimPrefix(ref, "sha256:")
	var prefix []*Image
	for id, im := range e.images {
		if slices.Contains(im.RepoTags, normalizeRef(ref)) || slices.Contains(im.RepoDigests, ref) {
			return im, true
		}
		if len(short) >= 12 && strings.HasPrefix(strings.TrimPrefix(id, "sha256:"), short) {
			prefix = append(prefix, im)
		}
	}
	if len(prefix) == 1 {
		return prefix[0], true
	}
	return nil, false
}

func (e *Engine) findNetwork(idOrName string) (*engine.Network, bool) {
	if n, ok := e.networks[idOrName]; ok {
		return n, true
	}
	for id, n := range e.networks {
		if n.Name == idOrName || (len(idOrName) >= 12 && strings.HasPrefix(id, idOrName)) {
			return n, true
		}
	}
	return nil, false
}

func notFound(op, what, name string) error {
	return Err(op, engine.CodeNotFound, "No such %s: %s", what, name)
}

func conflict(op, format string, args ...any) error {
	return Err(op, engine.CodeConflict, format, args...)
}

func (e *Engine) summary(c *Container) engine.Container {
	d := c.Details
	out := engine.Container{ID: d.ID, Names: []string{d.Name}, Image: d.Image, ImageID: d.ImageID, Command: c.Command,
		Created: d.Created, State: d.State.Status, Status: d.State.Status, Labels: maps.Clone(d.Labels), Ports: slices.Clone(d.Ports),
		Mounts: slices.Clone(d.Mounts), SizeRw: e.sizes[d.ID]}
	for n, ep := range d.Networks {
		out.Networks = append(out.Networks, n)
		// Like the Engine's list: the endpoint of every network, with
		// addresses only while the container runs.
		if !d.State.Running {
			ep.IPAddress, ep.IPv6Address = "", ""
		}
		ep.Aliases = slices.Clone(ep.Aliases)
		if out.Endpoints == nil {
			out.Endpoints = map[string]engine.EndpointInfo{}
		}
		out.Endpoints[n] = ep
	}
	sort.Strings(out.Networks)
	if d.State.Health != nil {
		out.Health = d.State.Health.Status
	}
	return out
}

// ListContainers implements engine.Engine.
func (e *Engine) ListContainers(_ context.Context, f engine.ContainerFilter) ([]engine.Container, error) {
	e.mu.Lock()
	defer e.unlock()
	if err := e.call("container.list"); err != nil {
		return nil, err
	}
	var out []engine.Container
	for _, c := range e.containers {
		if !f.All && !c.Details.State.Running {
			continue
		}
		if !matchLabels(c.Details.Labels, f.Labels) {
			continue
		}
		if len(f.Names) > 0 && !slices.ContainsFunc(f.Names, func(n string) bool { return strings.Contains(c.Details.Name, n) }) {
			continue
		}
		if len(f.IDs) > 0 && !slices.ContainsFunc(f.IDs, func(p string) bool { return strings.HasPrefix(c.Details.ID, p) }) {
			continue
		}
		out = append(out, e.summary(c))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Names[0] < out[j].Names[0] })
	return out, nil
}

func matchLabels(have map[string]string, want []string) bool {
	for _, w := range want {
		k, v, hasV := strings.Cut(w, "=")
		got, ok := have[k]
		if !ok || (hasV && got != v) {
			return false
		}
	}
	return true
}

// InspectContainer implements engine.Engine.
func (e *Engine) InspectContainer(_ context.Context, id string) (engine.ContainerDetails, error) {
	e.mu.Lock()
	defer e.unlock()
	if err := e.call("container.inspect"); err != nil {
		return engine.ContainerDetails{}, err
	}
	c, ok := e.findContainer(id)
	if !ok {
		return engine.ContainerDetails{}, notFound("container.inspect", "container", id)
	}
	d := c.Details
	d.Labels = maps.Clone(d.Labels)
	d.Networks = maps.Clone(d.Networks)
	return d, nil
}

// CreateContainer implements engine.Engine.
func (e *Engine) CreateContainer(_ context.Context, spec engine.ContainerSpec) (string, []string, error) {
	e.mu.Lock()
	defer e.unlock()
	if err := e.call("container.create"); err != nil {
		return "", nil, err
	}
	if _, ok := e.findImage(spec.Image); !ok {
		return "", nil, notFound("container.create", "image", spec.Image)
	}
	id, err := e.create(spec)
	if err == nil {
		e.containerEvent(e.containers[id], "create")
	}
	return id, nil, err
}

func (e *Engine) create(spec engine.ContainerSpec) (string, error) {
	const op = "container.create"
	name := spec.Name
	if name == "" {
		name = fmt.Sprintf("fake_%d", e.seq+1)
	}
	for _, c := range e.containers {
		if c.Details.Name == name {
			return "", conflict(op, `Conflict. The container name "/%s" is already in use by container "%s"`, name, c.Details.ID)
		}
	}
	im, _ := e.findImage(spec.Image)
	netMode := spec.NetworkMode
	if netMode == "" {
		netMode = "bridge"
	}
	nets := map[string]engine.EndpointInfo{}
	if netMode != "host" && netMode != "none" {
		n, ok := e.findNetwork(netMode)
		if !ok {
			return "", notFound(op, "network", netMode)
		}
		nets[n.Name] = engine.EndpointInfo{NetworkID: n.ID, IPAddress: "172.17.0.2", Aliases: slices.Clone(spec.NetworkAliases)}
	}
	var mounts []engine.Mount
	for _, m := range spec.Mounts {
		switch m.Type {
		case "volume":
			vname := m.Source
			if vname == "" {
				vname = e.newID("anon")
			}
			if _, ok := e.volumes[vname]; !ok {
				e.volumes[vname] = &engine.Volume{Name: vname, Driver: "local", Mountpoint: e.volumeDir(vname),
					Scope: "local", CreatedAt: e.now(), Labels: map[string]string{}}
			}
			mounts = append(mounts, engine.Mount{Type: "volume", Name: vname, Source: e.volumeDir(vname),
				Destination: m.Target, Driver: "local", ReadWrite: !m.ReadOnly})
		case "bind", "tmpfs":
			mounts = append(mounts, engine.Mount{Type: m.Type, Source: m.Source, Destination: m.Target, ReadWrite: !m.ReadOnly})
		default:
			return "", Err(op, engine.CodeInvalidArgument, "unsupported mount type %q", m.Type)
		}
	}
	var ports []engine.Port
	for _, p := range spec.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		ports = append(ports, engine.Port{PrivatePort: p.ContainerPort, PublicPort: p.HostPort, HostIP: p.HostIP, Protocol: proto})
	}
	id := e.newID("ctr-" + name)
	d := engine.ContainerDetails{ID: id, Name: name, Image: spec.Image, Created: e.now(), Platform: "linux",
		State: engine.ContainerState{Status: "created"}, Labels: maps.Clone(spec.Labels), Cmd: slices.Clone(spec.Cmd),
		Entrypoint: slices.Clone(spec.Entrypoint), WorkingDir: spec.WorkingDir, User: spec.User, Hostname: id[:12],
		RestartPolicy: spec.RestartPolicy, NetworkMode: netMode, Mounts: mounts, Networks: nets, Ports: ports,
		Resources: spec.Resources, Healthcheck: spec.Healthcheck}
	if d.RestartPolicy == "" {
		d.RestartPolicy = "no"
	}
	if d.Labels == nil {
		d.Labels = map[string]string{}
	}
	if im != nil {
		d.ImageID = im.ID
		for k, v := range im.Labels {
			if _, ok := d.Labels[k]; !ok {
				d.Labels[k] = v
			}
		}
	}
	if spec.Healthcheck != nil && len(spec.Healthcheck.Test) > 0 && spec.Healthcheck.Test[0] != "NONE" {
		d.State.Health = &engine.Health{Status: "starting"}
	}
	e.containers[id] = &Container{Details: d, Env: slices.Clone(spec.Env), Command: strings.Join(append(slices.Clone(spec.Entrypoint), spec.Cmd...), " ")}
	return id, nil
}

func (e *Engine) mutate(op, id string, fn func(c *Container) error) error {
	e.mu.Lock()
	defer e.unlock()
	if err := e.call(op); err != nil {
		return err
	}
	c, ok := e.findContainer(id)
	if !ok {
		return notFound(op, "container", id)
	}
	return fn(c)
}

// StartContainer implements engine.Engine.
func (e *Engine) StartContainer(_ context.Context, id string) error {
	return e.mutate("container.start", id, func(c *Container) error {
		if c.Details.State.Paused {
			return conflict("container.start", "cannot start a paused container, try unpause instead")
		}
		c.Details.State = engine.ContainerState{Status: "running", Running: true, Pid: 42, StartedAt: e.now(), Health: c.Details.State.Health}
		if h, ok := e.startHealth[c.Details.ImageID]; ok {
			c.Details.State.Health = &engine.Health{Status: h}
		}
		e.containerEvent(c, "start")
		return nil
	})
}

// StopContainer implements engine.Engine.
func (e *Engine) StopContainer(_ context.Context, id string, _ *time.Duration) error {
	return e.mutate("container.stop", id, func(c *Container) error {
		c.Details.State = engine.ContainerState{Status: "exited", StartedAt: c.Details.State.StartedAt, FinishedAt: e.now()}
		e.containerEvent(c, "die")
		e.containerEvent(c, "stop")
		return nil
	})
}

// RestartContainer implements engine.Engine.
func (e *Engine) RestartContainer(_ context.Context, id string, _ *time.Duration) error {
	return e.mutate("container.restart", id, func(c *Container) error {
		c.Details.RestartCount++
		c.Details.State = engine.ContainerState{Status: "running", Running: true, Pid: 43, StartedAt: e.now()}
		e.containerEvent(c, "restart")
		return nil
	})
}

// PauseContainer implements engine.Engine.
func (e *Engine) PauseContainer(_ context.Context, id string) error {
	return e.mutate("container.pause", id, func(c *Container) error {
		if !c.Details.State.Running {
			return conflict("container.pause", "container %s is not running", c.Details.ID)
		}
		c.Details.State.Paused, c.Details.State.Status = true, "paused"
		e.containerEvent(c, "pause")
		return nil
	})
}

// UnpauseContainer implements engine.Engine.
func (e *Engine) UnpauseContainer(_ context.Context, id string) error {
	return e.mutate("container.unpause", id, func(c *Container) error {
		if !c.Details.State.Paused {
			return conflict("container.unpause", "container %s is not paused", c.Details.ID)
		}
		c.Details.State.Paused, c.Details.State.Status = false, "running"
		e.containerEvent(c, "unpause")
		return nil
	})
}

// RenameContainer implements engine.Engine.
func (e *Engine) RenameContainer(_ context.Context, id, name string) error {
	return e.mutate("container.rename", id, func(c *Container) error {
		name = strings.TrimPrefix(name, "/")
		for _, other := range e.containers {
			if other.Details.Name == name && other.Details.ID != c.Details.ID {
				return conflict("container.rename", `Conflict. The container name "/%s" is already in use by container "%s"`, name, other.Details.ID)
			}
		}
		c.Details.Name = name
		e.containerEvent(c, "rename")
		return nil
	})
}

// KillContainer implements engine.Engine.
func (e *Engine) KillContainer(_ context.Context, id, _ string) error {
	return e.mutate("container.kill", id, func(c *Container) error {
		c.Details.State = engine.ContainerState{Status: "exited", ExitCode: 137, FinishedAt: e.now()}
		e.containerEvent(c, "kill")
		e.containerEvent(c, "die")
		return nil
	})
}

// RemoveContainer implements engine.Engine.
func (e *Engine) RemoveContainer(_ context.Context, id string, o engine.RemoveOptions) error {
	return e.mutate("container.remove", id, func(c *Container) error {
		if c.Details.State.Running && !o.Force {
			return conflict("container.remove", "cannot remove container %q: container is running: stop the container before removing or force remove", c.Details.Name)
		}
		delete(e.containers, c.Details.ID)
		e.containerEvent(c, "destroy")
		if o.Volumes {
			for _, m := range c.Details.Mounts {
				if m.Type == "volume" && len(m.Name) == 64 && !e.volumeUsed(m.Name) {
					delete(e.volumes, m.Name)
				}
			}
		}
		return nil
	})
}

// UpdateContainer implements engine.Engine.
func (e *Engine) UpdateContainer(_ context.Context, id string, u engine.ContainerUpdate) ([]string, error) {
	return nil, e.mutate("container.update", id, func(c *Container) error {
		if u.Resources != nil {
			c.Details.Resources = *u.Resources
		}
		if u.RestartPolicy != "" {
			c.Details.RestartPolicy = u.RestartPolicy
		}
		e.containerEvent(c, "update")
		return nil
	})
}

// WaitContainer implements engine.Engine.
func (e *Engine) WaitContainer(context.Context, string) (int64, error) {
	return 0, Err("container.wait", engine.CodeUnsupported, "not supported by the fake")
}

// ListImages implements engine.Engine.
func (e *Engine) ListImages(_ context.Context, _ bool) ([]engine.Image, error) {
	e.mu.Lock()
	defer e.unlock()
	if err := e.call("image.list"); err != nil {
		return nil, err
	}
	var out []engine.Image
	for _, im := range e.images {
		n := int64(0)
		for _, c := range e.containers {
			if c.Details.ImageID == im.ID {
				n++
			}
		}
		out = append(out, engine.Image{ID: im.ID, RepoTags: slices.Clone(im.RepoTags), RepoDigests: slices.Clone(im.RepoDigests),
			Created: im.Created, Size: im.Size, Containers: n, Labels: maps.Clone(im.Labels)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// InspectImage implements engine.Engine.
func (e *Engine) InspectImage(_ context.Context, ref string) (engine.ImageDetails, error) {
	e.mu.Lock()
	defer e.unlock()
	if err := e.call("image.inspect"); err != nil {
		return engine.ImageDetails{}, err
	}
	im, ok := e.findImage(ref)
	if !ok {
		return engine.ImageDetails{}, notFound("image.inspect", "image", ref)
	}
	d := im.ImageDetails
	d.RepoTags, d.RepoDigests, d.Labels = slices.Clone(d.RepoTags), slices.Clone(d.RepoDigests), maps.Clone(d.Labels)
	return d, nil
}

// PullImage implements engine.Engine.
func (e *Engine) PullImage(_ context.Context, ref string, o engine.PullOptions) (engine.PullResult, error) {
	e.mu.Lock()
	if err := e.call("image.pull"); err != nil {
		e.unlock()
		return engine.PullResult{}, err
	}
	if o.Auth != nil {
		a := *o.Auth
		e.pullAuths = append(e.pullAuths, &a)
	} else {
		e.pullAuths = append(e.pullAuths, nil)
	}
	tag := normalizeRef(ref)
	im, ok := e.findImage(tag)
	var id string
	switch {
	case e.remote[tag] != "":
		id = e.pullRemote(tag, e.remote[tag])
	case ok:
		id = im.ID
	default:
		id = e.addImage([]string{tag}, nil)
	}
	digest := ""
	if d := e.images[id].RepoDigests; len(d) > 0 {
		_, digest, _ = strings.Cut(d[0], "@")
	}
	e.emit("image", "pull", tag, map[string]string{"name": tag})
	e.unlock()
	if o.Progress != nil {
		o.Progress(engine.Progress{ID: "layer1", Status: "Downloading", Current: 512, Total: 1024})
		o.Progress(engine.Progress{ID: "layer1", Status: "Download complete", Current: 1024, Total: 1024})
		o.Progress(engine.Progress{Status: "Digest: " + digest})
	}
	return engine.PullResult{Digest: digest, ImageID: id}, nil
}

// Publish makes the registry serve digest for ref: the next pull of ref
// moves the tag to an image with that repository digest (a new image when
// none has it yet), like a tag moving to a new build (#20). The previous
// image keeps its ID and digest.
func (e *Engine) Publish(ref, digest string) {
	e.mu.Lock()
	defer e.unlock()
	e.remote[normalizeRef(ref)] = digest
}

// SetStartHealth makes containers of image (ID or reference) report the
// health status when they start ("healthy", "unhealthy", "starting").
func (e *Engine) SetStartHealth(image, status string) {
	e.mu.Lock()
	defer e.unlock()
	if im, ok := e.findImage(image); ok {
		e.startHealth[im.ID] = status
	}
}

// ImageDigests returns the repository digests of an image (ID or reference).
func (e *Engine) ImageDigests(image string) []string {
	e.mu.Lock()
	defer e.unlock()
	if im, ok := e.findImage(image); ok {
		return slices.Clone(im.RepoDigests)
	}
	return nil
}

// repoOf strips the tag of a normalized reference ("host:5000/app:1" ->
// "host:5000/app").
func repoOf(tag string) string {
	if i := strings.LastIndex(tag, ":"); i > strings.LastIndex(tag, "/") {
		return tag[:i]
	}
	return tag
}

// pullRemote moves tag to the image with the published digest. Callers
// hold e.mu.
func (e *Engine) pullRemote(tag, digest string) string {
	rd := repoOf(tag) + "@" + digest
	var target *Image
	for _, im := range e.images {
		if slices.Contains(im.RepoDigests, rd) {
			target = im
		}
	}
	if target == nil {
		id := "sha256:" + e.newID("img-"+rd)
		target = &Image{engine.ImageDetails{ID: id, RepoDigests: []string{rd}, Created: e.now(), Size: 1 << 20, OS: "linux", Architecture: "amd64"}}
		e.images[id] = target
	}
	for _, other := range e.images {
		if i := slices.Index(other.RepoTags, tag); i >= 0 && other != target {
			other.RepoTags = slices.Delete(other.RepoTags, i, i+1)
		}
	}
	if !slices.Contains(target.RepoTags, tag) {
		target.RepoTags = append(target.RepoTags, tag)
	}
	return target.ID
}

// TagImage implements engine.Engine.
func (e *Engine) TagImage(_ context.Context, source, target string) error {
	e.mu.Lock()
	defer e.unlock()
	if err := e.call("image.tag"); err != nil {
		return err
	}
	im, ok := e.findImage(source)
	if !ok {
		return notFound("image.tag", "image", source)
	}
	target = normalizeRef(target)
	for _, other := range e.images {
		if i := slices.Index(other.RepoTags, target); i >= 0 {
			other.RepoTags = slices.Delete(other.RepoTags, i, i+1)
		}
	}
	im.RepoTags = append(im.RepoTags, target)
	e.emit("image", "tag", im.ID, map[string]string{"name": target})
	return nil
}

// RemoveImage implements engine.Engine.
func (e *Engine) RemoveImage(_ context.Context, ref string, force, _ bool) ([]engine.DeletedImage, error) {
	const op = "image.remove"
	e.mu.Lock()
	defer e.unlock()
	if err := e.call(op); err != nil {
		return nil, err
	}
	im, ok := e.findImage(ref)
	if !ok {
		return nil, notFound(op, "image", ref)
	}
	for _, c := range e.containers {
		if c.Details.ImageID == im.ID {
			return nil, conflict(op, "conflict: unable to delete %s - image is being used by container %s", shortID(im.ID), shortID(c.Details.ID))
		}
	}
	byID := strings.HasPrefix(strings.TrimPrefix(ref, "sha256:"), shortID(im.ID))
	if byID && len(im.RepoTags) > 1 && !force {
		return nil, conflict(op, "conflict: unable to delete %s (must be forced) - image is referenced in multiple repositories", shortID(im.ID))
	}
	var out []engine.DeletedImage
	if !byID && len(im.RepoTags) > 1 {
		i := slices.Index(im.RepoTags, normalizeRef(ref))
		out = append(out, engine.DeletedImage{Untagged: im.RepoTags[i]})
		e.emit("image", "untag", im.ID, map[string]string{"name": im.RepoTags[i]})
		im.RepoTags = slices.Delete(im.RepoTags, i, i+1)
		return out, nil
	}
	for _, t := range im.RepoTags {
		out = append(out, engine.DeletedImage{Untagged: t})
		e.emit("image", "untag", im.ID, map[string]string{"name": t})
	}
	delete(e.images, im.ID)
	e.emit("image", "delete", im.ID, nil)
	return append(out, engine.DeletedImage{Deleted: im.ID}), nil
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// SaveImage implements engine.Engine: the archive is a small JSON
// manifest of the images (enough for a LoadImage round trip through the
// fake).
func (e *Engine) SaveImage(_ context.Context, refs []string) (io.ReadCloser, error) {
	const op = "image.save"
	e.mu.Lock()
	defer e.unlock()
	if err := e.call(op); err != nil {
		return nil, err
	}
	var out []engine.ImageDetails
	for _, r := range refs {
		im, ok := e.findImage(r)
		if !ok {
			return nil, notFound(op, "image", r)
		}
		out = append(out, im.ImageDetails)
	}
	b, err := json.Marshal(savedImages{Magic: savedMagic, Images: out})
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

const savedMagic = "enginefake-image-archive"

type savedImages struct {
	Magic  string                `json:"magic"`
	Images []engine.ImageDetails `json:"images"`
}

// LoadImage implements engine.Engine: it loads archives written by the
// fake's SaveImage (other archives are refused).
func (e *Engine) LoadImage(_ context.Context, r io.Reader) error {
	const op = "image.load"
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.unlock()
	if err := e.call(op); err != nil {
		return err
	}
	var s savedImages
	if json.Unmarshal(b, &s) != nil || s.Magic != savedMagic {
		return Err(op, engine.CodeInvalidArgument, "not an image archive")
	}
	for _, im := range s.Images {
		c := im
		e.images[c.ID] = &Image{ImageDetails: c}
	}
	return nil
}

// Build implements engine.Engine.
func (e *Engine) Build(context.Context, engine.BuildSpec) (engine.BuildResult, error) {
	return engine.BuildResult{}, Err("image.build", engine.CodeUnsupported, "not supported by the fake")
}

func (e *Engine) volumeUsed(name string) bool {
	for _, c := range e.containers {
		for _, m := range c.Details.Mounts {
			if m.Type == "volume" && m.Name == name {
				return true
			}
		}
	}
	return false
}

// ListVolumes implements engine.Engine.
func (e *Engine) ListVolumes(_ context.Context, labels ...string) ([]engine.Volume, error) {
	e.mu.Lock()
	defer e.unlock()
	if err := e.call("volume.list"); err != nil {
		return nil, err
	}
	var out []engine.Volume
	for _, v := range e.volumes {
		if matchLabels(v.Labels, labels) {
			c := *v
			c.Labels, c.Options = maps.Clone(v.Labels), maps.Clone(v.Options)
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// InspectVolume implements engine.Engine.
func (e *Engine) InspectVolume(_ context.Context, name string) (engine.Volume, error) {
	e.mu.Lock()
	defer e.unlock()
	if err := e.call("volume.inspect"); err != nil {
		return engine.Volume{}, err
	}
	v, ok := e.volumes[name]
	if !ok {
		return engine.Volume{}, notFound("volume.inspect", "volume", name)
	}
	c := *v
	c.Labels, c.Options = maps.Clone(v.Labels), maps.Clone(v.Options)
	return c, nil
}

// CreateVolume implements engine.Engine (idempotent like the Engine: an
// existing volume with the same name is returned).
func (e *Engine) CreateVolume(_ context.Context, spec engine.VolumeSpec) (engine.Volume, error) {
	e.mu.Lock()
	defer e.unlock()
	if err := e.call("volume.create"); err != nil {
		return engine.Volume{}, err
	}
	if v, ok := e.volumes[spec.Name]; ok {
		return *v, nil
	}
	driver := spec.Driver
	if driver == "" {
		driver = "local"
	}
	v := &engine.Volume{Name: spec.Name, Driver: driver, Mountpoint: e.volumeDir(spec.Name), Scope: "local",
		CreatedAt: e.now(), Labels: maps.Clone(spec.Labels), Options: maps.Clone(spec.DriverOpts)}
	if v.Labels == nil {
		v.Labels = map[string]string{}
	}
	e.volumes[spec.Name] = v
	e.emit("volume", "create", spec.Name, map[string]string{"driver": driver})
	return *v, nil
}

// SetVolumeMountpoint moves a volume's data directory (tests point it at
// a temporary directory) and updates the containers mounting it.
func (e *Engine) SetVolumeMountpoint(name, mountpoint string) {
	e.mu.Lock()
	defer e.unlock()
	if v, ok := e.volumes[name]; ok {
		v.Mountpoint = mountpoint
	}
	for _, c := range e.containers {
		for i := range c.Details.Mounts {
			if c.Details.Mounts[i].Type == "volume" && c.Details.Mounts[i].Name == name {
				c.Details.Mounts[i].Source = mountpoint
			}
		}
	}
}

// AddVolume adds a volume with labels (seeding).
func (e *Engine) AddVolume(name string, labels map[string]string) {
	_, _ = e.CreateVolume(context.Background(), engine.VolumeSpec{Name: name, Labels: labels})
}

// RemoveVolume implements engine.Engine.
func (e *Engine) RemoveVolume(_ context.Context, name string, _ bool) error {
	const op = "volume.remove"
	e.mu.Lock()
	defer e.unlock()
	if err := e.call(op); err != nil {
		return err
	}
	if _, ok := e.volumes[name]; !ok {
		return notFound(op, "volume", name)
	}
	if e.volumeUsed(name) {
		return conflict(op, "remove %s: volume is in use", name)
	}
	delete(e.volumes, name)
	e.emit("volume", "destroy", name, nil)
	return nil
}

func (e *Engine) networkCopy(n *engine.Network, withContainers bool) engine.Network {
	c := *n
	c.Labels = maps.Clone(n.Labels)
	c.Containers = nil
	if withContainers {
		c.Containers = map[string]engine.EndpointInfo{}
		for id, ct := range e.containers {
			if ep, ok := ct.Details.Networks[n.Name]; ok {
				c.Containers[id] = ep
			}
		}
	}
	return c
}

// ListNetworks implements engine.Engine.
func (e *Engine) ListNetworks(_ context.Context, labels ...string) ([]engine.Network, error) {
	e.mu.Lock()
	defer e.unlock()
	if err := e.call("network.list"); err != nil {
		return nil, err
	}
	var out []engine.Network
	for _, n := range e.networks {
		if matchLabels(n.Labels, labels) {
			out = append(out, e.networkCopy(n, false))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// InspectNetwork implements engine.Engine.
func (e *Engine) InspectNetwork(_ context.Context, idOrName string) (engine.Network, error) {
	e.mu.Lock()
	defer e.unlock()
	if err := e.call("network.inspect"); err != nil {
		return engine.Network{}, err
	}
	n, ok := e.findNetwork(idOrName)
	if !ok {
		return engine.Network{}, notFound("network.inspect", "network", idOrName)
	}
	return e.networkCopy(n, true), nil
}

// CreateNetwork implements engine.Engine.
func (e *Engine) CreateNetwork(_ context.Context, spec engine.NetworkSpec) (string, error) {
	const op = "network.create"
	e.mu.Lock()
	defer e.unlock()
	if err := e.call(op); err != nil {
		return "", err
	}
	if _, ok := e.findNetwork(spec.Name); ok {
		return "", conflict(op, "network with name %s already exists", spec.Name)
	}
	driver := spec.Driver
	if driver == "" {
		driver = "bridge"
	}
	id := e.newID("net-" + spec.Name)
	labels := maps.Clone(spec.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	e.networks[id] = &engine.Network{ID: id, Name: spec.Name, Driver: driver, Scope: "local", Internal: spec.Internal,
		Attachable: spec.Attachable, Created: e.now(), Labels: labels, Subnets: []string{"172.20.0.0/16"}, Gateways: []string{"172.20.0.1"}}
	e.emit("network", "create", id, map[string]string{"name": spec.Name, "type": driver})
	return id, nil
}

// AddNetwork adds a network with labels (seeding) and returns its ID.
func (e *Engine) AddNetwork(name string, labels map[string]string) string {
	id, err := e.CreateNetwork(context.Background(), engine.NetworkSpec{Name: name, Labels: labels})
	if err != nil {
		panic(err)
	}
	return id
}

// RemoveNetwork implements engine.Engine.
func (e *Engine) RemoveNetwork(_ context.Context, idOrName string) error {
	const op = "network.remove"
	e.mu.Lock()
	defer e.unlock()
	if err := e.call(op); err != nil {
		return err
	}
	n, ok := e.findNetwork(idOrName)
	if !ok {
		return notFound(op, "network", idOrName)
	}
	if slices.Contains([]string{"bridge", "host", "none"}, n.Name) {
		return Err(op, engine.CodeForbidden, "%s is a pre-defined network and cannot be removed", n.Name)
	}
	for _, c := range e.containers {
		if _, ok := c.Details.Networks[n.Name]; ok {
			return conflict(op, "error while removing network: network %s id %s has active endpoints", n.Name, n.ID)
		}
	}
	delete(e.networks, n.ID)
	e.emit("network", "destroy", n.ID, map[string]string{"name": n.Name})
	return nil
}

// ConnectNetwork implements engine.Engine.
func (e *Engine) ConnectNetwork(_ context.Context, netID, containerID string, aliases ...string) error {
	const op = "network.connect"
	e.mu.Lock()
	defer e.unlock()
	if err := e.call(op); err != nil {
		return err
	}
	n, ok := e.findNetwork(netID)
	if !ok {
		return notFound(op, "network", netID)
	}
	c, ok := e.findContainer(containerID)
	if !ok {
		return notFound(op, "container", containerID)
	}
	if _, ok := c.Details.Networks[n.Name]; ok {
		return nil
	}
	if c.Details.Networks == nil {
		c.Details.Networks = map[string]engine.EndpointInfo{}
	}
	c.Details.Networks[n.Name] = engine.EndpointInfo{NetworkID: n.ID, IPAddress: "172.20.0.2", Aliases: slices.Clone(aliases)}
	e.emit("network", "connect", n.ID, map[string]string{"name": n.Name, "container": c.Details.ID})
	return nil
}

// DisconnectNetwork implements engine.Engine.
func (e *Engine) DisconnectNetwork(_ context.Context, netID, containerID string, _ bool) error {
	const op = "network.disconnect"
	e.mu.Lock()
	defer e.unlock()
	if err := e.call(op); err != nil {
		return err
	}
	n, ok := e.findNetwork(netID)
	if !ok {
		return notFound(op, "network", netID)
	}
	c, ok := e.findContainer(containerID)
	if !ok {
		return notFound(op, "container", containerID)
	}
	delete(c.Details.Networks, n.Name)
	return nil
}

// emit appends an Engine event (callers hold e.mu). Event times advance
// by one second per event from the fake's clock, so a relay's coalescing
// window never merges two distinct operations.
func (e *Engine) emit(typ, action, actorID string, attrs map[string]string) {
	at := e.now().Add(time.Duration(len(e.events)+1) * time.Second)
	e.events = append(e.events, engine.Event{Type: typ, Action: action, ActorID: actorID, Attributes: attrs, Scope: "local", Time: at})
	if e.notify != nil {
		close(e.notify)
	}
	e.notify = make(chan struct{})
}

func (e *Engine) containerEvent(c *Container, action string) {
	e.emit("container", action, c.Details.ID, map[string]string{"name": c.Details.Name, "image": c.Details.Image})
}

// EmittedEvents returns the Engine events of the operations so far.
func (e *Engine) EmittedEvents() []engine.Event {
	e.mu.Lock()
	defer e.unlock()
	return slices.Clone(e.events)
}

// Events implements engine.Engine: it delivers the events of f.Types
// (all when empty) emitted after f.Since, or from now on when Since is
// zero, until ctx ends.
func (e *Engine) Events(ctx context.Context, f engine.EventFilter, fn func(engine.Event) error) error {
	e.mu.Lock()
	next := len(e.events)
	if !f.Since.IsZero() {
		next = 0
		for next < len(e.events) && !e.events[next].Time.After(f.Since) {
			next++
		}
	}
	e.mu.Unlock()
	for {
		e.mu.Lock()
		if e.notify == nil {
			e.notify = make(chan struct{})
		}
		pending, wait := slices.Clone(e.events[next:]), e.notify
		next = len(e.events)
		e.mu.Unlock()
		for _, ev := range pending {
			if len(f.Types) > 0 && !slices.Contains(f.Types, ev.Type) {
				continue
			}
			if err := fn(ev); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return Err("events", engine.CodeCanceled, "context canceled")
		case <-wait:
		}
	}
}

// Logs implements engine.Engine.
func (e *Engine) Logs(context.Context, string, engine.LogOptions, func(engine.LogEntry) error) error {
	return Err("container.logs", engine.CodeUnsupported, "not supported by the fake")
}

// Stats implements engine.Engine.
func (e *Engine) Stats(context.Context, string, bool, func(engine.Stats) error) error {
	return Err("container.stats", engine.CodeUnsupported, "not supported by the fake")
}

// CreateExec implements engine.Engine: it records the instance on a
// running container (see Execs); attaching is not supported.
func (e *Engine) CreateExec(_ context.Context, id string, spec engine.ExecSpec) (string, error) {
	e.mu.Lock()
	defer e.unlock()
	c, ok := e.findContainer(id)
	if !ok {
		return "", Err("exec.create", engine.CodeNotFound, "no such container: %s", id)
	}
	if !c.Details.State.Running {
		return "", Err("exec.create", engine.CodeConflict, "container %s is not running", id)
	}
	x := ExecInstance{ID: e.newID("exec"), ContainerID: c.Details.ID, Spec: spec}
	x.Spec.Cmd = append([]string(nil), spec.Cmd...)
	e.execs = append(e.execs, x)
	return x.ID, nil
}

// Execs returns the exec instances created so far.
func (e *Engine) Execs() []ExecInstance {
	e.mu.Lock()
	defer e.unlock()
	return append([]ExecInstance(nil), e.execs...)
}

// SetPaths declares paths that exist in a container's filesystem
// (PathExists); by default none do.
func (e *Engine) SetPaths(idOrName string, paths ...string) {
	e.mu.Lock()
	defer e.unlock()
	id := idOrName
	if c, ok := e.findContainer(idOrName); ok {
		id = c.Details.ID
	}
	if e.paths == nil {
		e.paths = map[string]map[string]bool{}
	}
	if e.paths[id] == nil {
		e.paths[id] = map[string]bool{}
	}
	for _, p := range paths {
		e.paths[id][p] = true
	}
}

// PathExists implements engine.Engine: the paths declared with SetPaths.
func (e *Engine) PathExists(_ context.Context, id, path string) (bool, error) {
	e.mu.Lock()
	defer e.unlock()
	c, ok := e.findContainer(id)
	if !ok {
		return false, Err("container.stat_path", engine.CodeNotFound, "no such container: %s", id)
	}
	return e.paths[c.Details.ID][path], nil
}

// AttachExec implements engine.Engine.
func (e *Engine) AttachExec(context.Context, string, engine.ExecIO) error {
	return Err("exec.attach", engine.CodeUnsupported, "not supported by the fake")
}

// ResizeExec implements engine.Engine.
func (e *Engine) ResizeExec(context.Context, string, uint, uint) error {
	return Err("exec.resize", engine.CodeUnsupported, "not supported by the fake")
}

// InspectExec implements engine.Engine.
func (e *Engine) InspectExec(context.Context, string) (engine.ExecStatus, error) {
	return engine.ExecStatus{}, Err("exec.inspect", engine.CodeUnsupported, "not supported by the fake")
}
