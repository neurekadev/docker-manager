// Package builds is the agent executor of image.build jobs (#33): a manual
// build from an HTTP(S) Git repository through the Engine's BuildKit (the
// Moby client's /build with a remote Git context; no docker or buildx CLI).
//
//	fetch_context  resolve the ref to a commit with an in-process
//	               ls-remote (internal/gitremote) using the job's Git
//	               credential, and record it (item "commit")
//	build          build <url>#<commit>[:<context>] — exactly the resolved
//	               commit — with the Git credential served to BuildKit as a
//	               session secret and base-image registry credentials from
//	               the command (#19); stream BuildKit progress and logs as
//	               job progress; honor cancellation and the timeout; record
//	               the image ID (item "image")
//
// Credentials exist only in memory for the attempt (StepContext.Secrets);
// every message leaving the executor is scrubbed of them.
package builds

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/buildrun"
	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/regauth"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/gitremote"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// DefaultCancelPoll is how often a running build checks for cancellation.
const DefaultCancelPoll = buildrun.DefaultCancelPoll

// Options configures the executor.
type Options struct {
	// Engine returns the connected Engine adapter (nil while disconnected).
	Engine func() engine.Engine
	// HTTP is the client for Git ref listing (default: 60 s timeout).
	HTTP   *http.Client
	Clock  clock.Clock
	Logger *slog.Logger
	// CancelPoll is how often a running build checks for cancellation.
	CancelPoll time.Duration
}

type executor struct {
	opts Options

	mu       sync.Mutex
	resolved map[string]resolution // job ID -> commit of this process
}

type resolution struct {
	commit, ref string
}

// Executor returns the image.build executor.
func Executor(o Options) jobexec.Executor {
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.CancelPoll <= 0 {
		o.CancelPoll = DefaultCancelPoll
	}
	e := &executor{opts: o, resolved: map[string]resolution{}}
	return jobexec.Executor{Kind: jobspec.ImageBuild, Steps: map[string]jobexec.StepFunc{
		"fetch_context": e.fetchContext,
		"build":         e.build,
	}}
}

// gitCredential returns the command's credential for the repository host.
// A job that names a Git credential never falls back to anonymous access.
func gitCredential(in jobspec.ImageBuildInput, s *protocol.CommandSecrets, repo gitremote.Repo) (*protocol.GitCredential, error) {
	if s != nil {
		for i := range s.Git {
			if strings.EqualFold(s.Git[i].Host, repo.Host()) {
				return &s.Git[i], nil
			}
		}
	}
	if len(in.GitCredentials) > 0 {
		return nil, errors.New("the job names a Git credential but the command carries none for " + repo.Host())
	}
	return nil, nil
}

// prepare decodes the input and picks the Git credential (no network).
func prepare(sc *jobexec.StepContext) (jobspec.ImageBuildInput, gitremote.Repo, *protocol.GitCredential, error) {
	in, err := jobspec.DecodeImageBuildInput(sc.Input)
	if err != nil {
		return in, gitremote.Repo{}, nil, err
	}
	repo, err := gitremote.ParseURL(in.GitURL)
	if err != nil {
		return in, repo, nil, err
	}
	cred, err := gitCredential(in, sc.Secrets, repo)
	return in, repo, cred, err
}

// lookup resolves the ref to a commit (ls-remote).
func (e *executor) lookup(ctx context.Context, in jobspec.ImageBuildInput, repo gitremote.Repo, cred *protocol.GitCredential) (resolution, error) {
	if gitremote.IsCommitID(in.Ref) {
		return resolution{commit: in.Ref}, nil
	}
	o := gitremote.Options{HTTP: e.opts.HTTP}
	if cred != nil {
		o.Credential = &gitremote.Credential{Username: cred.Username, Token: logging.Secret(cred.Secret)}
		o.AllowPlainHTTP = cred.PlainHTTP
	}
	refs, err := gitremote.LsRemote(ctx, repo, o)
	if err != nil {
		return resolution{}, err
	}
	commit, full, err := refs.Resolve(in.Ref)
	if err != nil {
		return resolution{}, err
	}
	return resolution{commit: commit, ref: full}, nil
}

func (e *executor) fetchContext(ctx context.Context, sc *jobexec.StepContext) error {
	in, repo, cred, err := prepare(sc)
	if err != nil {
		return scrubErr(sc.Secrets, err)
	}
	res, err := e.lookup(ctx, in, repo, cred)
	if err != nil {
		return scrubErr(sc.Secrets, err)
	}
	e.mu.Lock()
	e.resolved[sc.JobID] = res
	e.mu.Unlock()
	sc.Item(ctx, jobspec.BuildItemCommit, domain.ItemSucceeded, res.commit)
	if res.ref != "" {
		sc.Item(ctx, jobspec.BuildItemRef, domain.ItemSucceeded, res.ref)
	}
	sc.Progress(ctx, 5, "resolved "+repo.String()+" to commit "+res.commit)
	return nil
}

func (e *executor) build(ctx context.Context, sc *jobexec.StepContext) error {
	e.mu.Lock()
	res, ok := e.resolved[sc.JobID]
	delete(e.resolved, sc.JobID)
	e.mu.Unlock()
	in, repo, cred, err := prepare(sc)
	if err != nil {
		return scrubErr(sc.Secrets, err)
	}
	if !ok {
		// Resumed attempt: fetch_context ran in an earlier process.
		if res, err = e.lookup(ctx, in, repo, cred); err != nil {
			return scrubErr(sc.Secrets, err)
		}
		sc.Item(ctx, jobspec.BuildItemCommit, domain.ItemSucceeded, res.commit)
	}
	var eng engine.Engine
	if e.opts.Engine != nil {
		eng = e.opts.Engine()
	}
	if eng == nil {
		return errors.New("the Docker Engine is not connected")
	}
	remote := repo.String() + "#" + res.commit
	if in.ContextPath != "" {
		remote += ":" + in.ContextPath
	}
	spec := engine.BuildSpec{
		RemoteContext: remote, Dockerfile: in.Dockerfile, Tags: in.Tags, BuildArgs: in.BuildArgs, Target: in.Target,
		NoCache: in.NoCache, Pull: in.Pull, Platform: in.Platform, RegistryAuth: regauth.All(sc.Secrets),
	}
	if cred != nil {
		spec.GitAuth = []engine.GitAuth{{Host: repo.Host(), Username: cred.Username, Token: logging.Secret(cred.Secret)}}
	}
	out := buildrun.NewProgress(ctx, sc)
	spec.Progress = out.Event
	sc.Progress(ctx, 10, "building "+strings.Join(in.Tags, ", ")+" from "+repo.String()+" at "+res.commit)
	var result engine.BuildResult
	err = buildrun.Run(ctx, sc, buildrun.Options{Clock: e.opts.Clock, Poll: e.opts.CancelPoll, Timeout: in.Timeout()},
		func(bctx context.Context) error {
			var berr error
			result, berr = eng.Build(bctx, spec)
			return berr
		})
	out.Flush()
	if err != nil {
		if errors.Is(err, jobexec.ErrStepCancelled) || ctx.Err() != nil {
			return err
		}
		return scrubErr(sc.Secrets, err)
	}
	sc.Item(ctx, jobspec.BuildItemImage, domain.ItemSucceeded, result.ImageID)
	sc.Progress(ctx, 100, "built "+result.ImageID)
	return nil
}

// scrubErr returns err with the attempt's secrets removed from its text.
func scrubErr(s *protocol.CommandSecrets, err error) error { return buildrun.ScrubError(s, err) }
