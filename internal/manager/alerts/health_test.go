package alerts

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/observe"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

func disk(state string) protocol.SMARTDevice {
	return protocol.SMARTDevice{Name: "/dev/sda", Type: "sat", Protocol: protocol.DiskATA, Model: "WDC WD40EFZX", Serial: "WD-SERIAL-CANARY",
		SMARTSupported: true, Passed: boolp(true), State: state}
}

func TestDiskSeverityFollowsTheDiskState(t *testing.T) {
	cases := []struct {
		name  string
		dev   func() protocol.SMARTDevice
		sev   domain.AlertSeverity
		title string
	}{
		{"failed self-assessment", func() protocol.SMARTDevice {
			d := disk(protocol.DiskFailing)
			d.Passed = boolp(false)
			return d
		}, domain.AlertCritical, "Disk /dev/sda is failing"},
		{"attribute failing now", func() protocol.SMARTDevice {
			d := disk(protocol.DiskFailing)
			d.FailingAttributes = []protocol.SMARTAttribute{{ID: 5, Name: "Reallocated_Sector_Ct", WhenFailed: "now"}}
			return d
		}, domain.AlertCritical, "Disk /dev/sda is failing"},
		{"NVMe critical warning", func() protocol.SMARTDevice {
			d := disk(protocol.DiskFailing)
			d.Name, d.Type, d.CriticalWarning = "/dev/nvme0", "nvme", intp(4)
			return d
		}, domain.AlertCritical, "Disk /dev/nvme0 is failing"},
		{"bad sectors", func() protocol.SMARTDevice {
			d := disk(protocol.DiskWarning)
			d.Reallocated, d.Pending = i64(8), i64(2)
			return d
		}, domain.AlertWarning, "Disk /dev/sda needs attention"},
		{"worn out", func() protocol.SMARTDevice {
			d := disk(protocol.DiskWarning)
			d.PercentageUsed = intp(93)
			return d
		}, domain.AlertWarning, "Disk /dev/sda needs attention"},
		{"can't be read", func() protocol.SMARTDevice {
			d := disk(protocol.DiskError)
			d.ErrorCode = protocol.DiskErrOpenFailed
			return d
		}, domain.AlertWarning, "Disk /dev/sda can't be read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.health.set("env-1", []protocol.SMARTDevice{c.dev()}, nil, nil)
			f.evaluate("env-1")
			a := f.one()
			if a.Kind != domain.NotifyDiskHealth || a.Severity != c.sev || a.Title != c.title || a.EnvironmentID != "env-1" ||
				a.ResourceType != domain.AlertResourceDisk || a.Facts["device"] == "" || a.Facts["deviceType"] == "" {
				t.Fatalf("%+v", a)
			}
			for k, v := range a.Facts {
				if strings.Contains(v, "SERIAL") || strings.Contains(k, "serial") {
					t.Fatalf("the serial number reached the alert: %s=%s", k, v)
				}
			}
		})
	}
}

func TestSleepingAndHealthyDisksRaiseNothing(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	f.health.set("env-1", []protocol.SMARTDevice{disk(protocol.DiskOK), func() protocol.SMARTDevice {
		d := disk(protocol.DiskSleeping)
		d.Name = "/dev/sdb"
		return d
	}()}, nil, nil)
	f.evaluate("env-1")
	if as := f.firing(); len(as) != 0 {
		t.Fatalf("%+v", as)
	}
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("messages %+v", got)
	}
}

