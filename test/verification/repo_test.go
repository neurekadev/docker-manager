package verification

import (
	"go/build/constraint"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

// repoRoot is the repository root (two levels above this file).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func readRepo(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// goTest is a test function of the repository.
type goTest struct {
	Name string
	// Dir is the package directory, slash-separated and relative to the
	// repository root (e.g. internal/manager/app).
	Dir string
	// Constraint is the file's //go:build expression (nil: none).
	Constraint constraint.Expr
}

// needsTags reports whether the test only builds with custom tags
// (integration, e2e, faultinject): a plain `go test ./...` on Linux
// (the PR suite) does not compile it.
func (g goTest) needsTags() bool {
	return g.Constraint != nil && !g.Constraint.Eval(func(tag string) bool { return platformTag(tag) })
}

// dockerTagged reports whether the test needs the integration or e2e
// tag (a Docker Engine, the published images or the proxy stack).
func (g goTest) dockerTagged() bool {
	return g.needsTags() && !g.Constraint.Eval(func(tag string) bool { return platformTag(tag) || tag == "faultinject" })
}

func platformTag(tag string) bool {
	switch tag {
	case "linux", "unix", "amd64", "gc":
		return true
	}
	return false
}

var (
	testFuncRE = regexp.MustCompile(`(?m)^func ((?:Test|Fuzz|Benchmark)[A-Za-z0-9_]*)\(`)
	indexOnce  sync.Once
	testIndex  map[string][]goTest
	fileIndex  map[string][]string // base name -> repo-relative paths of spec/test sources
)

// index scans every *_test.go file (Go test functions) and every
// Playwright/Vitest source (by base name) once.
func index(t *testing.T) (map[string][]goTest, map[string][]string) {
	t.Helper()
	root := repoRoot(t)
	indexOnce.Do(func() {
		testIndex = map[string][]goTest{}
		fileIndex = map[string][]string{}
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(strings.TrimPrefix(strings.TrimPrefix(p, root), string(filepath.Separator)))
			if d.IsDir() {
				switch d.Name() {
				case "node_modules", ".git", ".svelte-kit", "build", "test-results", "playwright-report", ".claude":
					if rel != "" {
						return filepath.SkipDir
					}
				}
				return nil
			}
			name := d.Name()
			switch {
			case strings.HasSuffix(name, "_test.go"):
				b, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				src := string(b)
				var expr constraint.Expr
				for _, line := range strings.Split(src, "\n") {
					if constraint.IsGoBuild(line) {
						if expr, err = constraint.Parse(line); err != nil {
							return err
						}
						break
					}
					if strings.HasPrefix(line, "package ") {
						break
					}
				}
				for _, m := range testFuncRE.FindAllStringSubmatch(src, -1) {
					testIndex[m[1]] = append(testIndex[m[1]], goTest{Name: m[1], Dir: filepath.ToSlash(filepath.Dir(rel)), Constraint: expr})
				}
			case strings.HasSuffix(name, ".spec.ts") || strings.HasSuffix(name, ".test.ts"):
				fileIndex[name] = append(fileIndex[name], rel)
			}
			return nil
		})
	})
	if len(testIndex) == 0 {
		t.Fatal("no Go tests found below the repository root")
	}
	return testIndex, fileIndex
}

// workflow is the part of a GitHub Actions workflow the checks read.
type workflow struct {
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	Steps []workflowStep `yaml:"steps"`
}

type workflowStep struct {
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
}

func parseConstraint(line string) (constraint.Expr, error) { return constraint.Parse(line) }

func loadWorkflow(t *testing.T, name string) workflow {
	t.Helper()
	var w workflow
	if err := yaml.Unmarshal([]byte(readRepo(t, ".github/workflows/"+name)), &w); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(w.Jobs) == 0 {
		t.Fatalf("%s: no jobs", name)
	}
	return w
}

// goTestRun is one `go test` invocation of a workflow step.
type goTestRun struct {
	Job      string
	Tags     []string
	Run      *regexp.Regexp // nil: every test
	Packages []string       // ./..., ./internal/x/..., ./internal/x/
}

var (
	tagsFlagRE = regexp.MustCompile(`-tags[ =](\S+)`)
	runFlagRE  = regexp.MustCompile(`-run[ =]('[^']*'|"[^"]*"|\S+)`)
	pkgArgRE   = regexp.MustCompile(`(?:^|\s)(\./\S*)`)
)

// goTestRuns extracts the literal `go test` invocations of every job
// (line continuations joined). Invocations whose packages come from shell
// variables are kept with their literal packages only.
func goTestRuns(t *testing.T, w workflow) []goTestRun {
	t.Helper()
	var out []goTestRun
	for job, j := range w.Jobs {
		for _, s := range j.Steps {
			script := strings.ReplaceAll(s.Run, "\\\n", " ")
			for _, line := range strings.Split(script, "\n") {
				i := strings.Index(line, "go test ")
				if i < 0 {
					continue
				}
				cmd := line[i:]
				r := goTestRun{Job: job}
				// The -run value may be quoted and contain '|': read it
				// first, then cut the command at the first pipe or list
				// operator.
				if loc := runFlagRE.FindStringSubmatchIndex(cmd); loc != nil {
					val := cmd[loc[2]:loc[3]]
					re, err := regexp.Compile(strings.Trim(val, `'"`))
					if err != nil {
						t.Fatalf("%s: -run %s: %v", job, val, err)
					}
					r.Run = re
					cmd = cmd[:loc[0]] + cmd[loc[1]:]
				}
				if k := strings.IndexAny(cmd, "|;&"); k >= 0 {
					cmd = cmd[:k]
				}
				if m := tagsFlagRE.FindStringSubmatch(cmd); m != nil {
					r.Tags = strings.Split(m[1], ",")
				}
				for _, m := range pkgArgRE.FindAllStringSubmatch(cmd, -1) {
					r.Packages = append(r.Packages, m[1])
				}
				out = append(out, r)
			}
		}
	}
	return out
}

// covers reports whether the invocation compiles and selects test g.
func (r goTestRun) covers(g goTest) bool {
	if g.Constraint != nil && !g.Constraint.Eval(func(tag string) bool { return platformTag(tag) || slices.Contains(r.Tags, tag) }) {
		return false
	}
	if r.Run != nil && !r.Run.MatchString(g.Name) {
		return false
	}
	for _, p := range r.Packages {
		p = strings.TrimSuffix(strings.TrimPrefix(p, "./"), "/")
		switch {
		case p == "..." || p == "":
			return p == "..."
		case strings.HasSuffix(p, "/..."):
			base := strings.TrimSuffix(p, "/...")
			if g.Dir == base || strings.HasPrefix(g.Dir, base+"/") {
				return true
			}
		case g.Dir == p:
			return true
		}
	}
	return false
}
