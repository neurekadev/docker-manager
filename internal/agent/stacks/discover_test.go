package stacks

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/lifecycle"
	"github.com/neurekadev/docker-manager/internal/agent/storage"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func discovered(t *testing.T, e *env) map[string]protocol.DiscoveredProject {
	t.Helper()
	out, err := call[protocol.ComposeDiscoverOutput](t, e.svc.Requests()[protocol.ReqComposeDiscover], struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]protocol.DiscoveredProject{}
	var names []string
	for _, p := range out.Projects {
		if _, dup := by[p.Name]; dup {
			t.Errorf("project %s listed twice", p.Name)
		}
		by[p.Name] = p
		names = append(names, p.Name)
	}
	if !slices.IsSorted(names) {
		t.Errorf("projects not sorted: %v", names)
	}
	return by
}

// TestDiscoverFoldersInStackRoot: a folder of the stacks volume holding a
// Compose file is a containerless project adoptable in place, with the
// volumes its file names; a project with containers of that name wins, a
// folder whose files do not load is listed but not importable, and hidden
// folders or folders without a Compose file are ignored.
func TestDiscoverFoldersInStackRoot(t *testing.T) {
	e := newEnv(t)
	root := filepath.ToSlash(e.root)
	writeTree(t, e.root, map[string]string{
		"wiki/compose.yaml": "services:\n  app:\n    image: wiki:2\n    volumes: [data:/data]\n  db:\n    image: postgres:16\n" +
			"    profiles: [full]\nvolumes:\n  data:\n",
		"wiki/compose.override.yaml":     "services:\n  app:\n    environment: [DEBUG=1]\n",
		"renamed/docker-compose.yml":     "name: blog\nservices:\n  web:\n    image: ghost:5\n",
		"broken/compose.yaml":            "services: [oops\n",
		".docker-manager-x/compose.yaml": "services:\n  a:\n    image: a\n",
		"notes/readme.txt":               "no compose file here\n",
		"shop/compose.yaml":              "services:\n  web:\n    image: nginx\n",
	})
	e.eng.containers = []engine.Container{{ID: "1", Names: []string{"/shop-web-1"}, Image: "nginx", State: "exited",
		Labels: map[string]string{lifecycle.ComposeProjectLabel: "shop", lifecycle.ComposeServiceLabel: "web",
			labelWorkingDir: root + "/shop", labelConfigFiles: root + "/shop/compose.yaml"},
		Mounts: []engine.Mount{{Type: "volume", Name: "shop_cache", Destination: "/cache"}, {Type: "volume", Name: anonVolume, Destination: "/tmp"}}}}
	e.eng.volumes = []engine.Volume{
		{Name: "wiki_data"},
		{Name: "wiki_old", Labels: map[string]string{lifecycle.ComposeProjectLabel: "wiki"}},
		{Name: "shop_cache"},
		{Name: "shop_db", Labels: map[string]string{lifecycle.ComposeProjectLabel: "shop"}},
		{Name: "unrelated"},
	}
	by := discovered(t, e)
	if len(by) != 4 {
		t.Fatalf("projects %v", by)
	}
	wiki := by["wiki"]
	if !wiki.Containerless || !wiki.Adoptable || wiki.Copyable || wiki.Root != protocol.RootStacks || wiki.Dir != "wiki" ||
		wiki.WorkingDir != root+"/wiki" || len(wiki.ConfigFiles) != 0 {
		t.Errorf("wiki %+v", wiki)
	}
	// Every profile's services, without containers.
	if len(wiki.Services) != 2 || wiki.Services[0].Containers != 0 || wiki.Services[0].Running != 0 {
		t.Errorf("wiki services %+v", wiki.Services)
	}
	if !slices.Equal(wiki.Volumes, []string{"wiki_data", "wiki_old"}) {
		t.Errorf("wiki volumes %v", wiki.Volumes)
	}
	// The top-level name: names the project, not the folder.
	if blog := by["blog"]; !blog.Adoptable || blog.Dir != "renamed" || !blog.Containerless {
		t.Errorf("blog %+v", blog)
	}
	if broken := by["broken"]; broken.Adoptable || broken.Copyable || !broken.Containerless ||
		!strings.Contains(broken.Reason, "do not load") {
		t.Errorf("broken %+v", broken)
	}
	// The project with containers wins: not containerless, its volumes
	// from its labels and its containers' mounts (anonymous ones left out).
	shop := by["shop"]
	if shop.Containerless || !shop.Adoptable || shop.Services[0].Containers != 1 || !slices.Equal(shop.Volumes, []string{"shop_cache", "shop_db"}) {
		t.Errorf("shop %+v", shop)
	}
}

