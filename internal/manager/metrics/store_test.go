package metrics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

const env = "0190a6e0-3333-7000-8000-000000000003"

func f(v float64) *float64 { return &v }
func i(v int64) *int64     { return &v }

func openTest(t testing.TB, clk *clock.Fake, mut ...func(*Options)) *Store {
	t.Helper()
	o := Options{Path: filepath.Join(t.TempDir(), FileName), Clock: clk, Logger: testutil.Logger(t)}
	for _, m := range mut {
		m(&o)
	}
	s, err := Open(testutil.Context(t), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func hostSample(at time.Time, cpu float64) domain.MetricSample {
	return domain.MetricSample{At: at, Host: &domain.HostValues{CPUPercent: f(cpu), MemoryUsedBytes: i(int64(cpu * 1000)),
		MemoryTotalBytes: i(100_000), Load1: f(cpu / 100), NetworkRxBPS: f(cpu * 10)},
		Disks:      []domain.DiskValues{{Mount: "docker", UsedBytes: int64(cpu), TotalBytes: 1000}},
		Containers: []domain.ContainerValues{{Name: "web", CPUPercent: f(cpu / 2), MemoryBytes: i(int64(cpu))}}}
}

// values renders a series for comparisons ("-" for a gap).
func values(s domain.MetricSeries) string {
	out := ""
	for i, v := range s.Values {
		if i > 0 {
			out += " "
		}
		if v == nil {
			out += "-"
		} else {
			out += fmt.Sprintf("%g", math.Round(*v*100)/100)
		}
	}
	return out
}

func series(t *testing.T, r domain.MetricResult, key, mount string) domain.MetricSeries {
	t.Helper()
	for _, s := range r.Series {
		if s.Key == key && s.Mount == mount {
			return s
		}
	}
	t.Fatalf("no series %s %s in %+v", key, mount, r.Series)
	return domain.MetricSeries{}
}

func TestIngestIsIdempotentAcrossRedelivery(t *testing.T) {
	clk := testutil.FakeClock()
	s := openTest(t, clk)
	ctx := testutil.Context(t)
	t0 := clk.Now().Truncate(10 * time.Second)
	var batch []domain.MetricSample
	for k := range 6 {
		batch = append(batch, hostSample(t0.Add(time.Duration(k)*10*time.Second), float64(10*k)))
	}
	clk.Advance(time.Minute)
	res, err := s.Ingest(ctx, env, batch, &domain.MetricCursor{Epoch: "e1", LastSeq: 6})
	if err != nil || res.Inserted != 18 || res.Duplicates != 0 || !res.Host || fmt.Sprint(res.Containers) != "[web]" {
		t.Fatalf("%+v %v", res, err)
	}
	// The agent re-sends the same batches (manager restart without a
	// cursor, reconnect): nothing is stored twice. A timestamp a few
	// seconds off within the same 10 s slot is the same sample.
	for k := range batch {
		batch[k].At = batch[k].At.Add(3 * time.Second)
	}
	res, err = s.Ingest(ctx, env, batch, nil)
	if err != nil || res.Inserted != 0 || res.Duplicates != 18 || res.Host || len(res.Containers) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	st, err := s.Stats(ctx)
	if err != nil || st.Rows["host_raw"] != 6 || st.Rows["container_raw"] != 6 || st.Rows["disk_raw"] != 6 || st.Series != 3 {
		t.Fatalf("%+v %v", st, err)
	}
	c, ok, err := s.Cursor(ctx, env)
	if err != nil || !ok || c.Epoch != "e1" || c.LastSeq != 6 {
		t.Fatalf("%+v %v %v", c, ok, err)
	}
	r, err := s.Query(ctx, domain.MetricQuery{EnvironmentID: env, Kind: domain.MetricHost, From: t0, To: t0.Add(time.Minute),
		Step: 10 * time.Second, Keys: []string{"cpu.percent"}})
	if err != nil || values(r.Series[0]) != "0 10 20 30 40 50" || r.Resolution != LevelRaw {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestOfflineIntervalsAreGaps(t *testing.T) {
	clk := testutil.FakeClock()
	s := openTest(t, clk)
	ctx := testutil.Context(t)
	t0 := clk.Now().Truncate(time.Minute)
	var batch []domain.MetricSample
	for k := range 12 {
		if k >= 4 && k < 9 { // offline for 50 s
			continue
		}
		smp := hostSample(t0.Add(time.Duration(k)*10*time.Second), 20)
		if k == 2 {
			smp.Host.CPUPercent = nil // unknown value: a gap, not a zero
		}
		batch = append(batch, smp)
	}
	clk.Advance(3 * time.Minute)
	if _, err := s.Ingest(ctx, env, batch, nil); err != nil {
		t.Fatal(err)
	}
	r, err := s.Query(ctx, domain.MetricQuery{EnvironmentID: env, Kind: domain.MetricHost, From: t0, To: t0.Add(2 * time.Minute),
		Step: 10 * time.Second, Keys: []string{"cpu.percent", "memory.used_bytes", "disk.used_bytes"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "20 20 - 20 - - - - - 20 20 20"
	if got := values(series(t, r, "cpu.percent", "")); got != want {
		t.Fatalf("cpu\n got %s\nwant %s", got, want)
	}
	if got := values(series(t, r, "disk.used_bytes", "docker")); got != "20 20 20 20 - - - - - 20 20 20" {
		t.Fatalf("disk %s", got)
	}
	// Coarser steps average what exists; a bucket without samples is a gap.
	r, err = s.Query(ctx, domain.MetricQuery{EnvironmentID: env, Kind: domain.MetricContainer, Name: "web", From: t0, To: t0.Add(2 * time.Minute),
		Step: 30 * time.Second, Keys: []string{"cpu.percent", "memory.used_bytes"}})
	if err != nil || values(series(t, r, "cpu.percent", "")) != "10 10 - 10" || len(r.Timestamps) != 4 {
		t.Fatalf("%+v %v", r, err)
	}
	// Another environment or an unknown container has no data at all.
	r, err = s.Query(ctx, domain.MetricQuery{EnvironmentID: env, Kind: domain.MetricContainer, Name: "db", From: t0, To: t0.Add(time.Minute)})
	if err != nil || values(series(t, r, "cpu.percent", "")) != "- - - - - -" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestRollupsServeLongRanges(t *testing.T) {
	clk := testutil.FakeClock()
	s := openTest(t, clk)
	ctx := testutil.Context(t)
	t0 := clk.Now().Truncate(time.Hour)
	// Two hours of samples: CPU alternates 10/30 (average 20, max 30).
	var batch []domain.MetricSample
	for k := range 720 {
		batch = append(batch, hostSample(t0.Add(time.Duration(k)*10*time.Second), float64(10+20*(k%2))))
	}
	clk.Set(t0.Add(2*time.Hour + time.Minute))
	if _, err := s.Ingest(ctx, env, batch, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	w1, _ := s.Watermark(ctx, LevelMinute)
	w15, _ := s.Watermark(ctx, LevelQuarter)
	if !w1.Equal(t0.Add(2*time.Hour)) || !w15.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("watermarks %s %s", w1, w15)
	}
	st, _ := s.Stats(ctx)
	if st.Rows["host_1m"] != 120 || st.Rows["host_15m"] != 8 || st.Rows["container_1m"] != 120 || st.Rows["disk_15m"] != 8 {
		t.Fatalf("rows %+v", st.Rows)
	}
	// A 1 min step reads the 1 min rollup.
	r, err := s.Query(ctx, domain.MetricQuery{EnvironmentID: env, Kind: domain.MetricHost, From: t0, To: t0.Add(5 * time.Minute),
		Step: time.Minute, Keys: []string{"cpu.percent", "cpu.percent.max", "load.1"}})
	if err != nil || r.Resolution != LevelMinute || values(series(t, r, "cpu.percent", "")) != "20 20 20 20 20" ||
		values(series(t, r, "cpu.percent.max", "")) != "30 30 30 30 30" || values(series(t, r, "load.1", "")) != "0.2 0.2 0.2 0.2 0.2" {
		t.Fatalf("%+v %v", r, err)
	}
	// An automatic step over two hours uses the 15 min rollup, rounded to
	// its resolution, and includes the partial bucket at the end.
	r, err = s.Query(ctx, domain.MetricQuery{EnvironmentID: env, Kind: domain.MetricHost, From: t0, To: t0.Add(2 * time.Hour),
		Step: 20 * time.Minute, Keys: []string{"cpu.percent"}})
	if err != nil || r.Resolution != LevelQuarter || r.Step != 30*time.Minute || values(r.Series[0]) != "20 20 20 20" {
		t.Fatalf("%+v %v", r, err)
	}
	// Late samples (an agent backfilling after a reconnect) reopen the
	// rollups that covered them; the next pass recomputes them.
	late := hostSample(t0.Add(30*time.Minute+5*time.Second), 0)
	late.At = t0.Add(30 * time.Minute) // the slot of an existing sample: ignored
	if res, _ := s.Ingest(ctx, env, []domain.MetricSample{late}, nil); res.Inserted != 0 {
		t.Fatalf("%+v", res)
	}
	var more []domain.MetricSample
	for k := range 6 { // a new container's samples for minute 30
		more = append(more, domain.MetricSample{At: t0.Add(30*time.Minute + time.Duration(k)*10*time.Second),
			Containers: []domain.ContainerValues{{Name: "late", CPUPercent: f(4)}}})
	}
	if _, err := s.Ingest(ctx, env, more, nil); err != nil {
		t.Fatal(err)
	}
	if w1, _ := s.Watermark(ctx, LevelMinute); !w1.Equal(t0.Add(30 * time.Minute)) {
		t.Fatalf("watermark not moved back: %s", w1)
	}
	// Before the rollup ran again, queries read the raw samples.
	r, err = s.Query(ctx, domain.MetricQuery{EnvironmentID: env, Kind: domain.MetricContainer, Name: "late", From: t0.Add(29 * time.Minute),
		To: t0.Add(32 * time.Minute), Step: time.Minute, Keys: []string{"cpu.percent"}})
	if err != nil || values(r.Series[0]) != "- 4 -" {
		t.Fatalf("%+v %v", r, err)
	}
	if err := s.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	if w1, _ := s.Watermark(ctx, LevelMinute); !w1.Equal(t0.Add(2 * time.Hour)) {
		t.Fatalf("watermark %s", w1)
	}
	r, err = s.Query(ctx, domain.MetricQuery{EnvironmentID: env, Kind: domain.MetricContainer, Name: "late", From: t0, To: t0.Add(time.Hour),
		Step: 15 * time.Minute, Keys: []string{"cpu.percent"}})
	if err != nil || r.Resolution != LevelQuarter || values(r.Series[0]) != "- - 4 -" {
		t.Fatalf("%+v %v", r, err)
	}
	// Seven days later only the 15 min level still holds the data.
	clk.Set(t0.Add(8 * 24 * time.Hour))
	if _, err := s.Retain(ctx); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Stats(ctx)
	if st.Rows["host_raw"] != 0 || st.Rows["host_1m"] != 0 || st.Rows["host_15m"] != 8 {
		t.Fatalf("rows after a week %+v", st.Rows)
	}
	r, err = s.Query(ctx, domain.MetricQuery{EnvironmentID: env, Kind: domain.MetricHost, From: t0, To: t0.Add(2 * time.Hour),
		Keys: []string{"cpu.percent"}})
	if err != nil || r.Resolution != LevelQuarter || values(r.Series[0]) != "20 20 20 20 20 20 20 20" {
		t.Fatalf("%+v %v", r, err)
	}
	// After 90 days everything is gone, including the series.
	clk.Set(t0.Add(91 * 24 * time.Hour))
	res, err := s.Retain(ctx)
	if err != nil || res.Series != 4 {
		t.Fatalf("%+v %v", res, err)
	}
	st, _ = s.Stats(ctx)
	if st.Rows["host_15m"] != 0 || st.Series != 0 {
		t.Fatalf("%+v", st)
	}
}

func TestQueryValidation(t *testing.T) {
	clk := testutil.FakeClock()
	s := openTest(t, clk)
	ctx := testutil.Context(t)
	now := clk.Now()
	for name, q := range map[string]domain.MetricQuery{
		"kind":       {EnvironmentID: env, Kind: "stack"},
		"container":  {EnvironmentID: env, Kind: domain.MetricContainer},
		"key":        {EnvironmentID: env, Kind: domain.MetricHost, Keys: []string{"pids"}},
		"range":      {EnvironmentID: env, Kind: domain.MetricHost, From: now, To: now.Add(-time.Hour)},
		"points":     {EnvironmentID: env, Kind: domain.MetricHost, From: now.Add(-24 * time.Hour), To: now, Step: 10 * time.Second},
		"negative":   {EnvironmentID: env, Kind: domain.MetricHost, Step: -time.Second},
		"containerK": {EnvironmentID: env, Kind: domain.MetricContainer, Name: "web", Keys: []string{"disk.used_bytes"}},
	} {
		if _, err := s.Query(ctx, q); !errors.Is(err, domain.ErrMetricQuery) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A step finer than the stored resolution is raised to it.
	r, err := s.Query(ctx, domain.MetricQuery{EnvironmentID: env, Kind: domain.MetricHost, From: now.Add(-3 * 24 * time.Hour), To: now,
		Step: 10 * time.Second, Keys: []string{"cpu.percent"}})
	if !errors.Is(err, domain.ErrMetricQuery) {
		t.Fatalf("3 days at 1 min is 4320 points: %+v %v", r, err)
	}
	r, err = s.Query(ctx, domain.MetricQuery{EnvironmentID: env, Kind: domain.MetricHost, From: now.Add(-3 * 24 * time.Hour), To: now})
	if err != nil || r.Resolution != LevelQuarter || len(r.Timestamps) > MaxPoints || len(r.Series) != 12 {
		t.Fatalf("%+v %v", r.Resolution, err)
	}
}

func TestStorageLimitsAreEnforced(t *testing.T) {
	clk := testutil.FakeClock()
	s := openTest(t, clk, func(o *Options) { o.MaxSeries = 5 })
	ctx := testutil.Context(t)
	now := clk.Now().Truncate(10 * time.Second)
	smp := domain.MetricSample{At: now, Host: &domain.HostValues{CPUPercent: f(1)}}
	for k := range 10 {
		smp.Containers = append(smp.Containers, domain.ContainerValues{Name: fmt.Sprintf("c%d", k), CPUPercent: f(1)})
	}
	res, err := s.Ingest(ctx, env, []domain.MetricSample{smp}, nil)
	// host + 4 containers fill the 5 series; 6 containers are refused.
	if err != nil || res.Inserted != 5 || res.Refused != 6 {
		t.Fatalf("%+v %v", res, err)
	}
	// Samples older than the raw retention are not stored.
	old := domain.MetricSample{At: now.Add(-25 * time.Hour), Host: &domain.HostValues{CPUPercent: f(1)}}
	if res, _ := s.Ingest(ctx, env, []domain.MetricSample{old}, nil); res.Expired != 1 || res.Inserted != 0 {
		t.Fatalf("%+v", res)
	}

	// Storage cap: a day of samples of a host and 4 containers (~1.2 MB,
	// ten times the empty database), then a cap below the used size.
	// Retention shortens every level until the database fits, oldest data
	// first; at the minimum horizon new series are refused while hosts
	// keep reporting. (More containers add nothing but ingest time, which
	// the pure-Go SQLite under -race multiplies by 30 and more.)
	s2 := openTest(t, clk, func(o *Options) { o.MaxBytes = 1 << 40 })
	start := now.Add(-23 * time.Hour)
	for h := range 23 {
		var batch []domain.MetricSample
		for k := range 360 {
			b := domain.MetricSample{At: start.Add(time.Duration(h)*time.Hour + time.Duration(k)*10*time.Second), Host: &domain.HostValues{CPUPercent: f(5)}}
			for c := range 4 {
				b.Containers = append(b.Containers, domain.ContainerValues{Name: fmt.Sprintf("app-%03d", c), CPUPercent: f(1), MemoryBytes: i(1 << 20)})
			}
			batch = append(batch, b)
		}
		if _, err := s2.Ingest(ctx, env, batch, nil); err != nil {
			t.Fatal(err)
		}
	}
	full, err := s2.Size(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s2.opts.MaxBytes = full / 3
	r, err := s2.Retain(ctx)
	if err != nil || r.UsedBytes > s2.opts.MaxBytes || r.Horizon >= 1 || r.Full {
		t.Fatalf("used %d of %d: %+v %v", r.UsedBytes, s2.opts.MaxBytes, r, err)
	}
	// The newest samples survived, the oldest are gone.
	q, err := s2.Query(ctx, domain.MetricQuery{EnvironmentID: env, Kind: domain.MetricHost, From: start, To: now, Step: time.Hour,
		Keys: []string{"cpu.percent"}})
	if err != nil || q.Series[0].Values[0] != nil || q.Series[0].Values[len(q.Series[0].Values)-1] == nil {
		t.Fatalf("%v %v", values(q.Series[0]), err)
	}
	// A cap that cannot be met even at the minimum horizon refuses new
	// series (hosts are still accepted).
	s2.opts.MaxBytes = 4096
	if r, err := s2.Retain(ctx); err != nil || !r.Full || r.Horizon != minHorizon {
		t.Fatalf("%+v %v", r, err)
	}
	b := domain.MetricSample{At: now, Host: &domain.HostValues{CPUPercent: f(1)}, Containers: []domain.ContainerValues{{Name: "new-one", CPUPercent: f(1)}}}
	if res, err := s2.Ingest(ctx, "other-env", []domain.MetricSample{b}, nil); err != nil || res.Refused != 1 || !res.Host {
		t.Fatalf("%+v %v", res, err)
	}
	// Once space is available again the cap lifts.
	s2.opts.MaxBytes = 1 << 40
	if r, err := s2.Retain(ctx); err != nil || r.Full {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestLatestAndInventory(t *testing.T) {
	clk := testutil.FakeClock()
	s := openTest(t, clk)
	ctx := testutil.Context(t)
	if _, ok, err := s.Latest(ctx, env); ok || err != nil {
		t.Fatal(ok, err)
	}
	t0 := clk.Now().Truncate(10 * time.Second)
	if _, err := s.Ingest(ctx, env, []domain.MetricSample{hostSample(t0, 10), hostSample(t0.Add(10*time.Second), 40)}, nil); err != nil {
		t.Fatal(err)
	}
	l, ok, err := s.Latest(ctx, env)
	if err != nil || !ok || !l.At.Equal(t0.Add(10*time.Second)) || *l.Host.CPUPercent != 40 || *l.Host.MemoryTotalBytes != 100_000 ||
		len(l.Disks) != 1 || l.Disks[0].UsedBytes != 40 {
		t.Fatalf("%+v %v %v", l, ok, err)
	}
	rec := InventoryRecord{EnvironmentID: env, Data: []byte(`{"engineId":"E"}`), CollectedAt: t0, ReceivedAt: t0.Add(time.Second)}
	if err := s.SaveInventory(ctx, rec); err != nil {
		t.Fatal(err)
	}
	rec.Data = []byte(`{"engineId":"F"}`)
	if err := s.SaveInventory(ctx, rec); err != nil {
		t.Fatal(err)
	}
	all, err := s.Inventories(ctx)
	if err != nil || len(all) != 1 || string(all[0].Data) != `{"engineId":"F"}` || !all[0].CollectedAt.Equal(t0) {
		t.Fatalf("%+v %v", all, err)
	}
}

func TestLatestContainersKeepsGapsAndDropsStaleOnes(t *testing.T) {
	clk := testutil.FakeClock()
	s := openTest(t, clk)
	ctx := testutil.Context(t)
	t0 := clk.Now().Truncate(10 * time.Second)
	old := domain.MetricSample{At: t0.Add(-5 * time.Minute), Containers: []domain.ContainerValues{{Name: "gone", CPUPercent: f(1), MemoryBytes: i(1)}}}
	first := domain.MetricSample{At: t0.Add(-20 * time.Second), Containers: []domain.ContainerValues{
		{Name: "web", CPUPercent: f(5), MemoryBytes: i(100), MemoryLimitBytes: i(1000)}, {Name: "db", CPUPercent: f(2.5), MemoryBytes: i(50)}}}
	last := domain.MetricSample{At: t0.Add(-10 * time.Second), Containers: []domain.ContainerValues{{Name: "web", CPUPercent: f(7.25), MemoryBytes: nil}}}
	if _, err := s.Ingest(ctx, env, []domain.MetricSample{old, first, last}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.LatestContainers(ctx, env, time.Minute)
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	db, web := got[0], got[1]
	if db.Values.Name != "db" || math.Abs(*db.Values.CPUPercent-2.5) > 1e-9 || *db.Values.MemoryBytes != 50 || db.Values.MemoryLimitBytes != nil ||
		!db.At.Equal(first.At) {
		t.Fatalf("db %+v", db)
	}
	// The newest sample wins, with its gap kept (no memory: nil, not the
	// older value and not zero).
	if web.Values.Name != "web" || math.Abs(*web.Values.CPUPercent-7.25) > 1e-9 || web.Values.MemoryBytes != nil || !web.At.Equal(last.At) {
		t.Fatalf("web %+v", web)
	}
	if other, err := s.LatestContainers(ctx, "other-env", time.Minute); err != nil || len(other) != 0 {
		t.Fatalf("other environment: %+v %v", other, err)
	}
}

func TestQueryContainersReturnsEveryContainerWithValues(t *testing.T) {
	clk := testutil.FakeClock()
	s := openTest(t, clk)
	ctx := testutil.Context(t)
	t0 := clk.Now().Truncate(time.Minute)
	// More containers than one aggregate query binds, so the batches are
	// read and merged; "gone" has samples only after the range.
	var many []domain.ContainerValues
	for n := range seriesPerQuery + 5 {
		many = append(many, domain.ContainerValues{Name: fmt.Sprintf("c%03d", n), CPUPercent: f(float64(n % 10))})
	}
	samples := []domain.MetricSample{
		{At: t0, Containers: append([]domain.ContainerValues{{Name: "web", CPUPercent: f(4), MemoryBytes: i(100)},
			{Name: "db", MemoryBytes: i(50)}}, many...)},
		{At: t0.Add(30 * time.Second), Containers: []domain.ContainerValues{{Name: "web", CPUPercent: f(6), MemoryBytes: i(300)}}},
		{At: t0.Add(90 * time.Second), Containers: []domain.ContainerValues{{Name: "gone", CPUPercent: f(1)}}},
	}
	clk.Advance(3 * time.Minute)
	if _, err := s.Ingest(ctx, env, samples, nil); err != nil {
		t.Fatal(err)
	}
	r, err := s.QueryContainers(ctx, domain.MetricQuery{EnvironmentID: env, From: t0, To: t0.Add(time.Minute), Step: 30 * time.Second,
		Keys: []string{"cpu.percent", "memory.used_bytes"}})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string][]domain.MetricSeries{}
	var order []string
	for _, ser := range r.Series {
		if _, ok := byName[ser.Container]; !ok {
			order = append(order, ser.Container)
		}
		byName[ser.Container] = append(byName[ser.Container], ser)
	}
	if len(order) != seriesPerQuery+7 || order[0] != "c000" || order[len(order)-2] != "db" || order[len(order)-1] != "web" {
		t.Fatalf("containers %d: %v", len(order), order)
	}
	if _, ok := byName["gone"]; ok {
		t.Fatal("a container without values in the range was returned")
	}
	web := byName["web"]
	if len(web) != 2 || web[0].Key != "cpu.percent" || web[1].Key != "memory.used_bytes" {
		t.Fatalf("web %+v", web)
	}
	if got := values(web[0]); got != "4 6" || len(r.Timestamps) != 2 {
		t.Fatalf("web cpu %q", got)
	}
	// db has memory but no CPU: its CPU series is all gaps, not zeros.
	if got := values(byName["db"][0]); got != "- -" {
		t.Fatalf("db cpu %q", got)
	}
	if got := values(byName[fmt.Sprintf("c%03d", seriesPerQuery+4)][0]); got != "4 -" {
		t.Fatalf("last batch cpu %q", got)
	}
	if other, err := s.QueryContainers(ctx, domain.MetricQuery{EnvironmentID: "other-env"}); err != nil || len(other.Series) != 0 {
		t.Fatalf("other environment: %+v %v", other.Series, err)
	}
	if _, err := s.QueryContainers(ctx, domain.MetricQuery{EnvironmentID: env, Keys: []string{"load.1"}}); !errors.Is(err, domain.ErrMetricQuery) {
		t.Fatalf("host key: %v", err)
	}
}

func TestReopenKeepsSeriesAndWatermarks(t *testing.T) {
	clk := testutil.FakeClock()
	path := filepath.Join(t.TempDir(), FileName)
	ctx := context.Background()
	s, err := Open(ctx, Options{Path: path, Clock: clk, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	t0 := clk.Now().Truncate(10 * time.Second)
	if _, err := s.Ingest(ctx, env, []domain.MetricSample{hostSample(t0, 10)}, &domain.MetricCursor{Epoch: "e", LastSeq: 9, Skew: 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(ctx, Options{Path: path, Clock: clk, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if res, _ := s.Ingest(ctx, env, []domain.MetricSample{hostSample(t0, 10)}, nil); res.Duplicates != 3 {
		t.Fatalf("%+v", res)
	}
	if c, ok, _ := s.Cursor(ctx, env); !ok || c.LastSeq != 9 || c.Skew != 3*time.Second {
		t.Fatalf("%+v", c)
	}
}
