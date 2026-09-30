package gitremote_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/gitremote"
	"github.com/neurekadev/docker-manager/internal/gitremote/gittest"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

var (
	mainSHA  = strings.Repeat("a", 40)
	devSHA   = strings.Repeat("b", 40)
	tagObj   = strings.Repeat("c", 40)
	tagSHA   = strings.Repeat("d", 40)
	lightTag = strings.Repeat("e", 40)
)

func repo(private bool) *gittest.Repo {
	return &gittest.Repo{Head: "refs/heads/main", Private: private,
		Refs: map[string]string{"refs/heads/main": mainSHA, "refs/heads/dev": devSHA, "refs/tags/v1": tagObj, "refs/tags/light": lightTag,
			"refs/heads/v1": devSHA},
		Peeled: map[string]string{"refs/tags/v1": tagSHA}}
}

func TestLsRemoteAndResolve(t *testing.T) {
	for _, dumb := range []bool{false, true} {
		name := "smart"
		if dumb {
			name = "dumb"
		}
		t.Run(name, func(t *testing.T) {
			s := gittest.New(t, true, "bot", "token-canary-123")
			s.Dumb = dumb
			s.Add("/org/app.git", repo(false))
			r, err := gitremote.ParseURL(s.URL("/org/app.git"))
			if err != nil {
				t.Fatal(err)
			}
			refs, err := gitremote.LsRemote(testutil.Context(t), r, gitremote.Options{HTTP: s.Client()})
			if err != nil {
				t.Fatal(err)
			}
			if refs.Smart == dumb || refs.Head != "refs/heads/main" {
				t.Fatalf("refs %+v", refs)
			}
			for ref, want := range map[string][2]string{
				"":                {mainSHA, "refs/heads/main"},
				"main":            {mainSHA, "refs/heads/main"},
				"dev":             {devSHA, "refs/heads/dev"},
				"v1":              {tagSHA, "refs/tags/v1"}, // tags before branches, peeled to the commit
				"refs/heads/v1":   {devSHA, "refs/heads/v1"},
				"light":           {lightTag, "refs/tags/light"},
				"refs/heads/main": {mainSHA, "refs/heads/main"},
				devSHA:            {devSHA, ""},
			} {
				c, full, err := refs.Resolve(ref)
				if err != nil || c != want[0] || full != want[1] {
					t.Errorf("Resolve(%q) = %s %s %v, want %v", ref, c, full, err, want)
				}
			}
			for _, bad := range []string{"missing", "refs/heads/missing", "refs/tags/v1^{}"} {
				if _, _, err := refs.Resolve(bad); gitremote.ClassOf(err) != gitremote.ClassRefNotFound {
					t.Errorf("Resolve(%q) = %v", bad, err)
				}
			}
		})
	}
}

func TestPrivateRepositoryAuth(t *testing.T) {
	s := gittest.New(t, true, "bot", "token-canary-123")
	s.Add("/org/private.git", repo(true))
	r, _ := gitremote.ParseURL(s.URL("/org/private.git"))
	ctx := testutil.Context(t)
	_, err := gitremote.LsRemote(ctx, r, gitremote.Options{HTTP: s.Client()})
	if gitremote.ClassOf(err) != gitremote.ClassUnauthorized {
		t.Fatalf("anonymous: %v", err)
	}
	refs, err := gitremote.LsRemote(ctx, r, gitremote.Options{HTTP: s.Client(), Credential: &gitremote.Credential{Username: "bot", Token: "token-canary-123"}})
	if err != nil || refs.Commits["refs/heads/main"] != mainSHA {
		t.Fatalf("%+v %v", refs, err)
	}
	_, err = gitremote.LsRemote(ctx, r, gitremote.Options{HTTP: s.Client(), Credential: &gitremote.Credential{Username: "bot", Token: "wrong-token-456"}})
	if gitremote.ClassOf(err) != gitremote.ClassUnauthorized || !strings.Contains(err.Error(), "rejected the credential") ||
		strings.Contains(err.Error(), "wrong-token") {
		t.Fatalf("wrong token: %v", err)
	}
}

func TestPlainHTTPCredentialsNeedOptIn(t *testing.T) {
	s := gittest.New(t, false, "bot", "token-canary-123")
	s.Add("/org/private.git", repo(true))
	r, _ := gitremote.ParseURL(s.URL("/org/private.git"))
	ctx := testutil.Context(t)
	cred := &gitremote.Credential{Username: "bot", Token: "token-canary-123"}
	if _, err := gitremote.LsRemote(ctx, r, gitremote.Options{HTTP: s.Client(), Credential: cred}); gitremote.ClassOf(err) != gitremote.ClassInvalidURL {
		t.Fatalf("plain http with credential: %v", err)
	}
	if s.Hits() != 0 {
		t.Fatal("the server was contacted")
	}
	if _, err := gitremote.LsRemote(ctx, r, gitremote.Options{HTTP: s.Client(), Credential: cred, AllowPlainHTTP: true}); err != nil {
		t.Fatal(err)
	}
}

func TestServerFailures(t *testing.T) {
	s := gittest.New(t, true, "u", "p")
	s.Add("/org/app.git", repo(false))
	ctx := testutil.Context(t)
	missing, _ := gitremote.ParseURL(s.URL("/org/missing.git"))
	if _, err := gitremote.LsRemote(ctx, missing, gitremote.Options{HTTP: s.Client()}); gitremote.ClassOf(err) != gitremote.ClassNotFound {
		t.Fatalf("404: %v", err)
	}
	r, _ := gitremote.ParseURL(s.URL("/org/app.git"))
	for status, class := range map[int]string{http.StatusForbidden: gitremote.ClassForbidden, http.StatusTooManyRequests: gitremote.ClassRateLimited,
		http.StatusBadGateway: gitremote.ClassUnavailable, http.StatusTeapot: gitremote.ClassInvalidResponse} {
		s.FailStatus = status
		if _, err := gitremote.LsRemote(ctx, r, gitremote.Options{HTTP: s.Client()}); gitremote.ClassOf(err) != class {
			t.Errorf("%d: %v, want %s", status, err, class)
		}
	}
}

func TestParseURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://GitHub.com/Org/Repo.git":     "https://github.com/Org/Repo.git",
		"https://git.example.com:8443/a/b/":   "https://git.example.com:8443/a/b",
		"http://gitserver/public.git":         "http://gitserver/public.git",
		"https://git.example.com/a%20b/c.git": "",
		"ssh://git@github.com/org/repo.git":   "",
		"git@github.com:org/repo.git":         "",
		"https://user:pw@github.com/o/r.git":  "",
		"https://github.com/o/r.git#main":     "",
		"https://github.com/o/r.git?x=1":      "",
		"https://github.com":                  "",
		"https:///o/r.git":                    "",
		"":                                    "",
	} {
		r, err := gitremote.ParseURL(in)
		if want == "" {
			if err == nil {
				t.Errorf("ParseURL(%q) = %s, want error", in, r)
			}
			continue
		}
		if err != nil || r.String() != want {
			t.Errorf("ParseURL(%q) = %s, %v; want %s", in, r, err, want)
		}
	}
}
