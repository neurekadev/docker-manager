package migrations

import (
	"path/filepath"
	"testing"

	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// The default group goes; the initial Restricted group goes with it while
// it is as created (no rules, no members, never edited) and stays as an
// ordinary group once it is used.
func TestDefaultGroupIsDropped(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup string
		kept  bool
	}{
		{"unused", "", false},
		{"with a rule", `INSERT INTO group_permission_rules (group_id, capability, scope_kind, environment_id, resource_type, resource_id,
			effect, position) SELECT id, 'stack.read', 'instance', '', '', '', 'allow', 0 FROM groups WHERE name = 'Restricted'`, true},
		{"renamed", `UPDATE groups SET name = 'Guests', revision = revision + 1`, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := testutil.Context(t)
			dir := t.TempDir()
			db, err := store.Open(ctx, filepath.Join(dir, "docker-manager.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			migrateTo(t, db, "20261002160000", dir)
			if c.setup != "" {
				if _, err := db.ExecContext(ctx, c.setup); err != nil {
					t.Fatal(err)
				}
			}
			migrateTo(t, db, "", dir)
			var groups, tables int
			if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM groups),
				(SELECT count(*) FROM sqlite_schema WHERE name IN ('default_group', 'default_group_permanent'))`).Scan(&groups, &tables); err != nil {
				t.Fatal(err)
			}
			if want := map[bool]int{false: 0, true: 1}[c.kept]; groups != want || tables != 0 {
				t.Fatalf("%d groups (want %d), %d default group objects", groups, want, tables)
			}
		})
	}
}
