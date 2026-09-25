package authz_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/authztest"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/manager/events"
)

func principal(id string) authz.Principal { return authz.Principal{Kind: authz.KindUser, UserID: id} }

func TestTargetResources(t *testing.T) {
	got := authz.TargetResources("e1", []domain.JobTarget{{Type: domain.TargetStack, ID: "s1"}, {Type: domain.TargetPath, ID: "/a"},
		{Type: domain.TargetDestinationPath, ID: "/b"}, {Type: domain.TargetRepository, ID: "r1"}, {Type: domain.TargetVolume, ID: "v", EnvironmentID: "e2"}})
	want := []authz.Resource{{Type: "stack", ID: "s1", EnvironmentID: "e1"}, {Type: "backup_repository", ID: "r1"}, {Type: "volume", ID: "v", EnvironmentID: "e2"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i].Type != want[i].Type || got[i].ID != want[i].ID || got[i].EnvironmentID != want[i].EnvironmentID {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
	// Paths without a root are authorized as paths (instance/environment rules only).
	if got := authz.TargetResources("e1", []domain.JobTarget{{Type: domain.TargetPath, ID: "/srv"}}); got[0].Type != "path" || got[0].EnvironmentID != "e1" {
		t.Fatalf("path-only %+v", got)
	}
	if got := authz.TargetResources("e1", nil); got[0].Type != catalog.TypeEnvironment || got[0].ID != "e1" {
		t.Fatalf("no targets %+v", got)
	}
	if got := authz.TargetResources("", nil); got[0].Type != catalog.TypeInstance {
		t.Fatalf("instance job %+v", got)
	}
}

// TestJobVisibility: job.read/job.cancel follow the job's targets or the
// kind's own capability on every target, never the initiator.
func TestJobVisibility(t *testing.T) {
	ctx := context.Background()
	job := domain.Job{ID: "j", Kind: "container.restart", EnvironmentID: "e1", InitiatorUserID: "alice",
		Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}}}
	r := authz.JobResource(job)
	if !slices.Equal(r.JobCapabilities, []string{"container.restart"}) {
		t.Fatalf("job capabilities %v", r.JobCapabilities)
	}
	cases := []struct {
		name       string
		pol        *authztest.Policy
		read, stop bool
	}{
		{"initiator without grants", authztest.Only("alice"), false, false},
		{"job.read on the environment", authztest.Only("alice", "allow job.read @env:e1"), true, false},
		{"job.read elsewhere", authztest.Only("alice", "allow job.read @env:e2"), false, false},
		{"kind capability on the target", authztest.Only("alice", "allow container.restart @container:e1/web"), true, true},
		{"kind capability on another target", authztest.Only("alice", "allow container.restart @container:e1/db"), false, false},
		{"job.cancel via stack", authztest.Only("alice", "allow job.read @all", "allow job.cancel @env:e1"), true, true},
	}
	for _, c := range cases {
		if got := c.pol.Can(ctx, principal("alice"), "job.read", r).Allowed; got != c.read {
			t.Errorf("%s: job.read %v", c.name, got)
		}
		if got := c.pol.Can(ctx, principal("alice"), "job.cancel", r).Allowed; got != c.stop {
			t.Errorf("%s: job.cancel %v", c.name, got)
		}
	}
	// A multi-target job needs every target.
	multi := authz.JobResource(domain.Job{ID: "m", Kind: "stack.deploy", EnvironmentID: "e1",
		Targets: []domain.JobTarget{{Type: domain.TargetStack, ID: "s1"}, {Type: domain.TargetStack, ID: "s2"}}})
	if authztest.Only("bob", "allow job.read @stack:s1").Can(ctx, principal("bob"), "job.read", multi).Allowed {
		t.Fatal("partial target coverage shows the job")
	}
	if d := authztest.New().Owner("o").Can(ctx, principal("o"), "stack.read", multi); d.Allowed {
		t.Fatal("non-job capability on a job allowed")
	}
}

func TestViewLevels(t *testing.T) {
	ctx := context.Background()
	env := authz.EnvironmentResource("e1")
	c := func(rules ...string) authz.Checker {
		return authz.For(ctx, authztest.Only("u", rules...), principal("u"))
	}
	cases := []struct {
		name    string
		checker authz.Checker
		level   authz.Level
		actions []string
	}{
		{"nothing", c(), authz.Hidden, nil},
		{"read", c("allow environment.read @env:e1"), authz.Full, []string{"environment.read"}},
		{"metrics only", c("allow environment.metrics.read @all"), authz.Minimal, []string{"environment.metrics.read"}},
		{"container inside", c("allow container.restart @container:e1/web"), authz.Minimal, nil},
		{"container elsewhere", c("allow container.restart @container:e2/web"), authz.Hidden, nil},
		{"all containers", c("allow container.logs.read @all"), authz.Minimal, nil},
		{"denied inside", c("allow container.restart @env:e1", "deny container.restart @all"), authz.Minimal, nil},
		{"instance-only capability", c("allow audit.read @all"), authz.Hidden, nil},
	}
	for _, tc := range cases {
		v := authz.ViewOf(tc.checker, env)
		if v.Level != tc.level || !slices.Equal(v.Actions, tc.actions) || v.Visible() != (tc.level != authz.Hidden) {
			t.Errorf("%s: %+v (%s)", tc.name, v, v.Level)
		}
	}
	// Without a Reacher (plain Func authorizer) only direct capabilities count.
	f := authz.For(ctx, authz.Func(func(_ context.Context, _ authz.Principal, c string, _ authz.Resource) authz.Decision {
		return authz.Decision{Allowed: c == "environment.metrics.read"}
	}), principal("u"))
	if v := authz.ViewOf(f, env); v.Level != authz.Minimal {
		t.Fatalf("func checker view %+v", v)
	}
	if authz.Minimal.String() != "minimal" || authz.Full.String() != "full" || authz.Hidden.String() != "hidden" {
		t.Fatal("level strings")
	}
}

