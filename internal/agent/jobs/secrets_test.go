package jobs

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil/canary"
)

// scanDir asserts that no file below dir contains a canary.
func scanDir(t *testing.T, set *canary.Set, dir string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		n++
		set.AssertClean(t, "agent state file "+p, b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestCommandSecretsStayInMemory: the credentials of a command reach the
// executing step but never the journal on disk, the logs, the result, the
// job report or a resumed attempt's recovered state (#19, #33).
func TestCommandSecretsStayInMemory(t *testing.T) {
	set := canary.New()
	regSecret := set.New(canary.RegistryCredential, "registry token")
	gitSecret := set.New(canary.RegistryCredential, "git token")
	ctx := testutil.Context(t)
	dir := t.TempDir()

	inStep := make(chan struct{})
	release := make(chan struct{})
	var seen *protocol.CommandSecrets
	exec := jobexec.Executor{Kind: jobspec.ImagePull, Steps: map[string]jobexec.StepFunc{
		"pull": func(ctx context.Context, sc *jobexec.StepContext) error {
			seen = sc.Secrets
			sc.Progress(ctx, 10, "pulling with connection "+sc.Secrets.Registries[0].ConnectionID)
			sc.Item(ctx, "app:1", "succeeded", "pulled")
			close(inStep)
			<-release
			return nil
		},
	}}
	logger := set.CaptureLogger(t)
	s := &captureSender{}
	r, err := New(ctx, Options{StateDir: dir, Clock: testutil.FakeClock(), Logger: logger, Sender: s, Executors: []jobexec.Executor{exec}})
	if err != nil {
		t.Fatal(err)
	}
	secrets := &protocol.CommandSecrets{
		Registries: []protocol.RegistryCredential{{ConnectionID: "conn-1", Host: "ghcr.io", ServerAddress: "ghcr.io", Username: "bot", Secret: regSecret}},
		Git:        []protocol.GitCredential{{CredentialID: "git-1", Host: "git.example.com", Username: "bot", Secret: gitSecret}},
	}
	f, err := protocol.NewCommandFrame(protocol.JobRef{JobID: "job-1", Attempt: 1, FencingToken: 3}, testutil.Epoch.Add(time.Hour),
		protocol.CommandPayload{Kind: string(jobspec.ImagePull), Input: json.RawMessage(`{"reference":"ghcr.io/o/app:1","registryConnections":["conn-1"]}`),
			Secrets: secrets})
	if err != nil {
		t.Fatal(err)
	}
	// The frame itself carries them (that is the point of the command).
	if set.Scan(f.Payload) == nil {
		t.Fatal("the command frame does not carry the secrets")
	}
	if err := r.HandleFrame(ctx, f); err != nil {
		t.Fatal(err)
	}
	<-inStep
	// While the step runs (journaled in flight), nothing on disk holds them.
	if n := scanDir(t, set, dir); n == 0 {
		t.Fatal("no journal written")
	}
	for _, st := range r.Journal().Entries() {
		if st.Secrets != nil {
			t.Fatal("journal entry holds secrets in memory")
		}
	}
	set.AssertClean(t, "running report", r.Report())
	close(release)
	r.Wait()

	if seen == nil || seen.Registries[0].Secret != regSecret || seen.Git[0].Secret != gitSecret {
		t.Fatal("the step did not receive the command's secrets")
	}
	scanDir(t, set, dir)
	s.mu.Lock()
	for _, fr := range s.frames {
		set.AssertClean(t, "frame "+string(fr.Type), fr.Payload)
	}
	s.mu.Unlock()
	set.AssertClean(t, "finished report", r.Report())
	set.AssertClean(t, "fmt of secrets", []string{secrets.String(), secrets.GoString()})

	// After a restart the recovered entry has no secrets either; a resumed
	// attempt gets them again from the manager's new command.
	r2, err := New(ctx, Options{StateDir: dir, Clock: testutil.FakeClock(), Logger: logger, Sender: s, Executors: []jobexec.Executor{exec}})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range r2.Journal().Entries() {
		if st.Secrets != nil {
			t.Fatal("recovered entry holds secrets")
		}
	}
}

// TestCloneDropsSecrets guards the journal invariant at the type level.
func TestCloneDropsSecrets(t *testing.T) {
	st := jobexec.State{JobID: "j", Secrets: &protocol.CommandSecrets{Registries: []protocol.RegistryCredential{{Secret: "x"}}}}
	if c := st.Clone(); c.Secrets != nil {
		t.Fatal("Clone kept Secrets")
	}
	b, _ := json.Marshal(st)
	if string(b) != `{"jobId":"j","attempt":0,"fencingToken":0,"kind":""}` {
		t.Fatalf("serialized %s", b)
	}
}
