// Package health is the agent's disk health monitor (#143): SMART data of
// every disk read with the pinned smartctl (internal/agent/smartctl),
// cached and refreshed every DOCKER_AGENT_SMART_INTERVAL (a disk in
// standby is not woken: it keeps its previous values, marked sleeping
// unless they showed a problem, until it was not read for
// DOCKER_AGENT_SMART_WAKE_AFTER), a disk that a later scan no longer finds
// listed as missing until the agent restarts, and the Linux software RAID
// and ZFS pool state read from procfs on every request (cheap). It serves
// the host.health request.
// docs/internal/architecture/metrics.md, "Host health".
package health

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/agent/smartctl"
	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// SMART reads SMART data (smartctl.Runner implements it). Read with
// wake reads a device in standby too.
type SMART interface {
	Scan(ctx context.Context) ([]smartctl.ScanDevice, error)
	Read(ctx context.Context, dev smartctl.ScanDevice, wake bool) (smartctl.Reading, error)
}

// Defaults.
const (
	// DefaultInterval is how often every device is read
	// (DOCKER_AGENT_SMART_INTERVAL).
	DefaultInterval = 30 * time.Minute
	// DefaultScanInterval is how often the device list is scanned again
	// (besides the start and "Check disks now").
	DefaultScanInterval = 6 * time.Hour
	// DefaultWait is how long a host.health request that asked for a fresh
	// SMART read waits for it before answering with checking set.
	DefaultWait = 3 * time.Second
	// DefaultConcurrency bounds the devices read at once.
	DefaultConcurrency = 4
	// DefaultWakeAfter is how long a disk may stay unread because it is
	// in standby before a read wakes it (DOCKER_AGENT_SMART_WAKE_AFTER).
	DefaultWakeAfter = 24 * time.Hour
)

// Options configures a Monitor.
type Options struct {
	Clock  clock.Clock
	Logger *slog.Logger
	// Proc is procfs (the sampler's, DOCKER_AGENT_HOST_PROC).
	Proc fs.FS
	// SMART reads the disks; nil when SMART is turned off
	// (DOCKER_AGENT_SMART_ENABLED=false).
	SMART SMART
	// DevExists reports whether a block device node is visible in the
	// agent's /dev (default: stat /dev/<name>); a privileged agent sees
	// the host's devices.
	DevExists func(name string) bool
	// Interval, ScanInterval, Wait and Concurrency default to the
	// constants above.
	Interval     time.Duration
	ScanInterval time.Duration
	Wait         time.Duration
	Concurrency  int
	// WakeAfter defaults to DefaultWakeAfter; negative: a disk in standby
	// is never woken.
	WakeAfter time.Duration
}

// Monitor keeps the SMART state and answers host.health.
type Monitor struct {
	opts    Options
	log     *slog.Logger
	trigger chan struct{}

	mu        sync.Mutex
	status    string
	message   string
	checking  bool
	scannedAt time.Time
	checkedAt time.Time
	scan      []smartctl.ScanDevice
	devices   []protocol.SMARTDevice
	// missing are devices an earlier scan found and the last one did not
	// (kept until the agent restarts or a scan finds them again).
	missing []smartctl.ScanDevice
	// wakeClock is when a device's wake-up clock last started: when a scan
	// first found it, or the last read that woke it (a read that got no
	// data sets no read time, and must not wake the disk every round).
	wakeClock map[string]time.Time
	// requested counts check requests; completed is the last request a
	// finished round covered; done is closed (and replaced) when a round
	// ends.
	requested uint64
	completed uint64
	done      chan struct{}
	// logged remembers logged problems (each once until it clears).
	logged map[string]bool
}

