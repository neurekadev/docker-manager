//go:build integration

package registries_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/regauth"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/db/migrations"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/regclient"
	"github.com/neurekadev/dockyard/internal/manager/registries"
	"github.com/neurekadev/dockyard/internal/manager/secrets"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// Registry connections against the registry fixture (distribution with
// htpasswd auth behind the fault proxy) and two DinD Engines (#19):
//
//   - the manager-side connection test and digest check authenticate,
//     classify 401/403/429 and never retry anonymously;
//   - a job's credential, resolved by CommandSecrets exactly as the job
//     engine does at dispatch, pulls a private image on two Engines through
//     the agent's Engine adapter, and a rotation applies to the next job.

func integrationService(t *testing.T) (*registries.Service, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	t.Cleanup(cancel)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "dockyard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: filepath.Join(dir, "snap"),
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	key, err := secrets.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := registries.New(registries.Options{DB: db, Keyring: secrets.NewKeyring(key), Clock: clock.Real(), Logger: testutil.Logger(t),
		Guard: &guard{}, Audit: &recorder{},
		Client: regclient.New(regclient.Options{Logger: testutil.Logger(t), MaxRetryAfter: time.Second})})
	if err != nil {
		t.Fatal(err)
	}
	return svc, ctx
}

func TestRegistryConnectionAgainstRegistryFixture(t *testing.T) {
	nw := testharness.NewNetwork(t)
	reg := testharness.StartRegistry(t, testharness.RegistryOptions{Network: nw})
	svc, ctx := integrationService(t)
	img, err := testharness.NewTestImage(runtime.GOARCH, "private registry connection")
	if err != nil {
		t.Fatal(err)
	}
	if err := testharness.PushOCIImage(ctx, http.DefaultClient, reg.Direct, reg.User, reg.Password, "team/private", "v1", img); err != nil {
		t.Fatal(err)
	}
	proxyHost := strings.TrimPrefix(reg.ProxyURL, "http://")

	// Manager side: the connection test through the fault proxy.
	mc, err := svc.Create(ctx, domain.RegistryConnectionInput{Name: "fixture (manager)", Host: proxyHost, Username: reg.User,
		Secret: reg.Password, PlainHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	ref := proxyHost + "/team/private:v1"
	res, err := svc.ConnectionTest(ctx, mc.ID, ref, "linux/"+runtime.GOARCH)
	if err != nil || !res.OK || res.Digest != img.Digest {
		t.Fatalf("test %+v %v", res, err)
	}
	remove := reg.Proxy.Inject(testharness.FaultRule{Path: testharness.RegistryManifestPath, Status: http.StatusTooManyRequests, RetryAfter: "3600"})
	if res, _ := svc.ConnectionTest(ctx, mc.ID, ref, ""); res.ErrorClass != regclient.ClassRateLimited || res.RetryAfter != time.Hour {
		t.Errorf("429: %+v", res)
	}
	remove()
	remove = reg.Proxy.Inject(testharness.FaultRule{Path: testharness.RepositoryPath("team/private"), Status: http.StatusForbidden})
	// The 429 put the host into a cooldown for fresh checks too; a new
	// client-side cooldown would hide the 403, so check through a new
	// service client after the rule change.
	svc2, ctx2 := integrationService(t)
	mc2, err := svc2.Create(ctx2, domain.RegistryConnectionInput{Name: "fixture 2", Host: proxyHost, Username: reg.User, Secret: reg.Password, PlainHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if res, _ := svc2.ConnectionTest(ctx2, mc2.ID, ref, ""); res.ErrorClass != regclient.ClassForbidden {
		t.Errorf("403: %+v", res)
	}
	remove()
	wrong, err := svc2.Rotate(ctx2, mc2.ID, mc2.Revision, domain.RegistryCredentialRotation{Secret: "wrong-password-123"})
	if err != nil {
		t.Fatal(err)
	}
	before := len(reg.Proxy.Hits())
	if res, _ := svc2.ConnectionTest(ctx2, wrong.ID, ref, ""); res.ErrorClass != regclient.ClassUnauthorized {
		t.Errorf("401: %+v", res)
	}
	// Exactly the challenge probe and the refused authenticated request:
	// no anonymous retry.
	manifests := 0
	for _, h := range reg.Proxy.Hits()[before:] {
		if strings.Contains(h.Path, "/manifests/") {
			manifests++
		}
	}
	if manifests != 2 {
		t.Errorf("%d manifest requests for a refused credential, want 2 (challenge + credential, no anonymous retry)", manifests)
	}

	// Agent side: two Engines pull the private image with the credential
	// the job engine would put into their commands.
	eopts := reg.EngineOptions()
	engines := testharness.StartEngines(t, 2, eopts)
	ec, err := svc.Create(ctx, domain.RegistryConnectionInput{Name: "fixture (engines)", Host: reg.EngineAddress, Username: reg.User,
		Secret: reg.Password})
	if err != nil {
		t.Fatal(err)
	}
	engineRef := reg.EngineAddress + "/team/private:v1"
	job := func(id string) *domain.Job {
		in, _ := json.Marshal(map[string]any{"reference": engineRef, "registryConnections": []string{ec.ID}})
		return &domain.Job{ID: id, EnvironmentID: "env", Input: in}
	}
	for i, e := range engines {
		c, err := engine.Connect(ctx, engine.Options{Host: e.Host, Logger: testutil.Logger(t)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		creds, err := svc.CommandSecrets(ctx, job("job-"+e.Alias))
		if err != nil {
			t.Fatal(err)
		}
		auth, err := regauth.ForReference(&protocol.CommandSecrets{Registries: creds}, engineRef, true)
		if err != nil {
			t.Fatal(err)
		}
		pulled, err := c.PullImage(ctx, engineRef, engine.PullOptions{Auth: auth})
		if err != nil {
			t.Fatalf("engine %d pull: %v", i, err)
		}
		if pulled.Digest != "" && pulled.Digest != img.Digest {
			t.Errorf("engine %d digest %s, want %s", i, pulled.Digest, img.Digest)
		}
		if _, err := c.PullImage(ctx, engineRef, engine.PullOptions{}); !errors.Is(err, engine.ErrUnauthorized) {
			t.Errorf("engine %d anonymous pull: %v, want unauthorized", i, err)
		}
	}

	// Rotation applies to the next job: a wrong credential now fails.
	ec, err = svc.Rotate(ctx, ec.ID, ec.Revision, domain.RegistryCredentialRotation{Secret: "rotated-but-wrong-123"})
	if err != nil {
		t.Fatal(err)
	}
	creds, err := svc.CommandSecrets(ctx, job("job-after-rotation"))
	if err != nil {
		t.Fatal(err)
	}
	auth, _ := regauth.ForReference(&protocol.CommandSecrets{Registries: creds}, engineRef, true)
	c, err := engine.Connect(ctx, engine.Options{Host: engines[0].Host, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.RemoveImage(ctx, engineRef, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PullImage(ctx, engineRef, engine.PullOptions{Auth: auth}); !errors.Is(err, engine.ErrUnauthorized) {
		t.Errorf("pull after rotation to a wrong credential: %v, want unauthorized", err)
	}
}
