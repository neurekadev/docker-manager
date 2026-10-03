package backups

import (
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/backup"
)

// TestOtherRepositoryNote (#244): a location in another S3 repository is
// checked after the import; one in a local folder of an earlier version is
// not imported (nothing is said to be checked).
func TestOtherRepositoryNote(t *testing.T) {
	for _, c := range []struct {
		kind, want string
	}{
		{backup.KindS3, "Kept in another repository (B2): its credentials are restored with the manager state"},
		// Manifests of earlier versions without a kind.
		{"", "Kept in another repository (B2)"},
		{"local", "Kept in a local folder (B2), which Docker Manager no longer supports"},
	} {
		got := otherRepositoryNote(backup.RepositoryRef{Name: "B2", Destination: backup.Destination{Kind: c.kind}})
		if !strings.HasPrefix(got, c.want) {
			t.Errorf("%q: %s", c.kind, got)
		}
		if c.kind == "local" && strings.Contains(got, "checked after the import") {
			t.Errorf("a local folder is said to be checked: %s", got)
		}
	}
}
