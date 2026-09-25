package imageref

import "testing"

func TestParseNormalizes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"nginx", "docker.io/library/nginx:latest"},
		{"nginx:1.27", "docker.io/library/nginx:1.27"},
		{"docker.io/nginx", "docker.io/library/nginx:latest"},
		{"index.docker.io/nginx", "docker.io/library/nginx:latest"},
		{"registry-1.docker.io/nginx:1", "docker.io/library/nginx:1"},
		{"registry.hub.docker.com/library/nginx", "docker.io/library/nginx:latest"},
		{"Docker.IO/acme/app:2", "docker.io/acme/app:2"},
		{"acme/app", "docker.io/acme/app:latest"},
		{"ghcr.io/Owner-x/app:v1", ""}, // upper case repository is invalid
		{"ghcr.io/owner/app:v1", "ghcr.io/owner/app:v1"},
		{"GHCR.io/owner/app", "ghcr.io/owner/app:latest"},
		{"quay.io/org/team/app:3", "quay.io/org/team/app:3"},
		{"registry.lan:5000/team/app", "registry.lan:5000/team/app:latest"},
		{"localhost:5000/app", "localhost:5000/app:latest"},
		{"localhost/app", "localhost/app:latest"},
		{"10.0.0.2:5000/app:1", "10.0.0.2:5000/app:1"},
		{"ghcr.io/o/a@sha256:" + sha, "ghcr.io/o/a@sha256:" + sha},
		{"ghcr.io/o/a:1@sha256:" + sha, "ghcr.io/o/a:1@sha256:" + sha},
		{"", ""},
		{"https://ghcr.io/o/a", ""},
		{"ghcr.io/o/a:bad tag", ""},
	}
	for _, c := range cases {
		r, err := Parse(c.in)
		if c.want == "" {
			if err == nil {
				t.Errorf("Parse(%q) = %v, want error", c.in, r)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q): %v", c.in, err)
			continue
		}
		if r.String() != c.want {
			t.Errorf("Parse(%q) = %q, want %q", c.in, r.String(), c.want)
		}
	}
}

const sha = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestNormalizeHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"docker.io", "docker.io"},
		{"index.docker.io", "docker.io"},
		{"https://index.docker.io/v1/", "docker.io"},
		{"registry-1.docker.io", "docker.io"},
		{"registry.hub.docker.com", "docker.io"},
		{"GHCR.IO", "ghcr.io"},
		{"https://ghcr.io/", "ghcr.io"},
		{"registry.lan:5000", "registry.lan:5000"},
		{"localhost:5000", "localhost:5000"},
		{"localhost", "localhost"},
		{"[::1]:5000", "[::1]:5000"},
		{"", ""},
		{"intranet", ""},
		{"a b", ""},
		{"user@host.example", ""},
	}
	for _, c := range cases {
		got, err := NormalizeHost(c.in)
		if c.want == "" {
			if err == nil {
				t.Errorf("NormalizeHost(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("NormalizeHost(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestPatterns(t *testing.T) {
	cases := []struct {
		host, pattern, want string
		ok                  bool
	}{
		{"docker.io", "", "", true},
		{"docker.io", "nginx", "library/nginx", true},
		{"docker.io", "acme/*", "acme/*", true},
		{"docker.io", "/acme/app/", "acme/app", true},
		{"ghcr.io", "org/team/*", "org/team/*", true},
		{"ghcr.io", "org/*/app", "", false},
		{"ghcr.io", "*", "", false},
		{"ghcr.io", "org/app:1", "", false},
		{"ghcr.io", "Org/app", "", false},
	}
	for _, c := range cases {
		p, err := ParsePattern(c.host, c.pattern)
		if !c.ok {
			if err == nil {
				t.Errorf("ParsePattern(%q, %q) = %q, want error", c.host, c.pattern, p)
			}
			continue
		}
		if err != nil || string(p) != c.want {
			t.Errorf("ParsePattern(%q, %q) = %q, %v; want %q", c.host, c.pattern, p, err, c.want)
		}
	}

	match := []struct {
		pattern, repo string
		ok            bool
		spec          int
	}{
		{"", "anything/here", true, 0},
		{"acme/*", "acme/app", true, 2},
		{"acme/*", "acme/team/app", true, 2},
		{"acme/*", "acmex/app", false, 0},
		{"acme/*", "acme", false, 0},
		{"acme/team/*", "acme/team/app", true, 4},
		{"acme/app", "acme/app", true, 5},
		{"acme/app", "acme/app2", false, 0},
	}
	for _, m := range match {
		ok, spec := Pattern(m.pattern).Match(m.repo)
		if ok != m.ok || spec != m.spec {
			t.Errorf("%q.Match(%q) = %v, %d; want %v, %d", m.pattern, m.repo, ok, spec, m.ok, m.spec)
		}
	}
}

func TestAddresses(t *testing.T) {
	if APIHost("docker.io") != "registry-1.docker.io" || APIHost("ghcr.io") != "ghcr.io" {
		t.Fatal("APIHost")
	}
	if ServerAddress("docker.io") != DockerHubServerAddress || ServerAddress("registry.lan:5000") != "registry.lan:5000" {
		t.Fatal("ServerAddress")
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"nginx", "ghcr.io/o/a:1", "registry.lan:5000/a@sha256:" + sha, "index.docker.io/x", ":::", "0A/0"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r, err := Parse(s)
		if err != nil {
			return
		}
		again, err := Parse(r.String())
		if err != nil || again != r {
			t.Fatalf("Parse(%q) = %v; reparse %v, %v", s, r, again, err)
		}
	})
}
