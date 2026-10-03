package migrations

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// Every account except the owner keeps its group as its only membership,
// the groups keep their creation order, nothing that references an
// account is lost to the rebuild of users, and foreign keys are enforced
// again afterwards.
func TestUsersMoveToMemberships(t *testing.T) {
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "docker-manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrateTo(t, db, "20261002150000", dir)

	var restricted string
	if err := db.QueryRowContext(ctx, `SELECT group_id FROM default_group`).Scan(&restricted); err != nil {
		t.Fatal(err)
	}
	const now = "2026-10-01 09:00:00+00:00"
	for _, s := range []string{
		`INSERT INTO groups (id, name, created_at, updated_at) VALUES ('zz-ops', 'Ops', '` + now + `', '` + now + `')`,
		`INSERT INTO users (id, username, is_owner, group_id, status, webauthn_handle, created_at, updated_at) VALUES
			('u-owner', 'owner', 1, '` + restricted + `', 'active', x'01', '` + now + `', '` + now + `'),
			('u-rita', 'rita', 0, 'zz-ops', 'active', x'02', '` + now + `', '` + now + `'),
			('u-sam', 'sam', 0, '` + restricted + `', 'disabled', x'03', '` + now + `', '` + now + `')`,
		`INSERT INTO recovery_codes (id, user_id, code_hash, created_at) VALUES ('rc-1', 'u-rita', 'h1', '` + now + `')`,
		`INSERT INTO user_permission_rules (user_id, capability, scope_kind, environment_id, resource_type, resource_id, effect, position)
			VALUES ('u-rita', 'stack.read', 'instance', '', '', '', 'allow', 0)`,
	} {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	migrateTo(t, db, "", dir)

	type membership struct{ User, Group string }
	var got []membership
	rows, err := db.QueryContext(ctx, `SELECT user_id, group_id FROM user_groups ORDER BY user_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var m membership
		if err := rows.Scan(&m.User, &m.Group); err != nil {
			t.Fatal(err)
		}
		got = append(got, m)
	}
	_ = rows.Close()
	if want := []membership{{"u-rita", "zz-ops"}, {"u-sam", restricted}}; !slices.Equal(got, want) {
		t.Fatalf("memberships %v, want %v (the owner in none)", got, want)
	}
	var order []string
	if err := db.NewRaw(`SELECT name FROM groups ORDER BY position`).Scan(ctx, &order); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, []string{"Restricted", "Ops"}) {
		t.Fatalf("group order %v", order)
	}
	var codes, rules int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM recovery_codes), (SELECT count(*) FROM user_permission_rules)`).
		Scan(&codes, &rules); err != nil || codes != 1 || rules != 1 {
		t.Fatalf("rows referencing accounts: %d codes, %d rules (%v)", codes, rules, err)
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM users WHERE id = 'u-sam'`).Scan(&status); err != nil || status != "disabled" {
		t.Fatalf("sam is %q (%v)", status, err)
	}
	var fk int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign keys %d (%v)", fk, err)
	}
	// The rebuilt table keeps its rules: one owner, who cannot be deleted,
	// and a deleted account leaves its groups and its rows.
	if _, err := db.ExecContext(ctx, `DELETE FROM users WHERE id = 'u-owner'`); err == nil {
		t.Fatal("the owner was deleted")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM groups WHERE id = 'zz-ops'`); err == nil {
		t.Fatal("a group with members was deleted")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM users WHERE id = 'u-rita'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM recovery_codes) + (SELECT count(*) FROM user_groups WHERE user_id = 'u-rita')`).
		Scan(&codes); err != nil || codes != 0 {
		t.Fatalf("rows of a deleted account: %d (%v)", codes, err)
	}
}
