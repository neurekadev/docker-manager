package jobspec

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/domain"
)

// sampleTargets returns one target for every target type the spec's lock
// rules mention (paths get absolute IDs).
func sampleTargets(s Spec) []domain.JobTarget {
	var out []domain.JobTarget
	for _, r := range s.Locks {
		if r.Source != FromTargets {
			continue
		}
		id := "t-" + string(r.TargetType)
		if r.TargetType == domain.TargetPath || r.TargetType == domain.TargetDestinationPath {
			id = "/data/" + string(r.TargetType)
		}
		t := domain.JobTarget{Type: r.TargetType, ID: id}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// declaredKinds parses Go sources under root and returns every constant of
// type domain.JobKind (or JobKind inside package domain).
func declaredKinds(t *testing.T, fset *token.FileSet, files map[string][]byte) []domain.JobKind {
	t.Helper()
	var kinds []domain.JobKind
	for name, src := range files {
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				if !isJobKindType(vs.Type, f.Name.Name) {
					continue
				}
				for _, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Errorf("%s: JobKind constant with non-literal value", name)
						continue
					}
					kinds = append(kinds, domain.JobKind(strings.Trim(lit.Value, "\"`")))
				}
			}
		}
	}
	return kinds
}

func isJobKindType(e ast.Expr, pkg string) bool {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		return ok && id.Name == "domain" && x.Sel.Name == "JobKind"
	case *ast.Ident:
		return pkg == "domain" && x.Name == "JobKind"
	}
	return false
}

// missingLockDefinitions returns declared kinds without a usable lock
// definition in the registry.
func missingLockDefinitions(declared []domain.JobKind) []string {
	var problems []string
	for _, k := range declared {
		s, ok := Lookup(k)
		if !ok {
			problems = append(problems, string(k)+": no registered spec (no lock definition)")
			continue
		}
		if len(s.Locks) == 0 {
			problems = append(problems, string(k)+": empty lock definition")
			continue
		}
		locks, err := s.ComputeLocks("env-1", sampleTargets(s))
		if err != nil || len(locks) == 0 {
			problems = append(problems, string(k)+": lock definition yields no locks")
		}
	}
	return problems
}

// TestEveryJobKindHasLockDefinition is the lock-matrix completeness check
// (#26, #29): every JobKind constant declared anywhere in internal/ must be
// registered with a lock definition that produces locks, and every
// registered kind must be a declared constant.
func TestEveryJobKindHasLockDefinition(t *testing.T) {
	root := filepath.Join("..")
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[p] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	declared := declaredKinds(t, token.NewFileSet(), files)
	if len(declared) < 30 {
		t.Fatalf("found only %d JobKind constants; the source scan is broken", len(declared))
	}
	for _, p := range missingLockDefinitions(declared) {
		t.Error(p)
	}
	for _, k := range Kinds() {
		if !slices.Contains(declared, k) {
			t.Errorf("registered kind %s has no JobKind constant", k)
		}
	}
}

func TestCompletenessCheckDetectsMissingKind(t *testing.T) {
	src := []byte(`package feature
import "github.com/neurekadev/dockyard/internal/domain"
const NewThing domain.JobKind = "thing.frobnicate"
const Deploy domain.JobKind = "stack.deploy"
`)
	declared := declaredKinds(t, token.NewFileSet(), map[string][]byte{"feature.go": src})
	if !slices.Equal(declared, []domain.JobKind{"thing.frobnicate", "stack.deploy"}) {
		t.Fatalf("declared = %v", declared)
	}
	problems := missingLockDefinitions(declared)
	if len(problems) != 1 || !strings.Contains(problems[0], "thing.frobnicate") {
		t.Fatalf("problems = %v, want exactly the unregistered kind", problems)
	}
}

