package engine

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/session"
	"github.com/moby/go-archive"
	buildtypes "github.com/moby/moby/api/types/build"
	"github.com/moby/moby/client"
	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// BuildSpec describes an image build. Exactly one of ContextDir and
// RemoteContext is set. Builds always use the Engine's BuildKit (#33); the
// legacy builder is never used.
type BuildSpec struct {
	// ContextDir is a local directory sent to the Engine as the build
	// context (honoring .dockerignore; symlinks are archived, not followed).
	ContextDir string
	// RemoteContext is an http(s) Git URL ("https://host/repo.git#ref:subdir")
	// fetched by the Engine's BuildKit itself.
	RemoteContext string
	// Dockerfile is the Dockerfile path relative to the context (default
	// "Dockerfile"). DockerfileInline replaces it (local contexts only).
	Dockerfile       string
	DockerfileInline string
	Tags             []string
	// BuildArgs values end up in image history; never log them.
	BuildArgs   map[string]string
	Target      string
	Labels      map[string]string
	NoCache     bool
	Pull        bool
	Platform    string
	NetworkMode string
	ExtraHosts  []string
	ShmSize     int64
	CacheFrom   []string
	// RegistryAuth authenticates base-image pulls. The credentials are
	// served to BuildKit over a session on the Engine connection, from
	// memory only.
	RegistryAuth []RegistryAuth
	// Progress receives BuildKit progress (may be nil).
	Progress func(BuildEvent)
}

// BuildEvent is one BuildKit progress update.
type BuildEvent struct {
	// Step is the BuildKit vertex name, e.g. "[2/2] COPY hello.txt /hello.txt".
	Step string
	// Status is "started", "done", "cached", "error" or "log".
	Status string
	Error  string
	// Log is build output of the step (Status "log").
	Log []byte
}

// BuildResult reports a successful build.
type BuildResult struct {
	ImageID string
}

// inlineDockerfileName is where DockerfileInline is placed in the context.
const inlineDockerfileName = ".dockyard.inline.Dockerfile"

