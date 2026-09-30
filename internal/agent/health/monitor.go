// Package health is the agent's disk health monitor (#143): SMART data of
// every disk read with the pinned smartctl (internal/agent/smartctl),
// cached and refreshed every DOCKER_AGENT_SMART_INTERVAL (a disk in
// standby is never woken: it keeps its previous values, marked sleeping),
// and the Linux software RAID and ZFS pool state read from procfs on
// every request (cheap). It serves the host.health request.
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

// SMART reads SMART data (smartctl.Runner implements it).
type SMART interface {
	Scan(ctx context.Context) ([]smartctl.ScanDevice, error)
	Read(ctx context.Context, dev smartctl.ScanDevice) (smartctl.Reading, error)
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
	m := &Monitor{opts: opts, log: opts.Logger.With("component", "health"), trigger: make(chan struct{}, 1),
		status: protocol.SMARTOK, done: make(chan struct{}), logged: map[string]bool{}}
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
	scanned := m.scannedAt
	prev := make(map[string]protocol.SMARTDevice, len(m.devices))
	for _, d := range m.devices {
		prev[d.Name] = d
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
			if smartctl.IsCode(err, smartctl.CodeNotInstalled) {
				status, message = protocol.SMARTNotInstalled, "smartctl is not installed in the agent image"
			}
			m.logOnce("scan", "SMART disk scan failed", "error", err)
		default:
			m.clearLogged("scan")
			if len(devs) > protocol.MaxHealthDevices {
				devs = devs[:protocol.MaxHealthDevices]
			}
			scan, scanned = devs, m.opts.Clock.Now()
		}
	}
	if status == protocol.SMARTNotInstalled {
		m.finish(covers, func() {
			m.status, m.message, m.scan, m.devices = status, message, nil, nil
		})
		return
	}
	devices := m.readAll(ctx, scan, prev)
	if ctx.Err() != nil {
		m.finish(covers, nil)
		return
	}
	if status == protocol.SMARTOK && m.noAccess(devices) {
		status = protocol.SMARTNoAccess
	}
	checked := m.opts.Clock.Now().UTC()
	m.finish(covers, func() {
		m.status, m.message = status, message
		m.scan, m.scannedAt, m.devices = scan, scanned, devices
		m.checkedAt = checked
	})
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

// readAll reads every scanned device (Concurrency at a time) in scan
// order. A device in standby keeps its previous values, marked sleeping.
func (m *Monitor) readAll(ctx context.Context, scan []smartctl.ScanDevice, prev map[string]protocol.SMARTDevice) []protocol.SMARTDevice {
	out := make([]protocol.SMARTDevice, len(scan))
	sem := make(chan struct{}, m.opts.Concurrency)
	var wg sync.WaitGroup
	for i, dev := range scan {
		if dev.OpenError != "" {
			out[i] = protocol.SMARTDevice{Name: dev.Name, Type: dev.Type, Protocol: dev.Protocol, State: protocol.DiskError,
				ErrorCode: smartctl.ScanOpenError(dev)}
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = m.readOne(ctx, dev, prev[dev.Name])
		}()
	}
	wg.Wait()
	return out
}

func (m *Monitor) readOne(ctx context.Context, dev smartctl.ScanDevice, prev protocol.SMARTDevice) protocol.SMARTDevice {
	r, err := m.opts.SMART.Read(ctx, dev)
	if err != nil {
		if ctx.Err() == nil {
			m.logOnce("read "+dev.Name, "SMART read failed", "device", dev.Name, "error", err)
		}
		if prev.Name != "" && prev.State != protocol.DiskError {
			// Keep the last good values; the next round tries again.
			return prev
		}
		return protocol.SMARTDevice{Name: dev.Name, Type: dev.Type, Protocol: dev.Protocol, State: protocol.DiskError,
			ErrorCode: protocol.DiskErrOpenFailed}
	}
	m.clearLogged("read " + dev.Name)
	if r.Standby {
		if prev.Name != "" && prev.ReadAt != nil {
			prev.State, prev.ErrorCode = protocol.DiskSleeping, ""
			return prev
		}
		return r.Device
	}
	d := r.Device
	at := m.opts.Clock.Now().UTC()
	d.ReadAt = &at
	return d
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
	copy(s.Devices, m.devices)
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