func TestCatalogCoversV1Kinds(t *testing.T) {
	want := []domain.JobKind{
		"image.pull", "image.build", "image.remove",
		"container.create", "container.start", "container.stop", "container.restart", "container.pause",
		"container.unpause", "container.remove", "container.update",
		"stack.deploy", "stack.start", "stack.stop", "stack.restart", "stack.down", "stack.remove", "stack.build", "stack.update",
		"stack.migrate", "stack.remove_source", "volume.migrate", "volume.create", "volume.remove", "network.create", "network.remove",
		"update.check", "update.run", "prune.run", "backup.run", "restore.run", "backup.retention", "backup.verify",
		"backup.import", "files.archive", "files.extract", "files.metadata", "files.copy", "files.move", "files.delete",
		"manager.backup", "manager.retention", "manager.verify",
	}
	for _, k := range want {
		if _, ok := Lookup(k); !ok {
			t.Errorf("kind %s missing from the catalog", k)
		}
	}
	if len(Kinds()) != len(want) {
		t.Errorf("catalog has %d kinds, want %d: %v", len(Kinds()), len(want), Kinds())
	}
}

// TestAuthorizationTargets: migrations authorize their capability on the
// source stack or volume only; the stack's volumes and the destination's
// resources only take locks (#35).
func TestAuthorizationTargets(t *testing.T) {
	stackTarget := domain.JobTarget{Type: domain.TargetStack, ID: "s1"}
	srcVol := domain.JobTarget{Type: domain.TargetVolume, ID: "shop_data"}
	dstVol := domain.JobTarget{Type: domain.TargetVolume, ID: "shop_data", EnvironmentID: "dst"}
	all := []domain.JobTarget{stackTarget, srcVol, dstVol}
	s, _ := Lookup(StackMigrate)
	if got := s.AuthorizationTargets(all); len(got) != 1 || got[0] != stackTarget {
		t.Errorf("stack.migrate authorizes %v", got)
	}
	v, _ := Lookup(VolumeMigrate)
	if got := v.AuthorizationTargets([]domain.JobTarget{srcVol, dstVol}); len(got) != 1 || got[0] != srcVol {
		t.Errorf("volume.migrate authorizes %v", got)
	}
	r, _ := Lookup(StackRemoveSource)
	if got := r.AuthorizationTargets([]domain.JobTarget{stackTarget, srcVol}); len(got) != 1 || got[0] != stackTarget {
		t.Errorf("stack.remove_source authorizes %v", got)
	}
	d, _ := Lookup(StackDeploy)
	if got := d.AuthorizationTargets(all); len(got) != 3 {
		t.Errorf("kinds without LockOnly authorize every target, got %v", got)
	}
	// Locks still cover every target, on both environments.
	locks, err := s.ComputeLocks("src", all)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.JobLock{
		lock("host", "dst", "", shared), lock("host", "src", "", shared),
		lock("stack", "src", "s1", exclusive),
		lock("volume", "dst", "shop_data", exclusive), lock("volume", "src", "shop_data", exclusive),
	}
	if !slices.Equal(locks, want) {
		t.Errorf("locks %v, want %v", locks, want)
	}
}

func TestSpecValidateRejects(t *testing.T) {
	base, _ := Lookup(StackDeploy)
	cases := map[string]func(s *Spec){
		"no locks":           func(s *Spec) { s.Locks = nil },
		"bad kind":           func(s *Spec) { s.Kind = "Deploy" },
		"bad capability":     func(s *Spec) { s.Capability = "deploy" },
		"no deadline":        func(s *Spec) { s.OfflineDeadline = 0 },
		"no steps":           func(s *Spec) { s.Steps = nil },
		"dup steps":          func(s *Spec) { s.Steps = []Step{idem("a"), idem("a")} },
		"bad executor":       func(s *Spec) { s.Executor = "elsewhere" },
		"non-idem no guide":  func(s *Spec) { s.Steps = []Step{{Name: "x"}} },
		"optional only":      func(s *Spec) { s.Locks = []LockRule{optional(target(domain.LockStack, exclusive, domain.TargetStack))} },
		"host from targets":  func(s *Spec) { s.Locks = []LockRule{target(domain.LockHost, shared, domain.TargetStack)} },
		"unknown scope":      func(s *Spec) { s.Locks = []LockRule{{Scope: "planet", Mode: shared, Source: FromEnvironments}} },
		"manager no restart": func(s *Spec) { s.Executor = domain.ExecutorManager; s.OfflineDeadline = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := base
			s.Steps = slices.Clone(base.Steps)
			s.Locks = slices.Clone(base.Locks)
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("Validate accepted an invalid spec")
			}
		})
	}
	for _, s := range Catalog() {
		if err := s.Validate(); err != nil {
			t.Errorf("catalog: %v", err)
		}
	}
}