// New returns a Monitor. Call Run to start reading SMART data.
func New(opts Options) *Monitor {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Proc == nil {
		opts.Proc = os.DirFS("/proc")
	}
	if opts.DevExists == nil {
		opts.DevExists = func(name string) bool {
			_, err := os.Stat(filepath.Join("/dev", name))
			return err == nil
		}
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.ScanInterval <= 0 {
		opts.ScanInterval = DefaultScanInterval
	}
	if opts.Wait <= 0 {
		opts.Wait = DefaultWait
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = DefaultConcurrency
	}
	if opts.WakeAfter == 0 {
		opts.WakeAfter = DefaultWakeAfter
	}
	m := &Monitor{opts: opts, log: opts.Logger.With("component", "health"), trigger: make(chan struct{}, 1),
		status: protocol.SMARTOK, done: make(chan struct{}), logged: map[string]bool{}, wakeClock: map[string]time.Time{}}
	if opts.SMART == nil {
		m.status = protocol.SMARTDisabled
	} else {
		// The first round starts with Run: report it as running.
		m.checking = true
		m.requested = 1
	}
	return m
}

// Run reads SMART data at start, every Interval and on request until ctx
// ends. Without SMART it returns at once (RAID needs no loop).
func (m *Monitor) Run(ctx context.Context) {
	if m.opts.SMART == nil {
		return
	}
	clk := m.opts.Clock
	m.round(ctx, true)
	for {
		t := clk.NewTimer(m.opts.Interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
			m.round(ctx, false)
		case <-m.trigger:
			t.Stop()
			m.round(ctx, true)
		}
	}
}

// Check asks for a fresh SMART read of every device now (a rescan and a
// read of each; never a self-test). It returns the request's number for
// wait (0 without SMART).
func (m *Monitor) Check() uint64 {
	if m.opts.SMART == nil {
		return 0
	}
	m.mu.Lock()
	m.requested++
	want := m.requested
	m.checking = true
	m.mu.Unlock()
	select {
	case m.trigger <- struct{}{}:
	default: // a round is already queued: it covers this request
	}
	return want
}

// wait blocks until a round that started after request want has ended,
// timeout fires or ctx ends.
func (m *Monitor) wait(ctx context.Context, want uint64, timeout <-chan time.Time) {
	for {
		m.mu.Lock()
		if m.completed >= want {
			m.mu.Unlock()
			return
		}
		done := m.done
		m.mu.Unlock()
		select {
		case <-done:
		case <-timeout:
			return
		case <-ctx.Done():
			return
		}
	}
}

// round scans (when asked, at the first round or after ScanInterval) and
// reads every device.
func (m *Monitor) round(ctx context.Context, rescan bool) {
	m.mu.Lock()
	covers := m.requested
	scan := m.scan
	missing := m.missing
	scanned := m.scannedAt
	prev := make(map[string]protocol.SMARTDevice, len(m.devices))
	for _, d := range m.devices {
		prev[deviceKey(d.Name, d.Type)] = d
	}
	m.mu.Unlock()
	now := m.opts.Clock.Now()
	status, message := protocol.SMARTOK, ""
	if rescan || scanned.IsZero() || now.Sub(scanned) >= m.opts.ScanInterval {
		devs, err := m.opts.SMART.Scan(ctx)
		switch {
		case ctx.Err() != nil:
			m.finish(covers, nil)
			return
		case err != nil:
			status, message = protocol.SMARTError, "the disk scan failed"
			switch {
			case smartctl.IsCode(err, smartctl.CodeNotInstalled):
				status, message = protocol.SMARTNotInstalled, "smartctl is not installed in the agent image"
			case smartctl.IsCode(err, smartctl.CodeTimeout), smartctl.IsCode(err, smartctl.CodeStuck):
				message = "the disk scan did not finish"
			}
			m.logOnce("scan", "SMART disk scan failed", "error", err)
		default:
			m.clearLogged("scan")
			if len(devs) > protocol.MaxHealthDevices {
				devs = devs[:protocol.MaxHealthDevices]
			}
			missing = stillMissing(scan, missing, devs, prev)
			// Values carry over only for a device the previous scan found
			// too: a path that appeared (again) may be another disk.
			had := make(map[string]bool, len(scan))
			for _, d := range scan {
				had[deviceKey(d.Name, d.Type)] = true
			}
			for _, d := range devs {
				if k := deviceKey(d.Name, d.Type); !had[k] {
					delete(prev, k)
				}
			}
			scan, scanned = devs, m.opts.Clock.Now()
		}
	}
	if status == protocol.SMARTNotInstalled {
		m.finish(covers, func() {
			m.status, m.message, m.scan, m.devices, m.missing = status, message, nil, nil, nil
		})
		return
	}
	wake := m.wakeDue(scan, prev, now)
	devices := m.readAll(ctx, scan, prev, wake)
	if ctx.Err() != nil {
		m.finish(covers, nil)
		return
	}
	missing = withoutFound(missing, prev, devices)
	if room := protocol.MaxHealthDevices - len(devices); len(missing) > room {
		missing = missing[:max(room, 0)]
	}
	for _, dev := range missing {
		devices = append(devices, missingDevice(dev, prev[deviceKey(dev.Name, dev.Type)]))
	}
	if status == protocol.SMARTOK && m.noAccess(devices) {
		status = protocol.SMARTNoAccess
	}
	checked := m.opts.Clock.Now().UTC()
	m.finish(covers, func() {
		m.status, m.message = status, message
		m.scan, m.scannedAt, m.devices, m.missing = scan, scanned, devices, missing
		m.checkedAt = checked
	})
}

// stillMissing returns the devices missing after a scan found devs: those
// missing before that it did not find again, then those the previous scan
// (old) found that it no longer does, except a device without SMART data
// (prev: unsupported, such as a USB stick): nothing was watched there.
func stillMissing(old, missing, devs []smartctl.ScanDevice, prev map[string]protocol.SMARTDevice) []smartctl.ScanDevice {
	found := make(map[string]bool, len(devs))
	for _, d := range devs {
		found[deviceKey(d.Name, d.Type)] = true
	}
	var out []smartctl.ScanDevice
	listed := map[string]bool{}
	for _, list := range [][]smartctl.ScanDevice{missing, old} {
		for _, d := range list {
			k := deviceKey(d.Name, d.Type)
			if p := prev[k]; p.State == protocol.DiskError && p.ErrorCode == protocol.DiskErrUnsupported {
				continue
			}
			if !found[k] && !listed[k] {
				listed[k] = true
				out = append(out, d)
			}
		}
	}
	return out
}

// withoutFound drops a missing device whose last values (prev) name the
// serial number of a device read now (the same disk, found under another
// name after it dropped off the bus and came back).
func withoutFound(missing []smartctl.ScanDevice, prev map[string]protocol.SMARTDevice, devices []protocol.SMARTDevice) []smartctl.ScanDevice {
	serials := map[string]bool{}
	for _, d := range devices {
		if d.Serial != "" {
			serials[d.Serial] = true
		}
	}
	var out []smartctl.ScanDevice
	for _, d := range missing {
		if s := prev[deviceKey(d.Name, d.Type)].Serial; s != "" && serials[s] {
			continue
		}
		out = append(out, d)
	}
	return out
}

// missingDevice is a device no scan finds any more: its last values (and
// their read time) stay for reference, its state says it is missing.
func missingDevice(dev smartctl.ScanDevice, prev protocol.SMARTDevice) protocol.SMARTDevice {
	if prev.Name == "" {
		prev = protocol.SMARTDevice{Name: dev.Name, Type: dev.Type, Protocol: dev.Protocol}
	}
	prev.State, prev.ErrorCode = protocol.DiskError, protocol.DiskErrMissing
	return prev
}

// wakeDue returns the devices to read even in standby: those not read
// for WakeAfter since their last read, the last read that woke them or
// the scan that first found them, whichever is latest; at most one wake
// per WakeAfter, also when the waking read gets no data. It keeps the
// wake clocks of scan.
func (m *Monitor) wakeDue(scan []smartctl.ScanDevice, prev map[string]protocol.SMARTDevice, now time.Time) map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := make(map[string]time.Time, len(scan))
	wake := map[string]bool{}
	for _, d := range scan {
		k := deviceKey(d.Name, d.Type)
		since, ok := m.wakeClock[k]
		if _, carried := prev[k]; !ok || !carried {
			// New, or its values were dropped (another disk may hold the
			// path): its clock starts now.
			since = now
		}
		if p := prev[k]; p.ReadAt != nil && p.ReadAt.After(since) {
			since = *p.ReadAt
		}
		if m.opts.WakeAfter >= 0 && now.Sub(since) >= m.opts.WakeAfter {
			wake[k] = true
			since = now
		}
		seen[k] = since
	}
	m.wakeClock = seen
	return wake
}

