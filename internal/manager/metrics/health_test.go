package metrics

import (
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestHostHealthRecords (#143): the last disk health report per
// environment is replaced on save and read back for the restart.
func TestHostHealthRecords(t *testing.T) {
	clk := testutil.FakeClock()
	s := openTest(t, clk)
	ctx := testutil.Context(t)
	t0 := clk.Now()
	rec := HostHealthRecord{EnvironmentID: env, Data: []byte(`{"smart":{"status":"ok"}}`), CollectedAt: t0, ReceivedAt: t0.Add(time.Second)}
	if err := s.SaveHostHealth(ctx, rec); err != nil {
		t.Fatal(err)
	}
	rec.Data, rec.ReceivedAt = []byte(`{"smart":{"status":"no_access"}}`), t0.Add(time.Minute)
	if err := s.SaveHostHealth(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveHostHealth(ctx, HostHealthRecord{EnvironmentID: "env-2", Data: []byte(`{}`), CollectedAt: t0, ReceivedAt: t0}); err != nil {
		t.Fatal(err)
	}
	all, err := s.HostHealths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].EnvironmentID != env || string(all[0].Data) != `{"smart":{"status":"no_access"}}` ||
		!all[0].ReceivedAt.Equal(t0.Add(time.Minute)) || !all[0].CollectedAt.Equal(t0) {
		t.Fatalf("records %+v", all)
	}
	if err := s.SaveHostHealth(ctx, HostHealthRecord{EnvironmentID: env, Data: []byte(`not json`), CollectedAt: t0, ReceivedAt: t0}); err == nil {
		t.Fatal("invalid JSON stored")
	}
}
