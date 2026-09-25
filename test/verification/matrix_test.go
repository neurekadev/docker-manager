package verification

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const matrixPath = "docs/testing/verification-matrix.md"

// The statuses a row may combine (joined with " + ").
const (
	statusRanLocally = "automated — ran locally"
	statusWritten    = "automated — written, not yet executed (CI unavailable)"
	statusManual     = "manual"
)

// matrixRow is one verification item of the matrix.
type matrixRow struct {
	ID, Item, Owner, Evidence, Where string
	Statuses                         []string
	Line                             int
}

func (r matrixRow) has(status string) bool { return slices.Contains(r.Statuses, status) }

var rowIDRE = regexp.MustCompile(`^V\d{2}m?$`)

func parseMatrix(t *testing.T, src string) []matrixRow {
	t.Helper()
	var rows []matrixRow
	for i, line := range strings.Split(src, "\n") {
		if !strings.HasPrefix(line, "| V") {
			continue
		}
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) != 6 {
			t.Errorf("%s:%d: %d cells, want 6 (# | item | owner | evidence | where | status)", matrixPath, i+1, len(cells))
			continue
		}
		for k := range cells {
			cells[k] = strings.TrimSpace(cells[k])
		}
		r := matrixRow{ID: cells[0], Item: cells[1], Owner: cells[2], Evidence: cells[3], Where: cells[4], Line: i + 1}
		for _, s := range strings.Split(cells[5], " + ") {
			r.Statuses = append(r.Statuses, strings.TrimSpace(s))
		}
		rows = append(rows, r)
	}
	return rows
}

