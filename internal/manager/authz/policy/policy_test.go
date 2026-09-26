package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
)

var parseRule = ParseRule

func parseRules(t *testing.T, cat *catalog.Catalog, in []string) []Rule {
	t.Helper()
	out := make([]Rule, 0, len(in))
	for _, s := range in {
		r, err := parseRule(cat, s)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

type corpusCase struct {
	Name    string `yaml:"name"`
	Note    string `yaml:"note"`
	Catalog string `yaml:"catalog"`
	Subject struct {
		Owner    bool     `yaml:"owner"`
		Inactive bool     `yaml:"inactive"`
		User     []string `yaml:"user"`
		Group    []string `yaml:"group"`
		Token    []string `yaml:"token"`
	} `yaml:"subject"`
	Check struct {
		Capability string `yaml:"capability"`
		Resource   struct {
			Type    string   `yaml:"type"`
			ID      string   `yaml:"id"`
			Env     string   `yaml:"env"`
			Parents []string `yaml:"parents"`
		} `yaml:"resource"`
	} `yaml:"check"`
	Want struct {
		Decision string `yaml:"decision"`
		Source   string `yaml:"source"`
		Rule     string `yaml:"rule"`
	} `yaml:"want"`
}

// futureCatalog is catalog v2: v1 plus a new container key.
func futureCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cur := catalog.Default()
	snap, _ := cur.Lookup("container.restart")
	snap.Key, snap.Label, snap.Description, snap.Since = "container.snapshot", "Snapshot", "Checkpoint a container.", 2
	c, err := catalog.New(2, cur.Types(), append(cur.Capabilities(), snap))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestDecisionCorpus runs testdata/corpus.yaml: allow/deny/inherit,
// wildcard vs exact, user over group, group moves, stack-scoped dynamic
// scope, moved and deleted resources, future keys and API tokens.
func TestDecisionCorpus(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "corpus.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	var corpus struct {
		Cases []corpusCase `yaml:"cases"`
	}
	if err := dec.Decode(&corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus.Cases) < 50 {
		t.Fatalf("corpus has %d cases", len(corpus.Cases))
	}
	names := map[string]bool{}
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if names[c.Name] {
				t.Fatalf("duplicate case name")
			}
			names[c.Name] = true
			cat := catalog.Default()
			if c.Catalog == "future" {
				cat = futureCatalog(t)
			}
			s := Subject{Owner: c.Subject.Owner, Inactive: c.Subject.Inactive,
				UserRules: parseRules(t, cat, c.Subject.User), GroupRules: parseRules(t, cat, c.Subject.Group)}
			if c.Subject.Token != nil {
				s.Token = parseRules(t, cat, c.Subject.Token)
			}
			for _, set := range [][]Rule{s.UserRules, s.GroupRules, s.Token} {
				if errs := Validate(cat, set); len(errs) > 0 {
					t.Fatalf("corpus rules invalid: %v", errs)
				}
			}
			res := c.Check.Resource
			tg := Target{Type: res.Type, ID: res.ID, EnvironmentID: res.Env}
			for _, p := range res.Parents {
				typ, id, _ := strings.Cut(p, ":")
				id, env, _ := strings.Cut(id, "@")
				tg.Parents = append(tg.Parents, Ref{Type: typ, ID: id, EnvironmentID: env})
			}
			d := Evaluate(cat, s, c.Check.Capability, tg)
			if want := c.Want.Decision == "allow"; d.Allowed != want || string(d.Source) != c.Want.Source {
				t.Fatalf("got allowed=%v source=%s (%s), want %s/%s", d.Allowed, d.Source, d.Reason, c.Want.Decision, c.Want.Source)
			}
			if d.Reason == "" {
				t.Fatal("decision without a reason")
			}
			if (d.Source == SourceUser || d.Source == SourceGroup) != (d.Rule != nil) {
				t.Fatalf("rule %v for source %s", d.Rule, d.Source)
			}
			if c.Want.Rule != "" {
				want, err := parseRule(cat, c.Want.Rule)
				if err != nil {
					t.Fatal(err)
				}
				if d.Rule == nil || *d.Rule != want {
					t.Fatalf("decided by %v, want %v", d.Rule, want)
				}
			}
		})
	}
}

