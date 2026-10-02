package smartctl

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/protocol"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func i64(v int64) *int64 { return &v }
func iptr(v int) *int    { return &v }
func bptr(v bool) *bool  { return &v }

func TestParseScanKeepsValidDevicesOnce(t *testing.T) {
	devs, err := parseScan(fixture(t, "scan.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := []ScanDevice{
		{Name: "/dev/sda", Type: "sat", Protocol: protocol.DiskATA},
		{Name: "/dev/sdb", Type: "scsi", Protocol: protocol.DiskSCSI},
		{Name: "/dev/sdc", Type: "scsi", Protocol: protocol.DiskSCSI, OpenError: "/dev/sdc: Unknown USB bridge [0x152d:0x0578 (0x508)]"},
		{Name: "/dev/nvme0", Type: "nvme", Protocol: protocol.DiskNVMe},
		{Name: "/dev/bus/0", Type: "megaraid,0", Protocol: protocol.DiskSCSI},
		{Name: "/dev/bus/0", Type: "megaraid,1", Protocol: protocol.DiskSCSI},
	}
	if !reflect.DeepEqual(devs, want) {
		t.Fatalf("scan = %+v\nwant %+v", devs, want)
	}
	if got := ScanOpenError(devs[2]); got != protocol.DiskErrUnsupported {
		t.Errorf("unknown USB bridge = %q, want unsupported", got)
	}
	if _, err := parseScan([]byte("not json")); err == nil {
		t.Error("garbage scan output accepted")
	}
}

func TestParseReadFixtures(t *testing.T) {
	sda := ScanDevice{Name: "/dev/sda", Type: "sat", Protocol: protocol.DiskATA}
	for _, c := range []struct {
		file  string
		scan  ScanDevice
		exit  ExitBits
		check func(t *testing.T, r Reading)
	}{
		{"ata_healthy.json", sda, 0, func(t *testing.T, r Reading) {
			d := r.Device
			want := protocol.SMARTDevice{Name: "/dev/sda", Type: "sat", Protocol: protocol.DiskATA, Model: "WDC WD40EFZX-68AWUN0",
				Serial: "WD-WX12D3456789", Firmware: "81.00B81", CapacityBytes: 4000787030016, RotationRPM: iptr(5400),
				SMARTSupported: true, Passed: bptr(true), TemperatureC: iptr(36), PowerOnHours: i64(20560), Reallocated: i64(0),
				Pending: i64(0), OfflineUncorrectable: i64(0), State: protocol.DiskOK,
				Attributes: []protocol.SMARTAttributeRow{
					{ID: 1, Name: "Raw_Read_Error_Rate", Value: iptr(200), Worst: iptr(200), Threshold: iptr(51), Raw: i64(0), Prefailure: true},
					{ID: 5, Name: "Reallocated_Sector_Ct", Value: iptr(200), Worst: iptr(200), Threshold: iptr(140), Raw: i64(0),
						Prefailure: true},
					{ID: 9, Name: "Power_On_Hours", Value: iptr(72), Worst: iptr(72), Threshold: iptr(0), Raw: i64(20560)},
					{ID: 194, Name: "Temperature_Celsius", Value: iptr(114), Worst: iptr(101), Threshold: iptr(0), Raw: i64(36),
						RawText: "36 (Min/Max 20/49)"},
					{ID: 197, Name: "Current_Pending_Sector", Value: iptr(200), Worst: iptr(200), Threshold: iptr(0), Raw: i64(0)},
					{ID: 198, Name: "Offline_Uncorrectable", Value: iptr(100), Worst: iptr(253), Threshold: iptr(0), Raw: i64(0)},
				},
				Values: []protocol.SMARTValue{{Key: "power_cycle_count", Value: 41}}}
			if !reflect.DeepEqual(d, want) {
				t.Fatalf("got  %+v\nwant %+v", d, want)
			}
		}},
		{"ata_when_failed.json", sda, BitDiskFailing | BitPrefail, func(t *testing.T, r Reading) {
			d := r.Device
			if d.State != protocol.DiskFailing || d.Passed == nil || *d.Passed {
				t.Fatalf("state %q passed %v", d.State, d.Passed)
			}
			want := []protocol.SMARTAttribute{{ID: 5, Name: "Reallocated_Sector_Ct", WhenFailed: "now"},
				{ID: 190, Name: "Airflow_Temperature_Cel", WhenFailed: "past"}}
			if !reflect.DeepEqual(d.FailingAttributes, want) {
				t.Errorf("failing attributes %+v", d.FailingAttributes)
			}
			if *d.Reallocated != 3920 || *d.Pending != 16 {
				t.Errorf("reallocated %d pending %d", *d.Reallocated, *d.Pending)
			}
		}},
		{"ata_past_only.json", ScanDevice{Name: "/dev/sde", Type: "sat"}, BitPastPrefail, func(t *testing.T, r Reading) {
			// An attribute that was at its threshold once (a hot day)
			// warns; it does not mark the disk failing.
			if r.Device.State != protocol.DiskWarning || len(r.Device.FailingAttributes) != 1 {
				t.Fatalf("state %q attributes %+v", r.Device.State, r.Device.FailingAttributes)
			}
		}},
		{"ata_pending.json", ScanDevice{Name: "/dev/sdb", Type: "sat"}, BitErrorLog, func(t *testing.T, r Reading) {
			d := r.Device
			if d.State != protocol.DiskWarning || *d.Reallocated != 8 || *d.ReportedUncorrectable != 2 || *d.Pending != 8 ||
				*d.OfflineUncorrectable != 1 || d.Protocol != protocol.DiskATA {
				t.Fatalf("%+v", d)
			}
		}},
		{"nvme_healthy.json", ScanDevice{Name: "/dev/nvme0", Type: "nvme"}, 0, func(t *testing.T, r Reading) {
			d := r.Device
			if d.State != protocol.DiskOK || d.Protocol != protocol.DiskNVMe || *d.CriticalWarning != 0 || *d.AvailableSpare != 100 ||
				*d.AvailableSpareThreshold != 10 || *d.PercentageUsed != 2 || *d.MediaErrors != 0 || *d.PowerOnHours != 5012 ||
				*d.TemperatureC != 38 || d.CapacityBytes != 1000204886016 || d.RotationRPM != nil {
				t.Fatalf("%+v", d)
			}
			// The whole health log in smartctl's order; no ATA table.
			want := []protocol.SMARTValue{{Key: "critical_warning"}, {Key: "temperature", Value: 38}, {Key: "available_spare", Value: 100},
				{Key: "available_spare_threshold", Value: 10}, {Key: "percentage_used", Value: 2},
				{Key: "data_units_read", Value: 38519204}, {Key: "data_units_written", Value: 43826192}, {Key: "power_cycles", Value: 212},
				{Key: "power_on_hours", Value: 5012}, {Key: "unsafe_shutdowns", Value: 18}, {Key: "media_errors"},
				{Key: "num_err_log_entries"}}
			if !reflect.DeepEqual(d.Values, want) || d.Attributes != nil {
				t.Errorf("values %+v attributes %+v", d.Values, d.Attributes)
			}
		}},
		{"nvme_critical.json", ScanDevice{Name: "/dev/nvme1", Type: "nvme"}, BitDiskFailing, func(t *testing.T, r Reading) {
			d := r.Device
			if d.State != protocol.DiskFailing || *d.CriticalWarning != 4 || *d.PercentageUsed != 97 {
				t.Fatalf("%+v", d)
			}
			if d.MediaErrors == nil || *d.MediaErrors != math.MaxInt64 {
				t.Errorf("a 128-bit media error count is clamped, got %v", d.MediaErrors)
			}
			// Temperature and power-on hours fall back to the health log.
			if *d.TemperatureC != 51 || *d.PowerOnHours != 26280 || d.CapacityBytes != 500107862016 {
				t.Errorf("fallbacks: %+v", d)
			}
		}},
		{"scsi.json", ScanDevice{Name: "/dev/sdb", Type: "scsi"}, 0, func(t *testing.T, r Reading) {
			d := r.Device
			if d.State != protocol.DiskWarning || d.Protocol != protocol.DiskSCSI || d.Model != "SEAGATE ST4000NM0023" ||
				d.Firmware != "GS0F" || *d.GrownDefects != 12 || *d.UncorrectedErrors != 3 || *d.PowerOnHours != 51034 {
				t.Fatalf("%+v", d)
			}
			want := []protocol.SMARTValue{{Key: "read.total_errors_corrected", Value: 5}, {Key: "read.total_uncorrected_errors", Value: 1},
				{Key: "write.total_errors_corrected"}, {Key: "write.total_uncorrected_errors"},
				{Key: "verify.total_errors_corrected", Value: 2}, {Key: "verify.total_uncorrected_errors", Value: 2}}
			if !reflect.DeepEqual(d.Values, want) {
				t.Errorf("values %+v", d.Values)
			}
		}},
		{"usb_unsupported.json", ScanDevice{Name: "/dev/sdc", Type: "sat"}, BitCommandLine, func(t *testing.T, r Reading) {
			d := r.Device
			if d.State != protocol.DiskError || d.ErrorCode != protocol.DiskErrUnsupported || d.Name != "/dev/sdc" {
				t.Fatalf("%+v", d)
			}
		}},
		{"standby.json", sda, StandbyExit, func(t *testing.T, r Reading) {
			if !r.Standby || r.Device.State != protocol.DiskSleeping || r.Device.Name != "/dev/sda" || r.Device.Model != "" {
				t.Fatalf("%+v", r)
			}
		}},
		{"permission_denied.json", sda, BitOpenFailed, func(t *testing.T, r Reading) {
			if r.Device.State != protocol.DiskError || r.Device.ErrorCode != protocol.DiskErrPermissionDenied {
				t.Fatalf("%+v", r.Device)
			}
		}},
		{"virtual_disk.json", sda, BitCommandFailed, func(t *testing.T, r Reading) {
			d := r.Device
			if d.State != protocol.DiskError || d.ErrorCode != protocol.DiskErrUnsupported || d.SMARTSupported ||
				d.Model != "QEMU QEMU HARDDISK" || d.CapacityBytes != 34359738368 {
				t.Fatalf("%+v", d)
			}
		}},
	} {
		t.Run(c.file, func(t *testing.T) {
			r := parseRead(c.scan, fixture(t, c.file), c.exit, "")
			c.check(t, r)
			out := protocol.HostHealthOutput{SampledAt: testEpoch, SMART: protocol.SMARTReport{Status: protocol.SMARTOK,
				Devices: []protocol.SMARTDevice{r.Device}}, RAID: protocol.RAIDReport{ReadAt: testEpoch}}
			if err := out.Validate(); err != nil {
				t.Errorf("parsed device does not validate: %v", err)
			}
		})
	}
}

func TestParseReadWithoutJSON(t *testing.T) {
	sda := ScanDevice{Name: "/dev/sda", Type: "sat"}
	r := parseRead(sda, []byte("Smartctl open device: /dev/sda failed: No such device"), BitOpenFailed, "")
	if r.Device.State != protocol.DiskError || r.Device.ErrorCode != protocol.DiskErrOpenFailed {
		t.Fatalf("%+v", r.Device)
	}
	r = parseRead(sda, nil, BitOpenFailed, "open failed: Operation not permitted")
	if r.Device.ErrorCode != protocol.DiskErrPermissionDenied {
		t.Fatalf("stderr is classified too: %+v", r.Device)
	}
	// Exit 3 without a standby marker is not a sleeping disk.
	r = parseRead(sda, []byte(`{"smartctl":{"exit_status":3}}`), StandbyExit, "")
	if r.Standby || r.Device.State != protocol.DiskError {
		t.Fatalf("%+v", r)
	}
}

func TestExitBits(t *testing.T) {
	b := ExitBits(BitOpenFailed | BitErrorLog)
	if !b.Has(BitOpenFailed) || b.Has(BitCommandLine) || !b.DeviceProblem() {
		t.Errorf("bits %08b", b)
	}
	if ExitBits(BitCommandLine | BitOpenFailed).DeviceProblem() {
		t.Error("open failures are not device problems")
	}
}

func TestBoundKeepsValidUTF8(t *testing.T) {
	if got := bound("abc€", 5); got != "abc" {
		t.Errorf("bound = %q", got)
	}
	if got := bound("abc", 5); got != "abc" {
		t.Errorf("bound = %q", got)
	}
}

// TestParseReadNeverHealthyWithoutData: a disk is only healthy on a
// verdict or values. SMART turned off on the drive, or a read that got
// neither, is state error; smartctl's DISK FAILING exit bit stands in
// for a verdict the JSON lacks.
func TestParseReadNeverHealthyWithoutData(t *testing.T) {
	sda := ScanDevice{Name: "/dev/sda", Type: "sat", Protocol: protocol.DiskATA}
	device := `"device":{"name":"/dev/sda","type":"sat","protocol":"ATA"},"model_name":"WDC WD40EFZX","serial_number":"WD-1"`
	for _, c := range []struct {
		name  string
		json  string
		exit  ExitBits
		state string
		code  string
	}{
		{"SMART turned off", `{` + device + `,"smart_support":{"available":true,"enabled":false}}`, BitCommandFailed,
			protocol.DiskError, protocol.DiskErrSMARTDisabled},
		{"no verdict, no values", `{` + device + `,"smart_support":{"available":true,"enabled":true}}`, BitCommandFailed,
			protocol.DiskError, protocol.DiskErrNoData},
		{"DISK FAILING exit bit only", `{` + device + `,"smart_support":{"available":true,"enabled":true}}`, BitDiskFailing,
			protocol.DiskFailing, ""},
		{"attributes without a verdict", `{` + device + `,"smart_support":{"available":true,"enabled":true},` +
			`"ata_smart_attributes":{"table":[{"id":5,"name":"Reallocated_Sector_Ct","when_failed":"","raw":{"value":0}}]}}`,
			BitCommandFailed, protocol.DiskOK, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := parseRead(sda, []byte(c.json), c.exit, "")
			if r.Device.State != c.state || r.Device.ErrorCode != c.code {
				t.Fatalf("state %q code %q, want %q %q: %+v", r.Device.State, r.Device.ErrorCode, c.state, c.code, r.Device)
			}
			if r.Device.Model != "WDC WD40EFZX" {
				t.Errorf("the identity is kept: %+v", r.Device)
			}
			out := protocol.HostHealthOutput{SampledAt: testEpoch, SMART: protocol.SMARTReport{Status: protocol.SMARTOK,
				Devices: []protocol.SMARTDevice{r.Device}}, RAID: protocol.RAIDReport{ReadAt: testEpoch}}
			if err := out.Validate(); err != nil {
				t.Errorf("does not validate: %v", err)
			}
		})
	}
}

