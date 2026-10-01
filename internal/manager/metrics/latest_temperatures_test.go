package metrics

import (
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func TestLatestTemperaturesAreTheCurrentSensors(t *testing.T) {
	clk := testutil.FakeClock()
	s := openTest(t, clk)
	ctx := testutil.Context(t)
	if _, _, ok, err := s.LatestTemperatures(ctx, env); ok || err != nil {
		t.Fatalf("no samples yet: %v %v", ok, err)
	}
	t0 := clk.Now().Truncate(time.Hour)
	// The ACPI zone stops reporting ten minutes before the others.
	var batch []domain.MetricSample
	for k := range 90 {
		smp := domain.MetricSample{At: t0.Add(time.Duration(k) * 10 * time.Second), Host: &domain.HostValues{CPUPercent: f(1)},
			Temperatures: []domain.TemperatureValues{{Sensor: "coretemp: Package id 0", Celsius: 40 + float64(k)/10}}}
		if k < 30 {
			smp.Temperatures = append(smp.Temperatures, domain.TemperatureValues{Sensor: "acpitz", Celsius: 27.8})
		}
		batch = append(batch, smp)
	}
	clk.Set(t0.Add(16 * time.Minute))
	if _, err := s.Ingest(ctx, env, batch, nil); err != nil {
		t.Fatal(err)
	}
	temps, at, ok, err := s.LatestTemperatures(ctx, env)
	if err != nil || !ok || !at.Equal(t0.Add(89*10*time.Second)) || len(temps) != 1 || temps[0].Sensor != "coretemp: Package id 0" ||
		temps[0].Celsius != 48.9 {
		t.Fatalf("%+v %v %v %v", temps, at, ok, err)
	}
	// Another environment has none.
	if _, _, ok, _ := s.LatestTemperatures(ctx, "env-other"); ok {
		t.Fatal("another environment's readings")
	}
}
