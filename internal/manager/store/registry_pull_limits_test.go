package store

import (
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestRegistryPullLimitWritesAreMonotonic: an observation older than the
// stored one changes nothing (also across the sub-second text format); a
// newer one without a limit keeps the reported limit.
func TestRegistryPullLimitWritesAreMonotonic(t *testing.T) {
	ctx, db := backupTestDB(t)
	n := func(v int64) *int64 { return &v }
	record := func(at time.Time, remaining int64) bool {
		t.Helper()
		applied, err := RecordRegistryPullLimit(ctx, db, domain.RegistryPullLimit{Host: "docker.io", ConnectionID: "c1",
			Limit: n(200), Remaining: n(remaining), ObservedAt: &at, CheckedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		return applied
	}
	stored := func() domain.RegistryPullLimit {
		t.Helper()
		all, err := ListRegistryPullLimits(ctx, db)
		if err != nil || len(all) != 1 {
			t.Fatalf("%+v %v", all, err)
		}
		return all[0]
	}
	t0 := testutil.Epoch
	if !record(t0.Add(500*time.Millisecond), 150) {
		t.Fatal("first write not applied")
	}
	// Older answers (a whole second, and a whole-second time that sorts
	// before the stored fractional one) lose.
	for _, at := range []time.Time{t0, t0.Add(-time.Second)} {
		if record(at, 190) {
			t.Fatalf("older answer at %v applied", at)
		}
	}
	if p := stored(); *p.Remaining != 150 || !p.CheckedAt.Equal(t0.Add(500*time.Millisecond)) {
		t.Fatalf("after older answers: %+v", p)
	}
	// The same time and a later one win.
	if !record(t0.Add(500*time.Millisecond), 149) || !record(t0.Add(2*time.Second), 148) {
		t.Fatal("newer answer not applied")
	}
	later := t0.Add(time.Minute)
	applied, err := RecordRegistryPullLimit(ctx, db, domain.RegistryPullLimit{Host: "docker.io", ConnectionID: "c1", CheckedAt: later})
	if err != nil || !applied {
		t.Fatalf("answer without a limit: %v %v", applied, err)
	}
	if p := stored(); *p.Remaining != 148 || *p.Limit != 200 || !p.CheckedAt.Equal(later) || !p.ObservedAt.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("after an answer without a limit: %+v", p)
	}
	if err := DeleteRegistryPullLimits(ctx, db, "c1"); err != nil {
		t.Fatal(err)
	}
	if all, err := ListRegistryPullLimits(ctx, db); err != nil || len(all) != 0 {
		t.Fatalf("after delete: %+v %v", all, err)
	}
}
