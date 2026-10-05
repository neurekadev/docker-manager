package domain

import (
	"slices"
	"testing"
)

// TestTouchedServices: like the agent's lifecycle, a start also starts the
// services depended on, a restart the dependents declaring restart: true,
// transitively; a stop only the services named.
func TestTouchedServices(t *testing.T) {
	st := Stack{Services: []StackServiceDef{
		{Name: "web", DependsOn: []StackDependency{{Service: "api"}}},
		{Name: "api", DependsOn: []StackDependency{{Service: "db", Restart: true}, {Service: "cache"}}},
		{Name: "worker", DependsOn: []StackDependency{{Service: "db"}}},
		{Name: "db"}, {Name: "cache"},
	}}
	for _, c := range []struct {
		action   string
		services []string
		want     []string
	}{
		{"start", []string{"web"}, []string{"api", "cache", "db", "web"}},
		{"stop", []string{"db"}, []string{"db"}},
		{"restart", []string{"db"}, []string{"api", "db"}},
		{"restart", []string{"cache"}, []string{"cache"}},
		{"start", []string{"ghost"}, []string{"ghost"}},
	} {
		if got := st.TouchedServices(c.action, c.services); !slices.Equal(got, c.want) {
			t.Errorf("%s %v: got %v, want %v", c.action, c.services, got, c.want)
		}
	}
}