// section returns the bullet list text below a "### title" heading.
func section(src, title string) string {
	i := strings.Index(src, "### "+title)
	if i < 0 {
		return ""
	}
	rest := src[i+len("### "+title):]
	if j := strings.Index(rest, "\n#"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

var (
	tokenRE    = regexp.MustCompile("`([^`]+)`|\"([^\"]+)\"")
	goTestRE   = regexp.MustCompile(`^(?:Test|Fuzz|Benchmark)[A-Za-z0-9_]*(?:/.*)?$`)
	whereJobRE = regexp.MustCompile("X:`([a-z0-9-]+)`")
	repoDirs   = []string{"internal", "test", "e2e", "web", "scripts", "docs", "deploy", "cmd", "api", ".github"}
)

// matrixContext is what rows are checked against.
type matrixContext struct {
	root          string
	tests         map[string][]goTest
	files         map[string][]string
	ranLocally    map[string]bool // tagged tests that ran on the development host
	skippedOnWin  map[string]bool // untagged tests skipped on Windows
	linuxPending  map[string]bool // ran on Windows; their Linux-specific part is pending
	smoke         string
	devstackSpecs map[string]bool // Playwright specs the devstack runner runs
	devstackGroup map[string]bool
	jobs          map[string]bool // ci.yaml and extended.yaml
	extendedJobs  map[string]bool
}

func newMatrixContext(t *testing.T, src string) *matrixContext {
	t.Helper()
	tests, files := index(t)
	c := &matrixContext{root: repoRoot(t), tests: tests, files: files, ranLocally: map[string]bool{}, skippedOnWin: map[string]bool{},
		linuxPending: map[string]bool{},
		smoke:        readRepo(t, "scripts/smoke/deploy-smoke.sh"), devstackSpecs: map[string]bool{}, devstackGroup: map[string]bool{},
		jobs: map[string]bool{}, extendedJobs: map[string]bool{}}
	for title, set := range map[string]map[string]bool{"Tagged tests that ran locally": c.ranLocally,
		"Skipped on the Windows development host": c.skippedOnWin, "Linux results pending": c.linuxPending} {
		body := section(src, title)
		if body == "" {
			t.Fatalf("%s: no %q section", matrixPath, title)
		}
		for _, m := range tokenRE.FindAllStringSubmatch(body, -1) {
			if goTestRE.MatchString(m[1]) {
				set[m[1]] = true
				if _, ok := tests[m[1]]; !ok {
					t.Errorf("%s: %q lists %s, which is not a test in the repository", matrixPath, title, m[1])
				}
			}
		}
	}
	for name := range c.ranLocally {
		for _, g := range tests[name] {
			if !g.needsTags() {
				t.Errorf("%s: %s is listed as a tagged test that ran locally but has no build tag", matrixPath, name)
			}
		}
	}
	// The devstack runner's groups and the specs they run.
	script := readRepo(t, "scripts/ci/e2e-devstack.sh")
	m := regexp.MustCompile(`(?m)^all_groups=\(([^)]*)\)`).FindStringSubmatch(script)
	if m == nil {
		t.Fatal("scripts/ci/e2e-devstack.sh: no all_groups=(...)")
	}
	for _, g := range strings.Fields(m[1]) {
		c.devstackGroup[g] = true
		switch g {
		case "ui-setup":
			c.devstackSpecs["ui.spec.ts"] = true
		case "import":
			c.devstackSpecs["admin-automation.spec.ts"] = true
		default:
			c.devstackSpecs[g+".spec.ts"] = true
		}
	}
	for spec := range c.devstackSpecs {
		if _, err := os.Stat(filepath.Join(c.root, "e2e", "tests", spec)); err != nil {
			t.Errorf("scripts/ci/e2e-devstack.sh runs %s, which does not exist", spec)
		}
	}
	for _, wf := range []string{"ci.yaml", "extended.yaml"} {
		for job := range loadWorkflow(t, wf).Jobs {
			c.jobs[job] = true
			if wf == "extended.yaml" {
				c.extendedJobs[job] = true
			}
		}
	}
	return c
}

// rowFacts is what the evidence of a row refers to.
type rowFacts struct {
	refs       int      // test references of any kind
	local      int      // references that run on the Windows development host without Docker
	needsCI    []string // references that cannot (Docker, Linux, published images, TLS proxies)
	manualText bool
}

// checkEvidence resolves every token of a row's evidence.
func (c *matrixContext) checkEvidence(r matrixRow) (rowFacts, []error) {
	var errs []error
	fail := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("%s (line %d): "+format, append([]any{r.ID, r.Line}, a...)...))
	}
	f := rowFacts{manualText: strings.Contains(r.Evidence, "**Manual**")}
	var spec string // the Playwright spec that quoted titles belong to
	var specSrc string
	for _, m := range tokenRE.FindAllStringSubmatch(r.Evidence, -1) {
		if m[2] != "" { // a quoted test title
			if spec == "" {
				fail("quoted text %q does not follow a Playwright spec", m[2])
				continue
			}
			title := strings.TrimSuffix(m[2], "…")
			if !strings.Contains(specSrc, title) {
				fail("%s has no test titled %q", spec, m[2])
			}
			continue
		}
		tok := m[1]
		spec = ""
		switch {
		case goTestRE.MatchString(tok):
			name := strings.SplitN(tok, "/", 2)[0]
			gs, ok := c.tests[name]
			if !ok {
				fail("no Go test %s in the repository", name)
				continue
			}
			f.refs++
			// A name defined in several packages counts as tagged only if
			// every definition is.
			tagged := !slices.ContainsFunc(gs, func(g goTest) bool { return !g.dockerTagged() })
			switch {
			case c.skippedOnWin[name]:
				f.needsCI = append(f.needsCI, name+" (skipped on Windows)")
			case c.linuxPending[name]:
				f.local++
				f.needsCI = append(f.needsCI, name+" (Linux part)")
			case tagged && !c.ranLocally[name]:
				f.needsCI = append(f.needsCI, name+" (Docker)")
			default:
				f.local++
			}
		case strings.HasSuffix(tok, ".spec.ts") || strings.HasSuffix(tok, ".test.ts"):
			path := tok
			if !strings.Contains(tok, "/") {
				// A bare name is the Playwright spec when there is one,
				// else the unique Vitest file of that name.
				paths := c.files[tok]
				var e2e []string
				for _, p := range paths {
					if strings.HasPrefix(p, "e2e/tests/") {
						e2e = append(e2e, p)
					}
				}
				switch {
				case len(e2e) == 1:
					path = e2e[0]
				case len(paths) == 1:
					path = paths[0]
				case len(paths) == 0:
					fail("no Playwright or Vitest file %s", tok)
					continue
				default:
					fail("%s is ambiguous (%s): give its path", tok, strings.Join(paths, ", "))
					continue
				}
			}
			b, err := os.ReadFile(filepath.Join(c.root, filepath.FromSlash(path)))
			if err != nil {
				fail("%s: %v", tok, err)
				continue
			}
			f.refs++
			if strings.HasPrefix(path, "e2e/tests/") {
				spec, specSrc = path, string(b)
				if c.devstackSpecs[filepath.Base(path)] {
					f.local++
				} else {
					f.needsCI = append(f.needsCI, path+" (TLS proxy or real Engine stack)")
				}
			} else {
				f.local++ // Vitest runs in the web check
			}
		case strings.HasPrefix(tok, "smoke:"):
			step := strings.TrimPrefix(tok, "smoke:")
			if !strings.Contains(c.smoke, "step_"+strings.ReplaceAll(step, "-", "_")+"() {") {
				fail("scripts/smoke/deploy-smoke.sh has no step %s", step)
				continue
			}
			f.refs++
			f.needsCI = append(f.needsCI, tok+" (published images)")
		case strings.HasPrefix(tok, "devstack:"):
			g := strings.TrimPrefix(tok, "devstack:")
			if g != "*" && !c.devstackGroup[g] {
				fail("scripts/ci/e2e-devstack.sh has no group %s", g)
				continue
			}
			f.refs++
			f.local++
		case strings.HasPrefix(tok, "job:"):
			job := strings.TrimPrefix(tok, "job:")
			if !c.jobs[job] {
				fail("no workflow job %s in ci.yaml or extended.yaml", job)
				continue
			}
			f.refs++
			if job == "images" {
				f.needsCI = append(f.needsCI, tok+" (Docker)")
			} else {
				f.local++
			}
		case strings.Contains(tok, "/") && !strings.Contains(tok, " ") && slices.Contains(repoDirs, strings.SplitN(tok, "/", 2)[0]):
			if _, err := os.Stat(filepath.Join(c.root, filepath.FromSlash(tok))); err != nil {
				fail("path %s does not exist", tok)
			}
		}
	}
	return f, errs
}

