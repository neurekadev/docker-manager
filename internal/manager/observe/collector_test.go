package observe

import (
	"fmt"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func TestSampleFromCarriesTemperatures(t *testing.T) {
	b := protocol.MetricBatch{Seq: 1, At: testutil.Epoch, Temperatures: []protocol.TemperatureSample{
		{Sensor: "coretemp: Package id 0", Celsius: 48.5}, {Sensor: "nvme: Composite", Celsius: 38.85}}}
	s := sampleFrom(b, 5*time.Second, testutil.Epoch.Add(time.Minute))
	if fmt.Sprint(s.Temperatures) != "[{coretemp: Package id 0 48.5} {nvme: Composite 38.85}]" || !s.At.Equal(testutil.Epoch.Add(5*time.Second)) {
		t.Fatalf("%+v", s)
	}
	// An older agent sends none.
	if s := sampleFrom(protocol.MetricBatch{Seq: 2, At: testutil.Epoch}, 0, testutil.Epoch); s.Temperatures != nil {
		t.Fatalf("%+v", s.Temperatures)
	}
}
