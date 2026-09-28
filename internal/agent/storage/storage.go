// Package storage verifies the agent's host storage layout at startup (#28).
//
// Docker Engine resolves bind-mount sources, env_file paths and build
// contexts of a Compose project on the host. The agent runs in a container,
// so it only sees the same files at the same paths when Docker's volume
// directory (and every extra stack root) is mounted into the agent at its
// identical host path. Verify checks that instead of assuming it:
//
//  1. the Engine is not rootless and not Docker Desktop;
//  2. the agent finds its own container (mountinfo, cgroup, hostname);
//  3. the stacks volume exists, uses the local driver and its Mountpoint is
//     covered by an agent mount whose host source is the same path, visible
//     and writable inside the agent;
//  4. <DockerRootDir>/volumes and every DOCKER_AGENT_STACK_ROOTS entry pass the
//     same identical-path check.
//
// A failed check produces a Diagnostic with a stable code; stack operations
// are refused (Result.Allows) while the rest of the agent keeps working.
//
// Import mounts are optional: host directories holding existing Compose
// projects, mounted into the agent at or below ImportDir (read-only is
// enough). They are only read, to import a project by copying its
// directory into the stacks volume (Result.ImportSource); nothing is ever
// deployed from them.
package storage

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
)

// Diagnostic codes (stable; shown by the manager and UI).
const (
	CodeRootlessEngine      = "storage_rootless_engine"
	CodeDockerDesktop       = "storage_docker_desktop"
	CodeEngineUnavailable   = "storage_engine_unavailable"
	CodeSelfUnknown         = "storage_self_unknown"
	CodeStacksVolumeMissing = "storage_stacks_volume_missing"
	CodeStacksVolumeRemote  = "storage_stacks_volume_not_local"
	CodeMountMissing        = "storage_mount_missing"
	CodePathMismatch        = "storage_path_mismatch"
	CodeReadOnly            = "storage_read_only"
	CodeNotVisible          = "storage_path_not_visible"
	CodeNotWritable         = "storage_not_writable"
	CodeRootMismatch        = "storage_root_mismatch"
	CodeOutsideRoots        = "storage_outside_stack_roots"
	CodeUnverified          = "storage_unverified"
)

// Root kinds (protocol.Root.Kind).
const (
	KindStacks  = "stacks"
	KindVolumes = "volumes"
	KindBind    = "bind"
)

// Diagnostic explains a failed check.
type Diagnostic struct {
	Code    string
	Message string
	// Kind and Path identify the root the diagnostic is about.
	Kind string
	Path string
}

func (d Diagnostic) Error() string { return d.Message }

// DiagnosticCode lets other packages surface the code (compose.Options.Guard).
func (d Diagnostic) DiagnosticCode() string { return d.Code }

// ImportDir is where the agent looks for import mounts: every mount whose
// destination is ImportDir or below it (e.g. /opt/stacks:/import:ro, or
// several at /import/opt and /import/srv) exposes a host directory whose
// Compose projects can be imported by copy.
const ImportDir = "/import"

// ImportMount is a host directory mounted into the agent at or below
// ImportDir.
type ImportMount struct {
	// HostPath is the mount's source on the host (a bind source or a
	// volume's mountpoint); Path is where the agent sees it.
	HostPath string
	Path     string
}

// Root is a checked directory.
type Root struct {
	Kind string
	// Path is the host path, identical inside the agent when OK.
	Path string
	OK   bool
}

// Result is the outcome of Verify.
type Result struct {
	// Containerized is false when the agent runs directly on the host (host
	// paths are then its own paths).
	Containerized   bool
	SelfContainerID string
	// DockerRootDir is the Engine's data root.
	DockerRootDir string
	// StacksDir is the stacks volume's mountpoint (checked or not).
	StacksDir string
	// VolumesDir is <DockerRootDir>/volumes.
	VolumesDir  string
	Roots       []Root
	Diagnostics []Diagnostic
	// Imports are the agent's import mounts, longest host path first.
	Imports []ImportMount
}

// ImportSource maps a host directory (a Compose project's working
// directory from its labels) to the path the agent reads it at: below an
// import mount, or the directory itself when the agent runs directly on
// the host (once its layout was verified). ok is false when the agent
// cannot see it.
func (r Result) ImportSource(hostDir string) (string, bool) {
	d := clean(hostDir)
	if !path.IsAbs(d) || d == "/" {
		return "", false
	}
	if !r.Containerized && r.StacksOK() {
		return d, true
	}
	for _, m := range r.Imports {
		if within(m.HostPath, d) {
			return path.Join(m.Path, strings.TrimPrefix(strings.TrimPrefix(d, m.HostPath), "/")), true
		}
	}
	return "", false
}