func lock(scope domain.LockScope, env, name string, mode domain.LockMode) domain.JobLock {
	return domain.JobLock{Scope: scope, EnvironmentID: env, Name: name, Mode: mode}
}

func TestConflicts(t *testing.T) {
	cases := []struct {
		name string
		a, b domain.JobLock
		want bool
	}{
		{"same stack exclusive", lock("stack", "e1", "web", exclusive), lock("stack", "e1", "web", exclusive), true},
		{"same stack shared/exclusive", lock("stack", "e1", "web", shared), lock("stack", "e1", "web", exclusive), true},
		{"same stack shared", lock("stack", "e1", "web", shared), lock("stack", "e1", "web", shared), false},
		{"other stack", lock("stack", "e1", "web", exclusive), lock("stack", "e1", "db", exclusive), false},
		{"other environment", lock("stack", "e1", "web", exclusive), lock("stack", "e2", "web", exclusive), false},
		{"other scope same name", lock("stack", "e1", "web", exclusive), lock("volume", "e1", "web", exclusive), false},
		{"wildcard shared vs exclusive", lock("volume", "e1", "*", shared), lock("volume", "e1", "data", exclusive), true},
		{"wildcard shared vs shared", lock("volume", "e1", "*", shared), lock("volume", "e1", "data", shared), false},
		{"wildcard other env", lock("volume", "e1", "*", shared), lock("volume", "e2", "data", exclusive), false},
		{"host shared", lock("host", "e1", "", shared), lock("host", "e1", "", shared), false},
		{"path parent", lock("file_path", "e1", "/data", exclusive), lock("file_path", "e1", "/data/a/b", shared), true},
		{"path child", lock("file_path", "e1", "/data/a/b", shared), lock("file_path", "e1", "/data", exclusive), true},
		{"path sibling prefix", lock("file_path", "e1", "/data/ab", exclusive), lock("file_path", "e1", "/data/a", exclusive), false},
		{"path root", lock("file_path", "e1", "/", exclusive), lock("file_path", "e1", "/x", shared), true},
		{"path shared both", lock("file_path", "e1", "/data", shared), lock("file_path", "e1", "/data/x", shared), false},
		{"repository instance-wide", lock("repository", "", "r1", exclusive), lock("repository", "", "r1", shared), true},
	}
	for _, c := range cases {
		if got := Conflicts(c.a, c.b); got != c.want {
			t.Errorf("%s: Conflicts = %v, want %v", c.name, got, c.want)
		}
		if got := Conflicts(c.b, c.a); got != c.want {
			t.Errorf("%s (reversed): Conflicts = %v, want %v", c.name, got, c.want)
		}
	}
}

