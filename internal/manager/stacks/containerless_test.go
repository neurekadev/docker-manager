package stacks_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/lifecycle"
	"github.com/neurekadev/docker-manager/internal/agent/storage"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

const wikiYAML = "services:\n  app:\n    image: wiki:2\n    volumes: [data:/data]\nvolumes:\n  data:\n"

// TestImportContainerlessInPlace: a project folder of the stacks volume
// without containers is discovered with its volumes and imported in place
// without starting anything: its files are the first revision and the
// stack waits undeployed for its first deploy. A folder that is a stack's
// folder already, under another project name, is not offered again.
func TestImportContainerlessInPlace(t *testing.T) {
	h := newHarness(t)
	h.write(wikiYAML, "wiki", "compose.yaml")
	h.engine.volumes = []engine.Volume{{Name: "wiki_data", Labels: map[string]string{lifecycle.ComposeProjectLabel: "wiki"}}}

	list, err := h.svc.Discovered(h.ctx, env)
	if err != nil || len(list) != 1 {
		t.Fatalf("discovered %+v %v", list, err)
	}
	if d := list[0]; d.Name != "wiki" || !d.Containerless || !d.Adoptable || d.StackID != "" || !slices.Equal(d.Volumes, []string{"wiki_data"}) ||
		len(d.Services) != 1 || d.Services[0].Containers != 0 {
		t.Errorf("discovered %+v", d)
	}
	st, err := h.svc.Import(h.ctx, alice, domain.StackImport{EnvironmentID: env, ProjectName: "wiki"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != domain.StackUndeployed || st.Applied != nil || st.Observed == nil || st.EngineState != domain.EngineStateMissing ||
		st.Dir != "wiki" || st.Root != domain.StackRootStacks || st.Origin != domain.StackOriginImported || len(st.Services) != 1 {
		t.Errorf("imported %+v", st)
	}
	if revs := revisions(t, h, st.ID); len(revs) != 1 || revs[0].Source != domain.RevisionExternal {
		t.Errorf("revisions %+v", revs)
	}
	if h.disp.Pending(env) != 0 || len(h.comp.Calls()) != 0 {
		t.Errorf("the import started something: pending %d, Compose %v", h.disp.Pending(env), h.comp.Calls())
	}

	// The stack's folder names another project now: still that stack's.
	shop := h.create("shop", shopYAML, shopEnv)
	h.write("name: store\nservices:\n  web:\n    image: nginx:1.27\n", "shop", "compose.yaml")
	list, _ = h.svc.Discovered(h.ctx, env)
	i := slices.IndexFunc(list, func(d domain.DiscoveredStack) bool { return d.Name == "store" })
	if i < 0 || list[i].StackID != shop.ID || list[i].Adoptable {
		t.Fatalf("store %+v", list)
	}
	if _, err := h.svc.Import(h.ctx, alice, domain.StackImport{EnvironmentID: env, ProjectName: "store"}); stackErrCode(err) != domain.StackErrNotAdoptable {
		t.Errorf("import of a stack's folder: %v", err)
	}
	// A containerless folder does not keep a stack from being created
	// elsewhere; the name of an existing folder is still refused.
	h.write("services:\n  x:\n    image: x\n", "spare", "compose.yaml")
	if _, _, err := h.svc.Create(h.ctx, alice, domain.StackCreate{StackDefinition: domain.StackDefinition{EnvironmentID: env, Name: "spare",
		Files: files("services:\n  y:\n    image: y\n", "")}}); stackErrCode(err) != domain.StackErrDirectoryExists {
		t.Errorf("create over a containerless folder: %v", err)
	}
}

// TestImportContainerlessByCopy: a project without containers below an
// import mount is imported by copy only with an agent announcing
// stack.import_containerless; the job starts nothing and the stack ends
// undeployed with the copy's files as its (observed) revision.
func TestImportContainerlessByCopy(t *testing.T) {
	h := newHarness(t)
	imports := t.TempDir()
	if err := os.MkdirAll(filepath.Join(imports, "wiki"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imports, "wiki", "compose.yaml"), []byte(wikiYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	h.storage.Containerized = true
	h.storage.Imports = []storage.ImportMount{{HostPath: "/srv", Path: filepath.ToSlash(imports)}}
	list, err := h.svc.Discovered(h.ctx, env)
	if err != nil || len(list) != 1 || !list[0].Copyable || !list[0].Containerless || list[0].SourceDir != "/srv/wiki" {
		t.Fatalf("discovered %+v %v", list, err)
	}
	req := domain.StackImport{EnvironmentID: env, ProjectName: "wiki"}

	// An agent that copies projects, but not projects without containers.
	h.agents.features = map[string]bool{protocol.FeatureStackImportCopy: true}
	if _, _, err := h.svc.ImportCopy(h.ctx, alice, req, domain.StackJobRequest{}); stackErrCode(err) != domain.StackErrEnvironmentUnsupported {
		t.Fatalf("outdated agent: %v", err)
	}
	if list, _ := h.svc.Discovered(h.ctx, env); list[0].StackID != "" {
		t.Fatal("a refused import left a stack behind")
	}

	h.agents.features[protocol.FeatureStackImportContainerless] = true
	st, j, err := h.svc.ImportCopy(h.ctx, alice, req, domain.StackJobRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != domain.StackUndeployed || st.EngineState != domain.EngineStateMissing {
		t.Errorf("stack while importing %+v", st)
	}
	h.run()
	if j = h.job(j.ID); j.State != domain.JobSucceeded {
		t.Fatalf("import %s: %s %s", j.State, j.ErrorClass, j.ErrorMessage)
	}
	st = h.get(st.ID)
	if st.Status != domain.StackUndeployed || st.Applied != nil || st.AppliedAt != nil || st.Observed == nil ||
		st.EngineState != domain.EngineStateMissing || len(st.Images) != 0 || len(st.Services) != 1 || st.Dir != "wiki" {
		t.Errorf("imported %+v", st)
	}
	if revs := revisions(t, h, st.ID); len(revs) != 1 || revs[0].Source != domain.RevisionExternal || revs[0].AuthorUserID != "alice" {
		t.Errorf("revisions %+v", revs)
	}
	if h.read("wiki", "compose.yaml") != wikiYAML || len(h.comp.Calls()) != 0 {
		t.Errorf("copy or Compose calls: %v", h.comp.Calls())
	}
}