// importMounts returns the mounts at or below ImportDir, longest host path
// first (the most specific mount wins).
func importMounts(mounts []engine.Mount) []ImportMount {
	var out []ImportMount
	for _, m := range mounts {
		dst, src := clean(m.Destination), clean(m.Source)
		if src == "" || !within(ImportDir, dst) {
			continue
		}
		out = append(out, ImportMount{HostPath: src, Path: dst})
	}
	slices.SortStableFunc(out, func(a, b ImportMount) int { return len(b.HostPath) - len(a.HostPath) })
	return out
}

// StacksOK reports whether stack operations are allowed: the stacks volume
// passed every check.
func (r Result) StacksOK() bool { return r.rootOK(KindStacks, r.StacksDir) }

// VolumesOK reports whether Docker's volume directory passed the checks.
func (r Result) VolumesOK() bool { return r.rootOK(KindVolumes, r.VolumesDir) }

func (r Result) rootOK(kind, p string) bool {
	for _, root := range r.Roots {
		if root.Kind == kind && root.Path == p {
			return root.OK
		}
	}
	return false
}

// Allows returns nil when dir (a stack project directory) is inside a
// verified stack root: the stacks volume or a verified DOCKER_AGENT_STACK_ROOTS
// entry. Otherwise it returns the Diagnostic that blocks it.
func (r Result) Allows(dir string) error {
	d := clean(dir)
	for _, root := range r.Roots {
		if root.Kind == KindVolumes || !within(root.Path, d) {
			continue
		}
		if root.OK {
			return nil
		}
		for _, diag := range r.Diagnostics {
			if diag.Path == root.Path {
				return diag
			}
		}
		return Diagnostic{Code: CodeUnverified, Message: fmt.Sprintf("stack root %s is not verified", root.Path), Kind: root.Kind, Path: root.Path}
	}
	for _, diag := range r.Diagnostics {
		if diag.Path == "" { // a global failure (rootless, Docker Desktop, ...)
			return diag
		}
	}
	return Diagnostic{Code: CodeOutsideRoots, Message: fmt.Sprintf("%s is outside the stacks volume and the registered stack roots", d), Path: d}
}

// Options configures Verify.
type Options struct {
	Engine       engine.Engine
	StacksVolume string
	StackRoots   []string
	// ProcDir is /proc (tests use a fake tree); DockerEnvFile is /.dockerenv.
	ProcDir       string
	DockerEnvFile string
	// Hostname defaults to os.Hostname.
	Hostname func() (string, error)
}

// Verify runs the checks. It never returns an error: every failure is a
// Diagnostic in the Result.
func Verify(ctx context.Context, o Options) Result {
	if o.ProcDir == "" {
		o.ProcDir = "/proc"
	}
	if o.DockerEnvFile == "" {
		o.DockerEnvFile = "/.dockerenv"
	}
	if o.Hostname == nil {
		o.Hostname = os.Hostname
	}
	id := o.Engine.Identity()
	r := Result{DockerRootDir: clean(id.DockerRootDir)}
	r.VolumesDir = path.Join(r.DockerRootDir, "volumes")
	global := func(code, msg string) Result {
		r.Diagnostics = append(r.Diagnostics, Diagnostic{Code: code, Message: msg})
		r.Roots = append(r.Roots, Root{Kind: KindVolumes, Path: r.VolumesDir})
		if r.StacksDir != "" {
			r.Roots = append(r.Roots, Root{Kind: KindStacks, Path: r.StacksDir})
		}
		for _, sr := range o.StackRoots {
			r.Roots = append(r.Roots, Root{Kind: KindBind, Path: clean(sr)})
		}
		return r
	}
	switch {
	case id.Rootless:
		return global(CodeRootlessEngine, "the Docker Engine runs rootless: its data root ("+r.DockerRootDir+
			") and socket live in the user's home and runtime directories, so the identical-path layout Docker Manager needs "+
			"does not apply. Rootless Engines are not supported in v1; stack operations are disabled")
	case id.DockerDesktop:
		return global(CodeDockerDesktop, "the Docker Engine runs inside Docker Desktop's VM: volume and bind paths are VM paths, "+
			"not paths the agent can share. Docker Desktop is not supported in v1; stack operations are disabled")
	}

	v, err := o.Engine.InspectVolume(ctx, o.StacksVolume)
	if err != nil {
		if engine.IsCode(err, engine.CodeNotFound) {
			return global(CodeStacksVolumeMissing, fmt.Sprintf("the stacks volume %q does not exist: create it "+
				"(the documented compose.yaml creates it) or set DOCKER_AGENT_STACKS_VOLUME", o.StacksVolume))
		}
		return global(CodeEngineUnavailable, "cannot inspect the stacks volume: "+err.Error())
	}
	r.StacksDir = clean(v.Mountpoint)
	if v.Driver != "local" {
		return global(CodeStacksVolumeRemote, fmt.Sprintf("the stacks volume %q uses the %q driver; it must be a local volume "+
			"under %s", o.StacksVolume, v.Driver, r.VolumesDir))
	}

	self, containerized, err := findSelf(ctx, o)
	r.Containerized = containerized
	if err != nil {
		return global(CodeEngineUnavailable, "cannot inspect the agent's own container: "+err.Error())
	}
	if containerized && self == nil {
		return global(CodeSelfUnknown, "the agent cannot identify its own container (no container ID in /proc/self/mountinfo or "+
			"/proc/self/cgroup, and the hostname is not the container ID), so it cannot check its mounts; "+
			"do not override the container hostname")
	}
	var mounts []engine.Mount
	if self != nil {
		r.SelfContainerID = self.ID
		mounts = self.Mounts
	}
	r.Imports = importMounts(mounts)

	check := func(kind, p string) {
		// Docker's own volume directory is only read through (volume data
		// is written inside each volume's _data); nothing is written there.
		d := checkPath(kind, p, containerized, mounts, r.DockerRootDir, kind != KindVolumes)
		r.Roots = append(r.Roots, Root{Kind: kind, Path: p, OK: d == nil})
		if d != nil {
			r.Diagnostics = append(r.Diagnostics, *d)
		}
	}
	check(KindStacks, r.StacksDir)
	check(KindVolumes, r.VolumesDir)
	for _, sr := range o.StackRoots {
		check(KindBind, clean(sr))
	}
	return r
}

