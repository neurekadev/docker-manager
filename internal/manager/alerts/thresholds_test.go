package alerts

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// fakeMetrics serves the latest host sample and temperatures of env-1,
// stamped with the fixture's clock when set.
type fakeMetrics struct {
	mu     sync.Mutex
	latest domain.LatestMetrics
	temps  []domain.TemperatureValues
	at     time.Time
}

func (m *fakeMetrics) Latest(_ context.Context, env string) (domain.LatestMetrics, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if env != "env-1" || m.at.IsZero() {
		return domain.LatestMetrics{}, false, nil
	}
	l := m.latest
	l.At = m.at
	return l, true, nil
}

func (m *fakeMetrics) LatestTemperatures(_ context.Context, env string) ([]domain.TemperatureValues, time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if env != "env-1" || m.at.IsZero() || len(m.temps) == 0 {
		return nil, time.Time{}, false, nil
	}
	return m.temps, m.at, true, nil
}

// sample sets env-1's memory (percent of 16 GiB), the Docker data disk
// (percent of 100 GiB; negative: not reported) and two CPU sensors, read
// now, and evaluates them.
func (f *fixture) sample(m *fakeMetrics, memory, disk, celsius float64) {
	const gib = int64(1) << 30
	used, total := int64(memory*float64(16*gib)/100), 16*gib
	m.mu.Lock()
	m.latest = domain.LatestMetrics{Host: domain.HostValues{MemoryUsedBytes: &used, MemoryTotalBytes: &total}}
	if disk >= 0 {
		m.latest.Disks = []domain.DiskValues{{Mount: "docker", UsedBytes: int64(disk * float64(gib)), TotalBytes: 100 * gib}}
	}
	m.temps = []domain.TemperatureValues{{Sensor: "coretemp: Core 0", Celsius: celsius - 5}, {Sensor: "coretemp: Package id 0", Celsius: celsius}}
	m.at = f.clk.Now().UTC()
	m.mu.Unlock()
	if err := f.svc.EvaluateThresholds(f.ctx, "env-1"); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) metrics() *fakeMetrics {
	m := &fakeMetrics{}
	f.svc.SetMetrics(m)
	return m
}

func TestAShortSpikeRaisesNothing(t *testing.T) {
	f := newFixture(t)
	m := f.metrics()
	f.sample(m, 97, 10, 50)
	f.clk.Advance(ThresholdSustain - time.Minute)
	f.sample(m, 97, 10, 50)
	// It drops below before the sustain time ends: it starts again.
	f.clk.Advance(30 * time.Second)
	f.sample(m, 50, 10, 50)
	f.clk.Advance(30 * time.Second)
	f.sample(m, 97, 10, 50)
	f.clk.Advance(ThresholdSustain - time.Minute)
	f.sample(m, 97, 10, 50)
	if as := f.firing(); len(as) != 0 {
		t.Fatalf("%+v", as)
	}
}

func TestMemoryAboveItsThresholdsRaisesAndResolvesWithAMargin(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	m := f.metrics()
	f.sample(m, 92, 10, 50)
	f.clk.Advance(ThresholdSustain)
	f.sample(m, 93, 10, 50)
	a := f.one()
	if a.Kind != domain.NotifyMemory || a.Severity != domain.AlertWarning || a.Title != "homelab is running out of memory" ||
		a.Facts["usedPercent"] != "93" || a.Facts["warningAt"] != "90" || a.Facts["criticalAt"] != "95" {
		t.Fatalf("%+v", a)
	}
	if got := f.dispatch(); len(got) != 1 || got[0].msg.Tone != domain.ToneWarning {
		t.Fatalf("%+v", got)
	}
	// Lower again but within the margin: it stays as it is (the peak
	// is kept, nothing is sent).
	f.sample(m, 88, 10, 50)
	if a := f.one(); a.Facts["usedPercent"] != "93" || a.Revision != 1 {
		t.Fatalf("%+v", a)
	}
	// Critical, held long enough: worse, sent again.
	f.sample(m, 97, 10, 50)
	f.clk.Advance(ThresholdSustain)
	f.sample(m, 98, 10, 50)
	if a := f.one(); a.Severity != domain.AlertCritical || a.Facts["usedPercent"] != "98" || a.Escalation != 1 {
		t.Fatalf("%+v", a)
	}
	if got := f.dispatch(); len(got) != 1 || got[0].msg.Tone != domain.ToneCritical {
		t.Fatalf("%+v", got)
	}
	// Below the warning level by more than the margin: resolved.
	f.sample(m, 80, 10, 50)
	if len(f.firing()) != 0 {
		t.Fatal("still firing")
	}
	if got := f.dispatch(); len(got) != 1 || got[0].msg.Title != "[Docker Manager] Resolved: homelab is running out of memory" ||
		got[0].msg.Tone != domain.ToneSuccess {
		t.Fatalf("%+v", got)
	}
}

