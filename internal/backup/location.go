// Package backup holds what the manager and the agents share about DockYard
// backups (#10, #24): the layout of physical restic repositories under a
// backup repository (destination), snapshot tags, the portable backup-set
// manifest stored inside the repositories, and the retention algorithm.
// It has no database, HTTP or Docker dependencies.
package backup

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"runtime"
	"strings"

	"github.com/neurekadev/dockyard/internal/restic"
)

// A DockYard backup repository is a destination: a local directory on one
// executor's persistent mount, or an S3 bucket/prefix. Below it, every
// scope has its own restic repository so ownership, locking and retention
// stay separate (#10): the manager's state in "dockyard-manager", each
// environment's stack and volume data in "dockyard-env-<environmentId>".

// ScopeManager is the scope of manager-state snapshots.
const ScopeManager = "manager"

const envScopePrefix = "env:"

// EnvironmentScope is the scope of an environment's data.
func EnvironmentScope(environmentID string) string { return envScopePrefix + environmentID }

// ScopeEnvironment returns the environment of an environment scope.
func ScopeEnvironment(scope string) (string, bool) {
	id, ok := strings.CutPrefix(scope, envScopePrefix)
	return id, ok && id != ""
}

var scopeIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// ValidScope reports whether scope is the manager scope or a well-formed
// environment scope.
func ValidScope(scope string) bool {
	if scope == ScopeManager {
		return true
	}
	id, ok := ScopeEnvironment(scope)
	return ok && scopeIDRE.MatchString(id)
}

// ScopeDir is the directory (or key prefix) of a scope's restic repository.
func ScopeDir(scope string) string {
	if id, ok := ScopeEnvironment(scope); ok {
		return "dockyard-env-" + id
	}
	return "dockyard-manager"
}

// ScopeOfDir maps a scope directory back to its scope ("" when it is not
// one).
func ScopeOfDir(dir string) string {
	if dir == "dockyard-manager" {
		return ScopeManager
	}
	if id, ok := strings.CutPrefix(dir, "dockyard-env-"); ok && scopeIDRE.MatchString(id) {
		return EnvironmentScope(id)
	}
	return ""
}

// Destination kinds.
const (
	KindLocal = "local"
	KindS3    = "s3"
)

// Destination is where a backup repository keeps its restic repositories.
// It never holds credentials.
type Destination struct {
	Kind string `json:"kind"`
	// Path is the absolute directory of a local destination (on the
	// executor that owns it).
	Path string `json:"path,omitempty"`
	// S3: Endpoint is scheme://host[:port]; Prefix may be empty.
	Endpoint  string `json:"endpoint,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	Region    string `json:"region,omitempty"`
	PathStyle bool   `json:"pathStyle,omitempty"`
}

var bucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// AbsPath reports whether p is a clean, slash-separated absolute path
// other than the root. DockYard's executors are Linux; on Windows (tests
// and development only) a drive-absolute path like C:/x is accepted too.
func AbsPath(p string) bool {
	if p == "" || strings.Contains(p, "\\") || path.Clean(p) != p || p == "/" {
		return false
	}
	if path.IsAbs(p) {
		return true
	}
	return runtime.GOOS == "windows" && len(p) > 3 && p[1] == ':' && p[2] == '/'
}

// Validate checks the destination's form (not its reachability).
func (d Destination) Validate() error {
	switch d.Kind {
	case KindLocal:
		if !AbsPath(d.Path) {
			return errors.New("path must be a clean absolute directory other than /")
		}
		if d.Endpoint != "" || d.Bucket != "" || d.Prefix != "" {
			return errors.New("a local repository has no S3 settings")
		}
	case KindS3:
		u, err := url.Parse(d.Endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || (u.Path != "" && u.Path != "/") ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("endpoint must be an origin like https://s3.example.com (no path, credentials or query)")
		}
		if !bucketRE.MatchString(d.Bucket) {
			return errors.New("bucket must be a valid S3 bucket name")
		}
		if d.Prefix != "" {
			if strings.HasPrefix(d.Prefix, "/") || strings.HasSuffix(d.Prefix, "/") || path.Clean(d.Prefix) != d.Prefix ||
				strings.Contains(d.Prefix, "..") || strings.ContainsAny(d.Prefix, "\\?#") || len(d.Prefix) > 512 {
				return errors.New("prefix must be a relative key prefix like backups/dockyard (no leading or trailing slash)")
			}
		}
		if len(d.Region) > 64 {
			return errors.New("region too long")
		}
		if d.Path != "" {
			return errors.New("an S3 repository has no local path")
		}
	default:
		return fmt.Errorf("unknown repository kind %q", d.Kind)
	}
	return nil
}

// Repository returns the restic repository string of a scope.
func (d Destination) Repository(scope string) string {
	dir := ScopeDir(scope)
	if d.Kind == KindS3 {
		key := dir
		if d.Prefix != "" {
			key = d.Prefix + "/" + dir
		}
		return "s3:" + strings.TrimSuffix(d.Endpoint, "/") + "/" + d.Bucket + "/" + key
	}
	return path.Join(d.Path, dir)
}

// Base describes the destination without credentials (for display and
// manifests).
func (d Destination) Base() string {
	if d.Kind == KindS3 {
		b := strings.TrimSuffix(d.Endpoint, "/") + "/" + d.Bucket
		if d.Prefix != "" {
			b += "/" + d.Prefix
		}
		return b
	}
	return d.Path
}

// S3Credentials are the S3 access key pair of a destination.
type S3Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
}

// Location returns the restic location of a scope with the credentials.
func (d Destination) Location(scope string, creds S3Credentials) restic.Location {
	loc := restic.Location{Repository: d.Repository(scope)}
	if d.Kind == KindS3 {
		loc.S3 = &restic.S3{AccessKeyID: creds.AccessKeyID, SecretAccessKey: creds.SecretAccessKey, Region: d.Region, PathStyle: d.PathStyle}
	}
	return loc
}

// Within reports whether p equals or lies below dir (both clean, slash
// separated absolute paths).
func Within(p, dir string) bool {
	if dir == "/" {
		return strings.HasPrefix(p, "/")
	}
	return p == dir || strings.HasPrefix(p, dir+"/")
}
