package engine

import (
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/session/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// isolateHome points every Docker/BuildKit config location at empty
// temporary directories and returns them.
func isolateHome(t *testing.T) []string {
	t.Helper()
	dirs := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	t.Setenv("HOME", dirs[0])
	t.Setenv("USERPROFILE", dirs[0])
	t.Setenv("DOCKER_CONFIG", dirs[1])
	t.Setenv("XDG_CONFIG_HOME", dirs[2])
	return dirs
}

func assertEmptyDirs(t *testing.T, dirs []string) {
	t.Helper()
	for _, d := range dirs {
		_ = filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
			if err == nil && p != d {
				t.Errorf("file written to disk: %s", p)
			}
			return nil
		})
	}
}

func TestSessionAuthServesCredentialsFromMemory(t *testing.T) {
	dirs := isolateHome(t)
	ctx := testutil.Context(t)
	a := newAuthProvider([]RegistryAuth{
		{ServerAddress: "registry.example:5000", Username: "builder", Password: logging.Secret("s3cret-password")},
		{ServerAddress: "https://index.docker.io/v1/", Username: "hub-user", Password: logging.Secret("hub-token")},
	})
	srv := a.(auth.AuthServer)

	got, err := srv.Credentials(ctx, &auth.CredentialsRequest{Host: "registry.example:5000"})
	if err != nil || got.Username != "builder" || got.Secret != "s3cret-password" {
		t.Fatalf("credentials = %+v, %v", got, err)
	}
	got, err = srv.Credentials(ctx, &auth.CredentialsRequest{Host: "registry-1.docker.io"})
	if err != nil || got.Username != "hub-user" || got.Secret != "hub-token" {
		t.Fatalf("Docker Hub credentials = %+v, %v", got, err)
	}
	got, err = srv.Credentials(ctx, &auth.CredentialsRequest{Host: "other.example"})
	if err != nil || got.Username != "" || got.Secret != "" {
		t.Fatalf("unknown host must be anonymous: %+v, %v", got, err)
	}
	// The client-side token authority (whose seeds BuildKit persists in the
	// Docker config directory) is disabled.
	if _, err := srv.GetTokenAuthority(ctx, &auth.GetTokenAuthorityRequest{Host: "registry.example:5000"}); status.Code(err) != codes.Unavailable {
		t.Fatalf("GetTokenAuthority = %v, want Unavailable", err)
	}
	if _, err := srv.VerifyTokenAuthority(ctx, &auth.VerifyTokenAuthorityRequest{Host: "registry.example:5000"}); status.Code(err) != codes.Unavailable {
		t.Fatalf("VerifyTokenAuthority = %v, want Unavailable", err)
	}
	assertEmptyDirs(t, dirs)
}
