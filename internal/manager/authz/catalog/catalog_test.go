package catalog_test

import (
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
)

// TestEveryJobKindHasCatalogCapabilities: every capability a job kind can
// require is a grantable (or owner-only) catalog key (#17/#29: a job kind
// without a catalog capability fails CI).
func TestEveryJobKindHasCatalogCapabilities(t *testing.T) {
	cat := catalog.Default()
	for _, s := range jobspec.Catalog() {
		caps := s.PossibleCapabilities()
		if len(caps) == 0 {
			t.Errorf("%s declares no capability", s.Kind)
		}
		for _, c := range caps {
			if !cat.Has(c) {
				t.Errorf("job kind %s needs %s, which is not in the permission catalog", s.Kind, c)
			}
		}
	}
}

// TestFileJobsUseRootScopedKeys: file jobs resolve per-root keys, so a
// stack grant never opens a volume (and the selector of files.metadata
// matches the file routes' stack/volume.files.{change} selectors).
func TestFileJobsUseRootScopedKeys(t *testing.T) {
	cat := catalog.Default()
	for _, s := range jobspec.Catalog() {
		if !strings.HasPrefix(string(s.Kind), "files.") {
			continue
		}
		for _, c := range s.PossibleCapabilities() {
			if !strings.HasPrefix(c, "stack.files.") && !strings.HasPrefix(c, "volume.files.") {
				t.Errorf("%s: %s is not root-scoped", s.Kind, c)
			}
			cp, _ := cat.Lookup(c)
			if !cp.AllowsScope("resource", strings.SplitN(c, ".", 2)[0]) {
				t.Errorf("%s cannot be granted on a single root", c)
			}
		}
	}
}

// TestHostAccessCapabilitiesAreHighRisk (#12 security review): every
// capability that lets its holder run code in containers, write what a
// container runs with, or give a container host paths is marked high risk,
// so editors and previews show it distinctly. Docker socket access on the
// host is equivalent to root there.
func TestHostAccessCapabilitiesAreHighRisk(t *testing.T) {
	cat := catalog.Default()
	for _, key := range []string{
		"stack.create",             // writes a whole Compose definition (binds, privileged)
		"stack.definition.write",   // edits it
		"stack.files.write",        // writes files next to it (bind sources, env files)
		"stack.files.extract",      // unpacks archives there
		"container.create",         // standalone containers with bind mounts
		"container.exec",           // a shell inside a container
		"volume.files.write",       // data other containers run with
		"backup.restore",           // overwrites stacks and volumes
		"agent.enroll",             // adds hosts
		"backup_repository.manage", // destinations the Recovery Key opens
	} {
		cp, ok := cat.Lookup(key)
		if !ok {
			t.Errorf("%s is not in the catalog", key)
			continue
		}
		if cp.Risk != catalog.RiskHigh {
			t.Errorf("%s has risk %q, want high", key, cp.Risk)
		}
	}
}

func TestAuditCapabilitiesMatchCatalog(t *testing.T) {
	cat := catalog.Default()
	for _, a := range audit.Capabilities() {
		cp, ok := cat.Lookup(a.Key)
		if !ok || cp.Risk != catalog.RiskHigh || !cp.Instance || cp.Environment || len(cp.Resources) > 0 {
			t.Errorf("audit capability %s: catalog entry %+v", a.Key, cp)
		}
	}
}

func TestCatalogShape(t *testing.T) {
	cat := catalog.Default()
	if cat.Version() != catalog.Version || len(cat.Keys()) < 100 {
		t.Fatalf("catalog v%d with %d keys", cat.Version(), len(cat.Keys()))
	}
	// Issue #17's named examples and required keys.
	for _, k := range []string{"container.metrics.read", "container.details.read", "container.logs.read", "container.restart",
		"container.start", "container.stop", "container.exec", "stack.read", "stack.deploy", "stack.files.read", "stack.files.write",
		"volume.files.read", "volume.files.write", "stack.files.chmod", "stack.files.chown", "volume.files.chmod", "volume.files.chown",
		"backup.read", "backup.run", "backup.restore", "maintenance.preview", "maintenance.run", "agent.enroll", "settings.manage",
		"stack.definition.read", "stack.build", "image.build", "stack.migrate", "volume.migrate", "api_tokens.create",
		"audit.read", "audit.export", "job.read", "job.cancel"} {
		if !cat.Has(k) {
			t.Errorf("catalog lacks %s", k)
		}
	}
	// No generic read/write/execute grants.
	for _, k := range cat.Keys() {
		last := k[strings.LastIndex(k, ".")+1:]
		if k == "read" || strings.HasSuffix(k, ".all") || last == "execute" || last == "admin" || last == "any" {
			t.Errorf("generic capability %s", k)
		}
	}
	// Every scopable type with capabilities has a read capability, and
	// every capability names a type in the catalog.
	for _, rt := range cat.Types() {
		if rt.Scopable && rt.Read == "" && rt.Key != catalog.TypeService {
			t.Errorf("resource type %s has no read capability for the full view", rt.Key)
		}
	}
	for _, cp := range cat.Capabilities() {
		if cp.OwnerOnly && (cp.Type != catalog.TypeAdministration || cp.Risk != catalog.RiskHigh) {
			t.Errorf("owner-only %s must be an administration entry with high risk", cp.Key)
		}
	}
	if got := cat.Applicable(catalog.TypeContainer); !contains(got, "container.restart") || contains(got, "stack.read") || !contains(got, "job.read") {
		t.Errorf("applicable to containers: %v", got)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestNewRejectsInvalidCatalogs(t *testing.T) {
	cur := catalog.Default()
	types := cur.Types()
	caps := cur.Capabilities()
	base := caps[0]
	bad := map[string]catalog.Capability{
		"bad key":      func() catalog.Capability { c := base; c.Key = "Restart"; return c }(),
		"no label":     func() catalog.Capability { c := base; c.Key = "x.y"; c.Label = ""; return c }(),
		"future since": func() catalog.Capability { c := base; c.Key = "x.y"; c.Since = 9; return c }(),
		"unknown type": func() catalog.Capability { c := base; c.Key = "x.y"; c.Type = "planet"; return c }(),
		"no scope": func() catalog.Capability {
			c := base
			c.Key = "x.y"
			c.Instance, c.Environment, c.Resources = false, false, nil
			return c
		}(),
		"bad resource":  func() catalog.Capability { c := base; c.Key = "x.y"; c.Resources = []string{"job"}; return c }(),
		"bad risk":      func() catalog.Capability { c := base; c.Key = "x.y"; c.Risk = "spicy"; return c }(),
		"duplicate key": base,
		"owner scoped":  func() catalog.Capability { c := base; c.Key = "x.y"; c.OwnerOnly = true; return c }(),
	}
	for name, c := range bad {
		if _, err := catalog.New(catalog.Version, types, append(caps, c)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := catalog.New(catalog.Version, append(types, types[0]), caps); err == nil {
		t.Error("duplicate resource type accepted")
	}
}
