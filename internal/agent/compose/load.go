package compose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	"go.yaml.in/yaml/v4"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/lifecycle"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// DefaultConfigFiles are the file names searched in the project directory,
// in order, when ProjectSpec.ConfigFiles is empty. Only the directory itself
// is searched (the Compose CLI would also walk parent directories).
var DefaultConfigFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// ProjectSpec locates a Compose project on disk.
type ProjectSpec struct {
	// Name is the project name (normalized like Compose: lower case,
	// [a-z0-9_-]); empty uses the name in the file or the directory name.
	Name string
	// Dir is the absolute project directory (#28: a directory of the stacks
	// volume or a registered stack root at its identical host path).
	Dir string
	// ConfigFiles are paths relative to Dir; empty selects the first of
	// DefaultConfigFiles plus its matching override file (compose.override.yaml).
	ConfigFiles []string
	// EnvFiles are paths relative to Dir used for interpolation; empty uses
	// .env when present. The agent's own environment is never used for
	// interpolation.
	EnvFiles []string
	// Profiles to enable.
	Profiles []string
	// Content, when set, holds definition files (Dir-relative, slash
	// separated) that are loaded from memory instead of disk: Compose files
	// and the env files used for interpolation. The directory itself need
	// not exist (validation before a stack is created, #7); anything else the
	// project references (service env_files, build contexts, includes)
	// still resolves on disk.
	Content map[string][]byte
	// SkipEnvFiles does not read service env_files (validation of a
	// submitted definition: their values never matter there, and a
	// definition submitted through the API must not make the agent read
	// arbitrary files). Content mode only.
	SkipEnvFiles bool
}

// Project is a loaded, validated Compose project.
type Project struct {
	Name        string
	Dir         string
	ConfigFiles []string
	EnvFiles    []string
	Services    []ServiceInfo
	// Warnings are non-fatal findings (e.g. the obsolete top-level version key).
	Warnings []string
	// DefinitionFiles are the absolute paths of the project's definition:
	// Compose files, env files used for interpolation and service env_files
	// (sorted, deduplicated; files outside Dir are included, callers decide).
	DefinitionFiles []string
	// Binds are the resolved bind mounts of the enabled services.
	Binds []Bind
	// Volumes are the named-volume mounts of the enabled services and
	// AnonymousVolumes their anonymous volume mounts (backups, #10).
	Volumes          []VolumeMount
	AnonymousVolumes []VolumeMount

	model *types.Project
}

// VolumeMount is a volume mount of a service. Name is the Docker volume
// name (the project-scoped name or the external name); it is empty for
// anonymous volumes, whose names only the containers know.
type VolumeMount struct {
	Service  string
	Key      string
	Name     string
	Target   string
	External bool
	ReadOnly bool
}

// Bind is a bind mount of a service (Source is an absolute host path).
type Bind struct {
	Service  string
	Source   string
	Target   string
	ReadOnly bool
}

// LabelDescription is the display metadata label a Compose file may carry
// (#7). Docker Manager imports it once as the service's description; it
// never writes it. Its legacy key (protocol.LegacyLabelPrefix) is read as
// well. (The former icon label is ignored: services have no icon of their
// own.)
const LabelDescription = protocol.LabelDescription

// ServiceInfo summarizes a service.
type ServiceInfo struct {
	Name  string
	Image string
	// Build is true when the service has a build section.
	Build     bool
	DependsOn []Dependency
	Profiles  []string
	// Description is the service's LabelDescription.
	Description string
	// PullPolicy is the service's pull_policy (empty = default "missing").
	PullPolicy string
}

// Dependency is a depends_on entry.
type Dependency struct {
	Service string
	// Condition is service_started, service_healthy or
	// service_completed_successfully.
	Condition string
	Required  bool
	Restart   bool
}