// scopeFor returns a rule scope of kind for capability cp that matches the
// probe target of the capability's type, plus that target.
func probe(cat *catalog.Catalog, cp catalog.Capability) (Target, []Scope) {
	typ := cp.Type
	rt, _ := cat.Type(typ)
	tg := Target{Type: typ, ID: "r1", EnvironmentID: "e1"}
	if !rt.EnvironmentBound {
		tg.EnvironmentID = ""
	}
	if typ == catalog.TypeEnvironment {
		tg.ID = "e1"
	}
	if !rt.Scopable && typ != catalog.TypeEnvironment {
		tg.ID = ""
	}
	var scopes []Scope
	if cp.Instance {
		scopes = append(scopes, Instance())
	}
	if cp.Environment && tg.EnvironmentID != "" {
		scopes = append(scopes, Environment("e1"))
	}
	for _, r := range cp.Resources {
		prt, _ := cat.Type(r)
		env := ""
		if prt.NamedPerEnvironment {
			env = "e1"
		}
		if r == typ {
			scopes = append(scopes, Resource(r, env, "r1"))
			continue
		}
		// A parent type: the probe lives inside it.
		if tg.ID != "" {
			tg.Parents = append(tg.Parents, Ref{Type: r, ID: "p-" + r, EnvironmentID: env})
			scopes = append(scopes, Resource(r, env, "p-"+r))
		}
	}
	return tg, scopes
}

// TestEveryCapabilityScopeAndOverride is the exhaustive precedence matrix
// (verification matrix V20): for every grantable capability and every
// scope it supports, a group allow at that scope is granted, a user deny
// at the widest scope overrides it, a user allow restores it over a group
// deny, and no other capability key is granted by it.
func TestEveryCapabilityScopeAndOverride(t *testing.T) {
	cat := catalog.Default()
	checked := 0
	for _, cp := range cat.Capabilities() {
		if cp.OwnerOnly {
			if d := Evaluate(cat, Subject{GroupRules: []Rule{{cp.Key, Instance(), Allow}}}, cp.Key, Target{Type: catalog.TypeInstance}); d.Allowed {
				t.Errorf("owner-only %s granted by a rule", cp.Key)
			}
			if errs := Validate(cat, []Rule{{cp.Key, Instance(), Allow}}); len(errs) == 0 {
				t.Errorf("owner-only %s accepted in a rule", cp.Key)
			}
			continue
		}
		tg, scopes := probe(cat, cp)
		for _, sc := range scopes {
			if sc.Kind == ScopeResource && tg.ID == "" {
				continue
			}
			allow := Rule{cp.Key, sc, Allow}
			if errs := Validate(cat, []Rule{allow}); len(errs) > 0 {
				t.Errorf("%s: %v", allow, errs)
				continue
			}
			name := cp.Key + " @" + sc.String()
			if d := Evaluate(cat, Subject{}, cp.Key, tg); d.Allowed {
				t.Errorf("%s: allowed without rules", name)
			}
			if d := Evaluate(cat, Subject{GroupRules: []Rule{allow}}, cp.Key, tg); !d.Allowed || d.Source != SourceGroup {
				t.Errorf("%s: group allow not granted: %+v", name, d)
			}
			userDeny := Rule{cp.Key, widest(cp, tg), Deny}
			if d := Evaluate(cat, Subject{GroupRules: []Rule{allow}, UserRules: []Rule{userDeny}}, cp.Key, tg); d.Allowed || d.Source != SourceUser {
				t.Errorf("%s: user deny %s did not override: %+v", name, userDeny, d)
			}
			groupDeny := Rule{cp.Key, sc, Deny}
			userAllow := Rule{cp.Key, widest(cp, tg), Allow}
			if d := Evaluate(cat, Subject{GroupRules: []Rule{groupDeny}, UserRules: []Rule{userAllow}}, cp.Key, tg); !d.Allowed || d.Source != SourceUser {
				t.Errorf("%s: user allow %s did not override the group deny: %+v", name, userAllow, d)
			}
			// Reset to inherit: without the user rule the group decides.
			if d := Evaluate(cat, Subject{GroupRules: []Rule{groupDeny}}, cp.Key, tg); d.Allowed || d.Source != SourceGroup {
				t.Errorf("%s: inherit did not restore the group decision: %+v", name, d)
			}
			for _, other := range cat.Capabilities() {
				if other.Key == cp.Key {
					continue
				}
				if d := Evaluate(cat, Subject{GroupRules: []Rule{allow}}, other.Key, tg); d.Allowed {
					t.Errorf("%s also granted %s", name, other.Key)
				}
			}
			checked++
		}
	}
	if checked < 200 {
		t.Fatalf("only %d capability/scope combinations checked", checked)
	}
}