// checkPath verifies that p (a host path) is visible and writable inside
// the agent at the same path.
func checkPath(kind, p string, containerized bool, mounts []engine.Mount, dockerRoot string, needWrite bool) *Diagnostic {
	diag := func(code, format string, args ...any) *Diagnostic {
		return &Diagnostic{Code: code, Message: fmt.Sprintf(format, args...), Kind: kind, Path: p}
	}
	hint := fmt.Sprintf("mount %[1]s into the agent at the identical path (%[1]s:%[1]s)", p)
	if kind == KindStacks || kind == KindVolumes {
		hint = fmt.Sprintf("mount Docker's volume directory at its identical path (%[1]s/volumes:%[1]s/volumes)", dockerRoot)
	}
	if containerized {
		m := coveringMount(mounts, p)
		if m == nil {
			return diag(pick(kind, CodeMountMissing, CodeRootMismatch), "%s is not mounted into the agent: %s", p, hint)
		}
		if host := path.Join(clean(m.Source), strings.TrimPrefix(p, clean(m.Destination))); host != p {
			return diag(pick(kind, CodePathMismatch, CodeRootMismatch),
				"%s is mounted into the agent from %s: the Engine resolves bind mounts, env files and build contexts on the host, "+
					"so the agent must see the same files at the same path; %s", p, host, hint)
		}
		if !m.ReadWrite {
			return diag(CodeReadOnly, "%s is mounted read-only into the agent; stacks and volumes need a writable mount (restore, file manager)", p)
		}
	}
	local := filepath.FromSlash(p)
	st, err := os.Stat(local)
	if err != nil || !st.IsDir() {
		return diag(CodeNotVisible, "%s is not a directory inside the agent (%v); %s", p, errOr(err, "not a directory"), hint)
	}
	if !needWrite {
		return nil
	}
	f, err := os.CreateTemp(local, ".docker-manager-write-check-*")
	if err != nil {
		return diag(CodeNotWritable, "%s is not writable by the agent (%v); the agent must run as root with a read-write mount", p, err)
	}
	name := f.Name()
	_ = f.Close()
	if err := os.Remove(name); err != nil {
		return diag(CodeNotWritable, "%s: cannot remove a test file (%v)", p, err)
	}
	return nil
}

func pick(kind, volumeCode, rootCode string) string {
	if kind == KindBind {
		return rootCode
	}
	return volumeCode
}

func errOr(err error, s string) string {
	if err != nil {
		return err.Error()
	}
	return s
}

// coveringMount returns the mount with the longest destination that
// contains p.
func coveringMount(mounts []engine.Mount, p string) *engine.Mount {
	var best *engine.Mount
	for i := range mounts {
		dst := clean(mounts[i].Destination)
		if !within(dst, p) {
			continue
		}
		if best == nil || len(dst) > len(clean(best.Destination)) {
			best = &mounts[i]
		}
	}
	return best
}

