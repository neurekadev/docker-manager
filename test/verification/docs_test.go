package verification

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// guideTopics are the #12 user/admin documentation topics and the page of
// docs/guide/ that covers each.
var guideTopics = map[string]string{
	"deployment":                         "deployment.md",
	"first-run setup and first agent":    "first-run.md",
	"multi-host operation":               "multi-host.md",
	"PWA install, update and offline":    "pwa.md",
	"upgrades and migrations":            "upgrades.md",
	"backup and restore":                 "backup-restore.md",
	"policy safety":                      "policy-safety.md",
	"troubleshooting":                    "troubleshooting.md",
	"index linking every topic":          "README.md",
	"security review (#12 checklist)":    "../security/review-v1.md",
	"support matrix (#12 boundary)":      "../support-matrix.md",
	"verification map (#29 release map)": "../testing/verification-matrix.md",
}

var mdLinkRE = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// checkLinks fails for every relative Markdown link of file that does not
// resolve to an existing file or directory.
func checkLinks(t *testing.T, root, file string) {
	t.Helper()
	src := readRepo(t, file)
	for _, m := range mdLinkRE.FindAllStringSubmatch(src, -1) {
		target := m[1]
		if strings.Contains(target, "://") || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "mailto:") {
			continue
		}
		target = strings.SplitN(target, "#", 2)[0]
		rel := path.Clean(path.Join(path.Dir(file), target))
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s links to %s, which does not exist", file, m[1])
		}
	}
}

// TestGuideCoversEveryTopic (#12 V79/V81): the user/admin guide has a page
// per topic, the index and the README link them, and every relative link
// in the guide resolves.
func TestGuideCoversEveryTopic(t *testing.T) {
	root := repoRoot(t)
	index := readRepo(t, "docs/guide/README.md")
	for topic, page := range guideTopics {
		rel := path.Join("docs/guide", page)
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s: no %s", topic, rel)
			continue
		}
		if len(b) < 1500 {
			t.Errorf("%s: %s has only %d bytes", topic, rel, len(b))
		}
		if page != "README.md" && !strings.Contains(index, "("+page+")") {
			t.Errorf("docs/guide/README.md does not link %s (%s)", page, topic)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "docs", "guide"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			checkLinks(t, root, "docs/guide/"+e.Name())
		}
	}
	for _, f := range []string{"README.md", "deploy/README.md", "docs/support-matrix.md", "docs/security/review-v1.md"} {
		checkLinks(t, root, f)
	}
	if readme := readRepo(t, "README.md"); !strings.Contains(readme, "docs/guide/") {
		t.Error("README.md does not point to the guide in docs/guide/")
	}
}

// reviewChecklist is the #12 security checklist; each item needs its own
// section in docs/security/review-v1.md.
var reviewChecklist = []string{
	"Owner bootstrap", "Invite redemption and revocation", "Restricted default group", "Capability and override boundaries",
	"TOTP and passkey enforcement and recovery", "Secret storage", "TLS enforcement", "Docker socket exposure", "Path traversal",
	"CSRF", "WebSocket authorization", "Rate limiting", "Audit coverage", "Dependency vulnerabilities", "Session revocation",
	"API tokens",
}

// TestSecurityReviewCoversChecklist (#12 V82): the v1 security review has a
// section per checklist item, a findings summary and the residual
// limitations.
func TestSecurityReviewCoversChecklist(t *testing.T) {
	src := readRepo(t, "docs/security/review-v1.md")
	headings := map[string]bool{}
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(line, "#") {
			headings[strings.ToLower(strings.TrimSpace(strings.TrimLeft(line, "#")))] = true
		}
	}
	has := func(title string) bool {
		for h := range headings {
			if strings.HasPrefix(h, strings.ToLower(title)) {
				return true
			}
		}
		return false
	}
	for _, item := range append(reviewChecklist, "Findings", "Residual limitations") {
		if !has(item) {
			t.Errorf("docs/security/review-v1.md has no section %q", item)
		}
	}
}
