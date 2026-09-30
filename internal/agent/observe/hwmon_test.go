package observe

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"unicode/utf8"

	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// failFS fails opening the named paths (an unreadable sysfs attribute).
type failFS struct {
	fs.FS
	fail map[string]error
}

func (f failFS) Open(name string) (fs.File, error) {
	if err, ok := f.fail[name]; ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return f.FS.Open(name)
}

func sysFile(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s + "\n")} }

// hwmonFS is a host with an ACPI zone, an Intel CPU, two NVMe drives, a
// chip without a name, a chip with unconnected and broken inputs and an
// AMD CPU numbered hwmon10 (sorted after hwmon5, not after hwmon1).
func hwmonFS() fstest.MapFS {
	return fstest.MapFS{
		"class/hwmon/hwmon0/name":         sysFile("acpitz"),
		"class/hwmon/hwmon0/temp1_input":  sysFile("27800"),
		"class/hwmon/hwmon0/temp2_input":  sysFile("29000"),
		"class/hwmon/hwmon1/name":         sysFile("coretemp"),
		"class/hwmon/hwmon1/temp1_label":  sysFile("Package id 0"),
		"class/hwmon/hwmon1/temp1_input":  sysFile("48500"),
		"class/hwmon/hwmon1/temp2_label":  sysFile("Core 0"),
		"class/hwmon/hwmon1/temp2_input":  sysFile("45000"),
		"class/hwmon/hwmon1/temp2_max":    sysFile("100000"),
		"class/hwmon/hwmon2/name":         sysFile("nvme"),
		"class/hwmon/hwmon2/temp1_label":  sysFile("Composite"),
		"class/hwmon/hwmon2/temp1_input":  sysFile("38850"),
		"class/hwmon/hwmon2/temp2_label":  sysFile("Sensor 1"),
		"class/hwmon/hwmon2/temp2_input":  sysFile("38850"),
		"class/hwmon/hwmon2/temp3_label":  sysFile("Sensor 2"),
		"class/hwmon/hwmon2/temp3_input":  sysFile("40000"), // unreadable (failFS)
		"class/hwmon/hwmon3/name":         sysFile("nvme"),
		"class/hwmon/hwmon3/temp1_label":  sysFile("Composite"),
		"class/hwmon/hwmon3/temp1_input":  sysFile("41000"),
		"class/hwmon/hwmon4/temp1_input":  sysFile("55000"),
		"class/hwmon/hwmon5/name":         sysFile("nct6775"),
		"class/hwmon/hwmon5/temp1_input":  sysFile("0"),
		"class/hwmon/hwmon5/temp2_input":  sysFile("-128000"),
		"class/hwmon/hwmon5/temp3_input":  sysFile("255000"),
		"class/hwmon/hwmon5/temp4_input":  sysFile("n/a"),
		"class/hwmon/hwmon5/temp5_input":  sysFile("30000"),
		"class/hwmon/hwmon5/temp5_fault":  sysFile("1"),
		"class/hwmon/hwmon5/fan1_input":   sysFile("1200"),
		"class/hwmon/hwmon10/name":        sysFile("k10temp"),
		"class/hwmon/hwmon10/temp1_label": sysFile("Tctl"),
		"class/hwmon/hwmon10/temp1_input": sysFile("61250"),
		"class/hwmon/README":              sysFile("not a chip"),
	}
}

func sensorList(ts []protocol.TemperatureSample) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = fmt.Sprintf("%s=%g", t.Sensor, t.Celsius)
	}
	return strings.Join(parts, "; ")
}

func TestReadTemperaturesNamesEverySensor(t *testing.T) {
	fsys := failFS{FS: hwmonFS(), fail: map[string]error{"class/hwmon/hwmon2/temp3_input": errors.New("input/output error")}}
	got, err := readTemperatures(fsys)
	if err != nil {
		t.Fatal(err)
	}
	// Labels name the input; without one temp1 is the chip alone and the
	// others add temp<N>; the second NVMe drive is told apart; a chip
	// without a name keeps its hwmon name. Unreadable, unconnected (0),
	// implausible, garbage and faulted inputs are absent, never zero.
	want := "acpitz=27.8; acpitz: temp2=29; coretemp: Package id 0=48.5; coretemp: Core 0=45; nvme: Composite=38.85; " +
		"nvme: Sensor 1=38.85; nvme: Composite (2)=41; hwmon4=55; k10temp: Tctl=61.25"
	if s := sensorList(got); s != want {
		t.Fatalf("sensors\n got %s\nwant %s", s, want)
	}
	if err := (protocol.HostMetricsOutput{Epoch: "e", Now: testutil.Epoch, IntervalSeconds: 10,
		Batches: []protocol.MetricBatch{{Seq: 1, At: testutil.Epoch, Temperatures: got}}}).Validate(); err != nil {
		t.Fatalf("the sample does not validate: %v", err)
	}
}

