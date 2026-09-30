package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz/authztest"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// fakeStorageBackups answers the storage history from two repositories,
// r1 (100 stored, 300 before compression) and r2 (10 and 10), counting
// only those the query lets through; the first point is before any
// measurement.
type fakeStorageBackups struct {
	BackupService

	mu      sync.Mutex
	queries []domain.BackupStorageQuery
}

func (f *fakeStorageBackups) StorageHistory(_ context.Context, q domain.BackupStorageQuery) ([]domain.BackupStoragePoint, error) {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	p := domain.BackupStoragePoint{At: q.To, Known: true}
	for id, v := range map[string][2]int64{"r1": {100, 300}, "r2": {10, 10}} {
		if q.Repository == nil || q.Repository(id) {
			p.SizeBytes += v[0]
			p.UncompressedBytes += v[1]
		}
	}
	return []domain.BackupStoragePoint{{At: q.From}, p}, nil
}

func (f *fakeStorageBackups) last() domain.BackupStorageQuery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queries[len(f.queries)-1]
}

func storageHandler(t *testing.T, pol *authztest.Policy, svc BackupService) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	New(mux, Deps{Authorizer: pol, Idempotency: &memIdempotency{}, Builds: emptyBuilds{}, Backups: svc, Clock: testutil.FakeClock()})
	return authztest.Authenticate(withTestContext(t, mux, ""))
}

// TestBackupStorageHistoryShaping: only repositories the caller may read
// in full count (the capability of the storage figures); the default
// range is the last 30 days with daily points, unknown points are null
// and an environment selects its locations.
func TestBackupStorageHistoryShaping(t *testing.T) {
	svc := &fakeStorageBackups{}
	pol := authztest.New().Owner("olga").
		Member("rita", "r1-readers").Group("r1-readers", "allow backup_repository.read @backup_repository:r1")
	h := storageHandler(t, pol, svc)

	var got BackupStorageHistory
	getJSON(t, h, "olga", "/api/v1/backup-storage/history", &got)
	now := testutil.Epoch
	q := svc.last()
	if !q.To.Equal(now) || !q.From.Equal(now.Add(-30*24*time.Hour)) || q.Step != 24*time.Hour || q.Scope != "" {
		t.Errorf("default query %+v", q)
	}
	if got.StepSeconds != 86400 || !got.From.Equal(q.From) || !got.To.Equal(now) || len(got.Timestamps) != 2 {
		t.Fatalf("history %+v", got)
	}
	if got.StoredBytes[0] != nil || got.UncompressedBytes[0] != nil {
		t.Errorf("point before any measurement: %v %v", got.StoredBytes[0], got.UncompressedBytes[0])
	}
	if got.StoredBytes[1] == nil || *got.StoredBytes[1] != 110 || *got.UncompressedBytes[1] != 310 {
		t.Errorf("owner sees %v / %v, want both repositories", got.StoredBytes[1], got.UncompressedBytes[1])
	}

	getJSON(t, h, "rita", "/api/v1/backup-storage/history?environmentId=e1", &got)
	if q := svc.last(); q.Scope != "env:e1" || !q.Repository("r1") || q.Repository("r2") {
		t.Errorf("rita's query: scope %q, r1 %v, r2 %v", q.Scope, q.Repository("r1"), q.Repository("r2"))
	}
	if *got.StoredBytes[1] != 100 || *got.UncompressedBytes[1] != 300 {
		t.Errorf("rita sees %v / %v, want r1 only", *got.StoredBytes[1], *got.UncompressedBytes[1])
	}

	// A week: hourly points; the null stays null in JSON.
	from := now.Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	r := authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodGet, Path: "/api/v1/backup-storage/history?from=" + url.QueryEscape(from)})
	if r.Status != http.StatusOK {
		t.Fatalf("week: %d %s", r.Status, r.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(r.Body, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["stepSeconds"]) != "3600" || string(raw["storedBytes"]) != "[null,110]" {
		t.Errorf("week body %s", r.Body)
	}
}

// TestBackupStorageHistoryRange: the range must run forwards, ends at now
// at the latest and spans at most 731 days.
func TestBackupStorageHistoryRange(t *testing.T) {
	svc := &fakeStorageBackups{}
	h := storageHandler(t, authztest.New().Owner("olga"), svc)
	now := testutil.Epoch
	path := func(from, to time.Time) string {
		v := url.Values{}
		if !from.IsZero() {
			v.Set("from", from.Format(time.RFC3339))
		}
		if !to.IsZero() {
			v.Set("to", to.Format(time.RFC3339))
		}
		return "/api/v1/backup-storage/history?" + v.Encode()
	}
	for _, c := range []struct {
		name     string
		from, to time.Time
	}{
		{"from after to", now.Add(-time.Hour), now.Add(-2 * time.Hour)},
		{"empty range", now.Add(-time.Hour), now.Add(-time.Hour)},
		{"starts in the future", now.Add(time.Hour), time.Time{}},
		{"longer than 731 days", now.Add(-732 * 24 * time.Hour), time.Time{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodGet, Path: path(c.from, c.to)})
			if r.Status != http.StatusUnprocessableEntity {
				t.Errorf("status %d %s", r.Status, r.Body)
			}
		})
	}

	// A range into the future ends now; a year gets daily points.
	var got BackupStorageHistory
	getJSON(t, h, "olga", path(now.Add(-365*24*time.Hour), now.Add(48*time.Hour)), &got)
	if q := svc.last(); !q.To.Equal(now) || q.Step != 24*time.Hour || !got.To.Equal(now) {
		t.Errorf("clamped query %+v, history to %s", q, got.To)
	}
}

// TestBackupStorageStep: hourly points up to 8 days, daily beyond.
func TestBackupStorageStep(t *testing.T) {
	day := 24 * time.Hour
	for span, want := range map[time.Duration]time.Duration{time.Hour: time.Hour, 7 * day: time.Hour, 8 * day: time.Hour,
		8*day + time.Minute: day, 30 * day: day, 731 * day: day} {
		if got := backupStorageStep(span); got != want {
			t.Errorf("step for %s = %s, want %s", span, got, want)
		}
	}
}
