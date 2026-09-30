package protocol

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func metricsWith(temps []TemperatureSample) HostMetricsOutput {
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	return HostMetricsOutput{Epoch: "e", Now: at, IntervalSeconds: 10,
		Batches: []MetricBatch{{Seq: 1, At: at, Temperatures: temps}}}
}

func TestTemperatureSamplesAreBounded(t *testing.T) {
	ok := []TemperatureSample{{Sensor: "coretemp: Package id 0", Celsius: 48.5}, {Sensor: "nvme: Composite", Celsius: 38.85},
		{Sensor: "acpitz", Celsius: -5}}
	if err := metricsWith(ok).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := metricsWith(nil).Validate(); err != nil {
		t.Fatalf("an older agent without temperatures: %v", err)
	}
	many := make([]TemperatureSample, MaxTemperatureSamples+1)
	for i := range many {
		many[i] = TemperatureSample{Sensor: fmt.Sprintf("s%d", i), Celsius: 40}
	}
	for name, temps := range map[string][]TemperatureSample{
		"too many":   many,
		"no name":    {{Sensor: "", Celsius: 40}},
		"long name":  {{Sensor: strings.Repeat("x", MaxSensorNameLen+1), Celsius: 40}},
		"not UTF-8":  {{Sensor: "chip\xff", Celsius: 40}},
		"control":    {{Sensor: "chip\n", Celsius: 40}},
		"duplicate":  {{Sensor: "nvme: Composite", Celsius: 40}, {Sensor: "nvme: Composite", Celsius: 41}},
		"hot":        {{Sensor: "chip", Celsius: 1000}},
		"cold":       {{Sensor: "chip", Celsius: -273.15}},
		"not finite": {{Sensor: "chip", Celsius: math.NaN()}},
	} {
		if err := metricsWith(temps).Validate(); !errors.Is(err, ErrInvalidFrame) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