func TestDiskAlertResolvesAndIsSentOnlyWhenWorse(t *testing.T) {
	f := newFixture(t)
	ch := f.channel("ops", nil, true, nil, true)
	warn := disk(protocol.DiskWarning)
	warn.Reallocated = i64(8)
	f.health.set("env-1", []protocol.SMARTDevice{warn}, nil, nil)
	f.evaluate("env-1")
	a := f.one()
	if got := f.dispatch(); len(got) != 1 || got[0].channel != ch.ID || got[0].msg.Title != "Disk /dev/sda needs attention" {
		t.Fatalf("first message %+v", got)
	}
	// Same problem, more sectors: facts change, no message.
	warn.Reallocated = i64(12)
	f.health.set("env-1", []protocol.SMARTDevice{warn}, nil, nil)
	f.evaluate("env-1")
	if b := f.one(); b.ID != a.ID || b.Facts["reallocatedSectors"] != "12" || b.Revision != a.Revision+1 {
		t.Fatalf("%+v", b)
	}
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("more of the same was sent again: %+v", got)
	}
	// A new problem (pending sectors): worse, sent again.
	warn.Pending = i64(1)
	f.health.set("env-1", []protocol.SMARTDevice{warn}, nil, nil)
	f.evaluate("env-1")
	if got := f.dispatch(); len(got) != 1 {
		t.Fatalf("a new problem was not sent: %+v", got)
	}
	// Failing now: the severity rises, sent again.
	failing := warn
	failing.State, failing.Passed = protocol.DiskFailing, boolp(false)
	f.health.set("env-1", []protocol.SMARTDevice{failing}, nil, nil)
	f.evaluate("env-1")
	if b := f.one(); b.Severity != domain.AlertCritical || b.ID != a.ID {
		t.Fatalf("%+v", b)
	}
	if got := f.dispatch(); len(got) != 1 || !strings.Contains(got[0].msg.Title, "is failing") {
		t.Fatalf("%+v", got)
	}
	// The disk is replaced by a healthy one under the same path.
	f.health.set("env-1", []protocol.SMARTDevice{disk(protocol.DiskOK)}, nil, nil)
	f.evaluate("env-1")
	if as := f.firing(); len(as) != 0 {
		t.Fatalf("%+v", as)
	}
	if got := f.dispatch(); len(got) != 1 || got[0].msg.Title != "Resolved: Disk /dev/sda is failing" {
		t.Fatalf("resolution %+v", got)
	}
}

func TestSleepingOrUnreadableDiskKeepsItsAlert(t *testing.T) {
	f := newFixture(t)
	failing := disk(protocol.DiskFailing)
	failing.Passed = boolp(false)
	f.health.set("env-1", []protocol.SMARTDevice{failing}, nil, nil)
	f.evaluate("env-1")
	a := f.one()
	for _, state := range []string{protocol.DiskSleeping, protocol.DiskError} {
		d := failing
		d.State = state
		if state == protocol.DiskError {
			d.ErrorCode = protocol.DiskErrOpenFailed
		}
		f.clk.Advance(time.Minute)
		f.health.set("env-1", []protocol.SMARTDevice{d}, nil, nil)
		f.evaluate("env-1")
		if b := f.one(); b.ID != a.ID || b.Severity != domain.AlertCritical || b.Revision != a.Revision || !b.LastSeenAt.After(a.LastSeenAt) {
			t.Fatalf("%s: %+v", state, b)
		}
	}
}

func TestDiskNoLongerReportedResolvesAfterADayWithoutMessage(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	warn := disk(protocol.DiskWarning)
	warn.Pending = i64(3)
	f.health.set("env-1", []protocol.SMARTDevice{warn}, nil, nil)
	f.evaluate("env-1")
	f.dispatch()
	f.health.set("env-1", nil, nil, nil)
	f.clk.Advance(DiskRemovedAfter - time.Minute)
	f.evaluate("env-1")
	if len(f.firing()) != 1 {
		t.Fatal("resolved before a day")
	}
	f.clk.Advance(2 * time.Minute)
	f.evaluate("env-1")
	if as := f.firing(); len(as) != 0 {
		t.Fatalf("%+v", as)
	}
	res, err := f.svc.List(f.ctx, domain.AlertFilter{State: domain.AlertListResolved}, "", 0)
	if err != nil || len(res) != 1 || res[0].Resolution != domain.AlertResolvedRemoved {
		t.Fatalf("%+v %v", res, err)
	}
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("a removed disk was announced: %+v", got)
	}
}

