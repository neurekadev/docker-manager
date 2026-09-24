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
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	"go.yaml.in/yaml/v4"

	"github.com/neurekadev/dockyard/internal/agent/engine"
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

	model *types.Project
}

// ServiceInfo summarizes a service.
type ServiceInfo struct {
	Name  string
	Image string
	// Build is true when the service has a build section.
	Build     bool
	DependsOn []Dependency
	Profiles  []string
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

// Load reads and validates a project. Unsupported features are rejected
// with CodeUnsupportedFeature before anything touches the Engine.
func (a *Adapter) Load(ctx context.Context, spec ProjectSpec) (*Project, error) {
	const op = "compose.load"
	if !filepath.IsAbs(spec.Dir) {
		return nil, engine.Errorf(op, engine.CodeInvalidProject, "project directory must be an absolute path")
	}
	if st, err := os.Stat(spec.Dir); err != nil || !st.IsDir() {
		return nil, engine.Errorf(op, engine.CodeInvalidProject, "project directory %s does not exist", spec.Dir)
	}
	configs, err := configFiles(spec)
	if err != nil {
		return nil, engine.WrapCode(op, engine.CodeInvalidProject, err)
	}
	var envFiles []string
	for _, f := range spec.EnvFiles {
		p, err := within(spec.Dir, f)
		if err != nil {
			return nil, engine.WrapCode(op, engine.CodeInvalidProject, err)
		}
		envFiles = append(envFiles, p)
	}
	if spec.Name != "" && spec.Name != loader.NormalizeProjectName(spec.Name) {
		return nil, engine.Errorf(op, engine.CodeInvalidProject, "invalid project name %q (use lower-case letters, digits, '-' and '_')", spec.Name)
	}

	opts, err := cli.NewProjectOptions(configs,
		cli.WithWorkingDirectory(spec.Dir),
		cli.WithEnvFiles(envFiles...),
		cli.WithDotEnv,
		cli.WithName(spec.Name),
		cli.WithProfiles(spec.Profiles),
	)
	if err != nil {
		return nil, engine.WrapCode(op, engine.CodeInvalidProject, err)
	}
	model, err := opts.LoadProject(ctx)
	if err != nil {
		return nil, engine.WrapCode(op, engine.CodeInvalidProject, err)
	}
	model = withComposeLabels(model, envFiles)

	p := &Project{Name: model.Name, Dir: model.WorkingDir, ConfigFiles: model.ComposeFiles, EnvFiles: envFiles, model: model}
	if err := validate(model); err != nil {
		return nil, err
	}
	p.Warnings = obsoleteVersionWarnings(configs)
	for _, name := range model.ServiceNames() {
		s := model.Services[name]
		si := ServiceInfo{Name: name, Image: api.GetImageNameOrDefault(s, model.Name), Build: s.Build != nil, Profiles: s.Profiles}
		for dep, d := range s.DependsOn {
			si.DependsOn = append(si.DependsOn, Dependency{Service: dep, Condition: d.Condition, Required: d.Required, Restart: d.Restart})
		}
		sort.Slice(si.DependsOn, func(i, j int) bool { return si.DependsOn[i].Service < si.DependsOn[j].Service })
		p.Services = append(p.Services, si)
	}
	return p, nil
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
			out = append(out, p)
		}
		return out, nil
	}
	for _, name := range DefaultConfigFiles {
		p := filepath.Join(spec.Dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			out := []string{p}
			ext := filepath.Ext(name)
			override := filepath.Join(spec.Dir, strings.TrimSuffix(name, ext)+".override"+ext)
			if st, err := os.Stat(override); err == nil && !st.IsDir() {
				out = append(out, override)
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("no Compose file (%s) in %s", strings.Join(DefaultConfigFiles, ", "), spec.Dir)
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
// SDK and `docker compose` recognize the project's containers.
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
		p.Services[name] = s
	}
	return p.WithoutUnnecessaryResources()
}

// Unsupported Compose features (docs/support-matrix.md). Each is rejected
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

// obsoleteVersionWarnings reports a top-level `version` key (#7).
func obsoleteVersionWarnings(files []string) []string {
	var out []string
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // project file inside the validated project directory
		if err != nil {
			continue
		}
		var top map[string]any
		if err := yaml.Unmarshal(b, &top); err != nil {
			continue
		}
		if _, ok := top["version"]; ok {
			out = append(out, fmt.Sprintf("%s: the top-level `version` key is obsolete and ignored", filepath.Base(f)))
		}
	}
	return out
}

// errNotLoaded guards operations on a zero Project.
var errNotLoaded = errors.New("project is not loaded")
