package testharness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// MatrixPath is the Engine matrix file, relative to the repository root.
const MatrixPath = "test/matrix/engines.json"

// EnvEngine selects the Engine version for Engine-dependent tests.
const EnvEngine = "DOCKYARD_TEST_ENGINE"

// Engine roles used in the matrix file.
const (
	// RoleMinimum marks DockYard's minimum supported Engine (#21, #25 Q2);
	// it must be the lowest entry and match engine.MinSupportedAPIVersion.
	RoleMinimum = "minimum"
	RoleLatest  = "latest"
)

// EngineVersion is one entry of the Engine matrix.
type EngineVersion struct {
	// Version is the Docker Engine version, e.g. "29.8.1".
	Version string `json:"version"`
	// Image is the docker:<version>-dind image pinned by digest.
	Image string `json:"image"`
	// APIVersion is the highest Engine API version the Engine serves.
	APIVersion string   `json:"apiVersion"`
	Roles      []string `json:"roles"`
	Note       string   `json:"note,omitempty"`
}

// HasRole reports whether the entry carries role.
func (e EngineVersion) HasRole(role string) bool { return slices.Contains(e.Roles, role) }

// Matrix is the parsed test/matrix/engines.json.
type Matrix struct {
	Default string          `json:"default"`
	Engines []EngineVersion `json:"engines"`
}

var (
	versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	apiRe     = regexp.MustCompile(`^1\.\d+$`)
	imageRe   = regexp.MustCompile(`^docker:(\d+\.\d+\.\d+)-dind@sha256:[0-9a-f]{64}$`)
)

// Validate checks the invariants the workflow and helpers rely on: unique,
// ascending versions; dind images pinned by digest and matching the version;
// well-formed, non-decreasing API versions; a default that exists; exactly
// one minimum entry (the lowest) and exactly one latest entry.
func (m Matrix) Validate() error {
	if len(m.Engines) == 0 {
		return errors.New("matrix: no engines")
	}
	var errs []error
	seen := map[string]bool{}
	latest, minimum := 0, 0
	for i, e := range m.Engines {
		where := fmt.Sprintf("matrix: engines[%d] (%s)", i, e.Version)
		if !versionRe.MatchString(e.Version) {
			errs = append(errs, fmt.Errorf("%s: version must be MAJOR.MINOR.PATCH", where))
		}
		if seen[e.Version] {
			errs = append(errs, fmt.Errorf("%s: duplicate version", where))
		}
		seen[e.Version] = true
		if mm := imageRe.FindStringSubmatch(e.Image); mm == nil {
			errs = append(errs, fmt.Errorf("%s: image %q must be docker:<version>-dind@sha256:<digest>", where, e.Image))
		} else if mm[1] != e.Version {
			errs = append(errs, fmt.Errorf("%s: image tag %s does not match the version", where, mm[1]))
		}
		if !apiRe.MatchString(e.APIVersion) {
			errs = append(errs, fmt.Errorf("%s: apiVersion %q must look like 1.NN", where, e.APIVersion))
		}
		if i > 0 {
			prev := m.Engines[i-1]
			if CompareVersions(prev.Version, e.Version) >= 0 {
				errs = append(errs, fmt.Errorf("%s: versions must be strictly ascending", where))
			}
			if CompareVersions(prev.APIVersion, e.APIVersion) > 0 {
				errs = append(errs, fmt.Errorf("%s: apiVersion lower than the previous entry", where))
			}
		}
		if e.HasRole(RoleLatest) {
			latest++
		}
		if e.HasRole(RoleMinimum) {
			minimum++
			if i != 0 {
				errs = append(errs, fmt.Errorf("%s: the %q engine must be the lowest entry", where, RoleMinimum))
			}
		}
	}
	if latest != 1 {
		errs = append(errs, fmt.Errorf("matrix: want exactly one %q engine, got %d", RoleLatest, latest))
	}
	if minimum != 1 {
		errs = append(errs, fmt.Errorf("matrix: want exactly one %q engine, got %d", RoleMinimum, minimum))
	}
	if !seen[m.Default] {
		errs = append(errs, fmt.Errorf("matrix: default %q is not in engines", m.Default))
	}
	return errors.Join(errs...)
}

// Lookup resolves a version or one of the aliases "default" (or empty),
// "minimum" (the lowest entry) and "latest".
func (m Matrix) Lookup(name string) (EngineVersion, error) {
	name = strings.TrimSpace(name)
	switch name {
	case "", "default":
		name = m.Default
	case "minimum":
		if len(m.Engines) > 0 {
			return m.Engines[0], nil
		}
	case "latest":
		for _, e := range m.Engines {
			if e.HasRole(RoleLatest) {
				return e, nil
			}
		}
	}
	for _, e := range m.Engines {
		if e.Version == name {
			return e, nil
		}
	}
	versions := make([]string, 0, len(m.Engines))
	for _, e := range m.Engines {
		versions = append(versions, e.Version)
	}
	return EngineVersion{}, fmt.Errorf("engine %q is not in %s (have %s)", name, MatrixPath, strings.Join(versions, ", "))
}

// ParseMatrix parses and validates matrix JSON.
func ParseMatrix(data []byte) (Matrix, error) {
	var m Matrix
	if err := json.Unmarshal(data, &m); err != nil {
		return Matrix{}, fmt.Errorf("parse %s: %w", MatrixPath, err)
	}
	if err := m.Validate(); err != nil {
		return Matrix{}, err
	}
	return m, nil
}

// LoadMatrix reads test/matrix/engines.json from the repository root.
func LoadMatrix() (Matrix, error) {
	root, err := RepoRoot()
	if err != nil {
		return Matrix{}, err
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(MatrixPath)))
	if err != nil {
		return Matrix{}, err
	}
	return ParseMatrix(data)
}

// SelectEngine returns the matrix entry named by DOCKYARD_TEST_ENGINE, or
// the matrix default.
func SelectEngine() (EngineVersion, error) {
	m, err := LoadMatrix()
	if err != nil {
		return EngineVersion{}, err
	}
	return m.Lookup(os.Getenv(EnvEngine))
}

// RepoRoot walks up from the working directory to the directory holding
// DockYard's go.mod. `go test` runs with the package directory as working
// directory, so this finds the checkout under test.
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.HasPrefix(strings.ReplaceAll(string(data), "\r\n", "\n"), "module github.com/neurekadev/dockyard\n") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("repository root (go.mod of github.com/neurekadev/dockyard) not found above the working directory")
		}
		dir = parent
	}
}

// CompareVersions compares dotted numeric versions ("1.43" < "1.55",
// "24.0.9" < "25.0.5"). Missing components count as zero.
func CompareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// MinVersion returns the lower of two dotted versions.
func MinVersion(a, b string) string {
	if CompareVersions(a, b) <= 0 {
		return a
	}
	return b
}