func TestRAIDStatesAndProgress(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	arr := protocol.MDArray{Name: "md0", Level: "raid1", State: protocol.RAIDDegraded, Devices: 2, Active: 1,
		Members: []protocol.MDMember{{Name: "sda1", State: protocol.MemberActive}, {Name: "sdb1", Slot: 1, State: protocol.MemberFailed}}}
	f.health.set("env-1", nil, []protocol.MDArray{arr}, nil)
	f.evaluate("env-1")
	a := f.one()
	if a.Kind != domain.NotifyRAID || a.Severity != domain.AlertWarning || a.Title != "RAID md0 is degraded" || a.Facts["failedMembers"] != "sdb1" {
		t.Fatalf("%+v", a)
	}
	if got := f.dispatch(); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	// A rebuild starts and progresses: the alert follows, nobody is told.
	arr.State, arr.Action = protocol.RAIDRebuilding, protocol.MDRecovery
	arr.Members = []protocol.MDMember{{Name: "sda1", State: protocol.MemberActive}, {Name: "sdc1", Slot: 1, State: protocol.MemberReplacement}}
	for _, p := range []float64{12.5, 47.25} {
		arr.Progress = &p
		f.health.set("env-1", nil, []protocol.MDArray{arr}, nil)
		f.evaluate("env-1")
	}
	b := f.one()
	if b.ID != a.ID || b.Title != "RAID md0 is rebuilding" || b.Facts["progress"] != "47.25" || !strings.Contains(Detail(b), "Rebuild: 47.25% done") {
		t.Fatalf("%+v / %s", b, Detail(b))
	}
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("rebuild progress was sent: %+v", got)
	}
	// The array fails: critical, sent.
	arr.State, arr.Progress, arr.Action = protocol.RAIDFailed, nil, ""
	f.health.set("env-1", nil, []protocol.MDArray{arr}, nil)
	f.evaluate("env-1")
	if b := f.one(); b.Severity != domain.AlertCritical || b.Title != "RAID md0 has failed" {
		t.Fatalf("%+v", b)
	}
	if got := f.dispatch(); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	// Healthy again: resolved.
	arr.State, arr.Active = protocol.RAIDHealthy, 2
	arr.Members = []protocol.MDMember{{Name: "sda1", State: protocol.MemberActive}, {Name: "sdc1", Slot: 1, State: protocol.MemberActive}}
	f.health.set("env-1", nil, []protocol.MDArray{arr}, nil)
	f.evaluate("env-1")
	if len(f.firing()) != 0 {
		t.Fatal("still firing")
	}
	if got := f.dispatch(); len(got) != 1 || !strings.HasPrefix(got[0].msg.Title, "Resolved: ") {
		t.Fatalf("%+v", got)
	}
}

func TestMDSeverityMapping(t *testing.T) {
	for state, want := range map[string]domain.AlertSeverity{
		protocol.RAIDFailed: domain.AlertCritical, protocol.RAIDInactive: domain.AlertCritical,
		protocol.RAIDDegraded: domain.AlertWarning, protocol.RAIDRebuilding: domain.AlertWarning,
	} {
		o, problem := mdObservation(domain.Environment{ID: "env-1", Name: "homelab"}, protocol.MDArray{Name: "md1", State: state}, "k")
		if !problem || o.Severity != want {
			t.Errorf("%s: %v %s", state, problem, o.Severity)
		}
	}
	for _, state := range []string{protocol.RAIDHealthy, protocol.RAIDChecking} {
		if _, problem := mdObservation(domain.Environment{ID: "env-1"}, protocol.MDArray{Name: "md1", State: state}, "k"); problem {
			t.Errorf("%s raised an alert", state)
		}
	}
}

