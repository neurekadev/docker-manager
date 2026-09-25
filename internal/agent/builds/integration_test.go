//go:build integration

package builds_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/builds"
	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/gitremote"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

// Git builds (#33) on two DinD Engines against the Git server fixture
// (dumb HTTP; private* repositories need basic auth): the executor resolves
// the ref in process, BuildKit fetches exactly that commit with the Git
// credential served as a session secret, and no credential reaches the
// progress, the result or the journal.

type memJournal struct {
	mu    sync.Mutex
	saves []string
}

func (m *memJournal) Save(_ context.Context, st *jobexec.State) error {
	b, _ := json.Marshal(st)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saves = append(m.saves, string(b))
	return nil
}

type progress struct {
	mu   sync.Mutex
	msgs []string
}

func (p *progress) Progress(_ context.Context, _ *jobexec.State, pp protocol.ProgressPayload) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.msgs = append(p.msgs, pp.Message)
}

func TestGitBuildsOnTwoEngines(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Minute)
	defer cancel()
	nw := testharness.NewNetwork(t)
	git := testharness.StartGitServer(t, testharness.GitServerOptions{Network: nw})
	engines := testharness.StartEngines(t, 2, testharness.EngineOptions{Network: nw})
	set := canary.New()
	set.Register(canary.Password, "git fixture password", git.Password)

	// The Engines reach the server as http://gitserver; the test process
	// (standing in for the agent) through its published port.
	published := strings.TrimPrefix(git.URL, "http://")
	dialer := &net.Dialer{}
	hc := &http.Client{Timeout: time.Minute, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr == "gitserver:80" {
				addr = published
			}
			return dialer.DialContext(ctx, network, addr)
		}}}

	head := func(repo string, cred *gitremote.Credential) string {
		r, err := gitremote.ParseURL(git.RepoURL(git.InternalURL, repo, false))
		if err != nil {
			t.Fatal(err)
		}
		refs, err := gitremote.LsRemote(ctx, r, gitremote.Options{HTTP: hc, Credential: cred, AllowPlainHTTP: true})
		if err != nil {
			t.Fatal(err)
		}
		c, _, err := refs.Resolve("")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	secretsFor := func(password string) *protocol.CommandSecrets {
		return &protocol.CommandSecrets{Git: []protocol.GitCredential{{CredentialID: "g1", Host: "gitserver", Username: git.User,
			Secret: password, PlainHTTP: true}}}
	}

	for i, e := range engines {
		c, err := engine.Connect(ctx, engine.Options{Host: e.Host, Logger: testutil.Logger(t)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		exec := builds.Executor(builds.Options{Engine: func() engine.Engine { return c }, HTTP: hc, Logger: set.CaptureLogger(t)})
		run := func(repo, tag string, s *protocol.CommandSecrets, creds ...string) protocol.ResultPayload {
			in, _ := json.Marshal(jobspec.ImageBuildInput{GitURL: git.RepoURL(git.InternalURL, repo, false), Ref: testharness.GitDefaultBranch,
				Tags: []string{tag}, CredentialRefs: jobspec.CredentialRefs{GitCredentials: creds}})
			j, p := &memJournal{}, &progress{}
			res, err := jobexec.Run(ctx, exec, &jobexec.State{JobID: "job-" + tag, Attempt: 1, Kind: jobspec.ImageBuild, Input: in, Secrets: s},
				jobexec.Options{Journal: j, Reporter: p})
			if err != nil {
				t.Fatal(err)
			}
			set.AssertClean(t, "journal", j.saves)
			set.AssertClean(t, "progress", p.msgs)
			set.AssertClean(t, "result", res)
			return res
		}
		item := func(res protocol.ResultPayload, name string) string {
			for _, it := range res.Items {
				if it.Name == name {
					return it.Message
				}
			}
			return ""
		}

		// Public repository: the exact HEAD commit is built and recorded.
		tag := "dockyard-test/git-public:" + string(rune('a'+i))
		res := run("public", tag, nil)
		if res.Outcome != jobexec.OutcomeSucceeded || item(res, jobspec.BuildItemCommit) != head("public", nil) {
			t.Fatalf("engine %d public: %+v", i, res)
		}
		if d, err := c.InspectImage(ctx, tag); err != nil || d.ID != item(res, jobspec.BuildItemImage) {
			t.Fatalf("engine %d public image: %+v %v", i, d, err)
		}

		// Private repository with the Git credential.
		tag = "dockyard-test/git-private:" + string(rune('a'+i))
		res = run("private", tag, secretsFor(git.Password), "g1")
		want := head("private", &gitremote.Credential{Username: git.User, Token: logging.Secret(git.Password)})
		if res.Outcome != jobexec.OutcomeSucceeded || item(res, jobspec.BuildItemCommit) != want {
			t.Fatalf("engine %d private: %+v", i, res)
		}
		if _, err := c.InspectImage(ctx, tag); err != nil {
			t.Fatalf("engine %d private image: %v", i, err)
		}

		// Without (or with a wrong) credential the build fails before
		// BuildKit runs; nothing falls back to anonymous access.
		if res := run("private", "dockyard-test/git-private:anon", nil); res.Outcome != jobexec.OutcomeFailed {
			t.Fatalf("engine %d anonymous private build: %+v", i, res)
		}
		wrong := set.New(canary.Password, "wrong git password")
		if res := run("private", "dockyard-test/git-private:wrong", secretsFor(wrong), "g1"); res.Outcome != jobexec.OutcomeFailed ||
			!strings.Contains(res.Message, "401") {
			t.Fatalf("engine %d wrong credential: %+v", i, res)
		}
	}
}