func TestDiskSpaceAndTemperatureUseTheEnvironmentsThresholds(t *testing.T) {
	f := newFixture(t)
	m := f.metrics()
	set, err := store.GetAlertSettings(f.ctx, f.db)
	if err != nil {
		t.Fatal(err)
	}
	// env-1 allows its disk to fill up to 97% before a warning (99%
	// critical) and turns its memory alert off.
	set.Overrides = []domain.AlertThresholdOverride{{EnvironmentID: "env-1", DiskSpaceWarning: intp(97), DiskSpaceCritical: intp(99),
		MemoryWarning: intp(0), MemoryCritical: intp(0)}}
	if _, err := f.svc.UpdateSettings(f.ctx, set.Revision, set); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		f.sample(m, 99, 96, 91)
		f.clk.Advance(ThresholdSustain)
	}
	f.sample(m, 99, 96, 91)
	as := f.firing()
	if len(as) != 1 || as[0].Kind != domain.NotifyTemperature || as[0].Severity != domain.AlertCritical ||
		as[0].Facts["sensor"] != "coretemp: Package id 0" || as[0].Facts["celsius"] != "91" || as[0].Title != "homelab is running hot" {
		t.Fatalf("%+v", as)
	}
	f.sample(m, 99, 98, 91)
	f.clk.Advance(ThresholdSustain)
	f.sample(m, 99, 98, 91)
	disk, found, err := store.FiringAlert(f.ctx, f.db, diskSpaceKey("env-1", "docker"))
	if err != nil || !found || disk.Severity != domain.AlertWarning || disk.Facts["mount"] != "docker" || disk.Facts["warningAt"] != "97" ||
		disk.Facts["criticalAt"] != "99" || disk.Title != "Docker data disk on homelab is almost full" || disk.ResourceID != "docker" {
		t.Fatalf("%v %v %+v", err, found, disk)
	}
	// The filesystem is no longer reported: it ends silently.
	f.sample(m, 99, -1, 91)
	if _, found, _ := store.FiringAlert(f.ctx, f.db, diskSpaceKey("env-1", "docker")); found {
		t.Fatal("still firing")
	}
}

func TestStaleSamplesChangeNothing(t *testing.T) {
	f := newFixture(t)
	m := f.metrics()
	f.sample(m, 99, 10, 50)
	f.clk.Advance(ThresholdSustain)
	f.sample(m, 99, 10, 50)
	if len(f.firing()) != 1 {
		t.Fatal("not firing")
	}
	// The environment stops reporting: its last sample ages, the alert
	// stays.
	f.clk.Advance(ThresholdStale + time.Second)
	m.mu.Lock()
	m.latest.Host.MemoryUsedBytes = new(int64)
	m.mu.Unlock()
	if err := f.svc.EvaluateThresholds(f.ctx, "env-1"); err != nil {
		t.Fatal(err)
	}
	if len(f.firing()) != 1 {
		t.Fatal("resolved by a stale sample")
	}
}

func TestThresholdsAreValidated(t *testing.T) {
	f := newFixture(t)
	set, err := store.GetAlertSettings(f.ctx, f.db)
	if err != nil || set.Thresholds != domain.DefaultAlertThresholds() {
		t.Fatalf("%v %+v", err, set)
	}
	for name, c := range map[string]struct {
		mutate func(*domain.AlertSettings)
		field  string
	}{
		"warning above critical": {func(s *domain.AlertSettings) { s.Thresholds.MemoryWarning = 96 }, "memoryWarning"},
		"too hot":                {func(s *domain.AlertSettings) { s.Thresholds.TemperatureCritical = 151 }, "temperatureCritical"},
		"unknown environment": {func(s *domain.AlertSettings) {
			s.Overrides = []domain.AlertThresholdOverride{{EnvironmentID: "env-9"}}
		}, "overrides[0].environmentId"},
		"twice": {func(s *domain.AlertSettings) {
			s.Overrides = []domain.AlertThresholdOverride{{EnvironmentID: "env-1"}, {EnvironmentID: "env-1"}}
		}, "overrides[1].environmentId"},
		// An override is checked with the defaults it keeps.
		"override above the default critical": {func(s *domain.AlertSettings) {
			s.Overrides = []domain.AlertThresholdOverride{{EnvironmentID: "env-2", DiskSpaceWarning: intp(99)}}
		}, "overrides[0].diskSpaceWarning"},
	} {
		next := set
		next.Overrides = nil
		c.mutate(&next)
		_, err := f.svc.UpdateSettings(f.ctx, set.Revision, next)
		fe, ok := err.(*domain.FieldError)
		if !ok || fe.Field != c.field {
			t.Errorf("%s: %v, want a field error on %s", name, err, c.field)
		}
	}
	// A stale revision is refused.
	if _, err := f.svc.UpdateSettings(f.ctx, set.Revision+1, set); err != domain.ErrRevisionConflict {
		t.Fatalf("%v", err)
	}
	// 0 turns a level off: only critical is left.
	next := set
	next.Thresholds.MemoryWarning = 0
	got, err := f.svc.UpdateSettings(f.ctx, set.Revision, next)
	if err != nil || got.Revision != set.Revision+1 || got.Thresholds.MemoryWarning != 0 {
		t.Fatalf("%v %+v", err, got)
	}
}