func TestZFSSeverityMapping(t *testing.T) {
	for health, want := range map[string]domain.AlertSeverity{
		"DEGRADED": domain.AlertWarning, "FAULTED": domain.AlertCritical, "UNAVAIL": domain.AlertCritical,
		"SUSPENDED": domain.AlertCritical, "REMOVED": domain.AlertCritical,
	} {
		o, problem := zfsObservation(domain.Environment{ID: "env-1", Name: "homelab"}, protocol.ZFSPool{Name: "tank", Health: health}, "k")
		if !problem || o.Severity != want || o.ResourceType != domain.AlertResourceZFS {
			t.Errorf("%s: %v %+v", health, problem, o)
		}
	}
	if _, problem := zfsObservation(domain.Environment{ID: "env-1"}, protocol.ZFSPool{Name: "tank", Health: "ONLINE", State: protocol.RAIDHealthy}, "k"); problem {
		t.Error("ONLINE raised an alert")
	}
	f := newFixture(t)
	f.health.set("env-1", nil, nil, []protocol.ZFSPool{{Name: "tank", Health: "DEGRADED", State: protocol.RAIDDegraded}})
	f.evaluate("env-1")
	if a := f.one(); a.Title != "ZFS pool tank is degraded" || Detail(a) != "Pool health: DEGRADED." {
		t.Fatalf("%+v", a)
	}
	f.health.set("env-1", nil, nil, []protocol.ZFSPool{{Name: "tank", Health: "ONLINE", State: protocol.RAIDHealthy}})
	f.evaluate("env-1")
	if len(f.firing()) != 0 {
		t.Fatal("ONLINE did not resolve")
	}
}

func TestSMARTUnavailableKeepsAlertsUntilRemoved(t *testing.T) {
	f := newFixture(t)
	warn := disk(protocol.DiskWarning)
	warn.Pending = i64(3)
	f.health.set("env-1", []protocol.SMARTDevice{warn}, nil, nil)
	f.evaluate("env-1")
	// The agent's SMART reads stop (turned off): nothing is resolved as
	// fixed; the alert ends as removed a day later.
	f.health.mu.Lock()
	r := f.health.reports["env-1"]
	r.SMART = protocol.SMARTReport{Status: protocol.SMARTDisabled}
	f.health.reports["env-1"] = r
	f.health.mu.Unlock()
	f.evaluate("env-1")
	if len(f.firing()) != 1 {
		t.Fatal("resolved without a reading")
	}
}

// put replaces an environment's whole report.
func (h *fakeHealth) put(env string, r observe.HostHealth) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reports[env] = r
}

// report is a report with SMART status and devices, md arrays and the
// RAID message, sampled and received now.
func (f *fixture) report(status string, devs []protocol.SMARTDevice, md []protocol.MDArray, raidMessage string) observe.HostHealth {
	now := f.clk.Now().UTC()
	return observe.HostHealth{HostHealthOutput: protocol.HostHealthOutput{SampledAt: now,
		SMART: protocol.SMARTReport{Status: status, Devices: devs},
		RAID:  protocol.RAIDReport{ReadAt: now, MD: md, Message: raidMessage}}, ReceivedAt: now}
}

// byKey returns the firing alerts by dedupe key.
func (f *fixture) byKey() map[string]domain.Alert {
	f.t.Helper()
	out := map[string]domain.Alert{}
	for _, a := range f.firing() {
		out[a.DedupeKey] = a
	}
	return out
}

