package jobspec

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

// ImageBuildInput is the input of an image.build job (#33): a manual build
// from an HTTPS Git repository, run by the agent through the Engine's
// BuildKit. Credentials are referenced by ID (CredentialRefs) and resolved
// at dispatch; they are never part of the input.
type ImageBuildInput struct {
	CredentialRefs
	// GitURL is the normalized http(s) repository URL (no credentials).
	GitURL string `json:"gitUrl"`
	// Ref is a branch, tag, full ref or commit; empty means HEAD. The
	// agent resolves it to a commit before building and builds exactly
	// that commit.
	Ref string `json:"ref,omitempty"`
	// ContextPath is the build context inside the repository ("" = root).
	ContextPath string `json:"contextPath,omitempty"`
	// Dockerfile is relative to the context (default "Dockerfile").
	Dockerfile string `json:"dockerfile,omitempty"`
	Target     string `json:"target,omitempty"`
	// BuildArgs end up in the image history; they are never audited.
	BuildArgs map[string]string `json:"buildArgs,omitempty"`
	// Tags are the image names to produce ("name:tag").
	Tags     []string `json:"tags"`
	NoCache  bool     `json:"noCache,omitempty"`
	Pull     bool     `json:"pull,omitempty"`
	Platform string   `json:"platform,omitempty"`
	// TimeoutSeconds bounds the build step (default DefaultBuildTimeout).
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
}

// Build limits.
const (
	DefaultBuildTimeout = time.Hour
	MaxBuildTimeout     = 6 * time.Hour
	MaxBuildTags        = 16
	MaxBuildArgs        = 64
)

// Item names an image.build result carries (protocol.ItemPayload.Name);
// the item message holds the value.
const (
	// BuildItemCommit is the resolved commit ID.
	BuildItemCommit = "commit"
	// BuildItemRef is the full ref the commit was resolved from.
	BuildItemRef = "ref"
	// BuildItemImage is the built image ID.
	BuildItemImage = "image"
)

var (
	buildArgKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	targetRE      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	platformRE    = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9_]+(/[a-z0-9]+)?$`)
)

// Timeout is the effective build timeout.
func (in ImageBuildInput) Timeout() time.Duration {
	if in.TimeoutSeconds <= 0 {
		return DefaultBuildTimeout
	}
	return min(time.Duration(in.TimeoutSeconds)*time.Second, MaxBuildTimeout)
}

// CleanContextPath normalizes a context path inside a repository; it
// refuses absolute paths and paths leaving the repository.
func CleanContextPath(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return "", nil
	}
	c := path.Clean(p)
	if c == "." {
		return "", nil
	}
	if strings.HasPrefix(c, "../") || c == ".." || strings.ContainsAny(c, "#:\\\x00") {
		return "", errors.New("the context path must stay inside the repository and must not contain '#', ':' or '\\'")
	}
	return c, nil
}

// Validate checks the fields the agent relies on (the manager validates
// the request more thoroughly).
func (in ImageBuildInput) Validate() error {
	switch {
	case in.GitURL == "":
		return errors.New("gitUrl is required")
	case len(in.Tags) == 0 || len(in.Tags) > MaxBuildTags:
		return fmt.Errorf("1 to %d tags are required", MaxBuildTags)
	case len(in.BuildArgs) > MaxBuildArgs:
		return fmt.Errorf("at most %d build arguments", MaxBuildArgs)
	case in.Target != "" && !targetRE.MatchString(in.Target):
		return errors.New("invalid target stage name")
	case in.Platform != "" && !platformRE.MatchString(in.Platform):
		return errors.New("invalid platform (os/arch[/variant])")
	case in.TimeoutSeconds < 0:
		return errors.New("invalid timeout")
	}
	for k := range in.BuildArgs {
		if !buildArgKeyRE.MatchString(k) {
			return fmt.Errorf("invalid build argument name %q", k)
		}
	}
	if c, err := CleanContextPath(in.ContextPath); err != nil || c != in.ContextPath {
		return errors.New("invalid context path")
	}
	if in.Dockerfile != "" {
		d := path.Clean(in.Dockerfile)
		if path.IsAbs(d) || d == ".." || strings.HasPrefix(d, "../") {
			return errors.New("the Dockerfile must be inside the build context")
		}
	}
	return nil
}

// DecodeImageBuildInput strictly decodes and validates an image.build input.
func DecodeImageBuildInput(raw json.RawMessage) (ImageBuildInput, error) {
	var in ImageBuildInput
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, fmt.Errorf("image.build input: %w", err)
	}
	if err := in.Validate(); err != nil {
		return in, fmt.Errorf("image.build input: %w", err)
	}
	return in, nil
}
