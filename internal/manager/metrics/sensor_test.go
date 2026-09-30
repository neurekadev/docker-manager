package metrics

import (
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// sensorSeries finds the series of a key and temperature sensor.
func sensorSeries(t *testing.T, r domain.MetricResult, key, sensor string) domain.MetricSeries {
	t.Helper()
	for _, s := range r.Series {
		if s.Key == key && s.Sensor == sensor {
			return s
		}
	}
	t.Fatalf("no series %s %s in %+v", key, sensor, r.Series)
	return domain.MetricSeries{}
}

func TestSensorSeriesStoreTemperatures(t *testing.T) {
	clk := testutil.FakeClock()
	s := openTest(t, clk)
	ctx := testutil.Context(t)
	t0 := clk.Now().Truncate(time.Hour)
	// 20 minutes: the CPU package alternates 40/60 °C (average 50, maximum
	// 60); the NVMe drive has no reading in the second minute; the ACPI
	// zone reports only in the first minute.
	var batch []domain.MetricSample
	for k := range 120 {
		smp := domain.MetricSample{At: t0.Add(time.Duration(k) * 10 * time.Second), Host: &domain.HostValues{CPUPercent: f(1)},
			Temperatures: []domain.TemperatureValues{{Sensor: "coretemp: Package id 0", Celsius: float64(40 + 20*(k%2))}}}
		if k < 6 || k >= 12 {
			smp.Temperatures = append(smp.Temperatures, domain.TemperatureValues{Sensor: "nvme: Composite", Celsius: 35.25})
		}
		if k < 6 {
			smp.Temperatures = append(smp.Temperatures, domain.TemperatureValues{Sensor: "acpitz", Celsius: 27.8})
		}
		batch = append(batch, smp)
	}
	clk.Set(t0.Add(21 * time.Minute))
	res, err := s.Ingest(ctx, env, batch, nil)
	if err != nil || res.Inserted != 120+120+114+6 || !res.Host {
		t.Fatalf("%+v %v", res, err)
	}
	// Redelivery stores nothing twice.
	if res, err := s.Ingest(ctx, env, batch[:6], nil); err != nil || res.Inserted != 0 || res.Duplicates != 6*4 {
		t.Fatalf("%+v %v", res, err)
	}
	// Raw samples: the missing minute is a gap, never zero; each series is
	// labeled with its sensor, sorted by name.
	r, err := s.Query(ctx, domain.MetricQuery{EnvironmentID: env, Kind: domain.MetricHost, From: t0, To: t0.Add(2 * time.Minute),
		Step: 10 * time.Second, Keys: []string{"temperature.celsius"}})
	if err != nil || r.Resolution != LevelRaw || len(r.Series) != 3 || r.Series[0].Sensor != "acpitz" || r.Series[0].Mount != "" ||
		r.Series[0].Unit != "celsius" {
		t.Fatalf("%+v %v", r.Series, err)
	}
	if got := values(sensorSeries(t, r, "temperature.celsius", "nvme: Composite")); got != "35.25 35.25 35.25 35.25 35.25 35.25 - - - - - -" {
		t.Fatalf("nvme %s", got)
	}
	if got := values(sensorSeries(t, r, "temperature.celsius", "coretemp: Package id 0")); got != "40 60 40 60 40 60 40 60 40 60 40 60" {
		t.Fatalf("cpu %s", got)
	}
	// A sensor without a reading in the range is left out.
	r, err = s.Query(ctx, domain.MetricQuery{EnvironmentID: env, Kind: domain.MetricHost, From: t0.Add(5 * time.Minute),
		To: t0.Add(10 * time.Minute), Keys: []string{"temperature.celsius", "cpu.percent"}})
	if err != nil || len(r.Series) != 3 {
		t.Fatalf("%+v %v", r.Series, err)
	}
	for _, ser := range r.Series {
		if ser.Sensor == "acpitz" {
			t.Fatalf("acpitz without readings listed: %+v", r.Series)
		}
	}
	// Rollups keep the average and the maximum apart.
	if err := s.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Stats(ctx)
	if st.Rows["sensor_raw"] != 240 || st.Rows["sensor_1m"] != 20+19+1 || st.Rows["sensor_15m"] != 3 {
		t.Fatalf("rows %+v", st.Rows)
	}
	r, err = s.Query(ctx, domain.MetricQuery{EnvironmentID: env, Kind: domain.MetricHost, From: t0, To: t0.Add(4 * time.Minute),
		Step: time.Minute, Keys: []string{"temperature.celsius", "temperature.celsius.max"}})
	if err != nil || r.Resolution != LevelMinute {
		t.Fatalf("%+v %v", r, err)
	}
	cpu := "coretemp: Package id 0"
	avg, peak := values(sensorSeries(t, r, "temperature.celsius", cpu)), values(sensorSeries(t, r, "temperature.celsius.max", cpu))
	if avg != "50 50 50 50" || peak != "60 60 60 60" {
		t.Fatalf("1 min: average %s, maximum %s", avg, peak)
	}
	if got := values(sensorSeries(t, r, "temperature.celsius.max", "nvme: Composite")); got != "35.25 - 35.25 35.25" {
		t.Fatalf("nvme %s", got)
	}
	r, err = s.Query(ctx, domain.MetricQuery{EnvironmentID: env, Kind: domain.MetricHost, From: t0, To: t0.Add(15 * time.Minute),
		Step: 15 * time.Minute, Keys: []string{"temperature.celsius", "temperature.celsius.max"}})
	if err != nil || r.Resolution != LevelQuarter || values(sensorSeries(t, r, "temperature.celsius", cpu)) != "50" ||
		values(sensorSeries(t, r, "temperature.celsius.max", cpu)) != "60" || values(sensorSeries(t, r, "temperature.celsius", "acpitz")) != "27.8" {
		t.Fatalf("%+v %v", r.Series, err)
	}
	// After 90 days the sensor series are gone with their data.
	clk.Set(t0.Add(91 * 24 * time.Hour))
	if _, err := s.Retain(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Stats(ctx); st.Series != 0 || st.Rows["sensor_15m"] != 0 {
		t.Fatalf("%+v", st)
	}
}

func TestSensorSeriesCountTowardsTheSeriesCap(t *testing.T) {
	clk := testutil.FakeClock()
	s := openTest(t, clk, func(o *Options) { o.MaxSeries = 3 })
	ctx := testutil.Context(t)
	smp := domain.MetricSample{At: clk.Now().Truncate(10 * time.Second), Host: &domain.HostValues{CPUPercent: f(1)},
		Temperatures: []domain.TemperatureValues{{Sensor: "a", Celsius: 30}, {Sensor: "b", Celsius: 31}, {Sensor: "c", Celsius: 32}}}
	// The host and two sensors fill the 3 series; the third sensor is
	// refused (the host is always accepted).
	if res, err := s.Ingest(ctx, env, []domain.MetricSample{smp}, nil); err != nil || res.Inserted != 3 || res.Refused != 1 {
		t.Fatalf("%+v %v", res, err)
	}
}
