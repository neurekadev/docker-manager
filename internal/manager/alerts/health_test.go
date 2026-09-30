package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
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
		}, domain.AlertCritical, "Disk /dev/sda on homelab is failing"},
		{"attribute failing now", func() protocol.SMARTDevice {
			d := disk(protocol.DiskFailing)
			d.FailingAttributes = []protocol.SMARTAttribute{{ID: 5, Name: "Reallocated_Sector_Ct", WhenFailed: "now"}}
			return d
		}, domain.AlertCritical, "Disk /dev/sda on homelab is failing"},
		{"NVMe critical warning", func() protocol.SMARTDevice {
			d := disk(protocol.DiskFailing)
			d.Name, d.Type, d.CriticalWarning = "/dev/nvme0", "nvme", intp(4)
			return d
		}, domain.AlertCritical, "Disk /dev/nvme0 on homelab is failing"},
		{"bad sectors", func() protocol.SMARTDevice {
			d := disk(protocol.DiskWarning)
			d.Reallocated, d.Pending = i64(8), i64(2)
			return d
		}, domain.AlertWarning, "Disk /dev/sda on homelab needs attention"},
		{"worn out", func() protocol.SMARTDevice {
			d := disk(protocol.DiskWarning)
			d.PercentageUsed = intp(93)
			return d
		}, domain.AlertWarning, "Disk /dev/sda on homelab needs attention"},
		{"can't be read", func() protocol.SMARTDevice {
			d := disk(protocol.DiskError)
			d.ErrorCode = protocol.DiskErrOpenFailed
			return d
		}, domain.AlertWarning, "Disk /dev/sda on homelab can't be read"},
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
	if got := f.dispatch(); len(got) != 1 || got[0].channel != ch.ID || got[0].msg.Title != "[Docker Manager] Disk /dev/sda on homelab needs attention" {
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
	if got := f.dispatch(); len(got) != 1 || got[0].msg.Title != "[Docker Manager] Resolved: Disk /dev/sda on homelab is failing" {
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
	if a.Kind != domain.NotifyRAID || a.Severity != domain.AlertWarning || a.Title != "RAID md0 on homelab is degraded" || a.Facts["failedMembers"] != "sdb1" {
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
	if b.ID != a.ID || b.Title != "RAID md0 on homelab is rebuilding" || b.Facts["progress"] != "47.25" || !strings.Contains(Detail(b), "Rebuild: 47.25% done") {
		t.Fatalf("%+v / %s", b, Detail(b))
	}
	if got := f.dispatch(); len(got) != 0 {
		t.Fatalf("rebuild progress was sent: %+v", got)
	}
	// The array fails: critical, sent.
	arr.State, arr.Progress, arr.Action = protocol.RAIDFailed, nil, ""
	f.health.set("env-1", nil, []protocol.MDArray{arr}, nil)
	f.evaluate("env-1")
	if b := f.one(); b.Severity != domain.AlertCritical || b.Title != "RAID md0 on homelab has failed" {
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
	if got := f.dispatch(); len(got) != 1 || !strings.HasPrefix(got[0].msg.Title, "[Docker Manager] Resolved: ") {
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
	if a := f.one(); a.Title != "ZFS pool tank on homelab is degraded" || Detail(a) != "Pool health: DEGRADED." {
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
