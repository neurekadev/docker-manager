package stacks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/buildrun"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/compose"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Stack builds (#33): the images of Compose build sections are built by
// the Compose adapter through the Engine's BuildKit (engine.Client.Build,
// never the Compose SDK's build path or a CLI), for an explicit stack.build
// and in the build step of stack.deploy. Both stream BuildKit progress and
// build output as job progress scrubbed of the command's credentials, stop
// on cancellation (the job ends cancelled; images built before stay, the
// interrupted one is not tagged) and on their timeout, and use the
// command's registry credentials for base images (#19).

// errNoBuildSections refuses a stack build without anything to build.
var errNoBuildSections = errors.New("no build section to build")

// buildTimeout is the effective build timeout of a stack job.
func buildTimeout(in protocol.StackJobInput) time.Duration {
	if in.BuildTimeoutSeconds <= 0 {
		return jobspec.DefaultBuildTimeout
	}
	return min(time.Duration(in.BuildTimeoutSeconds)*time.Second, jobspec.MaxBuildTimeout)
}

// buildServices returns the build services an operation on services
// (empty = all) covers. Named services must exist and have a build section.
func buildServices(p *compose.Project, services []string) ([]string, error) {
	var out []string
	for _, s := range p.Services {
		if s.Build && (len(services) == 0 || slices.Contains(services, s.Name)) {
			out = append(out, s.Name)
		}
	}
	for _, name := range services {
		if !slices.Contains(out, name) {
			if !slices.ContainsFunc(p.Services, func(s compose.ServiceInfo) bool { return s.Name == name }) {
				return nil, fmt.Errorf("%w: the project has no service %q", errNoBuildSections, name)
			}
			return nil, fmt.Errorf("%w: service %q has no build section", errNoBuildSections, name)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: the project has no service with a build section", errNoBuildSections)
	}
	return out, nil
}

// fetchSources (stack.build) loads the project on disk and checks that
// there is something to build; its services and warnings become the
// output.
func (s *Service) fetchSources(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := input(sc)
	if err != nil {
		return err
	}
	p, dir, err := s.project(ctx, in)
	if err != nil {
		return err
	}
	names, err := buildServices(p, in.Services)
	if err != nil {
		return err
	}
	bs := binds(p, dir)
	sc.Progress(ctx, 5, fmt.Sprintf("loaded %s: building %s", p.Name, strings.Join(names, ", ")))
	return update(ctx, sc, func(o *protocol.StackJobOutput) {
		o.Services = serviceInfos(p)
		o.Warnings = warnings(p, bs)
	})
}

// stackBuild (stack.build) rebuilds the build sections.
func (s *Service) stackBuild(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := input(sc)
	if err != nil {
		return err
	}
	p, _, err := s.project(ctx, in)
	if err != nil {
		return err
	}
	if _, err := buildServices(p, in.Services); err != nil {
		return err
	}
	return s.runBuild(ctx, sc, in, p, false)
}

// runBuild builds the project's build sections (every one, or with
// missingOnly only the images missing on the host) and reports the built
// images in the output and as items.
func (s *Service) runBuild(ctx context.Context, sc *jobexec.StepContext, in protocol.StackJobInput, p *compose.Project, missingOnly bool) error {
	if !slices.ContainsFunc(p.Services, func(sv compose.ServiceInfo) bool { return sv.Build }) {
		return nil
	}
	creds, err := auth(sc, in)
	if err != nil {
		return err
	}
	c, err := s.composer()
	if err != nil {
		return err
	}
	out := buildrun.NewProgress(ctx, sc)
	current := ""
	var built []protocol.AppliedImage
	o := compose.BuildOptions{
		RunOptions:  compose.RunOptions{Auth: creds},
		Services:    in.Services,
		NoCache:     in.NoCache,
		PullBase:    in.PullBase,
		MissingOnly: missingOnly,
		BuildEvents: func(image string, ev engine.BuildEvent) {
			if image != current {
				current = image
				out.SetPrefix(image)
				out.Send("building")
			}
			out.Event(ev)
		},
		Built: func(b compose.BuiltImage) {
			built = append(built, protocol.AppliedImage{Service: b.Service, Image: b.Image, ImageID: b.ImageID, Build: true})
			sc.Item(ctx, b.Image, domain.ItemSucceeded, b.ImageID)
		},
	}
	sc.Progress(ctx, 20, "building images")
	err = buildrun.Run(ctx, sc, buildrun.Options{Clock: s.opts.Clock, Poll: s.opts.BuildCancelPoll, Timeout: buildTimeout(in)},
		func(bctx context.Context) error { return c.Build(bctx, p, o) })
	out.SetPrefix("")
	if uerr := update(ctx, sc, func(o *protocol.StackJobOutput) { o.Built = append(o.Built, built...) }); uerr != nil && err == nil {
		err = uerr
	}
	if err != nil {
		if errors.Is(err, jobexec.ErrStepCancelled) || ctx.Err() != nil {
			return err
		}
		return buildrun.ScrubError(sc.Secrets, err)
	}
	if len(built) > 0 {
		sc.Progress(ctx, -1, fmt.Sprintf("built %d image(s)", len(built)))
	}
	return nil
}