// finish applies a round's result (nil: none, the round was cancelled)
// and wakes the requests it covers.
func (m *Monitor) finish(covers uint64, apply func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if apply != nil {
		apply()
	}
	if covers > m.completed {
		m.completed = covers
	}
	if m.completed >= m.requested {
		m.checking = false
	}
	close(m.done)
	m.done = make(chan struct{})
}

// deviceKey identifies a device: its path and smartctl type (disks
// behind one RAID controller share the controller's path and differ by
// type, e.g. megaraid,0 and megaraid,1).
func deviceKey(name, typ string) string { return name + "\x00" + typ }

// readAll reads every scanned device (Concurrency at a time) in scan
// order; those in wake even in standby. A device in standby keeps its
// previous values, marked sleeping (failing and warning stay: standby
// clears no problem).
func (m *Monitor) readAll(ctx context.Context, scan []smartctl.ScanDevice, prev map[string]protocol.SMARTDevice,
	wake map[string]bool) []protocol.SMARTDevice {
	out := make([]protocol.SMARTDevice, len(scan))
	sem := make(chan struct{}, m.opts.Concurrency)
	var wg sync.WaitGroup
	for i, dev := range scan {
		if dev.OpenError != "" {
			code := smartctl.ScanOpenError(dev)
			if p := prev[deviceKey(dev.Name, dev.Type)]; watched(p) {
				// A disk read before that the scan can't open: a failed
				// read (never "no SMART data", which ends its alerts).
				if code == protocol.DiskErrUnsupported {
					code = protocol.DiskErrOpenFailed
				}
				out[i] = readFailed(dev, p, code)
				continue
			}
			out[i] = protocol.SMARTDevice{Name: dev.Name, Type: dev.Type, Protocol: dev.Protocol, State: protocol.DiskError,
				ErrorCode: code}
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			k := deviceKey(dev.Name, dev.Type)
			out[i] = m.readOne(ctx, dev, prev[k], wake[k])
		}()
	}
	wg.Wait()
	return out
}