// TestEveryEventTypeHasAVisibilityRule (#17/#29): every event type of the
// manager bus has a filtering rule.
func TestEveryEventTypeHasAVisibilityRule(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join("..", "events", "events.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			vs := s.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if len(vs.Values) <= i || strings.HasPrefix(name.Name, "Resource") {
					continue // resource type constants
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, _ := strconv.Unquote(lit.Value)
				n++
				if !authz.HasEventRule(v) {
					t.Errorf("event type %s (%s) has no visibility rule in authz/events.go", v, name.Name)
				}
			}
		}
	}
	if n < 18 {
		t.Fatalf("found only %d event types", n)
	}
}

func TestEventVisibility(t *testing.T) {
	ctx := context.Background()
	c := func(rules ...string) authz.Checker {
		return authz.For(ctx, authztest.Only("u", rules...), principal("u"))
	}
	online := events.Event{Type: events.EnvironmentOnline, ResourceType: events.ResourceEnvironment, ResourceID: "e1", EnvironmentID: "e1"}
	docker := events.Event{Type: events.DockerEvent, ResourceType: events.ResourceContainer, ResourceID: "web", EnvironmentID: "e1"}
	files := events.Event{Type: events.FilesInvalidated, ResourceType: events.ResourceFileScope, ResourceID: "stack:s1", EnvironmentID: "e1",
		Paths: []string{"compose.yaml"}, Attributes: map[string]string{"scopeKind": "stack", "scopeId": "s1"}}
	enroll := events.Event{Type: events.EnrollmentCreated, ResourceType: events.ResourceEnrollment, ResourceID: "x"}
	job := events.Event{Type: events.JobUpdated, ResourceType: events.ResourceJob, ResourceID: "j", EnvironmentID: "e1",
		Job: &domain.Job{ID: "j", Kind: "container.restart", EnvironmentID: "e1", Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}}}}
	policy := events.Event{Type: events.ResourceChanged, ResourceType: "backup_policy", ResourceID: "p1", Attributes: map[string]string{"action": "backup_policy.manage"}}
	group := events.Event{Type: events.ResourceChanged, ResourceType: "group", ResourceID: "g1"}
	token := events.Event{Type: events.ResourceChanged, ResourceType: "api_token", ResourceID: "t1"}
	unknown := events.Event{Type: "future.event"}
	cases := []struct {
		name  string
		c     authz.Checker
		event events.Event
		want  bool
	}{
		{"restricted: environment status", c(), online, false},
		{"metrics-only: environment status", c("allow environment.metrics.read @env:e1"), online, true},
		{"restart-only: container event", c("allow container.restart @container:e1/web"), docker, true},
		{"restart-only: other container", c("allow container.restart @container:e1/db"), docker, false},
		{"stack read: file paths", c("allow stack.read @all"), files, false},
		{"stack files: file paths", c("allow stack.files.read @stack:s1"), files, true},
		{"enrollments need agent.enroll", c("allow agent.read @all"), enroll, false},
		{"agent.enroll", c("allow agent.enroll @all"), enroll, true},
		{"unknown types: not for users", c("allow environment.read @all"), unknown, false},
		{"job: job.read on its environment", c("allow job.read @env:e1"), job, true},
		{"job: the kind's capability on the target", c("allow container.restart @container:e1/web"), job, true},
		{"job: other environment", c("allow job.read @env:e2"), job, false},
		{"job without its record", c("allow job.read @all"), events.Event{Type: events.JobUpdated, ResourceID: "j"}, false},
		{"policy change: policy readers", c("allow backup_policy.read @all"), policy, true},
		{"policy change: others", c("allow environment.read @all"), policy, false},
		{"group change: users", c("allow environment.read @all"), group, false},
		{"token change: users", c("allow api_tokens.create @all"), token, false},
	}
	for _, tc := range cases {
		if got := authz.EventVisible(tc.c, tc.event); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	owner := authz.For(ctx, authztest.New().Owner("o"), principal("o"))
	if !authz.EventVisible(owner, unknown) || !authz.EventVisible(owner, files) || !authz.EventVisible(owner, group) || !authz.EventVisible(owner, token) {
		t.Fatal("owner sees every event")
	}
}
