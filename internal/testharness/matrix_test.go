package testharness

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestLoadMatrixIsValid(t *testing.T) {
	m, err := LoadMatrix()
	if err != nil {
		t.Fatal(err)
	}
	def, err := m.Lookup("default")
	if err != nil || def.Version != m.Default {
		t.Fatalf("default lookup = %+v, %v", def, err)
	}
	latest, err := m.Lookup("latest")
	if err != nil || !latest.HasRole(RoleLatest) {
		t.Fatalf("latest lookup = %+v, %v", latest, err)
	}
	minimum, err := m.Lookup("minimum")
	if err != nil || minimum.Version != m.Engines[0].Version {
		t.Fatalf("minimum lookup = %+v, %v", minimum, err)
	}
	if CompareVersions(minimum.APIVersion, "1.40") < 0 {
		t.Errorf("minimum engine API %s is below the Moby client's floor 1.40", minimum.APIVersion)
	}
}

// The matrix is defined once: the workflow reads engines.json instead of
// listing Engine versions itself.
func TestWorkflowReadsMatrixFile(t *testing.T) {
	root, err := RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	wf, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "extended.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wf), MatrixPath) {
		t.Errorf("extended.yaml does not read %s", MatrixPath)
	}
	if hard := regexp.MustCompile(`docker:\d+\.\d+(\.\d+)?-dind`).FindString(string(wf)); hard != "" {
		t.Errorf("extended.yaml hard-codes an Engine image (%s); add it to %s instead", hard, MatrixPath)
	}
}

func TestSelectEngineFromEnv(t *testing.T) {
	m, err := LoadMatrix()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range m.Engines {
		t.Setenv(EnvEngine, e.Version)
		got, err := SelectEngine()
		if err != nil || got.Version != e.Version {
			t.Errorf("SelectEngine with %s=%s = %+v, %v", EnvEngine, e.Version, got, err)
		}
	}
	t.Setenv(EnvEngine, "1.2.3")
	if _, err := SelectEngine(); err == nil || !strings.Contains(err.Error(), "not in") {
		t.Errorf("unknown engine: err = %v", err)
	}
}

func TestMatrixValidateRejects(t *testing.T) {
	const digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	good := func() Matrix {
		return Matrix{Default: "25.0.5", Engines: []EngineVersion{
			{Version: "24.0.9", Image: "docker:24.0.9-dind@" + digest, APIVersion: "1.43", Roles: []string{RoleMinimumCandidate}},
			{Version: "25.0.5", Image: "docker:25.0.5-dind@" + digest, APIVersion: "1.44", Roles: []string{RoleLatest}},
		}}
	}
	if err := good().Validate(); err != nil {
		t.Fatalf("good matrix: %v", err)
	}
	cases := map[string]struct {
		mutate func(*Matrix)
		want   string
	}{
		"unpinned image":    {func(m *Matrix) { m.Engines[0].Image = "docker:24.0.9-dind" }, "must be docker:<version>-dind@sha256"},
		"tag mismatch":      {func(m *Matrix) { m.Engines[0].Image = "docker:24.0.8-dind@" + digest }, "does not match"},
		"bad api":           {func(m *Matrix) { m.Engines[0].APIVersion = "v1.43" }, "apiVersion"},
		"descending":        {func(m *Matrix) { m.Engines[0], m.Engines[1] = m.Engines[1], m.Engines[0] }, "ascending"},
		"duplicate":         {func(m *Matrix) { m.Engines[1] = m.Engines[0]; m.Engines[1].Roles = []string{RoleLatest} }, "duplicate"},
		"missing default":   {func(m *Matrix) { m.Default = "26.0.0" }, "default"},
		"no latest":         {func(m *Matrix) { m.Engines[1].Roles = nil }, "exactly one"},
		"no minimum":        {func(m *Matrix) { m.Engines[0].Roles = nil }, "at least one"},
		"api goes down":     {func(m *Matrix) { m.Engines[1].APIVersion = "1.42" }, "lower than the previous"},
		"version malform":   {func(m *Matrix) { m.Engines[0].Version = "24.0" }, "MAJOR.MINOR.PATCH"},
		"no engines at all": {func(m *Matrix) { m.Engines = nil }, "no engines"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := good()
			tc.mutate(&m)
			err := m.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.43", "1.55", -1},
		{"1.9", "1.10", -1},
		{"24.0.9", "25.0.5", -1},
		{"29.8.1", "29.8.1", 0},
		{"29.8", "29.8.0", 0},
		{"28.5.2", "28.10.0", -1},
		{"1.55", "1.44", 1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if got := MinVersion("1.55", "1.43"); got != "1.43" {
		t.Errorf("MinVersion = %s", got)
	}
}