// widest is the least specific scope cp supports that matches tg.
func widest(cp catalog.Capability, tg Target) Scope {
	switch {
	case cp.Instance:
		return Instance()
	case cp.Environment && tg.EnvironmentID != "":
		return Environment(tg.EnvironmentID)
	}
	return Resource(tg.Type, tg.EnvironmentID, tg.ID)
}

func TestValidateRules(t *testing.T) {
	cat := catalog.Default()
	bad := map[string]Rule{
		"unknown capability":        {"container.teleport", Instance(), Allow},
		"owner-only":                {"users.manage", Instance(), Allow},
		"bad effect":                {"container.restart", Instance(), "maybe"},
		"instance with environment": {"container.restart", Scope{Kind: ScopeInstance, EnvironmentID: "e1"}, Allow},
		"environment without id":    {"container.restart", Scope{Kind: ScopeEnvironment}, Allow},
		"environment not allowed":   {"audit.read", Environment("e1"), Allow},
		"instance not allowed":      {"environment.read", Resource("environment", "", "e1"), Allow},
		"resource type mismatch":    {"container.restart", Resource("volume", "e1", "data"), Allow},
		"missing resource id":       {"container.restart", Resource("container", "e1", ""), Allow},
		"named without environment": {"container.restart", Resource("container", "", "web"), Allow},
		"global id with env":        {"stack.read", Resource("stack", "e1", "s1"), Allow},
		"unknown scope kind":        {"stack.read", Scope{Kind: "planet"}, Allow},
		"create on one resource":    {"stack.create", Resource("stack", "", "s1"), Allow},
	}
	for name, r := range bad {
		if errs := Validate(cat, []Rule{r}); len(errs) == 0 {
			t.Errorf("%s: %v accepted", name, r)
		}
	}
	good := []Rule{
		{"container.restart", Instance(), Allow},
		{"container.restart", Environment("e1"), Deny},
		{"container.restart", Resource("stack", "", "s1"), Allow},
		{"container.restart", Resource("service", "", "s1/web"), Allow},
		{"container.restart", Resource("container", "e1", "web"), Deny},
		{"stack.read", Resource("stack", "", "s1"), Allow},
	}
	if errs := Validate(cat, good); len(errs) > 0 {
		t.Fatalf("valid rules rejected: %v", errs)
	}
	// Ambiguous duplicates are rejected, whatever their effects.
	for _, second := range []Effect{Allow, Deny} {
		errs := Validate(cat, []Rule{{"container.restart", Environment("e1"), Allow}, {"container.restart", Environment("e1"), second}})
		if len(errs) != 1 || errs[0].Index != 1 || !strings.Contains(errs[0].Error(), "duplicates rule 0") {
			t.Fatalf("duplicate (%s) not rejected: %v", second, errs)
		}
	}
}

func TestShorthandRoundTrip(t *testing.T) {
	cat := catalog.Default()
	for _, s := range []string{"allow container.restart @all", "deny stack.read @env:e1", "allow container.logs.read @stack:s1",
		"allow container.restart @container:e1/web", "deny container.exec @service:s1/web"} {
		r, err := ParseRule(cat, s)
		if err != nil || r.Shorthand() != s {
			t.Errorf("%q -> %+v %v -> %q", s, r, err, r.Shorthand())
		}
	}
	for _, bad := range []string{"allow container.restart", "allow container.restart all", "allow container.restart @container:web",
		"allow x @stack:", "a b c d"} {
		if _, err := ParseRule(cat, bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	defer func() {
		if recover() == nil {
			t.Fatal("MustParseRules accepted an invalid rule")
		}
	}()
	MustParseRules(cat, "allow users.manage @all")
}

func TestDecisionReasonsAreReadable(t *testing.T) {
	cat := catalog.Default()
	s := Subject{GroupRules: []Rule{{"container.restart", Resource("container", "e1", "web"), Allow}}}
	d := Evaluate(cat, s, "container.restart", Target{Type: "container", ID: "web", EnvironmentID: "e1"})
	if d.Reason != "group rule: allow container.restart on container web in environment e1" {
		t.Fatalf("reason %q", d.Reason)
	}
	if Instance().String() != "all resources" || Environment("e1").String() != "environment e1" {
		t.Fatal("scope strings")
	}
}