// TestVerificationMatrix (#29 Done-when 1): every #12 verification item
// maps to existing automated tests or a documented manual procedure with
// a reason, and its status matches what those tests need to run.
func TestVerificationMatrix(t *testing.T) {
	src := readRepo(t, matrixPath)
	rows := parseMatrix(t, src)
	c := newMatrixContext(t, src)

	seen := map[string]bool{}
	for _, r := range rows {
		if !rowIDRE.MatchString(r.ID) {
			t.Errorf("line %d: row ID %q", r.Line, r.ID)
		}
		if seen[r.ID] {
			t.Errorf("%s appears twice", r.ID)
		}
		seen[r.ID] = true
		if r.Item == "" || r.Owner == "" || r.Where == "" {
			t.Errorf("%s: empty item, owner or where", r.ID)
		}
		for _, s := range r.Statuses {
			if s != statusRanLocally && s != statusWritten && s != statusManual {
				t.Errorf("%s: status %q (allowed: %q, %q, %q)", r.ID, s, statusRanLocally, statusWritten, statusManual)
			}
		}
		for _, m := range whereJobRE.FindAllStringSubmatch(r.Where, -1) {
			if !c.extendedJobs[m[1]] {
				t.Errorf("%s: where names X:%s, which is not a job of extended.yaml", r.ID, m[1])
			}
		}
		f, errs := c.checkEvidence(r)
		for _, err := range errs {
			t.Error(err)
		}
		automated := r.has(statusRanLocally) || r.has(statusWritten)
		switch {
		case automated && f.refs == 0:
			t.Errorf("%s: status %v but the evidence names no test", r.ID, r.Statuses)
		case !automated && !r.has(statusManual):
			t.Errorf("%s: no status", r.ID)
		}
		if r.has(statusManual) != f.manualText {
			t.Errorf("%s: a manual status needs a **Manual** procedure in the evidence, and a procedure needs the manual status", r.ID)
		}
		if f.manualText && !strings.Contains(r.Evidence, "Reason:") {
			t.Errorf("%s: the manual procedure gives no Reason:", r.ID)
		}
		if len(f.needsCI) > 0 && !r.has(statusWritten) {
			t.Errorf("%s: %s cannot run on the development host without Docker; add status %q", r.ID, strings.Join(f.needsCI, ", "), statusWritten)
		}
		if r.has(statusWritten) && len(f.needsCI) == 0 {
			t.Errorf("%s: status %q but every named test runs locally", r.ID, statusWritten)
		}
		if r.has(statusRanLocally) && f.local == 0 {
			t.Errorf("%s: status %q but no named test runs locally", r.ID, statusRanLocally)
		}
	}
	for i := 1; i <= 84; i++ {
		if id := fmt.Sprintf("V%02d", i); !seen[id] {
			t.Errorf("%s (a #12 verification item) is missing from the matrix", id)
		}
	}
}
