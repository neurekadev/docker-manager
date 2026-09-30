package compose

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/lifecycle"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestLoadFromContent (#7): a definition that is not on disk yet loads
// from memory like it would from its project directory (env interpolation
// from its .env only), and every service carries Docker Manager's dependency
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
		"compose.override.yaml": []byte("services:\n  web:\n    labels:\n      docker-manager.description: Web frontend\n"),
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
	var description string
	for _, s := range p.Services {
		if s.Name == "web" {
			description = s.Description
		}
	}
	if description != "Web frontend" {
		t.Errorf("description label %q", description)
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

// TestDescriptionLabelKeys: the description label is read under its
// current key and under its legacy key (Compose files written before the
// label prefix changed); the current key wins when a service has both.
func TestDescriptionLabelKeys(t *testing.T) {
	p, err := LoadProject(testutil.Context(t), ProjectSpec{Dir: filepath.Join(t.TempDir(), "app"), Content: map[string][]byte{
		"compose.yaml": []byte(`services:
  db:
    image: db:1
    labels:
      dev.neureka.docker-manager.description: Legacy database
  web:
    image: web:1
    labels:
      docker-manager.description: Current web
      dev.neureka.docker-manager.description: Old web
  cache:
    image: cache:1
`)}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range p.Services {
		got[s.Name] = s.Description
	}
	if got["db"] != "Legacy database" || got["web"] != "Current web" || got["cache"] != "" {
		t.Errorf("descriptions %v", got)
	}
	// Docker Manager writes its dependency label under the current key only.
	for name, s := range p.model.Services {
		if _, ok := s.CustomLabels[lifecycle.DependsOnLabel]; !ok || lifecycle.DependsOnLabel != "docker-manager.depends_on" {
			t.Errorf("service %s lacks %s", name, lifecycle.DependsOnLabel)
		}
		for k := range s.CustomLabels {
			if strings.HasPrefix(k, "dev.neureka.") {
				t.Errorf("service %s gets the legacy label %s", name, k)
			}
		}
	}
}
