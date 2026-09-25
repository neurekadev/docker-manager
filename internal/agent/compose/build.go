package compose

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"

	"github.com/neurekadev/dockyard/internal/agent/engine"
)

// BuildOptions configures Build.
type BuildOptions struct {
	RunOptions
	// Services limits the build (empty = every service with a build section).
	Services []string
	NoCache  bool
	// PullBase pulls newer base images.
	PullBase bool
	// MissingOnly builds only images that are missing on the host (and
	// services with pull_policy: build), like a deploy does; otherwise
	// every build section is rebuilt.
	MissingOnly bool
	// BuildEvents, when set, receives the raw BuildKit progress (steps and
	// build output) of each image instead of the summarized Events.
	BuildEvents func(image string, ev engine.BuildEvent)
	// Built is called after each image was built.
	Built func(b BuiltImage)
}

// BuiltImage is an image the adapter built for a service.
type BuiltImage struct {
	Service string
	// Image is the service's image reference (the tag it was built as).
	Image   string
	ImageID string
}

// Build builds the images of services with a build section through the
// Engine's BuildKit (an explicit stack build or a deploy's build step,
// #33).
func (a *Adapter) Build(ctx context.Context, p *Project, o BuildOptions) error {
	if err := a.guard("compose.build", p); err != nil {
		return err
	}
	model, err := selected(p, o.Services)
	if err != nil {
		return engine.WrapCode("compose.build", engine.CodeInvalidArgument, err)
	}
	return a.buildImages(ctx, model, buildRequest{all: !o.MissingOnly, noCache: o.NoCache, pull: o.PullBase, auth: o.Auth,
		events: o.Events, buildEvents: o.BuildEvents, built: o.Built})
}

type buildRequest struct {
	// all rebuilds every build service; otherwise only missing images.
	all         bool
	noCache     bool
	pull        bool
	auth        []engine.RegistryAuth
	events      func(Event)
	buildEvents func(image string, ev engine.BuildEvent)
	built       func(BuiltImage)
}

// buildImages builds the images of the model's build services (sorted by
// name; build sections cannot depend on each other because
// additional_contexts is rejected) and marks them so the SDK neither
// rebuilds nor pulls them. It mutates model.Services.
func (a *Adapter) buildImages(ctx context.Context, model *types.Project, req buildRequest) error {
	const op = "compose.build"
	id := a.opts.Engine.Identity()
	hostPlatform := id.OS + "/" + id.Arch
	names := make([]string, 0, len(model.Services))
	for name, s := range model.Services {
		if s.Build != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		s := model.Services[name]
		image := api.GetImageNameOrDefault(s, model.Name)
		policy, _, err := s.GetPullPolicy()
		if err != nil {
			return engine.WrapCode(op, engine.CodeInvalidProject, err)
		}
		need := req.all || policy == types.PullPolicyBuild
		if !need {
			if _, err := a.opts.Engine.InspectImage(ctx, image); err != nil {
				if !engine.IsCode(err, engine.CodeNotFound) {
					return err
				}
				need = true
			}
		}
		if need {
			spec, err := buildSpec(s, image, hostPlatform, req)
			if err != nil {
				return engine.WrapCode(op, engine.CodeUnsupportedFeature, fmt.Errorf("service %q: %w", name, err))
			}
			if req.events != nil {
				req.events(Event{Resource: "Image " + image, Status: "working", Text: "Building"})
				spec.Progress = func(ev engine.BuildEvent) {
					if ev.Status == "log" {
						return
					}
					req.events(Event{Resource: "Image " + image, Status: "working", Text: ev.Step, Details: ev.Status})
				}
			}
			if req.buildEvents != nil {
				spec.Progress = func(ev engine.BuildEvent) { req.buildEvents(image, ev) }
			}
			res, err := a.opts.Engine.Build(ctx, spec)
			if err != nil {
				return err
			}
			if req.events != nil {
				req.events(Event{Resource: "Image " + image, Status: "done", Text: "Built"})
			}
			if req.built != nil {
				req.built(BuiltImage{Service: name, Image: image, ImageID: res.ImageID})
			}
		}
		// The image now exists locally: the SDK must use it as is.
		s.PullPolicy = types.PullPolicyNever
		model.Services[name] = s
	}
	return nil
}

// buildSpec maps a Compose build section to an Engine BuildKit build.
func buildSpec(s types.ServiceConfig, image, hostPlatform string, req buildRequest) (engine.BuildSpec, error) {
	b := s.Build
	if msgs := unsupportedBuildKeys(b); len(msgs) > 0 {
		return engine.BuildSpec{}, fmt.Errorf("%s", strings.Join(msgs, "; "))
	}
	for _, p := range b.Platforms {
		if p != hostPlatform {
			return engine.BuildSpec{}, fmt.Errorf("build.platforms %q: only the Engine platform %s is supported", p, hostPlatform)
		}
	}
	if s.Platform != "" && s.Platform != hostPlatform {
		return engine.BuildSpec{}, fmt.Errorf("platform %q: only the Engine platform %s can be built", s.Platform, hostPlatform)
	}
	spec := engine.BuildSpec{
		Dockerfile:       b.Dockerfile,
		DockerfileInline: b.DockerfileInline,
		Tags:             append([]string{image}, b.Tags...),
		Target:           b.Target,
		Labels:           maps.Clone(map[string]string(b.Labels)),
		NoCache:          b.NoCache || req.noCache,
		Pull:             b.Pull || req.pull,
		NetworkMode:      b.Network,
		ExtraHosts:       b.ExtraHosts.AsList(":"),
		ShmSize:          int64(b.ShmSize),
		CacheFrom:        b.CacheFrom,
		RegistryAuth:     req.auth,
	}
	if spec.DockerfileInline != "" {
		spec.Dockerfile = ""
	}
	if len(b.Args) > 0 {
		spec.BuildArgs = map[string]string{}
		for k, v := range b.Args {
			if v != nil {
				spec.BuildArgs[k] = *v
			}
		}
	}
	if isRemoteContext(b.Context) {
		spec.RemoteContext = b.Context
	} else {
		spec.ContextDir = b.Context
	}
	return spec, nil
}