// DeclaredName returns the project name the Compose files of spec set
// with a top-level name: (interpolated like Compose does), or "" when none
// sets one. spec.Name is ignored: a stack's project name overrides the
// file's, so this is the only way to see it.
func DeclaredName(ctx context.Context, spec ProjectSpec) (string, error) {
	const op = "compose.load"
	configs, err := configFiles(spec)
	if err != nil {
		return "", engine.WrapCode(op, engine.CodeInvalidProject, err)
	}
	named := false
	for _, f := range configs {
		var b []byte
		if spec.Content != nil {
			r, _ := filepath.Rel(spec.Dir, f)
			b = spec.Content[filepath.ToSlash(r)]
		} else if b, err = os.ReadFile(f); err != nil { //nolint:gosec // project file inside the validated project directory
			return "", engine.WrapCode(op, engine.CodeInvalidProject, err)
		}
		var n struct {
			Name string `yaml:"name,omitempty"`
		}
		if err := yaml.Unmarshal(b, &n); err != nil {
			return "", engine.WrapCode(op, engine.CodeInvalidProject, fmt.Errorf("%s: %w", filepath.Base(f), err))
		}
		named = named || n.Name != ""
	}
	if !named {
		return "", nil
	}
	spec.Name = ""
	p, err := LoadProject(ctx, spec)
	if err != nil {
		return "", err
	}
	return p.Name, nil
}

// NormalizeProjectName is the project name Compose derives from a
// directory name (lower case, only [a-z0-9_-]); "" when nothing is left.
func NormalizeProjectName(dirName string) string {
	return loader.NormalizeProjectName(dirName)
}

// Load reads and validates a project. Unsupported features are rejected
// with CodeUnsupportedFeature before anything touches the Engine.
func (a *Adapter) Load(ctx context.Context, spec ProjectSpec) (*Project, error) {
	if err := a.guardDir("compose.load", spec.Dir); err != nil {
		return nil, err
	}
	return LoadProject(ctx, spec)
}

// LoadProject is Load without the storage guard, for callers that already
// checked the directory (and tests).
func LoadProject(ctx context.Context, spec ProjectSpec) (*Project, error) {
	const op = "compose.load"
	if !filepath.IsAbs(spec.Dir) {
		return nil, engine.Errorf(op, engine.CodeInvalidProject, "project directory must be an absolute path")
	}
	if spec.Content == nil {
		if st, err := os.Stat(spec.Dir); err != nil || !st.IsDir() {
			return nil, engine.Errorf(op, engine.CodeInvalidProject, "project directory %s does not exist", spec.Dir)
		}
	}
	configs, err := configFiles(spec)
	if err != nil {
		return nil, engine.WrapCode(op, engine.CodeInvalidProject, err)
	}
	envFiles, err := envFilesOf(spec)
	if err != nil {
		return nil, engine.WrapCode(op, engine.CodeInvalidProject, err)
	}
	if spec.Name != "" && spec.Name != loader.NormalizeProjectName(spec.Name) {
		return nil, engine.Errorf(op, engine.CodeInvalidProject, "invalid project name %q (use lower-case letters, digits, '-' and '_')", spec.Name)
	}
	contents := map[string][]byte{}
	var model *types.Project
	if spec.Content != nil {
		model, err = loadContent(ctx, spec, configs, envFiles, contents)
	} else {
		model, err = loadDisk(ctx, spec, configs, envFiles, contents)
	}
	if err != nil {
		return nil, engine.WrapCode(op, engine.CodeInvalidProject, err)
	}
	model = withComposeLabels(model, envFiles)

	p := &Project{Name: model.Name, Dir: model.WorkingDir, ConfigFiles: model.ComposeFiles, EnvFiles: envFiles, model: model}
	if err := validate(model); err != nil {
		return nil, err
	}
	p.Warnings = obsoleteVersionWarnings(configs, contents)
	defs := append(slices.Clone(configs), envFiles...)
	for _, name := range model.ServiceNames() {
		s := model.Services[name]
		si := ServiceInfo{Name: name, Image: api.GetImageNameOrDefault(s, model.Name), Build: s.Build != nil, Profiles: s.Profiles,
			Description: protocol.LabelValue(s.Labels, LabelDescription), PullPolicy: s.PullPolicy}
		for dep, d := range s.DependsOn {
			si.DependsOn = append(si.DependsOn, Dependency{Service: dep, Condition: d.Condition, Required: d.Required, Restart: d.Restart})
		}
		sort.Slice(si.DependsOn, func(i, j int) bool { return si.DependsOn[i].Service < si.DependsOn[j].Service })
		p.Services = append(p.Services, si)
		for _, ef := range s.EnvFiles {
			defs = append(defs, ef.Path)
		}
		for _, v := range s.Volumes {
			switch v.Type {
			case types.VolumeTypeBind:
				p.Binds = append(p.Binds, Bind{Service: name, Source: v.Source, Target: v.Target, ReadOnly: v.ReadOnly})
			case types.VolumeTypeVolume:
				if v.Source == "" {
					p.AnonymousVolumes = append(p.AnonymousVolumes, VolumeMount{Service: name, Target: v.Target, ReadOnly: v.ReadOnly})
					continue
				}
				vc := model.Volumes[v.Source]
				vn := vc.Name
				if vn == "" {
					vn = model.Name + "_" + v.Source
				}
				p.Volumes = append(p.Volumes, VolumeMount{Service: name, Key: v.Source, Name: vn, Target: v.Target,
					External: bool(vc.External), ReadOnly: v.ReadOnly})
			}
		}
	}
	slices.Sort(defs)
	p.DefinitionFiles = slices.Compact(defs)
	return p, nil
}