func mustLocks(t *testing.T, kind domain.JobKind, env string, targets ...domain.JobTarget) []domain.JobLock {
	t.Helper()
	s, ok := Lookup(kind)
	if !ok {
		t.Fatalf("unknown kind %s", kind)
	}
	l, err := s.ComputeLocks(env, targets)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func conflictsAny(a, b []domain.JobLock) bool {
	_, _, ok := FirstConflict(a, b)
	return ok
}

// TestMatrixSemantics checks the lock-matrix examples of #26 against the
// catalog.
func TestMatrixSemantics(t *testing.T) {
	stack := func(id string) domain.JobTarget { return domain.JobTarget{Type: domain.TargetStack, ID: id} }
	vol := func(id string) domain.JobTarget { return domain.JobTarget{Type: domain.TargetVolume, ID: id} }
	repo := domain.JobTarget{Type: domain.TargetRepository, ID: "repo-1"}
	path := func(p string) domain.JobTarget { return domain.JobTarget{Type: domain.TargetPath, ID: p} }
	dest := func(p string) domain.JobTarget { return domain.JobTarget{Type: domain.TargetDestinationPath, ID: p} }

	deployWeb := mustLocks(t, StackDeploy, "e1", stack("web"))
	cases := []struct {
		name string
		a, b []domain.JobLock
		want bool
	}{
		{"deploy vs deploy same stack", deployWeb, mustLocks(t, StackDeploy, "e1", stack("web")), true},
		{"deploy vs update same stack", deployWeb, mustLocks(t, UpdateRun, "e1", stack("web")), true},
		{"deploy vs build same stack", deployWeb, mustLocks(t, StackBuild, "e1", stack("web")), true},
		{"deploy vs backup same stack", deployWeb, mustLocks(t, BackupRun, "e1", stack("web"), repo), true},
		{"deploy vs restore same stack", deployWeb, mustLocks(t, RestoreRun, "e1", stack("web"), repo), true},
		{"deploy vs deploy other stack", deployWeb, mustLocks(t, StackDeploy, "e1", stack("db")), false},
		{"deploy vs deploy other environment", deployWeb, mustLocks(t, StackDeploy, "e2", stack("web")), false},
		{"volume restore vs backup of volume", mustLocks(t, RestoreRun, "e1", vol("data"), repo), mustLocks(t, BackupRun, "e1", vol("data"), repo), true},
		{"volume restore vs prune", mustLocks(t, RestoreRun, "e1", vol("data"), repo), mustLocks(t, PruneRun, "e1"), true},
		{"volume restore vs file write on volume", mustLocks(t, RestoreRun, "e1", vol("data"), repo),
			mustLocks(t, FilesDelete, "e1", vol("data"), path("/var/lib/docker/volumes/data/_data/x")), true},
		// #14: prune is serialized with deployments, builds, updates,
		// migrations, backup shutdowns and restores (a deploy's freshly
		// pulled image has no container yet, so revalidation alone
		// cannot protect it).
		{"prune vs deploy", mustLocks(t, PruneRun, "e1"), deployWeb, true},
		{"prune vs stack build", mustLocks(t, PruneRun, "e1"), mustLocks(t, StackBuild, "e1", stack("web")), true},
		{"prune vs update", mustLocks(t, PruneRun, "e1"), mustLocks(t, UpdateRun, "e1", stack("web")), true},
		{"prune vs backup with shutdown", mustLocks(t, PruneRun, "e1"), mustLocks(t, BackupRun, "e1", stack("web"), repo), true},
		{"prune vs image pull", mustLocks(t, PruneRun, "e1"),
			mustLocks(t, ImagePull, "e1", domain.JobTarget{Type: domain.TargetImage, ID: "nginx:1"}), true},
		{"prune vs deploy on another environment", mustLocks(t, PruneRun, "e1"), mustLocks(t, StackDeploy, "e2", stack("web")), false},
		{"prune vs prune", mustLocks(t, PruneRun, "e1"), mustLocks(t, PruneRun, "e1"), false},
		{"prune vs volume remove", mustLocks(t, PruneRun, "e1"), mustLocks(t, VolumeRemove, "e1", vol("data")), true},
		{"backups share a repository", mustLocks(t, BackupRun, "e1", vol("a"), repo), mustLocks(t, BackupRun, "e2", vol("b"), repo), false},
		{"retention excludes backup", mustLocks(t, BackupRetention, "e1", repo), mustLocks(t, BackupRun, "e1", vol("a"), repo), true},
		{"manager retention excludes backup", mustLocks(t, ManagerRetention, "", repo), mustLocks(t, BackupRun, "e1", vol("a"), repo), true},
		{"manager backup waits for host backups", mustLocks(t, ManagerBackup, "", repo), mustLocks(t, BackupRun, "e1", vol("a"), repo), true},
		{"verification shares with backups", mustLocks(t, BackupVerify, "e1", repo), mustLocks(t, BackupRun, "e2", vol("a"), repo), false},
		{"file copy into dir being deleted", mustLocks(t, FilesCopy, "e1", path("/srv/a"), dest("/srv/b/c")),
			mustLocks(t, FilesDelete, "e1", path("/srv/b")), true},
		{"file reads share", mustLocks(t, FilesArchive, "e1", path("/srv/a"), dest("/srv/x.tar")),
			mustLocks(t, FilesArchive, "e1", path("/srv/a"), dest("/srv/y.tar")), false},
		{"container restart during stack deploy", deployWeb,
			mustLocks(t, ContainerRestart, "e1", domain.JobTarget{Type: domain.TargetContainer, ID: "web-1"}, stack("web")), true},
	}
	for _, c := range cases {
		if got := conflictsAny(c.a, c.b); got != c.want {
			t.Errorf("%s: conflict = %v, want %v\n a=%v\n b=%v", c.name, got, c.want, c.a, c.b)
		}
	}
}

func TestComputeLocksSortedDedupedAndValidated(t *testing.T) {
	s, _ := Lookup(StackMigrate)
	locks, err := s.ComputeLocks("src", []domain.JobTarget{
		{Type: domain.TargetStack, ID: "web"},
		{Type: domain.TargetStack, ID: "web", EnvironmentID: "dst"},
		{Type: domain.TargetVolume, ID: "data"},
		{Type: domain.TargetStack, ID: "web"}, // duplicate
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.JobLock{
		lock("host", "dst", "", shared), lock("host", "src", "", shared),
		lock("stack", "dst", "web", exclusive), lock("stack", "src", "web", exclusive),
		lock("volume", "src", "data", exclusive),
	}
	if !slices.Equal(locks, want) {
		t.Fatalf("locks =\n %v\nwant\n %v", locks, want)
	}

	// Exclusive wins over shared for the same resource.
	got := NormalizeLocks([]domain.JobLock{lock("file_path", "e", "/a", shared), lock("file_path", "e", "/a", exclusive)})
	if len(got) != 1 || got[0].Mode != exclusive {
		t.Fatalf("normalize = %v", got)
	}

	bad := map[string][]domain.JobTarget{
		"missing required": nil,
		"relative path":    {{Type: domain.TargetStack, ID: "web"}, {Type: domain.TargetPath, ID: "a/b"}},
		"unclean path":     {{Type: domain.TargetStack, ID: "web"}, {Type: domain.TargetPath, ID: "/a/../b"}},
		"wildcard id":      {{Type: domain.TargetStack, ID: "*"}},
		"empty id":         {{Type: domain.TargetStack, ID: ""}},
		"control char":     {{Type: domain.TargetStack, ID: "we\x00b"}},
		"unknown type":     {{Type: domain.TargetStack, ID: "web"}, {Type: "planet", ID: "x"}},
	}
	deploy, _ := Lookup(BackupRun)
	for name, targets := range bad {
		if _, err := deploy.ComputeLocks("e1", targets); !errors.Is(err, domain.ErrJobInvalid) {
			t.Errorf("%s: err = %v, want ErrJobInvalid", name, err)
		}
	}
	if _, err := deploy.ComputeLocks("", []domain.JobTarget{{Type: domain.TargetRepository, ID: "r"}}); !errors.Is(err, domain.ErrJobInvalid) {
		t.Errorf("agent kind without environment accepted: %v", err)
	}
	ret, _ := Lookup(ManagerRetention)
	if l, err := ret.ComputeLocks("", []domain.JobTarget{{Type: domain.TargetRepository, ID: "r"}}); err != nil || len(l) != 1 || l[0].EnvironmentID != "" {
		t.Errorf("manager repository kind: %v %v", l, err)
	}
}

// TestMatrixDocUpToDate fails when the generated lock-matrix table in
// docs/architecture/job-engine.md differs from the catalog.
func TestMatrixDocUpToDate(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "architecture", "job-engine.md")
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc = bytes.ReplaceAll(doc, []byte("\r\n"), []byte("\n"))
	want, err := ReplaceMatrix(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(doc, want) {
		t.Fatal("docs/architecture/job-engine.md lock matrix is stale; run: bash scripts/generate.sh")
	}
	for _, k := range Kinds() {
		if !bytes.Contains(doc, []byte("| `"+string(k)+"` |")) {
			t.Errorf("doc lacks a row for %s", k)
		}
	}
}

func TestSpecHelpers(t *testing.T) {
	s, _ := Lookup(BackupRun)
	if st, ok := s.Step("snapshot"); !ok || st.Idempotent || !st.SafePoint || st.Recovery == "" {
		t.Fatalf("snapshot step = %+v %v", st, ok)
	}
	if _, ok := s.Step("nope"); ok {
		t.Fatal("unknown step found")
	}
	if !s.HasCompensation(CompStartContainers) || s.HasCompensation("x") {
		t.Fatal("HasCompensation")
	}
	if fmtDuration(90*time.Second) != "1m30s" || fmtDuration(2*time.Hour) != "2h" || fmtDuration(10*time.Minute) != "10m" {
		t.Fatal("fmtDuration")
	}
}