func TestScanFailureStillEvaluatesDisksAndWarns(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	failing := disk(protocol.DiskFailing)
	failing.Passed = boolp(false)
	// The scan failed; the agent read the disks it knew.
	f.health.put("env-1", f.report(protocol.SMARTError, []protocol.SMARTDevice{failing}, nil, ""))
	f.evaluate("env-1")
	as := f.byKey()
	if d := as[diskKey("env-1", "/dev/sda", "sat")]; d.Severity != domain.AlertCritical {
		t.Fatalf("the read disk was not evaluated: %+v", as)
	}
	m := as[monitorKey("env-1", domain.NotifyDiskHealth)]
	if m.Severity != domain.AlertWarning || m.Title != "Disks can't be scanned" || m.ResourceType != domain.AlertResourceEnvironment ||
		!strings.Contains(Detail(m), "could not scan") {
		t.Fatalf("monitoring alert %+v / %s", m, Detail(m))
	}
	// One digest of both alerts.
	if got := f.dispatch(); len(got) != 1 || !slices.Contains(digestLines(got[0].msg), "Warning: Disks can't be scanned (homelab)") {
		t.Fatalf("messages %+v", got)
	}
	// The scan works again: the monitoring alert resolves, the disk's stays.
	f.health.put("env-1", f.report(protocol.SMARTOK, []protocol.SMARTDevice{failing}, nil, ""))
	f.evaluate("env-1")
	if as := f.firing(); len(as) != 1 || as[0].DedupeKey != diskKey("env-1", "/dev/sda", "sat") {
		t.Fatalf("%+v", as)
	}
	got := f.dispatch()
	if len(got) != 1 || got[0].msg.Title != "Resolved: Disks can't be scanned" ||
		got[0].msg.Body != "Disk health is watched again." {
		t.Fatalf("resolution %+v", got)
	}
}

// TestBlindMonitoringKeepsAlerts: while SMART or RAID can't be read, the
// alerts of disks and arrays no longer reported are kept past a day (what
// can't be seen is not gone), next to a monitoring alert.
func TestBlindMonitoringKeepsAlerts(t *testing.T) {
	f := newFixture(t)
	warn := disk(protocol.DiskWarning)
	warn.Pending = i64(3)
	md := protocol.MDArray{Name: "md0", Level: "raid1", State: protocol.RAIDDegraded, Devices: 2, Active: 1}
	f.health.put("env-1", f.report(protocol.SMARTOK, []protocol.SMARTDevice{warn}, []protocol.MDArray{md}, ""))
	f.evaluate("env-1")
	if len(f.firing()) != 2 {
		t.Fatalf("%+v", f.firing())
	}
	for i := 0; i < 3; i++ {
		f.clk.Advance(DiskRemovedAfter / 2)
		f.health.put("env-1", f.report(protocol.SMARTNoAccess, nil, nil, "the software RAID state could not be read"))
		f.evaluate("env-1")
	}
	as := f.byKey()
	for _, key := range []string{diskKey("env-1", "/dev/sda", "sat"), raidKey("env-1", "md", "md0"),
		monitorKey("env-1", domain.NotifyDiskHealth), monitorKey("env-1", domain.NotifyRAID)} {
		if _, ok := as[key]; !ok {
			t.Errorf("%s is not firing: %+v", key, as)
		}
	}
	if a := as[monitorKey("env-1", domain.NotifyDiskHealth)]; a.Title != "Disks can't be opened" {
		t.Errorf("%+v", a)
	}
	if a := as[monitorKey("env-1", domain.NotifyRAID)]; a.Title != "RAID state can't be read" {
		t.Errorf("%+v", a)
	}
	// Monitoring works again and nothing is reported: removed a day later.
	f.health.put("env-1", f.report(protocol.SMARTOK, nil, nil, ""))
	f.evaluate("env-1")
	if len(f.firing()) != 0 {
		t.Fatalf("still firing %+v", f.firing())
	}
}

// TestNoAccessRaisesOneAlert: disks that refuse to open because the agent
// is not privileged raise the monitoring alert, not one per disk.
func TestNoAccessRaisesOneAlert(t *testing.T) {
	f := newFixture(t)
	denied := disk(protocol.DiskError)
	denied.ErrorCode, denied.Passed = protocol.DiskErrPermissionDenied, nil
	f.health.put("env-1", f.report(protocol.SMARTNoAccess, []protocol.SMARTDevice{denied}, nil, ""))
	f.evaluate("env-1")
	if a := f.one(); a.DedupeKey != monitorKey("env-1", domain.NotifyDiskHealth) || !strings.Contains(Detail(a), "privileged: true") {
		t.Fatalf("%+v", a)
	}
}

