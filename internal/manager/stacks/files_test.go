package stacks_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/secrets"
	"github.com/neurekadev/docker-manager/internal/manager/stacks"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

type fakeSystems struct{ caps string }

func (f fakeSystems) EnvironmentSystem(_ context.Context, id string) (domain.EnvironmentSystem, error) {
	if id != env {
		return domain.EnvironmentSystem{}, domain.ErrEnvironmentNotFound
	}
	return domain.EnvironmentSystem{Environment: domain.Environment{ID: env}, Agent: &domain.Agent{ID: "ag", Capabilities: f.caps}}, nil
}

// TestFileManagerHooks (#15): stack file scopes resolve to the project
// directory below the stacks volume the agent reported, and a
// file-manager save of a definition file records a revision.
func TestFileManagerHooks(t *testing.T) {
	h := newHarness(t)
	key, err := secrets.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	caps := `{"roots":[{"kind":"volumes","path":"/var/lib/docker/volumes","watch":"inotify"},` +
		`{"kind":"stacks","path":"/var/lib/docker/volumes/docker-manager_stacks/_data","watch":"inotify"}]}`
	svc, err := stacks.New(stacks.Options{DB: h.db, Clock: h.clk, Logger: testutil.Logger(t), Keyring: secrets.NewKeyring(key),
		Agents: h.agents, Environments: fakeEnvironments{h.agents}, Jobs: h.eng, Bus: h.bus, Systems: fakeSystems{caps}})
	if err != nil {
		t.Fatal(err)
	}
	st := h.create("shop", shopYAML, shopEnv)
	root, err := svc.StackFileRoot(h.ctx, st.ID)
	if err != nil || root.EnvironmentID != env || root.Dir != "/var/lib/docker/volumes/docker-manager_stacks/_data/shop" {
		t.Fatalf("root %+v %v", root, err)
	}
	// The file manager guards the stack's definition files (#180).
	for _, want := range []string{"compose.yaml", ".env", "compose.override.yaml"} {
		if !slices.Contains(root.DefinitionFiles, want) {
			t.Errorf("definition files %v lack %s", root.DefinitionFiles, want)
		}
	}
	if _, err := svc.StackFileRoot(h.ctx, "nope"); !errors.Is(err, domain.ErrFileScopeNotFound) {
		t.Errorf("unknown stack: %v", err)
	}
	// The stack header's host path (#22) is the same directory.
	if p, err := svc.HostPath(h.ctx, st.ID); err != nil || p != root.Dir {
		t.Errorf("host path %q %v", p, err)
	}
	if _, err := h.svc.HostPath(h.ctx, st.ID); stackErrCode(err) != domain.StackErrRootUnavailable {
		t.Errorf("host path without systems: %v", err)
	}
	// Without reported capabilities the root is unavailable.
	if _, err := h.svc.StackFileRoot(h.ctx, st.ID); stackErrCode(err) != domain.StackErrRootUnavailable {
		t.Errorf("no systems: %v", err)
	}

	sub := h.bus.Subscribe(0, func(e events.Event) bool { return e.Type == events.StackRevisionRecorded })
	defer sub.Close()
	h.write(shopYAML+"# edited in the file manager\n", "shop", "compose.yaml")
	ctx, err := authz.WithPrincipal(h.ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	svc.StackSourcesChanged(ctx, st.ID, []string{"compose.yaml"})
	select {
	case e := <-sub.C():
		if e.ResourceID != st.ID || e.Attributes["source"] != string(domain.RevisionFileManager) {
			t.Errorf("event %+v", e)
		}
	case <-h.ctx.Done():
		t.Fatal("no revision recorded")
	}
	revs := revisions(t, h, st.ID)
	if revs[0].Source != domain.RevisionFileManager || revs[0].AuthorUserID != "alice" || !h.get(st.ID).UndeployedChanges() {
		t.Errorf("revision %+v", revs[0])
	}
}

// TestValidateSourceSave (#7, #15): a file-manager save of a definition
// file is validated with the rest of the definition on disk; errors refuse
// it, other files are never checked.
func TestValidateSourceSave(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	if err := h.svc.ValidateSourceSave(h.ctx, st.ID, "compose.yaml", []byte(shopYAML+"# still fine\n")); err != nil {
		t.Errorf("valid save: %v", err)
	}
	err := h.svc.ValidateSourceSave(h.ctx, st.ID, "compose.yaml", []byte("services:\n  web:\n    image: [nginx\n"))
	if stackErrCode(err) != domain.StackErrInvalidDefinition {
		t.Errorf("broken YAML: %v", err)
	}
	var se *domain.StackError
	if errors.As(err, &se) && len(se.Issues) == 0 {
		t.Error("the refusal lists no findings")
	}
	// The .env feeds interpolation: a missing required variable breaks it.
	if err := h.svc.ValidateSourceSave(h.ctx, st.ID, "compose.yaml",
		[]byte("services:\n  web:\n    image: nginx:${MISSING:?set MISSING}\n")); stackErrCode(err) != domain.StackErrInvalidDefinition {
		t.Errorf("unset required variable: %v", err)
	}
	if err := h.svc.ValidateSourceSave(h.ctx, st.ID, ".env", []byte("DB_TAG=17\n")); err != nil {
		t.Errorf("valid .env: %v", err)
	}
	if err := h.svc.ValidateSourceSave(h.ctx, st.ID, "html/index.html", []byte("services: [")); err != nil {
		t.Errorf("a workspace file is not a definition file: %v", err)
	}
	if err := h.svc.ValidateSourceSave(h.ctx, "nope", "compose.yaml", nil); !errors.Is(err, domain.ErrFileScopeNotFound) {
		t.Errorf("unknown stack: %v", err)
	}
}

// TestDefinitionPaths (#180): a stack's definition is its observed files,
// the names stacks are created with and every Compose and env file it
// declares, cleaned like file manager paths; paths outside the project
// directory are left out.
func TestDefinitionPaths(t *testing.T) {
	st := domain.Stack{ConfigFiles: []string{"compose.yaml", "./deploy/compose.prod.yml", "/srv/other/compose.yml", "../shared/compose.yml"},
		EnvFiles: []string{"config//app.env", "config/app.env", "../secrets.env"}}
	got := stacks.DefinitionPaths(st, []string{"stack.yml", "compose.yaml"})
	for _, want := range []string{"stack.yml", "compose.yaml", "compose.override.yaml", ".env", "deploy/compose.prod.yml", "config/app.env"} {
		if !slices.Contains(got, want) {
			t.Errorf("definition paths %v lack %s", got, want)
		}
	}
	for _, p := range got {
		if p == "" || p[0] == '/' || p == "." || slices.Contains([]string{"../shared/compose.yml", "../secrets.env", "./deploy/compose.prod.yml"}, p) {
			t.Errorf("definition paths %v hold %q", got, p)
		}
	}
	if n := len(got); n != len(slices.Compact(slices.Sorted(slices.Values(got)))) {
		t.Errorf("definition paths %v hold duplicates", got)
	}
	for p, want := range map[string]bool{"deploy/compose.prod.yml": true, "./config/app.env": true, "stack.yml": true, "html/x": false, "../secrets.env": false} {
		if got := stacks.IsDefinitionFile(st, []string{"stack.yml"}, p); got != want {
			t.Errorf("IsDefinitionFile(%q) = %v, want %v", p, got, want)
		}
	}
}

// TestIncludedFilesAreDefinitionFiles (#283): a Compose file the
// definition includes is recorded with it and guarded as a definition
// file, a change to it or to a directory above it records a revision, and
// a save of it is not validated from memory (the validation would load
// its old content from disk, so a broken included file could not be
// fixed).
func TestIncludedFilesAreDefinitionFiles(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	h.write("services:\n  cache:\n    image: redis:7\n", "shop", "lib", "cache", "cache.yaml")
	h.write("include: [lib/cache/cache.yaml]\n"+shopYAML, "shop", "compose.yaml")
	if rev, err := h.svc.ExternalChange(h.ctx, st.ID, []string{"compose.yaml"}, false); err != nil || rev == nil {
		t.Fatalf("compose.yaml change %+v %v", rev, err)
	}
	root, err := h.svc.Root(h.ctx, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(root.DefinitionFiles, "lib/cache/cache.yaml") ||
		!stacks.IsDefinitionFile(h.get(st.ID), root.DefinitionFiles, "./lib/cache/cache.yaml") {
		t.Fatalf("definition files %v lack the included file", root.DefinitionFiles)
	}
	h.write("services:\n  cache:\n    image: redis:8\n", "shop", "lib", "cache", "cache.yaml")
	if rev, err := h.svc.ExternalChange(h.ctx, st.ID, []string{"lib"}, false); err != nil || rev == nil {
		t.Fatalf("a change below lib/ %+v %v", rev, err)
	}
	h.write("services: [broken", "shop", "lib", "cache", "cache.yaml")
	if err := h.svc.ValidateSourceSave(h.ctx, st.ID, "lib/cache/cache.yaml", []byte("services:\n  cache:\n    image: redis:8\n")); err != nil {
		t.Errorf("the save fixing an included file: %v", err)
	}
}