// loadDisk loads the project's files from disk.
func loadDisk(ctx context.Context, spec ProjectSpec, configs, envFiles []string, contents map[string][]byte) (*types.Project, error) {
	opts, err := cli.NewProjectOptions(configs,
		cli.WithWorkingDirectory(spec.Dir),
		cli.WithEnvFiles(envFiles...),
		cli.WithDotEnv,
		cli.WithName(spec.Name),
		cli.WithProfiles(spec.Profiles),
	)
	if err != nil {
		return nil, err
	}
	model, err := opts.LoadProject(ctx)
	if err != nil {
		return nil, err
	}
	for _, f := range configs {
		if b, err := os.ReadFile(f); err == nil { //nolint:gosec // project file inside the validated project directory
			contents[f] = b
		}
	}
	return model, nil
}

// loadContent loads the project from spec.Content (validation of a
// definition that is not on disk yet, and the deploy's load from the exact
// bytes it snapshotted). It mirrors loadDisk: only the env files feed
// interpolation (never the agent's environment), and the name comes from
// spec, the Compose file or the directory, in that order. Service env_files
// resolve from disk unless SkipEnvFiles is set.
func loadContent(ctx context.Context, spec ProjectSpec, configs, envFiles []string, contents map[string][]byte) (*types.Project, error) {
	rel := func(abs string) string {
		r, _ := filepath.Rel(spec.Dir, abs)
		return filepath.ToSlash(r)
	}
	env := types.Mapping{}
	for _, f := range envFiles {
		b, ok := spec.Content[rel(f)]
		if !ok {
			return nil, fmt.Errorf("couldn't find env file: %s", rel(f))
		}
		vars, err := dotenv.UnmarshalBytesWithLookup(b, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
		if err != nil {
			return nil, fmt.Errorf("env file %s: %w", rel(f), err)
		}
		env.Merge(vars)
	}
	details := types.ConfigDetails{WorkingDir: spec.Dir, Environment: env}
	named := false
	for _, f := range configs {
		b := spec.Content[rel(f)]
		contents[f] = b
		details.ConfigFiles = append(details.ConfigFiles, types.ConfigFile{Filename: f, Content: b})
		var n struct {
			Name string `yaml:"name,omitempty"`
		}
		if err := yaml.Unmarshal(b, &n); err != nil {
			return nil, fmt.Errorf("%s: %w", rel(f), err)
		}
		named = named || n.Name != ""
	}
	model, err := loader.LoadWithContext(ctx, details, loader.WithProfiles(spec.Profiles), func(o *loader.Options) {
		o.SkipResolveEnvironment = spec.SkipEnvFiles
		switch {
		case spec.Name != "":
			o.SetProjectName(spec.Name, true)
		case !named:
			o.SetProjectName(loader.NormalizeProjectName(filepath.Base(spec.Dir)), false)
		}
	})
	if err != nil {
		return nil, err
	}
	model.ComposeFiles = configs
	return model, nil
}

// configFiles resolves the Compose files of spec (absolute paths inside Dir).
func configFiles(spec ProjectSpec) ([]string, error) {
	if len(spec.ConfigFiles) > 0 {
		out := make([]string, 0, len(spec.ConfigFiles))
		for _, f := range spec.ConfigFiles {
			p, err := within(spec.Dir, f)
			if err != nil {
				return nil, err
			}
			if spec.Content != nil {
				if _, ok := spec.Content[filepath.ToSlash(f)]; !ok {
					return nil, fmt.Errorf("compose file %s is missing", f)
				}
			}
			out = append(out, p)
		}
		return out, nil
	}
	exists := func(name string) bool {
		if spec.Content != nil {
			_, ok := spec.Content[name]
			return ok
		}
		st, err := os.Stat(filepath.Join(spec.Dir, name))
		return err == nil && !st.IsDir()
	}
	for _, name := range DefaultConfigFiles {
		if exists(name) {
			out := []string{filepath.Join(spec.Dir, name)}
			ext := filepath.Ext(name)
			if override := strings.TrimSuffix(name, ext) + ".override" + ext; exists(override) {
				out = append(out, filepath.Join(spec.Dir, override))
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("no Compose file (%s) in %s", strings.Join(DefaultConfigFiles, ", "), spec.Dir)
}

// envFilesOf resolves the env files used for interpolation (absolute paths
// inside Dir): the explicit ones, otherwise .env when present.
func envFilesOf(spec ProjectSpec) ([]string, error) {
	var out []string
	for _, f := range spec.EnvFiles {
		p, err := within(spec.Dir, f)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if len(spec.EnvFiles) > 0 {
		return out, nil
	}
	if spec.Content != nil {
		if _, ok := spec.Content[".env"]; ok {
			out = append(out, filepath.Join(spec.Dir, ".env"))
		}
		return out, nil
	}
	if st, err := os.Stat(filepath.Join(spec.Dir, ".env")); err == nil && !st.IsDir() {
		out = append(out, filepath.Join(spec.Dir, ".env"))
	}
	return out, nil
}

// within resolves rel inside dir and refuses paths that escape it.
func within(dir, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("%q must be relative to the project directory", rel)
	}
	p := filepath.Join(dir, rel)
	r, err := filepath.Rel(dir, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q is outside the project directory", rel)
	}
	return p, nil
}

// withComposeLabels adds the labels the Compose CLI's loader sets, so the
// SDK and `docker compose` recognize the project's containers, plus
// Docker Manager's dependency label.
func withComposeLabels(p *types.Project, envFiles []string) *types.Project {
	for name, s := range p.Services {
		s.CustomLabels = map[string]string{
			api.ProjectLabel:     p.Name,
			api.ServiceLabel:     name,
			api.VersionLabel:     api.ComposeVersion,
			api.WorkingDirLabel:  p.WorkingDir,
			api.ConfigFilesLabel: strings.Join(p.ComposeFiles, ","),
			api.OneoffLabel:      "False",
		}
		if len(envFiles) > 0 {
			s.CustomLabels[api.EnvironmentFileLabel] = strings.Join(envFiles, ",")
		}
		// Compose's own depends_on label omits `required`; the
		// dependency-aware lifecycle (#7, #10) reads this one.
		s.CustomLabels[lifecycle.DependsOnLabel] = lifecycle.FormatDependsOn(dependenciesOf(s))
		p.Services[name] = s
	}
	return p.WithoutUnnecessaryResources()
}

func dependenciesOf(s types.ServiceConfig) []lifecycle.Dependency {
	out := make([]lifecycle.Dependency, 0, len(s.DependsOn))
	for dep, d := range s.DependsOn {
		out = append(out, lifecycle.Dependency{Service: dep, Condition: d.Condition, Required: d.Required, Restart: d.Restart})
	}
	return out
}

// Unsupported Compose features (docs/internal/support-matrix.md). Each is rejected
// because it would run code outside the Engine, leak credentials or cannot
// be honored by the Engine's BuildKit without buildx.
func validate(p *types.Project) error {
	const op = "compose.validate"
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if len(p.Models) > 0 {
		add("top-level models (Docker Model Runner) are not supported")
	}
	for _, name := range p.ServiceNames() {
		s := p.Services[name]
		if s.Provider != nil {
			add("service %q: provider services are not supported (they execute external plugins)", name)
		}
		if len(s.Models) > 0 {
			add("service %q: models are not supported", name)
		}
		if s.UseAPISocket {
			add("service %q: use_api_socket is not supported (it would expose the Docker socket and registry credentials to the container)", name)
		}
		if b := s.Build; b != nil {
			for _, msg := range unsupportedBuildKeys(b) {
				add("service %q: %s", name, msg)
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return engine.Errorf(op, engine.CodeUnsupportedFeature, "%s", strings.Join(problems, "; "))
	}
	return nil
}

func unsupportedBuildKeys(b *types.BuildConfig) []string {
	var out []string
	if len(b.Secrets) > 0 {
		out = append(out, "build.secrets is not supported in v1")
	}
	if len(b.SSH) > 0 {
		out = append(out, "build.ssh is not supported in v1")
	}
	if len(b.AdditionalContexts) > 0 {
		out = append(out, "build.additional_contexts is not supported")
	}
	if len(b.CacheTo) > 0 {
		out = append(out, "build.cache_to is not supported")
	}
	if len(b.Entitlements) > 0 || b.Privileged {
		out = append(out, "privileged builds and build.entitlements are not supported")
	}
	if len(b.NoCacheFilter) > 0 {
		out = append(out, "build.no_cache_filter is not supported")
	}
	if len(b.Ulimits) > 0 {
		out = append(out, "build.ulimits is not supported")
	}
	if b.Isolation != "" && b.Isolation != "default" {
		out = append(out, "build.isolation is not supported")
	}
	if b.Provenance != "" || b.SBOM != "" {
		out = append(out, "build.provenance and build.sbom attestations are not supported")
	}
	if len(b.Platforms) > 1 {
		out = append(out, "multi-platform builds (build.platforms with more than one entry) are not supported")
	}
	if b.Network != "" && !slices.Contains([]string{"default", "host", "none"}, b.Network) {
		out = append(out, "build.network must be default, host or none")
	}
	if isRemoteContext(b.Context) {
		if err := engine.ValidateGitContext(b.Context); err != nil {
			out = append(out, "build.context: "+err.Error())
		}
		if b.DockerfileInline != "" {
			out = append(out, "build.dockerfile_inline needs a local build context")
		}
	}
	return out
}

func isRemoteContext(ctx string) bool {
	return strings.Contains(ctx, "://") || strings.HasPrefix(ctx, "git@")
}

// ObsoleteVersionWarning is the text of the obsolete-version warning.
const ObsoleteVersionWarning = "the top-level `version` key is obsolete and ignored"

// obsoleteVersionWarnings reports a top-level `version` key (#7).
func obsoleteVersionWarnings(files []string, contents map[string][]byte) []string {
	var out []string
	for _, f := range files {
		b, ok := contents[f]
		if !ok {
			continue
		}
		var top map[string]any
		if err := yaml.Unmarshal(b, &top); err != nil {
			continue
		}
		if _, ok := top["version"]; ok {
			out = append(out, fmt.Sprintf("%s: %s", filepath.Base(f), ObsoleteVersionWarning))
		}
	}
	return out
}

// errNotLoaded guards operations on a zero Project.
var errNotLoaded = errors.New("project is not loaded")