// TestUnsupportedDisksRaiseNothing: a disk without SMART data (a virtual
// disk) never alerts; an alert it had ends as removed.
func TestUnsupportedDisksRaiseNothing(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	unsupported := disk(protocol.DiskError)
	unsupported.ErrorCode, unsupported.SMARTSupported, unsupported.Passed = protocol.DiskErrUnsupported, false, nil
	f.health.set("env-1", []protocol.SMARTDevice{unsupported}, nil, nil)
	f.evaluate("env-1")
	if as := f.firing(); len(as) != 0 {
		t.Fatalf("%+v", as)
	}
	unreadable := disk(protocol.DiskError)
	unreadable.ErrorCode = protocol.DiskErrOpenFailed
	f.health.set("env-1", []protocol.SMARTDevice{unreadable}, nil, nil)
	f.evaluate("env-1")
	f.dispatch()
	f.health.set("env-1", []protocol.SMARTDevice{unsupported}, nil, nil)
	f.evaluate("env-1")
	res, err := f.svc.List(f.ctx, domain.AlertFilter{State: domain.AlertListResolved}, "", 0)
	if err != nil || len(res) != 1 || res[0].Resolution != domain.AlertResolvedRemoved {
		t.Fatalf("%+v %v", res, err)
	}
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("announced as fixed: %+v", got)
	}
}

// TestMissingDiskAlert: a disk no scan finds any more is missing; that is
// worse than unreadable (sent again).
func TestMissingDiskAlert(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	d := disk(protocol.DiskError)
	d.ErrorCode = protocol.DiskErrTimeout
	f.health.set("env-1", []protocol.SMARTDevice{d}, nil, nil)
	f.evaluate("env-1")
	if got := f.dispatch(); len(got) != 1 || got[0].msg.Title != "Disk /dev/sda can't be read" {
		t.Fatalf("%+v", got)
	}
	d.ErrorCode = protocol.DiskErrMissing
	f.health.set("env-1", []protocol.SMARTDevice{d}, nil, nil)
	f.evaluate("env-1")
	a := f.one()
	if a.Title != "Disk /dev/sda is missing" || a.Severity != domain.AlertWarning ||
		!strings.Contains(Detail(a), "the agent no longer finds it") {
		t.Fatalf("%+v / %s", a, Detail(a))
	}
	if got := f.dispatch(); len(got) != 1 {
		t.Fatalf("missing was not sent: %+v", got)
	}
}

// TestDiskAlertFollowsItsDisk: when disk names move (a reboot, a
// hot-swap), an alert stays with its disk (its serial number) instead of
// resolving for one path and firing for another.
func TestDiskAlertFollowsItsDisk(t *testing.T) {
	f := newFixture(t)
	f.channel("ops", nil, true, nil, true)
	x := disk(protocol.DiskWarning)
	x.Serial, x.Pending = "X-SERIAL", i64(4)
	y := disk(protocol.DiskWarning)
	y.Name, y.Serial, y.Reallocated = "/dev/sdb", "Y-SERIAL", i64(9)
	f.health.set("env-1", []protocol.SMARTDevice{x, y}, nil, nil)
	f.evaluate("env-1")
	before := f.byKey()
	ax, ay := before[diskKey("env-1", "/dev/sda", "sat")], before[diskKey("env-1", "/dev/sdb", "sat")]
	if ax.ID == "" || ay.ID == "" || ax.Facts["diskId"] == "" || ax.Facts["diskId"] == ay.Facts["diskId"] {
		t.Fatalf("%+v", before)
	}
	f.dispatch()
	// The names swap.
	x.Name, y.Name = "/dev/sdb", "/dev/sda"
	f.health.set("env-1", []protocol.SMARTDevice{y, x}, nil, nil)
	f.evaluate("env-1")
	after := f.byKey()
	if a := after[diskKey("env-1", "/dev/sdb", "sat")]; a.ID != ax.ID || a.Title != "Disk /dev/sdb needs attention" ||
		a.Facts["pendingSectors"] != "4" {
		t.Fatalf("x's alert %+v", a)
	}
	if a := after[diskKey("env-1", "/dev/sda", "sat")]; a.ID != ay.ID || a.Facts["reallocatedSectors"] != "9" {
		t.Fatalf("y's alert %+v", a)
	}
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("a move was announced: %+v", got)
	}
	// x is replaced: another, healthy disk at its path; x is gone.
	z := disk(protocol.DiskOK)
	z.Name, z.Serial = "/dev/sdb", "Z-SERIAL"
	f.health.set("env-1", []protocol.SMARTDevice{y, z}, nil, nil)
	f.evaluate("env-1")
	if as := f.firing(); len(as) != 1 || as[0].ID != ay.ID {
		t.Fatalf("%+v", as)
	}
	res, err := f.svc.List(f.ctx, domain.AlertFilter{State: domain.AlertListResolved}, "", 0)
	if err != nil || len(res) != 1 || res[0].ID != ax.ID || res[0].Resolution != domain.AlertResolvedRemoved {
		t.Fatalf("%+v %v", res, err)
	}
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("a replaced disk was announced as healthy: %+v", got)
	}
}