// TestDiscoverFoldersInImportMount: import mounts are searched two folder
// levels deep for Compose files (not below a project, not deeper); such a
// project is importable by copy from its host path, named after its host
// folder, unless the stacks volume has that name or a project with
// containers is called so.
func TestDiscoverFoldersInImportMount(t *testing.T) {
	stacks, imports, single := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, imports, map[string]string{
		"apps/blog/compose.yaml": "services:\n  web:\n    image: ghost:5\n    volumes: [content:/var/lib/ghost, media:/media]\n" +
			"volumes:\n  content:\n  media:\n    external: true\n    name: shared-media\n",
		"apps/blog/nested/compose.yaml": "services:\n  x:\n    image: x\n",
		"too/deep/down/compose.yaml":    "services:\n  x:\n    image: x\n",
		"named/compose.yaml":            "name: shop\nservices:\n  web:\n    image: nginx\n",
		"taken/compose.yaml":            "services:\n  web:\n    image: nginx\n",
		".hidden/compose.yaml":          "services:\n  x:\n    image: x\n",
		"env-outside/compose.yaml":      "services:\n  web:\n    image: nginx\n    env_file: [../shared.env]\n",
		"shared.env":                    "A=1\n",
	})
	writeTree(t, single, map[string]string{"compose.yml": "services:\n  app:\n    image: app:1\n"})
	if err := os.Mkdir(filepath.Join(stacks, "taken"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := &storage.Result{Containerized: true, StacksDir: filepath.ToSlash(stacks),
		Roots: []storage.Root{{Kind: storage.KindStacks, Path: filepath.ToSlash(stacks), OK: true}},
		Imports: []storage.ImportMount{{HostPath: "/opt/single-app", Path: filepath.ToSlash(single)},
			{HostPath: "/srv", Path: filepath.ToSlash(imports)}}}
	eng := &fakeEngine{images: map[string]engine.ImageDetails{},
		containers: []engine.Container{{ID: "1", Names: []string{"/shop-web-1"}, Image: "nginx", State: "running",
			Labels: map[string]string{lifecycle.ComposeProjectLabel: "shop", lifecycle.ComposeServiceLabel: "web",
				labelWorkingDir: "/home/me/shop", labelConfigFiles: "/home/me/shop/compose.yaml"}}},
		volumes: []engine.Volume{{Name: "blog_content"}, {Name: "shared-media"}}}
	e := &env{root: stacks, c: &fakeComposer{}, eng: eng}
	e.svc = New(Options{Deps: fakeDeps{c: e.c, eng: eng, st: res}, Clock: testutil.FakeClock(), Logger: testutil.Logger(t)})

	by := discovered(t, e)
	blog := by["blog"]
	if !blog.Containerless || !blog.Copyable || blog.Adoptable || blog.SourceDir != "/srv/apps/blog" || blog.WorkingDir != "/srv/apps/blog" {
		t.Errorf("blog %+v", blog)
	}
	if !slices.Equal(blog.Volumes, []string{"blog_content", "shared-media"}) {
		t.Errorf("blog volumes %v", blog.Volumes)
	}
	// A mount of the project folder itself: named after the host folder.
	if app := by["single-app"]; !app.Copyable || app.SourceDir != "/opt/single-app" {
		t.Errorf("single-app %+v", app)
	}
	if taken := by["taken"]; taken.Copyable || !strings.Contains(taken.Reason, "already has a directory taken") {
		t.Errorf("taken %+v", taken)
	}
	if out := by["env-outside"]; out.Copyable || !strings.Contains(out.Reason, "shared.env is outside it") {
		t.Errorf("env-outside %+v", out)
	}
	// Skipped: below a project, too deep, hidden, and a folder whose
	// project a project with containers is already.
	for _, n := range []string{"nested", "down", "hidden"} {
		if _, ok := by[n]; ok {
			t.Errorf("%s listed", n)
		}
	}
	if shop := by["shop"]; shop.Containerless || shop.WorkingDir != "/home/me/shop" {
		t.Errorf("shop %+v", shop)
	}
	if len(by) != 5 {
		t.Errorf("projects %v", by)
	}
}

// TestDiscoverFoldersDuplicateNames: two folders resolving to the same
// project name are listed once and importable neither way.
func TestDiscoverFoldersDuplicateNames(t *testing.T) {
	e := newEnv(t)
	writeTree(t, e.root, map[string]string{
		"a/compose.yaml": "name: same\nservices:\n  x:\n    image: x\n",
		"b/compose.yaml": "name: same\nservices:\n  y:\n    image: y\n",
	})
	by := discovered(t, e)
	if s := by["same"]; len(by) != 1 || s.Adoptable || s.Dir != "" || !strings.Contains(s.Reason, "several folders") {
		t.Errorf("same %+v", by)
	}
}