// within reports whether p is root or below it.
func within(root, p string) bool {
	return p == root || root == "/" || strings.HasPrefix(p, root+"/")
}

// clean normalizes an Engine path (slash-separated).
func clean(p string) string {
	if p == "" {
		return ""
	}
	return path.Clean(filepath.ToSlash(p))
}

var containerIDRE = regexp.MustCompile(`[0-9a-f]{64}`)

// findSelf identifies the agent's container: IDs from /proc/self/mountinfo
// (the hostname/hosts/resolv.conf bind mounts come from
// <data-root>/containers/<id>/), then /proc/self/cgroup (cgroup v1 and
// host-namespace v2), then the hostname when it is an ID prefix (Docker's
// default). containerized is false when nothing suggests a container.
func findSelf(ctx context.Context, o Options) (*engine.ContainerDetails, bool, error) {
	var candidates []string
	if b, err := os.ReadFile(filepath.Join(o.ProcDir, "self", "mountinfo")); err == nil {
		candidates = append(candidates, mountinfoIDs(string(b))...)
	}
	if b, err := os.ReadFile(filepath.Join(o.ProcDir, "self", "cgroup")); err == nil {
		for _, id := range containerIDRE.FindAllString(string(b), -1) {
			if !slices.Contains(candidates, id) {
				candidates = append(candidates, id)
			}
		}
	}
	for _, id := range candidates {
		d, err := o.Engine.InspectContainer(ctx, id)
		if err == nil {
			return &d, true, nil
		}
		if !engine.IsCode(err, engine.CodeNotFound) {
			return nil, true, err
		}
	}
	if host, err := o.Hostname(); err == nil && regexp.MustCompile(`^[0-9a-f]{12,64}$`).MatchString(host) {
		d, err := o.Engine.InspectContainer(ctx, host)
		switch {
		case err == nil && strings.HasPrefix(d.ID, host):
			return &d, true, nil
		case err != nil && !engine.IsCode(err, engine.CodeNotFound):
			return nil, true, err
		}
	}
	_, envErr := os.Stat(o.DockerEnvFile)
	containerized := len(candidates) > 0 || envErr == nil
	return nil, containerized, nil
}

// mountinfoIDs extracts container IDs from mountinfo roots of the form
// .../containers/<id>/...
func mountinfoIDs(mountinfo string) []string {
	var out []string
	for _, line := range strings.Split(mountinfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		root := fields[3]
		if i := strings.Index(root, "/containers/"); i >= 0 {
			rest := root[i+len("/containers/"):]
			if id := containerIDRE.FindString(rest); id != "" && strings.HasPrefix(rest, id) && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	return out
}

// VolumeAccess says whether file browsing, watching and backup (#15, #23,
// #10) may use a Docker volume through the agent's volume directory mount.
type VolumeAccess struct {
	Supported bool
	// Reason explains an unsupported volume (shown read-only in the UI).
	Reason string
}

// remoteTypes are local-driver mount types that point at remote storage.
var remoteTypes = []string{"nfs", "nfs4", "cifs", "smb", "smb3", "sshfs", "glusterfs", "ceph"}

// AccessFor decides v1 support for a volume (decided in #28, recorded in
// docs/internal/support-matrix.md): only local-driver volumes stored under the
// verified volume directory are supported. Non-local drivers (plugins) and
// local volumes backed by NFS/CIFS mount options live elsewhere or are only
// mounted while a container uses them; they are shown read-only.
func (r Result) AccessFor(v engine.Volume) VolumeAccess {
	if v.Driver != "local" {
		return VolumeAccess{Reason: fmt.Sprintf("volume driver %q: only local volumes support file access, watching and backup in v1", v.Driver)}
	}
	if t := strings.ToLower(v.Options["type"]); slices.Contains(remoteTypes, t) || strings.Contains(v.Options["o"], "addr=") {
		return VolumeAccess{Reason: fmt.Sprintf("local volume backed by remote storage (type %q): not supported for file access, watching and backup in v1", t)}
	}
	if !r.VolumesOK() {
		return VolumeAccess{Reason: "Docker's volume directory is not mounted into the agent at its identical path"}
	}
	if !within(r.VolumesDir, clean(v.Mountpoint)) {
		return VolumeAccess{Reason: fmt.Sprintf("volume data at %s is outside %s", clean(v.Mountpoint), r.VolumesDir)}
	}
	return VolumeAccess{Supported: true}
}
