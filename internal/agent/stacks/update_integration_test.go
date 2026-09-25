//go:build integration

package stacks_test

import (
	"encoding/json"
	"net/http"
	"runtime"
	"testing"

	"github.com/neurekadev/dockyard/internal/imageref"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testharness"
)

// Digest-driven updates on a real Engine (#20), in the compose-fixtures
// job: a private image on the registry fixture (htpasswd, through the
// fault proxy) is deployed with the command's registry credential (#19);
// the same tag then names a new build and update.run pulls it with the
// credential and recreates exactly the changed service through the Compose
// SDK from the unchanged definition; an unchanged digest is a no-op; a
// broken candidate fails the run with the quarantine flag; the definition
// files are byte-for-byte identical throughout (compose.read hash).

func updateEnv(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func updateOutput(t *testing.T, res protocol.ResultPayload) protocol.UpdateRunOutput {
	t.Helper()
	var o protocol.UpdateRunOutput
	if err := json.Unmarshal(res.Output, &o); err != nil {
		t.Fatalf("update output: %v (%+v)", err, res)
	}
	return o
}

func TestComposeDigestUpdateFromPrivateRegistry(t *testing.T) {
	nw := testharness.NewNetwork(t)
	reg := testharness.StartRegistry(t, testharness.RegistryOptions{Network: nw})
	r := newRigWith(t, reg.EngineOptions())
	ctx := t.Context()
	push := func(img *testharness.OCIImage) {
		t.Helper()
		if err := testharness.PushOCIImage(ctx, http.DefaultClient, reg.Direct, reg.User, reg.Password, "team/app", "1.0", img); err != nil {
			t.Fatal(err)
		}
	}
	v1, err := testharness.NewWorkloadImage(runtime.GOARCH, r.bin, "1")
	if err != nil {
		t.Fatal(err)
	}
	push(v1)
	ref := reg.EngineAddress + "/team/app:1.0"
	host, _ := imageref.Parse(ref)
	secrets := updateEnv(t, protocol.CommandSecrets{Registries: []protocol.RegistryCredential{{ConnectionID: "conn-1", Host: host.Host,
		ServerAddress: reg.EngineAddress, Username: reg.User, Secret: reg.Password}}})
	r.put("priv", map[string][]byte{
		"compose.yaml": []byte(`services:
  app:
    image: ` + ref + `
    command: ["serve"]
  worker:
    image: ` + w + `
    command: ["serve"]
    depends_on:
      app:
        condition: service_started
        restart: true
  idle:
    image: ` + ref + `
    command: ["serve"]
`),
		".env": []byte("UNUSED=1\n"),
	})
	deployIn := protocol.StackJobInput{StackID: "priv", Stack: protocol.ProjectRef{Root: protocol.RootStacks, Dir: "priv", ProjectName: "priv"},
		RegistryConnections: []string{"conn-1"}}
	// Without the credential the private image cannot be deployed (never anonymous).
	if res := r.job("stack.deploy", "priv", []string{"STACKJOBS_INPUT=" + updateEnv(t, deployIn)}); res.Outcome != "failed" {
		t.Fatalf("deploy without credential: %+v", res)
	}
	res := r.job("stack.deploy", "priv", []string{"STACKJOBS_INPUT=" + updateEnv(t, deployIn), "STACKJOBS_SECRETS=" + secrets})
	if res.Outcome != "succeeded" {
		t.Fatalf("deploy: %+v", res)
	}
	var applied protocol.StackJobOutput
	if err := json.Unmarshal(res.Output, &applied); err != nil {
		t.Fatal(err)
	}
	hash := applied.Sources.Hash
	for _, img := range applied.Images {
		if img.Service == "app" && img.Digest != v1.Digest {
			t.Fatalf("applied app digest %s, want %s", img.Digest, v1.Digest)
		}
	}
	// idle was stopped before the update: it must stay stopped.
	if err := r.eng.StopContainer(ctx, "priv-idle-1", nil); err != nil {
		t.Fatal(err)
	}
	app, worker := r.inspect("priv-app-1"), r.inspect("priv-worker-1")

	update := func(digest string, env ...string) protocol.ResultPayload {
		in := protocol.UpdateRunInput{PolicyID: "pol-1", StackID: "priv", Stack: &deployIn.Stack, ExpectSourceHash: hash,
			Services: []protocol.UpdateService{
				{Service: "app", Reference: ref, Digest: digest, RegistryConnection: "conn-1"},
				{Service: "idle", Reference: ref, Digest: digest, RegistryConnection: "conn-1"},
			}, RegistryConnections: []string{"conn-1"}, WaitTimeoutSeconds: 60}
		return r.job("update.run", "priv", append([]string{"STACKJOBS_INPUT=" + updateEnv(t, in)}, env...))
	}

	// Unchanged digest: nothing is recreated.
	res = update(v1.Digest, "STACKJOBS_SECRETS="+secrets)
	if res.Outcome != "succeeded" {
		t.Fatalf("no-op update: %+v", res)
	}
	if o := updateOutput(t, res); o.Result("app").Outcome != protocol.UpdateUnchanged || r.inspect("priv-app-1").ID != app.ID {
		t.Fatalf("unchanged digest recreated: %+v", o.Services)
	}

	// The tag names a new build.
	v2, err := testharness.NewWorkloadImage(runtime.GOARCH, r.bin, "2")
	if err != nil {
		t.Fatal(err)
	}
	push(v2)
	if res := update(v2.Digest); res.Outcome != "failed" || res.ErrorClass != "credential_unavailable" {
		t.Fatalf("update without the credential: %+v", res)
	}
	res = update(v2.Digest, "STACKJOBS_SECRETS="+secrets)
	if res.Outcome != "succeeded" {
		t.Fatalf("update: %+v", res)
	}
	o := updateOutput(t, res)
	if o.Result("app").Outcome != protocol.UpdateUpdated || o.Result("idle").Outcome != protocol.UpdateKeptStopped ||
		o.SourceHashBefore != hash || o.SourceHashAfter != hash || o.SourceHashAfterPull != hash {
		t.Fatalf("update output %+v", o)
	}
	newApp := r.inspect("priv-app-1")
	if newApp.ID == app.ID || !newApp.State.Running || newApp.Image != ref {
		t.Fatalf("app after the update: %+v", newApp)
	}
	if w2 := r.inspect("priv-worker-1"); !w2.State.StartedAt.After(worker.State.StartedAt) || !w2.State.Running {
		t.Error("worker (restart: true) was not restarted with app")
	}
	if idle := r.inspect("priv-idle-1"); idle.State.Running {
		t.Error("the stopped service was started")
	}
	if got := r.read("priv"); got.Snapshot.Hash != hash {
		t.Fatalf("definition changed: %s, want %s", got.Snapshot.Hash, hash)
	}

	// A broken build: the replaced container cannot start; the run fails
	// and asks for quarantine; the definition is still untouched.
	broken, err := testharness.NewTestImage(runtime.GOARCH, "not a program")
	if err != nil {
		t.Fatal(err)
	}
	push(broken)
	res = update(broken.Digest, "STACKJOBS_SECRETS="+secrets)
	if res.Outcome != "failed" {
		t.Fatalf("broken update: %+v", res)
	}
	if o := updateOutput(t, res); !o.Quarantine {
		t.Errorf("broken update without quarantine: %+v", o)
	}
	if got := r.read("priv"); got.Snapshot.Hash != hash {
		t.Fatalf("definition changed by a failed update: %s, want %s", got.Snapshot.Hash, hash)
	}
}
