package engine

import (
	"encoding/base64"
	"testing"

	"github.com/moby/buildkit/session/secrets"
	"github.com/moby/buildkit/session/secrets/secretsprovider"

	"code.neureka.dev/docker-manager/docker-manager/internal/logging"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// TestGitAuthServedAsSessionSecret: private Git contexts (#33) authenticate
// through the BuildKit session secret GIT_AUTH_HEADER.<host>, which
// BuildKit's Git source sends as an Authorization header; nothing is put
// into the URL or written to disk.
func TestGitAuthServedAsSessionSecret(t *testing.T) {
	dirs := isolateHome(t)
	ctx := testutil.Context(t)
	m := gitSecrets([]GitAuth{{Host: "Git.Example.com:8443", Username: "bot", Token: logging.Secret("ghp_tokenvalue123")}})
	want := "basic " + base64.StdEncoding.EncodeToString([]byte("bot:ghp_tokenvalue123"))
	if string(m["GIT_AUTH_HEADER.git.example.com:8443"]) != want || len(m) != 1 {
		t.Fatalf("secrets %v", m)
	}
	srv := secretsprovider.FromMap(m).(secrets.SecretsServer)
	got, err := srv.GetSecret(ctx, &secrets.GetSecretRequest{ID: "GIT_AUTH_HEADER.git.example.com:8443"})
	if err != nil || string(got.Data) != want {
		t.Fatalf("GetSecret = %v, %v", got, err)
	}
	if _, err := srv.GetSecret(ctx, &secrets.GetSecretRequest{ID: "GIT_AUTH_HEADER.other.example"}); err == nil {
		t.Fatal("unknown host answered")
	}
	if err := validateBuildSpec(BuildSpec{RemoteContext: "https://bot:tok@git.example.com/r.git"}); err == nil {
		t.Fatal("credentials in the Git URL accepted")
	}
	assertEmptyDirs(t, dirs)
}