func (m *Monitor) readOne(ctx context.Context, dev smartctl.ScanDevice, prev protocol.SMARTDevice, wake bool) protocol.SMARTDevice {
	logKey := "read " + deviceKey(dev.Name, dev.Type)
	r, err := m.opts.SMART.Read(ctx, dev, wake)
	if err != nil {
		if ctx.Err() == nil {
			m.logOnce(logKey, "SMART read failed", "device", dev.Name, "type", dev.Type, "error", err)
		}
		code := protocol.DiskErrOpenFailed
		if smartctl.IsCode(err, smartctl.CodeTimeout) || smartctl.IsCode(err, smartctl.CodeStuck) {
			code = protocol.DiskErrTimeout
		}
		return readFailed(dev, prev, code)
	}
	m.clearLogged(logKey)
	if r.Device.Serial != "" && prev.Serial != "" && r.Device.Serial != prev.Serial {
		// Another disk now holds the path: nothing of the previous one
		// carries over.
		prev = protocol.SMARTDevice{}
	}
	if r.Device.State == protocol.DiskError && r.Device.ErrorCode == protocol.DiskErrUnsupported && watched(prev) {
		// A disk read before that now answers without SMART data (a
		// dying disk, a confused bridge): a failed read with its last
		// values, never a disk without SMART (that ends its alerts and
		// would not be missed when it disappears).
		return readFailed(dev, prev, protocol.DiskErrNoData)
	}
	if r.Standby {
		if prev.Name != "" && prev.ReadAt != nil {
			// A problem the kept measurements show stays until a read
			// clears it: a failing disk that spins down never looks merely
			// asleep. Derived from the values, since prev.State may be the
			// error of a failed read since. The temperature is not kept: a
			// disk in standby cools, and an old reading over its limit must
			// not keep it "too hot" (#212).
			prev.TemperatureC = nil
			prev.State, prev.ErrorCode = protocol.DeriveDiskState(prev), ""
			if prev.State == protocol.DiskOK {
				prev.State = protocol.DiskSleeping
			}
			return prev
		}
		return r.Device
	}
	d := r.Device
	if d.State == protocol.DiskError && d.ErrorCode != protocol.DiskErrUnsupported {
		// Nothing about its health was read (permission denied, open
		// failed, SMART turned off, no data): like a failed read, and
		// never with a read time. Never read before: what identifies the
		// disk stands in.
		if prev.Name == "" || prev.ReadAt == nil {
			return protocol.SMARTDevice{Name: d.Name, Type: d.Type, Protocol: d.Protocol, Model: d.Model, Serial: d.Serial,
				Firmware: d.Firmware, CapacityBytes: d.CapacityBytes, RotationRPM: d.RotationRPM, SMARTSupported: d.SMARTSupported,
				State: d.State, ErrorCode: d.ErrorCode}
		}
		return readFailed(dev, prev, d.ErrorCode)
	}
	at := m.opts.Clock.Now().UTC()
	d.ReadAt = &at
	return d
}

// watched reports a device with SMART data read before (a read time):
// it never turns into a device without SMART data.
func watched(prev protocol.SMARTDevice) bool {
	return prev.Name != "" && prev.ReadAt != nil && prev.SMARTSupported
}

