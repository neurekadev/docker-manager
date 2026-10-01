package health

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/agent/smartctl"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

var testEpoch = testutil.Epoch

// fakeSMART answers scans and reads from maps; with gate set, reads
// announce themselves on entered and wait for the gate.
type fakeSMART struct {
	mu       sync.Mutex
	scan     []smartctl.ScanDevice
	scanErr  error
	readings map[string]smartctl.Reading
	readErr  map[string]error
	scans    int
	gate     chan struct{}
	entered  chan string
}

func (f *fakeSMART) Scan(context.Context) ([]smartctl.ScanDevice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scans++
	return append([]smartctl.ScanDevice(nil), f.scan...), f.scanErr
}

func (f *fakeSMART) scanCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scans
}

func (f *fakeSMART) Read(ctx context.Context, dev smartctl.ScanDevice) (smartctl.Reading, error) {
	f.mu.Lock()
	gate, entered := f.gate, f.entered
	// "name|type" answers one of several disks behind a controller path.
	r, ok := f.readings[dev.Name+"|"+dev.Type]
	if !ok {
		r = f.readings[dev.Name]
	}
	err := f.readErr[dev.Name+"|"+dev.Type]
	if err == nil {
		err = f.readErr[dev.Name]
	}
	f.mu.Unlock()
	if gate != nil {
		entered <- dev.Name
		select {
		case <-gate:
		case <-ctx.Done():
			return smartctl.Reading{}, ctx.Err()
		}
	}
	return r, err
}

func (f *fakeSMART) set(name string, r smartctl.Reading) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readings[name] = r
}

func healthy(name, serial string) smartctl.Reading {
	passed, temp := true, 35
	return smartctl.Reading{Device: protocol.SMARTDevice{Name: name, Type: "sat", Protocol: protocol.DiskATA, Model: "WDC WD40EFZX",
		Serial: serial, SMARTSupported: true, Passed: &passed, TemperatureC: &temp, State: protocol.DiskOK}}
}

func standby(name string) smartctl.Reading {
	return smartctl.Reading{Standby: true, Device: protocol.SMARTDevice{Name: name, Type: "sat", State: protocol.DiskSleeping}}
}

// hostProc is a procfs with two disks, one md array and one ZFS pool.
func hostProc() fstest.MapFS {
	return fstest.MapFS{
		"partitions": {Data: []byte(`major minor  #blocks  name

   8        0  976762584 sda
   8        1  976761560 sda1
   8       16  976762584 sdb
 259        0  500107608 nvme0n1
 259        1     524288 nvme0n1p1
   7        0      65536 loop0
  11        0    1048575 sr0
 253        0   20971520 dm-0
`)},
		"mdstat": {Data: []byte(`Personalities : [raid1]
md0 : active raid1 sdb1[1] sda1[0]
      976630464 blocks super 1.2 [2/2] [UU]

unused devices: <none>
`)},
		"spl/kstat/zfs/tank/state":   {Data: []byte("DEGRADED\n")},
		"spl/kstat/zfs/arcstats":     {Data: []byte("x")},
		"spl/kstat/zfs/backup/state": {Data: []byte("ONLINE\n")},
	}
}

func newMonitor(t *testing.T, smart *fakeSMART, proc fstest.MapFS, visible bool) (*Monitor, *fakeSMART) {
	t.Helper()
	opts := Options{Clock: testutil.FakeClock(), Logger: testutil.Logger(t), Proc: proc, DevExists: func(string) bool { return visible }}
	if smart != nil {
		if smart.readings == nil {
			smart.readings = map[string]smartctl.Reading{}
		}
		opts.SMART = smart
	}
	return New(opts), smart
}

