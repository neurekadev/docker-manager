// Package imageref parses and normalizes container image references and
// registry hosts the way Docker does, for registry connection matching
// (#19), digest checks (#20) and builds (#33). It is shared by the manager
// and the agent and has no Docker SDK dependency.
//
// Normalization:
//
//   - a reference without a registry host is on Docker Hub ("nginx" is
//     docker.io/library/nginx:latest);
//   - every Docker Hub alias (docker.io, index.docker.io,
//     registry-1.docker.io, registry.hub.docker.com) is the host docker.io;
//   - hosts are lower-case and keep an explicit port (registry.lan:5000);
//   - a reference without tag or digest means the tag "latest".
package imageref

import (
	"errors"
	"fmt"
	"strings"

	"github.com/distribution/reference"
)

// DockerHub is the canonical host of Docker Hub.
const DockerHub = "docker.io"

// dockerHubAPI is the host serving Docker Hub's registry API.
const dockerHubAPI = "registry-1.docker.io"

// DockerHubServerAddress is the server address the Engine expects in
// X-Registry-Auth for Docker Hub.
const DockerHubServerAddress = "https://index.docker.io/v1/"

// dockerHubAliases are the host names that mean Docker Hub.
var dockerHubAliases = map[string]bool{
	"docker.io":               true,
	"index.docker.io":         true,
	"registry-1.docker.io":    true,
	"registry.hub.docker.com": true,
}

// ErrInvalid is wrapped by every parse error.
var ErrInvalid = errors.New("invalid image reference")

// Ref is a parsed, normalized image reference.
type Ref struct {
	// Host is the canonical registry host (docker.io for Docker Hub).
	Host string
	// Repository is the repository path on the host ("library/nginx").
	Repository string
	// Tag is the tag; "latest" when neither tag nor digest was given.
	Tag string
	// Digest is the pinned manifest digest ("sha256:..."), if any.
	Digest string
}

// Name is host/repository.
func (r Ref) Name() string { return r.Host + "/" + r.Repository }

// String is the fully qualified reference (host/repository[:tag][@digest]).
func (r Ref) String() string {
	s := r.Name()
	if r.Tag != "" {
		s += ":" + r.Tag
	}
	if r.Digest != "" {
		s += "@" + r.Digest
	}
	return s
}

// Pinned reports whether the reference names an immutable digest.
func (r Ref) Pinned() bool { return r.Digest != "" }

// Object is what a manifest request names: the digest if pinned, else the tag.
func (r Ref) Object() string {
	if r.Digest != "" {
		return r.Digest
	}
	return r.Tag
}

