package compose

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/neurekadev/dockyard/internal/agent/lifecycle"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestLoadFromContent (#7): a definition that is not on disk yet loads
// from memory like it would from its project directory (env interpolation
// from its .env only), and every service carries DockYard's dependency
// label with the required flag Compose's own label lacks.
func TestLoadFromContent(t *testing.T) {
	t.Setenv("TAG", "from-agent-env")
	dir := filepath.Join(t.TempDir(), "shop") // does not exist
	p, err := LoadProject(testutil.Context(t), ProjectSpec{Dir: dir, Content: map[string][]byte{
		"compose.yaml": []byte(`services:
  db:
    image: db:${TAG:-none}
  web:
    image: web:1
    depends_on:
      db:
        condition: service_healthy
        restart: true
      cache:
        condition: service_started
        required: false
    volumes: ["./html:/html"]
  cache:
    image: cache:1
    profiles: [extras]
`),
		"compose.override.yaml": []byte("services:\n  web:\n    labels:\n      dev.neureka.dockyard.icon: globe\n"),
		".env":                  []byte("TAG=16\n"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "shop" || len(p.ConfigFiles) != 2 {
		t.Errorf("project %s files %v", p.Name, p.ConfigFiles)
	}
	if img := p.model.Services["db"].Image; img != "db:16" {
		t.Errorf("db image %q: interpolation must use the in-memory .env only", img)
	}
	web := p.model.Services["web"]
	if got := web.CustomLabels[lifecycle.DependsOnLabel]; got != "cache:service_started:false:false,db:service_healthy:true:true" {
		t.Errorf("dependency label %q", got)
	}
	deps, err := lifecycle.ParseDependsOn(web.CustomLabels[lifecycle.DependsOnLabel])
	if err != nil || len(deps) != 2 || deps[0].Required || !deps[1].Required {
		t.Errorf("parsed %+v %v", deps, err)
	}
	var icon string
	for _, s := range p.Services {
		if s.Name == "web" {
			icon = s.Icon
		}
	}
	if icon != "globe" {
		t.Errorf("icon label %q", icon)
	}
	if len(p.Binds) != 1 || p.Binds[0].Source != filepath.Join(dir, "html") {
		t.Errorf("binds %+v", p.Binds)
	}
	want := []string{filepath.Join(dir, ".env"), filepath.Join(dir, "compose.override.yaml"), filepath.Join(dir, "compose.yaml")}
	if !slices.Equal(p.DefinitionFiles, want) {
		t.Errorf("definition files %v, want %v", p.DefinitionFiles, want)
	}
	// Without a Compose file in the content, loading fails.
	if _, err := LoadProject(testutil.Context(t), ProjectSpec{Dir: dir, Content: map[string][]byte{".env": []byte("A=1\n")}}); err == nil {
		t.Error("content without a Compose file loaded")
	}
}