// TestParseReadEndToEndErrors: attribute 184 counts end-to-end errors (a
// warning).
func TestParseReadEndToEndErrors(t *testing.T) {
	sda := ScanDevice{Name: "/dev/sda", Type: "sat", Protocol: protocol.DiskATA}
	r := parseRead(sda, []byte(`{"device":{"name":"/dev/sda","type":"sat","protocol":"ATA"},"smart_status":{"passed":true},`+
		`"ata_smart_attributes":{"table":[{"id":184,"name":"End-to-End_Error","when_failed":"","raw":{"value":2}}]}}`), 0, "")
	if r.Device.EndToEndErrors == nil || *r.Device.EndToEndErrors != 2 || r.Device.State != protocol.DiskWarning {
		t.Fatalf("%+v", r.Device)
	}
}

// TestParseReadNVMeTemperatureOnly: an NVMe critical warning that is only
// the temperature bit is a warning; smartctl's failed verdict for it does
// not make the drive failing.
func TestParseReadNVMeTemperatureOnly(t *testing.T) {
	nvme := ScanDevice{Name: "/dev/nvme0", Type: "nvme", Protocol: protocol.DiskNVMe}
	r := parseRead(nvme, []byte(`{"device":{"name":"/dev/nvme0","type":"nvme","protocol":"NVMe"},"smart_status":{"passed":false},`+
		`"nvme_smart_health_information_log":{"critical_warning":2,"temperature":78,"available_spare":100,"available_spare_threshold":10}}`),
		BitDiskFailing, "")
	if r.Device.State != protocol.DiskWarning || *r.Device.CriticalWarning != protocol.NVMeWarnTemperature {
		t.Fatalf("%+v", r.Device)
	}
}

