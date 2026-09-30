// Package buildinfo exposes the version metadata stamped into both Docker Manager
// executables at link time.
//
// Release builds set the variables with -ldflags, for example:
//
//	-X github.com/neurekadev/docker-manager/internal/buildinfo.Version=0.0.0-edge
//	-X github.com/neurekadev/docker-manager/internal/buildinfo.Commit=<git sha>
//	-X github.com/neurekadev/docker-manager/internal/buildinfo.Date=<RFC 3339 commit time>
//
// Local builds fall back to the VCS information recorded by the Go toolchain.
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

// DefaultVersion is the version reported by builds that do not set one.
// Docker Manager publishes only rolling `edge` images; there are no semver releases yet.
const DefaultVersion = "0.0.0-edge"

// Set with -ldflags "-X". They are variables (not constants) for that reason.
var (
	Version = DefaultVersion
	Commit  = ""
	Date    = ""
)

// Info is the resolved build metadata.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"goVersion"`
}

// Get returns the build metadata, filling gaps from debug.ReadBuildInfo.
func Get() Info {
	info := Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
	}
	if info.Version == "" {
		info.Version = DefaultVersion
	}
	if info.Commit == "" || info.Date == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					if info.Commit == "" {
						info.Commit = s.Value
					}
				case "vcs.time":
					if info.Date == "" {
						info.Date = s.Value
					}
				}
			}
		}
	}
	if info.Commit == "" {
		info.Commit = "unknown"
	}
	if info.Date == "" {
		info.Date = "unknown"
	}
	return info
}

// String renders the metadata for `version` subcommands.
func (i Info) String() string {
	return i.Version + " (commit " + i.Commit + ", built " + i.Date + ", " + i.GoVersion + ")"
}