// TestNVMeTooHotIsAWarning: an NVMe drive whose only critical warning is
// its temperature is a warning, said in words.
func TestNVMeTooHotIsAWarning(t *testing.T) {
	f := newFixture(t)
	d := disk(protocol.DiskWarning)
	d.Name, d.Type, d.Protocol, d.Passed, d.CriticalWarning = "/dev/nvme0", "nvme", protocol.DiskNVMe, boolp(false), intp(protocol.NVMeWarnTemperature)
	f.health.set("env-1", []protocol.SMARTDevice{d}, nil, nil)
	f.evaluate("env-1")
	a := f.one()
	if a.Severity != domain.AlertWarning || !strings.Contains(Detail(a), "Too hot") || strings.Contains(Detail(a), "self-assessment") {
		t.Fatalf("%+v / %s", a, Detail(a))
	}
}

// TestStaleHealthWarns: disks the agent stopped reading, and an online
// environment that stopped reporting, raise the monitoring alerts.
func TestStaleHealthWarns(t *testing.T) {
	f := newFixture(t)
	r := f.report(protocol.SMARTOK, []protocol.SMARTDevice{disk(protocol.DiskOK)}, nil, "")
	r.SMART.IntervalSeconds = 1800
	checked := r.SampledAt.Add(-time.Hour)
	r.SMART.CheckedAt = &checked
	f.health.put("env-1", r)
	f.evaluate("env-1")
	if len(f.firing()) != 0 {
		t.Fatalf("within twice the interval and the slack: %+v", f.firing())
	}
	checked = r.SampledAt.Add(-2*time.Hour - time.Minute)
	f.health.put("env-1", r)
	f.evaluate("env-1")
	if a := f.one(); a.Title != "Disk health is out of date" || a.Facts["reason"] != reasonStale {
		t.Fatalf("%+v", a)
	}
	// The report itself stops coming while the environment is online.
	f.clk.Advance(HealthReportStale)
	f.evaluate("env-1")
	as := f.byKey()
	if a := as[monitorKey("env-1", domain.NotifyDiskHealth)]; a.Facts["reason"] != reasonNoReport {
		t.Errorf("%+v", a)
	}
	if a := as[monitorKey("env-1", domain.NotifyRAID)]; a.Title != "RAID state is out of date" {
		t.Errorf("%+v", as)
	}
	// Offline: the offline alert says it; nothing stale here.
	f.setEnvironment("env-1", false, f.clk.Now(), domain.EnvironmentActive)
	f.evaluate("env-1")
	if as := f.firing(); len(as) != 1 || as[0].Facts["reason"] != reasonStale {
		t.Fatalf("%+v", as)
	}
}
