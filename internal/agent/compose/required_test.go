package compose

import (
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/engine/enginetest"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// A dependency on a service disabled by its profile is fine with
// required: false (the SDK only warns) but invalid with required: true.
func TestRequiredDependencyOnDisabledService(t *testing.T) {
	a := newAdapter(t, enginetest.Start(t, enginetest.Options{}))
	yaml := `
services:
  web:
    image: app
    depends_on:
      cache:
        condition: service_started
        required: REQUIRED
  cache:
    image: cache
    profiles: [extras]
`
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"compose.yaml": strings.Replace(yaml, "REQUIRED", "false", 1)})
	p, err := a.Load(testutil.Context(t), ProjectSpec{Dir: dir, Name: "optional"})
	if err != nil {
		t.Fatalf("optional dependency on a disabled service: %v", err)
	}
	if len(p.Services) != 1 || p.Services[0].Name != "web" {
		t.Errorf("services %+v", p.Services)
	}

	dir = t.TempDir()
	writeFiles(t, dir, map[string]string{"compose.yaml": strings.Replace(yaml, "REQUIRED", "true", 1)})
	_, err = a.Load(testutil.Context(t), ProjectSpec{Dir: dir, Name: "required"})
	t.Logf("required dependency on a disabled service: %v", err)
	if engine.CodeOf(err) != engine.CodeInvalidProject {
		t.Fatalf("required dependency on a disabled service must fail: %v", err)
	}
}
