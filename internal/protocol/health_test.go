package protocol

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validHealth() HostHealthOutput {
	at := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	passed, temp, pct := true, 36, 50.5
	return HostHealthOutput{
		SampledAt: at,
		SMART: SMARTReport{Status: SMARTOK, ScannedAt: &at, CheckedAt: &at, Devices: []SMARTDevice{
			{Name: "/dev/sda", Type: "sat", Protocol: DiskATA, Model: "WDC", Serial: "WD-1", SMARTSupported: true, Passed: &passed,
				TemperatureC: &temp, State: DiskOK, ReadAt: &at},
			{Name: "/dev/sdb", Type: "sat", State: DiskError, ErrorCode: DiskErrPermissionDenied},
		}},
		RAID: RAIDReport{ReadAt: at,
			MD: []MDArray{{Name: "md0", Level: "raid1", State: RAIDRebuilding, Devices: 2, Active: 1, Action: MDRecovery, Progress: &pct,
				Members: []MDMember{{Name: "sda1", Slot: 0, State: MemberActive}, {Name: "sdb1", Slot: 1, State: MemberFailed}}}},
			ZFS: []ZFSPool{{Name: "tank", Health: "DEGRADED", State: RAIDDegraded}}},
	}
}

func TestHostHealthValidate(t *testing.T) {
	if err := validHealth().Validate(); err != nil {
		t.Fatal(err)
	}
	neg, big, tooHot := int64(-1), 300, 5000
	bad := map[string]func(o *HostHealthOutput){
		"no sampledAt":       func(o *HostHealthOutput) { o.SampledAt = time.Time{} },
		"no raid.readAt":     func(o *HostHealthOutput) { o.RAID.ReadAt = time.Time{} },
		"unknown status":     func(o *HostHealthOutput) { o.SMART.Status = "maybe" },
		"unknown state":      func(o *HostHealthOutput) { o.SMART.Devices[0].State = "grumpy" },
		"error without code": func(o *HostHealthOutput) { o.SMART.Devices[1].ErrorCode = "" },
		"code without error": func(o *HostHealthOutput) { o.SMART.Devices[0].ErrorCode = DiskErrOpenFailed },
		"no device name":     func(o *HostHealthOutput) { o.SMART.Devices[0].Name = "" },
		"long serial":        func(o *HostHealthOutput) { o.SMART.Devices[0].Serial = strings.Repeat("x", 256) },
		"invalid UTF-8":      func(o *HostHealthOutput) { o.SMART.Devices[0].Model = "\xff" },
		"negative counter":   func(o *HostHealthOutput) { o.SMART.Devices[0].Pending = &neg },
		"spare out of range": func(o *HostHealthOutput) { o.SMART.Devices[0].AvailableSpare = &big },
		"temperature":        func(o *HostHealthOutput) { o.SMART.Devices[0].TemperatureC = &tooHot },
		"unknown protocol":   func(o *HostHealthOutput) { o.SMART.Devices[0].Protocol = "sata" },
		"attribute when": func(o *HostHealthOutput) {
			o.SMART.Devices[0].FailingAttributes = []SMARTAttribute{{ID: 5, WhenFailed: "soon"}}
		},
		"too many devices":    func(o *HostHealthOutput) { o.SMART.Devices = make([]SMARTDevice, MaxHealthDevices+1) },
		"array state":         func(o *HostHealthOutput) { o.RAID.MD[0].State = "sad" },
		"array action":        func(o *HostHealthOutput) { o.RAID.MD[0].Action = "scrub" },
		"active over devices": func(o *HostHealthOutput) { o.RAID.MD[0].Active = 3 },
		"progress over 100":   func(o *HostHealthOutput) { p := 101.0; o.RAID.MD[0].Progress = &p },
		"member state":        func(o *HostHealthOutput) { o.RAID.MD[0].Members[0].State = "tired" },
		"too many arrays":     func(o *HostHealthOutput) { o.RAID.MD = make([]MDArray, MaxHealthArrays+1) },
		"zfs health":          func(o *HostHealthOutput) { o.RAID.ZFS[0].Health = "SLEEPY" },
		"zfs without name":    func(o *HostHealthOutput) { o.RAID.ZFS[0].Name = "" },
		"long message":        func(o *HostHealthOutput) { o.RAID.Message = strings.Repeat("m", 600) },
	}
	for name, mut := range bad {
		o := validHealth()
		mut(&o)
		if err := o.Validate(); !errors.Is(err, ErrInvalidFrame) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestHostHealthInputValidate(t *testing.T) {
	for _, ok := range []string{"", HealthRefreshSMART, HealthRefreshRAID} {
		if err := (HostHealthInput{Refresh: ok}).Validate(); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	if err := (HostHealthInput{Refresh: "selftest"}).Validate(); err == nil {
		t.Error("selftest accepted")
	}
}

func TestDeriveDiskState(t *testing.T) {
	f, tr := false, true
	one, zero := int64(1), int64(0)
	i := func(v int) *int { return &v }
	for _, c := range []struct {
		name string
		d    SMARTDevice
		want string
	}{
		{"healthy", SMARTDevice{Passed: &tr, Reallocated: &zero, Pending: &zero}, DiskOK},
		{"self-assessment failed", SMARTDevice{Passed: &f}, DiskFailing},
		{"attribute failing now", SMARTDevice{Passed: &tr, FailingAttributes: []SMARTAttribute{{ID: 5, WhenFailed: "now"}}}, DiskFailing},
		{"attribute failed in the past", SMARTDevice{Passed: &tr, FailingAttributes: []SMARTAttribute{{ID: 190, WhenFailed: "past"}}}, DiskWarning},
		{"nvme critical warning", SMARTDevice{CriticalWarning: i(1)}, DiskFailing},
		{"pending sector", SMARTDevice{Pending: &one}, DiskWarning},
		{"reallocated sector", SMARTDevice{Reallocated: &one}, DiskWarning},
		{"reported uncorrectable", SMARTDevice{ReportedUncorrectable: &one}, DiskWarning},
		{"offline uncorrectable", SMARTDevice{OfflineUncorrectable: &one}, DiskWarning},
		{"media errors", SMARTDevice{MediaErrors: &one}, DiskWarning},
		{"grown defects", SMARTDevice{GrownDefects: &one}, DiskWarning},
		{"uncorrected errors", SMARTDevice{UncorrectedErrors: &one}, DiskWarning},
		{"worn", SMARTDevice{PercentageUsed: i(Worn)}, DiskWarning},
		{"almost worn", SMARTDevice{PercentageUsed: i(Worn - 1)}, DiskOK},
		{"spare below threshold", SMARTDevice{AvailableSpare: i(5), AvailableSpareThreshold: i(10)}, DiskWarning},
		{"spare at threshold", SMARTDevice{AvailableSpare: i(10), AvailableSpareThreshold: i(10)}, DiskOK},
	} {
		if got := DeriveDiskState(c.d); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}