// Parse parses an image reference such as "nginx", "ghcr.io/org/app:1.2",
// "registry.lan:5000/team/app@sha256:..." or "index.docker.io/library/redis".
func Parse(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Ref{}, fmt.Errorf("%w: empty", ErrInvalid)
	}
	if len(s) > 1024 {
		return Ref{}, fmt.Errorf("%w: too long", ErrInvalid)
	}
	if strings.Contains(s, "://") {
		return Ref{}, fmt.Errorf("%w: must not contain a URL scheme", ErrInvalid)
	}
	named, err := reference.ParseNormalizedNamed(s)
	if err != nil {
		return Ref{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	host := strings.ToLower(reference.Domain(named))
	path := reference.Path(named)
	if !strings.ContainsAny(host, ".:") && host != "localhost" {
		// Docker treats an upper-case first segment ("MYREG/app") as a
		// host; lower-cased it would silently become a Docker Hub path.
		return Ref{}, fmt.Errorf("%w: registry host %q must contain a dot or a port, or be localhost", ErrInvalid, host)
	}
	if dockerHubAliases[host] {
		// Re-normalize through docker.io so single-segment names get the
		// library/ namespace like every other Docker Hub spelling.
		hub, err := reference.ParseNormalizedNamed(DockerHub + "/" + path)
		if err != nil {
			return Ref{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		host, path = DockerHub, reference.Path(hub)
	}
	r := Ref{Host: host, Repository: path}
	if t, ok := named.(reference.Tagged); ok {
		r.Tag = t.Tag()
	}
	if d, ok := named.(reference.Digested); ok {
		r.Digest = d.Digest().String()
	}
	if r.Tag == "" && r.Digest == "" {
		r.Tag = "latest"
	}
	return r, nil
}

// NormalizeHost normalizes a registry host as entered by a user: scheme,
// path and a trailing slash are dropped, the host is lower-cased and every
// Docker Hub alias becomes docker.io. It returns an error for values that
// are not a host[:port].
func NormalizeHost(s string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(s))
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexByte(h, '/'); i >= 0 {
		h = h[:i]
	}
	if h == "" {
		return "", errors.New("registry host is empty")
	}
	if dockerHubAliases[h] {
		return DockerHub, nil
	}
	// A host is valid when "<host>/x" parses as a reference on that host.
	parsed, err := reference.Parse(h + "/x")
	named, isNamed := parsed.(reference.Named)
	if err != nil || !isNamed || strings.ToLower(reference.Domain(named)) != h || strings.ContainsAny(h, "@") {
		return "", fmt.Errorf("%q is not a registry host (host or host:port)", s)
	}
	if !strings.ContainsAny(h, ".:") && h != "localhost" {
		return "", fmt.Errorf("%q is not a registry host: use a DNS name, an IP address, host:port or localhost", s)
	}
	return h, nil
}

// IsDockerHub reports whether host (normalized) is Docker Hub.
func IsDockerHub(host string) bool { return host == DockerHub }

// APIHost is the host serving the registry API for a normalized host
// (registry-1.docker.io for Docker Hub).
func APIHost(host string) string {
	if IsDockerHub(host) {
		return dockerHubAPI
	}
	return host
}

// ServerAddress is the server address for Engine registry auth
// (X-Registry-Auth) of a normalized host.
func ServerAddress(host string) string {
	if IsDockerHub(host) {
		return DockerHubServerAddress
	}
	return host
}

// Pattern is a repository matcher of a registry connection:
//
//	""            every repository on the host
//	"org/app"     exactly that repository
//	"org/*"       every repository below org/ (any depth)
//
// On Docker Hub, single-segment names get the library/ namespace
// ("nginx" == "library/nginx").
type Pattern string

// ParsePattern validates and normalizes a repository matcher for host.
func ParsePattern(host, s string) (Pattern, error) {
	s = strings.Trim(strings.TrimSpace(s), "/")
	if s == "" {
		return "", nil
	}
	if len(s) > 255 {
		return "", errors.New("repository matcher is too long")
	}
	prefix, wildcard := strings.CutSuffix(s, "/*")
	if wildcard {
		if prefix == "" || strings.Contains(prefix, "*") {
			return "", errors.New(`repository matcher must be "namespace/*" or an exact repository`)
		}
	} else if strings.Contains(s, "*") {
		return "", errors.New(`the only wildcard allowed is a trailing "/*"`)
	}
	parsed, err := reference.Parse(host + "/" + prefix)
	if err != nil {
		return "", fmt.Errorf("invalid repository matcher: %v", err)
	}
	named, ok := parsed.(reference.Named)
	if !ok || reference.Domain(named) != host {
		return "", errors.New("invalid repository matcher")
	}
	if _, tagged := named.(reference.Tagged); tagged {
		return "", errors.New("a repository matcher must not contain a tag")
	}
	if _, digested := named.(reference.Digested); digested {
		return "", errors.New("a repository matcher must not contain a digest")
	}
	path := reference.Path(named)
	if IsDockerHub(host) && !wildcard && !strings.Contains(path, "/") {
		path = "library/" + path
	}
	if wildcard {
		return Pattern(path + "/*"), nil
	}
	return Pattern(path), nil
}

// Match reports whether repository (normalized, as in Ref.Repository)
// matches the pattern, and how specific the match is: 0 for the empty
// pattern, 2×segments for a namespace pattern and 2×segments+1 for an exact
// repository, so an exact repository beats its namespace and a deeper
// namespace beats a shallower one.
func (p Pattern) Match(repository string) (bool, int) {
	if p == "" {
		return true, 0
	}
	s := string(p)
	if prefix, ok := strings.CutSuffix(s, "/*"); ok {
		if strings.HasPrefix(repository, prefix+"/") {
			return true, 2 * (strings.Count(prefix, "/") + 1)
		}
		return false, 0
	}
	if repository == s {
		return true, 2*(strings.Count(s, "/")+1) + 1
	}
	return false, 0
}