// TestWalkValuesKeepsNumbersOnly: strings, booleans, arrays and objects
// nested twice are left out, large numbers are clamped and the list is
// bounded.
func TestWalkValuesKeepsNumbersOnly(t *testing.T) {
	var vs []protocol.SMARTValue
	walkValues([]byte(`{"a":1,"s":"12.5","b":true,"n":null,"arr":[1,2],"o":{"x":2,"deep":{"y":3}},"big":1e40,"f":2.5}`), "", &vs)
	want := []protocol.SMARTValue{{Key: "a", Value: 1}, {Key: "o.x", Value: 2}, {Key: "big", Value: math.MaxInt64}, {Key: "f", Value: 2}}
	if !reflect.DeepEqual(vs, want) {
		t.Fatalf("values %+v", vs)
	}
	vs = nil
	walkValues([]byte(`not json`), "", &vs)
	if vs != nil {
		t.Errorf("garbage gave %+v", vs)
	}
	var many strings.Builder
	many.WriteString("{")
	for i := range protocol.MaxSMARTValues + 5 {
		if i > 0 {
			many.WriteString(",")
		}
		fmt.Fprintf(&many, `"k%d":%d`, i, i)
	}
	many.WriteString("}")
	vs = nil
	walkValues([]byte(many.String()), "", &vs)
	if len(vs) != protocol.MaxSMARTValues {
		t.Errorf("%d values, want %d", len(vs), protocol.MaxSMARTValues)
	}
}