func TestMonitorKeepsSleepingDisksValues(t *testing.T) {
	clk := testutil.FakeClock()
	smart := &fakeSMART{scan: []smartctl.ScanDevice{{Name: "/dev/sda", Type: "sat"}, {Name: "/dev/sdb", Type: "sat"},
		{Name: "/dev/sdc", Type: "scsi", OpenError: "/dev/sdc: Unknown USB bridge [0x152d:0x0578]"}},
		readings: map[string]smartctl.Reading{"/dev/sda": healthy("/dev/sda", "S1"), "/dev/sdb": healthy("/dev/sdb", "S2")}}
	m := New(Options{Clock: clk, Logger: testutil.Logger(t), Proc: hostProc(), SMART: smart, DevExists: func(string) bool { return true }})
	ctx := testutil.Context(t)

	m.round(ctx, true)
	first := m.Report()
	if first.SMART.Status != protocol.SMARTOK || first.SMART.Checking || len(first.SMART.Devices) != 3 {
		t.Fatalf("first report %+v", first.SMART)
	}
	if d := first.SMART.Devices[2]; d.State != protocol.DiskError || d.ErrorCode != protocol.DiskErrUnsupported {
		t.Errorf("a device the scan could not open: %+v", d)
	}
	readAt := *first.SMART.Devices[1].ReadAt

	// sdb spins down; sda is read again. The sleeping disk keeps its
	// values and read time, and the scan is not repeated.
	clk.Advance(30 * time.Minute)
	smart.set("/dev/sdb", standby("/dev/sdb"))
	m.round(ctx, false)
	r := m.Report()
	sdb := r.SMART.Devices[1]
	if sdb.State != protocol.DiskSleeping || sdb.Serial != "S2" || sdb.TemperatureC == nil || !sdb.ReadAt.Equal(readAt) {
		t.Fatalf("sleeping disk %+v", sdb)
	}
	if sda := r.SMART.Devices[0]; !sda.ReadAt.After(readAt) || sda.State != protocol.DiskOK {
		t.Errorf("awake disk %+v", sda)
	}
	if smart.scans != 1 {
		t.Errorf("scanned %d times, want once (rescans every 6 h or on request)", smart.scans)
	}
	if !r.SMART.CheckedAt.After(*first.SMART.CheckedAt) || !r.SMART.ScannedAt.Equal(*first.SMART.ScannedAt) {
		t.Errorf("checkedAt %v scannedAt %v", r.SMART.CheckedAt, r.SMART.ScannedAt)
	}

	// A disk asleep since the start has no values yet.
	clk.Advance(DefaultScanInterval)
	smart.scan = append(smart.scan, smartctl.ScanDevice{Name: "/dev/sdd", Type: "sat"})
	smart.set("/dev/sdd", standby("/dev/sdd"))
	m.round(ctx, false)
	r = m.Report()
	if smart.scans != 2 || len(r.SMART.Devices) != 4 {
		t.Fatalf("after 6 h: %d scans, %d devices", smart.scans, len(r.SMART.Devices))
	}
	if d := r.SMART.Devices[3]; d.State != protocol.DiskSleeping || d.ReadAt != nil {
		t.Errorf("never read sleeping disk %+v", d)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMonitorKeepsLastValuesWhenAReadFails(t *testing.T) {
	smart := &fakeSMART{scan: []smartctl.ScanDevice{{Name: "/dev/sda", Type: "sat"}},
		readings: map[string]smartctl.Reading{"/dev/sda": healthy("/dev/sda", "S1")}}
	m, _ := newMonitor(t, smart, hostProc(), true)
	ctx := testutil.Context(t)
	m.round(ctx, true)
	smart.mu.Lock()
	smart.readErr = map[string]error{"/dev/sda": &smartctl.Error{Op: "read", Code: smartctl.CodeTimeout}}
	smart.mu.Unlock()
	before := *m.Report().SMART.Devices[0].ReadAt
	m.round(ctx, false)
	// The failure shows (never "healthy"); the last measurements and
	// their read time stay for reference.
	if d := m.Report().SMART.Devices[0]; d.State != protocol.DiskError || d.ErrorCode != protocol.DiskErrOpenFailed ||
		d.Serial != "S1" || d.TemperatureC == nil || !d.ReadAt.Equal(before) {
		t.Fatalf("%+v", d)
	}
	if err := m.Report().Validate(); err != nil {
		t.Fatal(err)
	}
	// The next good read clears it.
	smart.mu.Lock()
	smart.readErr = nil
	smart.mu.Unlock()
	m.round(ctx, false)
	if d := m.Report().SMART.Devices[0]; d.State != protocol.DiskOK || d.ErrorCode != "" {
		t.Fatalf("%+v", d)
	}
	// A device never read successfully is an error.
	m2, _ := newMonitor(t, &fakeSMART{scan: smart.scan, readErr: map[string]error{"/dev/sda": &smartctl.Error{Op: "read", Code: smartctl.CodeTimeout}}},
		hostProc(), true)
	m2.round(ctx, true)
	if d := m2.Report().SMART.Devices[0]; d.State != protocol.DiskError || d.ErrorCode != protocol.DiskErrOpenFailed {
		t.Fatalf("%+v", d)
	}
}

// TestMonitorErrorReadingIsAFailedRead: a reading where nothing was read
// (permission denied) has no read time; after a good read it keeps the
// last measurements like a failed read.
func TestMonitorErrorReadingIsAFailedRead(t *testing.T) {
	denied := smartctl.Reading{Device: protocol.SMARTDevice{Name: "/dev/sda", Type: "sat", State: protocol.DiskError,
		ErrorCode: protocol.DiskErrPermissionDenied}}
	smart := &fakeSMART{scan: []smartctl.ScanDevice{{Name: "/dev/sda", Type: "sat"}},
		readings: map[string]smartctl.Reading{"/dev/sda": denied}}
	m, _ := newMonitor(t, smart, hostProc(), true)
	ctx := testutil.Context(t)
	m.round(ctx, true)
	if d := m.Report().SMART.Devices[0]; d.State != protocol.DiskError || d.ErrorCode != protocol.DiskErrPermissionDenied || d.ReadAt != nil {
		t.Fatalf("never read: %+v", d)
	}
	// Asleep before any good read: still nothing to show.
	smart.set("/dev/sda", standby("/dev/sda"))
	m.round(ctx, false)
	if d := m.Report().SMART.Devices[0]; d.State != protocol.DiskSleeping || d.ReadAt != nil {
		t.Fatalf("asleep, never read: %+v", d)
	}
	smart.set("/dev/sda", healthy("/dev/sda", "S1"))
	m.round(ctx, false)
	before := *m.Report().SMART.Devices[0].ReadAt
	smart.set("/dev/sda", denied)
	m.round(ctx, false)
	if d := m.Report().SMART.Devices[0]; d.State != protocol.DiskError || d.ErrorCode != protocol.DiskErrPermissionDenied ||
		d.Serial != "S1" || d.TemperatureC == nil || !d.ReadAt.Equal(before) {
		t.Fatalf("after a good read: %+v", d)
	}
	if err := m.Report().Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestMonitorSleepingDiskKeepsItsProblem: standby clears no problem; a
// healthy disk is marked sleeping.
func TestMonitorSleepingDiskKeepsItsProblem(t *testing.T) {
	for _, state := range []string{protocol.DiskFailing, protocol.DiskWarning, protocol.DiskOK} {
		t.Run(state, func(t *testing.T) {
			r := healthy("/dev/sda", "S1")
			r.Device.State = state
			smart := &fakeSMART{scan: []smartctl.ScanDevice{{Name: "/dev/sda", Type: "sat"}},
				readings: map[string]smartctl.Reading{"/dev/sda": r}}
			m, _ := newMonitor(t, smart, hostProc(), true)
			ctx := testutil.Context(t)
			m.round(ctx, true)
			smart.set("/dev/sda", standby("/dev/sda"))
			m.round(ctx, false)
			want := state
			if state == protocol.DiskOK {
				want = protocol.DiskSleeping
			}
			if d := m.Report().SMART.Devices[0]; d.State != want || d.Serial != "S1" || d.ReadAt == nil {
				t.Fatalf("got %+v, want state %s", d, want)
			}
		})
	}
}

// TestMonitorKeepsDisksBehindOneControllerApart: disks behind a RAID
// controller share its path and differ by type; each keeps its own values.
func TestMonitorKeepsDisksBehindOneControllerApart(t *testing.T) {
	disk0, disk1 := healthy("/dev/bus/0", "S0"), healthy("/dev/bus/0", "S1")
	disk0.Device.Type, disk1.Device.Type = "megaraid,0", "megaraid,1"
	smart := &fakeSMART{scan: []smartctl.ScanDevice{{Name: "/dev/bus/0", Type: "megaraid,0"}, {Name: "/dev/bus/0", Type: "megaraid,1"}},
		readings: map[string]smartctl.Reading{"/dev/bus/0|megaraid,0": disk0, "/dev/bus/0|megaraid,1": disk1}}
	m, _ := newMonitor(t, smart, hostProc(), true)
	ctx := testutil.Context(t)
	m.round(ctx, true)
	smart.mu.Lock()
	smart.readings["/dev/bus/0|megaraid,1"] = standby("/dev/bus/0")
	smart.mu.Unlock()
	m.round(ctx, false)
	devs := m.Report().SMART.Devices
	if len(devs) != 2 || devs[0].Serial != "S0" || devs[0].State != protocol.DiskOK ||
		devs[1].Serial != "S1" || devs[1].State != protocol.DiskSleeping || devs[1].Type != "megaraid,1" {
		t.Fatalf("%+v", devs)
	}
}

func TestMonitorDetectsNoAccess(t *testing.T) {
	ctx := testutil.Context(t)
	denied := smartctl.Reading{Device: protocol.SMARTDevice{Name: "/dev/sda", Type: "sat", State: protocol.DiskError,
		ErrorCode: protocol.DiskErrPermissionDenied}}
	for _, c := range []struct {
		name    string
		proc    fstest.MapFS
		visible bool
		scan    []smartctl.ScanDevice
		reads   map[string]smartctl.Reading
		want    string
		devices int
	}{
		{"disks but no device nodes (not privileged)", hostProc(), false, nil, nil, protocol.SMARTNoAccess, 0},
		{"every device refuses to open", hostProc(), true, []smartctl.ScanDevice{{Name: "/dev/sda", Type: "sat"}},
			map[string]smartctl.Reading{"/dev/sda": denied}, protocol.SMARTNoAccess, 1},
		{"visible disks without SMART (virtual disks)", hostProc(), true, nil, nil, protocol.SMARTOK, 0},
		{"no real disks", fstest.MapFS{"partitions": {Data: []byte("major minor #blocks name\n 7 0 100 loop0\n 253 0 100 dm-0\n")}}, false,
			nil, nil, protocol.SMARTOK, 0},
		{"one readable disk", hostProc(), true, []smartctl.ScanDevice{{Name: "/dev/sda", Type: "sat"}, {Name: "/dev/sdb", Type: "sat"}},
			map[string]smartctl.Reading{"/dev/sda": denied, "/dev/sdb": healthy("/dev/sdb", "S2")}, protocol.SMARTOK, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, _ := newMonitor(t, &fakeSMART{scan: c.scan, readings: c.reads}, c.proc, c.visible)
			m.round(ctx, true)
			r := m.Report()
			if r.SMART.Status != c.want || len(r.SMART.Devices) != c.devices {
				t.Fatalf("status %q with %d devices, want %q with %d", r.SMART.Status, len(r.SMART.Devices), c.want, c.devices)
			}
		})
	}
}

func TestMonitorScanFailures(t *testing.T) {
	ctx := testutil.Context(t)
	m, _ := newMonitor(t, &fakeSMART{scanErr: &smartctl.Error{Op: "scan", Code: smartctl.CodeNotInstalled}}, hostProc(), true)
	m.round(ctx, true)
	if r := m.Report(); r.SMART.Status != protocol.SMARTNotInstalled || r.SMART.Message == "" || r.SMART.Checking {
		t.Fatalf("%+v", r.SMART)
	}
	m, _ = newMonitor(t, &fakeSMART{scanErr: &smartctl.Error{Op: "scan", Code: smartctl.CodeFailed, Message: "boom"}}, hostProc(), true)
	m.round(ctx, true)
	if r := m.Report(); r.SMART.Status != protocol.SMARTError || r.SMART.Message != "the disk scan failed" {
		t.Fatalf("%+v", r.SMART)
	}
}

func TestMonitorWithoutSMARTStillReportsRAID(t *testing.T) {
	m, _ := newMonitor(t, nil, hostProc(), true)
	m.Run(testutil.Context(t)) // returns at once
	out, err := m.HostHealth(testutil.Context(t), json.RawMessage(`{"refresh":"smart"}`))
	if err != nil {
		t.Fatal(err)
	}
	r := out.(protocol.HostHealthOutput)
	if r.SMART.Status != protocol.SMARTDisabled || r.SMART.Checking || len(r.RAID.MD) != 1 || len(r.RAID.ZFS) != 2 {
		t.Fatalf("%+v", r)
	}
	want := []protocol.ZFSPool{{Name: "backup", Health: "ONLINE", State: protocol.RAIDHealthy},
		{Name: "tank", Health: "DEGRADED", State: protocol.RAIDDegraded}}
	if !reflect.DeepEqual(r.RAID.ZFS, want) || r.RAID.MD[0].State != protocol.RAIDHealthy {
		t.Errorf("raid %+v", r.RAID)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestHostHealthRejectsUnknownRefresh(t *testing.T) {
	m, _ := newMonitor(t, nil, hostProc(), true)
	_, err := m.HostHealth(testutil.Context(t), json.RawMessage(`{"refresh":"selftest"}`))
	var he *session.HandlerError
	if !errors.As(err, &he) || he.Code != protocol.CodeInvalidArgument {
		t.Fatalf("err = %v", err)
	}
}

func TestHostHealthRefreshWaitsThenReportsChecking(t *testing.T) {
	clk := testutil.FakeClock()
	smart := &fakeSMART{scan: []smartctl.ScanDevice{{Name: "/dev/sda", Type: "sat"}},
		readings: map[string]smartctl.Reading{"/dev/sda": healthy("/dev/sda", "S1")}}
	m := New(Options{Clock: clk, Logger: testutil.Logger(t), Proc: hostProc(), SMART: smart, DevExists: func(string) bool { return true }})
	if r := m.Report(); !r.SMART.Checking {
		t.Fatal("the first read is reported as running")
	}
	ctx, cancel := context.WithCancel(testutil.Context(t))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); m.Run(ctx) }()
	defer func() { cancel(); wg.Wait() }()
	// The first round ends; Run waits for the interval timer.
	if err := clk.BlockUntilWaiters(ctx, 1); err != nil {
		t.Fatal(err)
	}
	m.wait(ctx, 1, nil)
	if r := m.Report(); r.SMART.Checking || len(r.SMART.Devices) != 1 {
		t.Fatalf("after the first round %+v", r.SMART)
	}

	// A quick read answers the request with its result.
	out, err := m.HostHealth(ctx, json.RawMessage(`{"refresh":"smart"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r := out.(protocol.HostHealthOutput); r.SMART.Checking || smart.scanCount() != 2 {
		t.Fatalf("quick check: checking %v, %d scans", r.SMART.Checking, smart.scanCount())
	}

	// A slow read: the request waits DefaultWait, then answers with
	// checking set; the result follows when the read ends.
	gate := make(chan struct{})
	smart.mu.Lock()
	smart.gate, smart.entered = gate, make(chan string, 4)
	smart.mu.Unlock()
	res := make(chan protocol.HostHealthOutput, 1)
	go func() {
		out, _ := m.HostHealth(ctx, json.RawMessage(`{"refresh":"smart"}`))
		res <- out.(protocol.HostHealthOutput)
	}()
	// Run is inside the read (its interval timer stopped): the only
	// timer left is the request's wait.
	<-smart.entered
	if err := clk.BlockUntilWaiters(ctx, 1); err != nil {
		t.Fatal(err)
	}
	clk.Advance(DefaultWait)
	r := <-res
	if !r.SMART.Checking {
		t.Fatalf("slow check: %+v", r.SMART)
	}
	close(gate)
	m.wait(ctx, 3, nil)
	if r := m.Report(); r.SMART.Checking || smart.scanCount() != 3 {
		t.Fatalf("after the slow check: checking %v, %d scans", r.SMART.Checking, smart.scanCount())
	}
}

func TestWholeDisks(t *testing.T) {
	disks, err := WholeDisks(hostProc())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"sda", "sdb", "nvme0n1"}; !reflect.DeepEqual(disks, want) {
		t.Fatalf("disks %q", disks)
	}
	if _, err := WholeDisks(fstest.MapFS{}); err == nil {
		t.Error("a missing /proc/partitions is an error")
	}
}

func TestReadRAIDWithoutMDOrZFS(t *testing.T) {
	r := ReadRAID(fstest.MapFS{}, testEpoch)
	if r.Message != "" || r.MD == nil || r.ZFS == nil || len(r.MD)+len(r.ZFS) != 0 || !r.ReadAt.Equal(testEpoch) {
		t.Fatalf("%+v", r)
	}
	// A pool with an unknown state line is left out and reported.
	r = ReadRAID(fstest.MapFS{"spl/kstat/zfs/odd/state": {Data: []byte("WEIRD\n")}}, testEpoch)
	if len(r.ZFS) != 0 || r.Message == "" {
		t.Fatalf("%+v", r)
	}
}

// TestReadRAIDReportsUnreadablePools: a pool whose state cannot be read
// is reported (the RAID card says the state could not be read) while the
// readable pools stay; a directory without a state file is not a pool.
func TestReadRAIDReportsUnreadablePools(t *testing.T) {
	proc := fstest.MapFS{
		"spl/kstat/zfs/tank/state":     {Data: []byte("ONLINE\n")},
		"spl/kstat/zfs/broken/state/x": {Data: []byte("a directory, not a state file")},
		"spl/kstat/zfs/gone/io":        {Data: []byte("no state file")},
	}
	r := ReadRAID(proc, testEpoch)
	if r.Message != "the state of some ZFS pools could not be read" {
		t.Fatalf("message %q", r.Message)
	}
	if want := []protocol.ZFSPool{{Name: "tank", Health: "ONLINE", State: protocol.RAIDHealthy}}; !reflect.DeepEqual(r.ZFS, want) {
		t.Fatalf("pools %+v", r.ZFS)
	}
	if err := (protocol.HostHealthOutput{SampledAt: testEpoch, SMART: protocol.SMARTReport{Status: protocol.SMARTOK}, RAID: r}).Validate(); err != nil {
		t.Fatal(err)
	}
	// Without the unreadable pool nothing is reported.
	delete(proc, "spl/kstat/zfs/broken/state/x")
	if r := ReadRAID(proc, testEpoch); r.Message != "" || len(r.ZFS) != 1 {
		t.Fatalf("%+v", r)
	}
}
