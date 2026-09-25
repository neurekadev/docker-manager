package imageref

import (
	"strings"

	"github.com/distribution/reference"
)

// Helpers for digest-driven updates (#20).

// ExplicitTag reports whether s names a tag itself ("app:1.2"), rather
// than implying "latest" ("app") or naming only a digest ("app@sha256:").
func ExplicitTag(s string) bool {
	named, err := reference.ParseNormalizedNamed(strings.TrimSpace(s))
	if err != nil {
		return false
	}
	_, ok := named.(reference.Tagged)
	return ok
}

// DigestFor picks the repository digest of ref from an image's
// RepoDigests ("repo@sha256:..."): the digest pinned in ref itself, or the
// entry whose repository matches ref's (Docker Hub spellings and the
// library/ namespace compare equal). "" when none matches.
func DigestFor(ref string, repoDigests []string) string {
	r, err := Parse(ref)
	if err != nil {
		return ""
	}
	if r.Digest != "" {
		return r.Digest
	}
	for _, rd := range repoDigests {
		d, err := Parse(rd)
		if err != nil || d.Digest == "" {
			continue
		}
		if d.Name() == r.Name() {
			return d.Digest
		}
	}
	return ""
}

// DigestsFor returns every repository digest of ref's repository in an
// image's RepoDigests (an image pulled through several index digests that
// share its platform manifest carries several), in order.
func DigestsFor(ref string, repoDigests []string) []string {
	r, err := Parse(ref)
	if err != nil {
		return nil
	}
	var out []string
	for _, rd := range repoDigests {
		d, err := Parse(rd)
		if err == nil && d.Digest != "" && d.Name() == r.Name() {
			out = append(out, d.Digest)
		}
	}
	return out
}

// SameTag reports whether two tagged references name the same repository
// and tag after normalization ("nginx:1" and "docker.io/library/nginx:1").
// Digest-pinned references never match.
func SameTag(a, b string) bool {
	ra, err := Parse(a)
	if err != nil || ra.Digest != "" {
		return false
	}
	rb, err := Parse(b)
	if err != nil || rb.Digest != "" {
		return false
	}
	return ra.Name() == rb.Name() && ra.Tag == rb.Tag
}

// VersionTag reports whether a tag reads like a version ("1", "1.25.3",
// "v2.0.1-alpine", "2024.06.1") rather than a moving name such as
// "latest", "main" or "stable". Both are eligible for digest updates
// (#25); non-version tags are shown with a warning because what they
// point to can change meaning.
func VersionTag(tag string) bool {
	t := strings.ToLower(tag)
	t = strings.TrimPrefix(t, "v")
	if t == "" || t[0] < '0' || t[0] > '9' {
		return false
	}
	for _, c := range t {
		switch {
		case c >= '0' && c <= '9', c == '.':
		case c == '-' || c == '_' || c == '+':
			return true // a suffix after the version ("1.2-alpine")
		default:
			return false
		}
	}
	return true
}