// Build runs a BuildKit build and returns the image ID. Build failures
// return CodeBuildFailed with BuildKit's error message.
func (c *Client) Build(ctx context.Context, spec BuildSpec) (BuildResult, error) {
	const op = "image.build"
	if err := validateBuildSpec(spec); err != nil {
		return BuildResult{}, &Error{Op: op, Code: CodeInvalidArgument, Message: err.Error(), err: err}
	}
	opts := client.ImageBuildOptions{
		Version:     buildtypes.BuilderBuildKit,
		Tags:        spec.Tags,
		Dockerfile:  spec.Dockerfile,
		Target:      spec.Target,
		Labels:      spec.Labels,
		NoCache:     spec.NoCache,
		PullParent:  spec.Pull,
		NetworkMode: spec.NetworkMode,
		ExtraHosts:  spec.ExtraHosts,
		ShmSize:     spec.ShmSize,
		CacheFrom:   spec.CacheFrom,
		Remove:      true,
	}
	if len(spec.BuildArgs) > 0 {
		opts.BuildArgs = make(map[string]*string, len(spec.BuildArgs))
		for k, v := range spec.BuildArgs {
			opts.BuildArgs[k] = &v
		}
	}
	if spec.Platform != "" {
		p, err := parsePlatform(spec.Platform)
		if err != nil {
			return BuildResult{}, &Error{Op: op, Code: CodeInvalidArgument, Message: err.Error(), err: err}
		}
		opts.Platforms = []ocispec.Platform{p}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var body io.Reader
	if spec.RemoteContext != "" {
		opts.RemoteContext = spec.RemoteContext
	} else {
		rc, dockerfile, err := contextArchive(spec.ContextDir, spec.Dockerfile, spec.DockerfileInline)
		if err != nil {
			return BuildResult{}, &Error{Op: op, Code: CodeInvalidArgument, Message: err.Error(), err: err}
		}
		defer rc.Close()
		body = rc
		opts.Dockerfile = dockerfile
	}

	if len(spec.RegistryAuth) > 0 {
		sess, err := c.startSession(ctx, spec.RegistryAuth)
		if err != nil {
			return BuildResult{}, err
		}
		defer func() { _ = sess.Close() }()
		opts.SessionID = sess.ID()
	}

	res, err := c.api.ImageBuild(ctx, body, opts)
	if err != nil {
		return BuildResult{}, wrap(op, err)
	}
	defer res.Body.Close()
	var out BuildResult
	dec := json.NewDecoder(res.Body)
	for {
		var m struct {
			ID    string           `json:"id"`
			Aux   *json.RawMessage `json:"aux"`
			Error *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
			Stream string `json:"stream"`
		}
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if ctx.Err() != nil {
				return BuildResult{}, wrap(op, ctx.Err())
			}
			return BuildResult{}, wrap(op, err)
		}
		if m.Error != nil {
			return BuildResult{}, newError(op, CodeBuildFailed, "%s", m.Error.Message)
		}
		if m.Aux == nil {
			continue
		}
		switch m.ID {
		case "moby.image.id":
			var id struct{ ID string }
			if json.Unmarshal(*m.Aux, &id) == nil {
				out.ImageID = id.ID
			}
		case "moby.buildkit.trace":
			if spec.Progress != nil {
				emitTrace(*m.Aux, spec.Progress)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return BuildResult{}, wrap(op, err)
	}
	if out.ImageID == "" {
		return BuildResult{}, newError(op, CodeBuildFailed, "the Engine reported no image ID")
	}
	return out, nil
}

// emitTrace decodes a BuildKit status message (protobuf in a JSON byte
// string) into BuildEvents.
func emitTrace(raw json.RawMessage, fn func(BuildEvent)) {
	var dt []byte
	if err := json.Unmarshal(raw, &dt); err != nil {
		return
	}
	var st controlapi.StatusResponse
	if err := st.UnmarshalVT(dt); err != nil {
		return
	}
	names := map[string]string{}
	for _, v := range st.Vertexes {
		names[v.Digest] = v.Name
		ev := BuildEvent{Step: v.Name}
		switch {
		case v.Error != "":
			ev.Status, ev.Error = "error", v.Error
		case v.Cached:
			ev.Status = "cached"
		case v.Completed != nil:
			ev.Status = "done"
		case v.Started != nil:
			ev.Status = "started"
		default:
			continue
		}
		fn(ev)
	}
	for _, l := range st.Logs {
		fn(BuildEvent{Step: names[l.Vertex], Status: "log", Log: l.Msg})
	}
}

func validateBuildSpec(s BuildSpec) error {
	switch {
	case s.ContextDir == "" && s.RemoteContext == "":
		return errors.New("a build needs a local context directory or a Git URL")
	case s.ContextDir != "" && s.RemoteContext != "":
		return errors.New("set either a local context directory or a Git URL, not both")
	case s.RemoteContext != "" && s.DockerfileInline != "":
		return errors.New("an inline Dockerfile needs a local build context")
	case s.DockerfileInline != "" && s.Dockerfile != "":
		return errors.New("set either dockerfile or dockerfile_inline, not both")
	}
	if s.RemoteContext != "" {
		if err := ValidateGitContext(s.RemoteContext); err != nil {
			return err
		}
	}
	if s.Dockerfile != "" && (path.IsAbs(filepath.ToSlash(s.Dockerfile)) || strings.HasPrefix(path.Clean(filepath.ToSlash(s.Dockerfile)), "../")) {
		return errors.New("the Dockerfile must be inside the build context")
	}
	return nil
}

// ValidateGitContext accepts http(s) Git URLs with an optional
// "#ref[:subdir]" fragment. SSH Git URLs are out of v1 (#33) and URLs with
// embedded credentials are refused (credentials go through the session).
func ValidateGitContext(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("invalid Git URL")
	}
	switch u.Scheme {
	case "https", "http":
	default:
		return errors.New("only http(s) Git URLs are supported (SSH Git access is not in v1)")
	}
	if u.User != nil {
		return errors.New("git URLs must not embed credentials")
	}
	if u.Host == "" {
		return errors.New("git URL has no host")
	}
	return nil
}

// contextArchive tars dir for the Engine, applying .dockerignore and
// keeping the Dockerfile and .dockerignore themselves. It returns the
// Dockerfile name to pass to the Engine.
func contextArchive(dir, dockerfile, inline string) (io.ReadCloser, string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, "", errors.New("build context not found")
	}
	if !info.IsDir() {
		return nil, "", errors.New("build context is not a directory")
	}
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	dockerfile = path.Clean(filepath.ToSlash(dockerfile))
	excludes, err := readDockerignore(dir)
	if err != nil {
		return nil, "", err
	}
	if len(excludes) > 0 {
		pm, err := patternmatcher.New(excludes)
		if err != nil {
			return nil, "", errors.New("invalid .dockerignore pattern")
		}
		for _, keep := range []string{".dockerignore", dockerfile} {
			if m, _ := pm.MatchesOrParentMatches(keep); m {
				excludes = append(excludes, "!"+keep)
			}
		}
	}
	rc, err := archive.TarWithOptions(dir, &archive.TarOptions{
		ExcludePatterns: excludes,
		ChownOpts:       &archive.ChownOpts{UID: 0, GID: 0},
	})
	if err != nil {
		return nil, "", err
	}
	if inline == "" {
		return rc, dockerfile, nil
	}
	content := []byte(inline)
	return archive.ReplaceFileTarWrapper(rc, map[string]archive.TarModifierFunc{
		inlineDockerfileName: func(string, *tar.Header, io.Reader) (*tar.Header, []byte, error) {
			return &tar.Header{Name: inlineDockerfileName, Mode: 0o600, ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg}, content, nil
		},
	}), inlineDockerfileName, nil
}

func readDockerignore(dir string) ([]string, error) {
	f, err := os.Open(filepath.Join(dir, ".dockerignore"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	return ignorefile.ReadAll(f)
}

// startSession opens a BuildKit session over the Engine connection that
// answers registry credential requests from memory.
func (c *Client) startSession(ctx context.Context, auths []RegistryAuth) (*session.Session, error) {
	const op = "image.build.session"
	sess, err := session.NewSession(ctx, "dockyard")
	if err != nil {
		return nil, wrap(op, err)
	}
	sess.Allow(newAuthProvider(auths))
	go func() {
		err := sess.Run(ctx, func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {
			return c.api.DialHijack(ctx, "/session", proto, meta)
		})
		if err != nil && ctx.Err() == nil {
			c.log.Warn("BuildKit session ended", "error", err)
		}
	}()
	return sess, nil
}