func TestReadTemperaturesWithoutSensors(t *testing.T) {
	// No sysfs or no hwmon class (a VM): no sensors, nothing to report.
	for name, fsys := range map[string]fs.FS{
		"empty":    fstest.MapFS{},
		"no hwmon": fstest.MapFS{"class/net/eth0/address": sysFile("00:00:00:00:00:01")},
		"no temps": fstest.MapFS{"class/hwmon/hwmon0/name": sysFile("fan"), "class/hwmon/hwmon0/fan1_input": sysFile("900")},
	} {
		if got, err := readTemperatures(fsys); err != nil || len(got) != 0 {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
	// A class directory that cannot be listed is an error (logged once by
	// the sampler), still without sensors.
	fsys := failFS{FS: hwmonFS(), fail: map[string]error{"class/hwmon": fs.ErrPermission}}
	if got, err := readTemperatures(fsys); err == nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	// An unreadable chip name falls back to the hwmon name.
	fsys = failFS{FS: hwmonFS(), fail: map[string]error{"class/hwmon/hwmon1/name": fs.ErrPermission}}
	got, err := readTemperatures(fsys)
	if err != nil || !strings.Contains(sensorList(got), "hwmon1: Package id 0=48.5") {
		t.Fatalf("%s %v", sensorList(got), err)
	}
}

func TestReadTemperaturesIsBounded(t *testing.T) {
	fsys := fstest.MapFS{"class/hwmon/hwmon0/name": sysFile("many")}
	for n := 1; n <= 40; n++ {
		fsys["class/hwmon/hwmon0/temp"+strconv.Itoa(n)+"_input"] = sysFile(strconv.Itoa(20000 + n*100))
	}
	long := strings.Repeat("ü", 40) // 80 bytes
	fsys["class/hwmon/hwmon1/name"] = sysFile("board\tchip")
	fsys["class/hwmon/hwmon1/temp1_label"] = sysFile(long + "\x07")
	fsys["class/hwmon/hwmon1/temp1_input"] = sysFile("33000")
	got, err := readTemperatures(fsys)
	if err != nil || len(got) != protocol.MaxTemperatureSamples {
		t.Fatalf("%d sensors %v", len(got), err)
	}
	if got[0].Sensor != "many" || got[1].Sensor != "many: temp2" || got[31].Sensor != "many: temp32" {
		t.Fatalf("%s", sensorList(got))
	}
	// Long names are cut on a character boundary; control characters and
	// tabs never reach the manager.
	delete(fsys, "class/hwmon/hwmon0/name")
	for n := 1; n <= 40; n++ {
		delete(fsys, "class/hwmon/hwmon0/temp"+strconv.Itoa(n)+"_input")
	}
	got, err = readTemperatures(fsys)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	s := got[0].Sensor
	if len(s) > protocol.MaxSensorNameLen || !utf8.ValidString(s) || !strings.HasPrefix(s, "board chip: üü") || strings.ContainsAny(s, "\t\x07") {
		t.Fatalf("name %q (%d bytes)", s, len(s))
	}
}

func TestTicksCarryTemperatures(t *testing.T) {
	s := newTestSampler(t, procFS(statA, meminfoText, "0 0 0", "1", netA), nil)
	sys := hwmonFS()
	s.opts.Sys = sys
	b := s.Tick(context.Background(), testutil.Epoch)
	if len(b.Temperatures) != 10 || b.Temperatures[2].Sensor != "coretemp: Package id 0" || b.Temperatures[2].Celsius != 48.5 {
		t.Fatalf("%s", sensorList(b.Temperatures))
	}
	// A sensor without a reading now is absent from this sample (a gap),
	// not zero.
	delete(sys, "class/hwmon/hwmon1/temp1_input")
	b = s.Tick(context.Background(), testutil.Epoch.Add(protocol.MetricsInterval))
	if len(b.Temperatures) != 9 || strings.Contains(sensorList(b.Temperatures), "Package id 0") {
		t.Fatalf("%s", sensorList(b.Temperatures))
	}
	// Without hwmon the batch has none.
	s.opts.Sys = fstest.MapFS{}
	if b := s.Tick(context.Background(), testutil.Epoch.Add(2*protocol.MetricsInterval)); b.Temperatures != nil {
		t.Fatalf("%v", b.Temperatures)
	}
}