// readFailed is a device whose read failed with code: the last
// measurements (and their read time) stay for reference, but the state
// reports the failure (a disk the agent cannot read never looks healthy).
// The next round tries again.
func readFailed(dev smartctl.ScanDevice, prev protocol.SMARTDevice, code string) protocol.SMARTDevice {
	if prev.Name != "" && prev.ReadAt != nil {
		prev.State, prev.ErrorCode = protocol.DiskError, code
		return prev
	}
	return protocol.SMARTDevice{Name: dev.Name, Type: dev.Type, Protocol: dev.Protocol, State: protocol.DiskError, ErrorCode: code}
}

// noAccess reports that the host has disks the agent cannot read: the
// kernel lists real disks, but none of their device nodes is visible in
// the agent container (not privileged), or every device refused to open
// with a permission error.
func (m *Monitor) noAccess(devices []protocol.SMARTDevice) bool {
	disks, err := WholeDisks(m.opts.Proc)
	if err != nil || len(disks) == 0 {
		return false
	}
	visible := false
	for _, d := range disks {
		if m.opts.DevExists(d) {
			visible = true
			break
		}
	}
	if !visible {
		return true
	}
	if len(devices) == 0 {
		return false // visible disks without SMART (virtual disks)
	}
	for _, d := range devices {
		if d.ErrorCode != protocol.DiskErrPermissionDenied {
			return false
		}
	}
	return true
}

// Report returns the current state: the cached SMART data and the RAID
// state read now.
func (m *Monitor) Report() protocol.HostHealthOutput {
	now := m.opts.Clock.Now().UTC()
	out := protocol.HostHealthOutput{SampledAt: now, RAID: ReadRAID(m.opts.Proc, now)}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := protocol.SMARTReport{Status: m.status, Message: m.message, Checking: m.checking, Devices: make([]protocol.SMARTDevice, len(m.devices))}
	if m.opts.SMART != nil {
		s.IntervalSeconds = int64(m.opts.Interval / time.Second)
	}
	copy(s.Devices, m.devices)
	fitDetails(s.Devices, protocol.MaxHealthDetailBytes)
	if !m.scannedAt.IsZero() {
		t := m.scannedAt.UTC()
		s.ScannedAt = &t
	}
	if !m.checkedAt.IsZero() {
		t := m.checkedAt
		s.CheckedAt = &t
	}
	out.SMART = s
	return out
}

// fitDetails leaves out the attribute tables and values of the last
// devices (shallow copies: the cached devices keep theirs) until those of
// all devices encode to at most budget bytes.
func fitDetails(devs []protocol.SMARTDevice, budget int) {
	sizes := make([]int, len(devs))
	total := 0
	for i, d := range devs {
		if len(d.Attributes) == 0 && len(d.Values) == 0 {
			continue
		}
		a, _ := json.Marshal(d.Attributes)
		v, _ := json.Marshal(d.Values)
		sizes[i] = len(a) + len(v)
		total += sizes[i]
	}
	for i := len(devs) - 1; i >= 0 && total > budget; i-- {
		if sizes[i] > 0 {
			devs[i].Attributes, devs[i].Values = nil, nil
			total -= sizes[i]
		}
	}
}

// HostHealth serves host.health: refresh "smart" starts a fresh read of
// every device and waits up to Wait for it (a longer read answers with
// checking set; the manager asks again); "raid" and "" answer at once
// (RAID is read on every request).
func (m *Monitor) HostHealth(ctx context.Context, raw json.RawMessage) (any, error) {
	var in protocol.HostHealthInput
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: "invalid host.health input"}
		}
	}
	if err := in.Validate(); err != nil {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: err.Error()}
	}
	if in.Refresh == protocol.HealthRefreshSMART {
		if want := m.Check(); want > 0 {
			t := m.opts.Clock.NewTimer(m.opts.Wait)
			m.wait(ctx, want, t.C())
			t.Stop()
		}
	}
	return m.Report(), nil
}

func (m *Monitor) logOnce(key, msg string, args ...any) {
	m.mu.Lock()
	seen := m.logged[key]
	m.logged[key] = true
	m.mu.Unlock()
	if !seen {
		m.log.Warn(msg, args...)
	}
}

func (m *Monitor) clearLogged(key string) {
	m.mu.Lock()
	delete(m.logged, key)
	m.mu.Unlock()
}
