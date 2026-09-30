package protocol

import (
	"slices"
	"time"
	"unicode/utf8"
)

// Disk health (#143): the input and output of host.health. The agent reads
// SMART data with its pinned smartctl (cached, refreshed every
// DOCKER_AGENT_SMART_INTERVAL; a disk in standby is never woken) and the
// Linux software RAID and ZFS pool state from procfs on every request.
// docs/internal/architecture/metrics.md, "Host health".

// host.health bounds.
const (
	// MaxHealthDevices bounds the SMART devices of one answer.
	MaxHealthDevices = 256
	// MaxHealthArrays bounds the md arrays and the ZFS pools of one answer
	// (each).
	MaxHealthArrays = 64
	// MaxHealthMembers bounds the members of one md array.
	MaxHealthMembers = 128
	// MaxFailingAttributes bounds the failing ATA attributes of one device.
	MaxFailingAttributes = 32
	// maxHealthText bounds the free-text fields (model, serial, names).
	maxHealthText = 255
	// maxHealthMessage bounds an error message.
	maxHealthMessage = 512
)

// host.health refresh scopes.
const (
	// HealthRefreshSMART starts a fresh SMART read of every device now (a
	// device scan and a read of each; never a self-test).
	HealthRefreshSMART = "smart"
	// HealthRefreshRAID asks for a fresh RAID read (the agent reads RAID
	// on every request anyway; never a scrub).
	HealthRefreshRAID = "raid"
)

// SMART report statuses.
const (
	// SMARTOK: devices were scanned (the list may be empty: no disk
	// reports SMART data, for example virtual disks).
	SMARTOK = "ok"
	// SMARTDisabled: DOCKER_AGENT_SMART_ENABLED is false.
	SMARTDisabled = "disabled"
	// SMARTNoAccess: the host has disks the agent cannot open (the agent
	// container is not privileged).
	SMARTNoAccess = "no_access"
	// SMARTNotInstalled: the smartctl binary is missing.
	SMARTNotInstalled = "not_installed"
	// SMARTError: the device scan failed.
	SMARTError = "error"
)

// SMART device states.
const (
	DiskOK       = "ok"
	DiskWarning  = "warning"
	DiskFailing  = "failing"
	DiskSleeping = "sleeping"
	DiskError    = "error"
)

// SMART device error codes (state error).
const (
	DiskErrPermissionDenied = "permission_denied"
	DiskErrOpenFailed       = "open_failed"
	// DiskErrUnsupported: the device answers but reports no SMART data
	// (virtual disks, unknown USB bridges).
	DiskErrUnsupported = "unsupported"
)

// SMART device protocols.
const (
	DiskATA  = "ata"
	DiskNVMe = "nvme"
	DiskSCSI = "scsi"
)

// RAID array states (md arrays and ZFS pools).
const (
	RAIDHealthy    = "healthy"
	RAIDDegraded   = "degraded"
	RAIDRebuilding = "rebuilding"
	RAIDChecking   = "checking"
	RAIDFailed     = "failed"
	RAIDInactive   = "inactive"
)

// md member states.
const (
	MemberActive      = "active"
	MemberSpare       = "spare"
	MemberFailed      = "failed"
	MemberReplacement = "replacement"
	MemberJournal     = "journal"
)

// md sync actions (the progress line of /proc/mdstat).
const (
	MDRecovery = "recovery"
	MDResync   = "resync"
	MDReshape  = "reshape"
	MDCheck    = "check"
	MDRepair   = "repair"
)

// ZFS pool health values (/proc/spl/kstat/zfs/<pool>/state).
var zfsHealth = []string{"ONLINE", "DEGRADED", "FAULTED", "OFFLINE", "UNAVAIL", "REMOVED", "SUSPENDED"}

// ZFSHealthValues returns the pool health values the agent reports.
func ZFSHealthValues() []string { return slices.Clone(zfsHealth) }

// HostHealthInput asks for the host's disk health.
type HostHealthInput struct {
	// Refresh is "", HealthRefreshSMART or HealthRefreshRAID.
	Refresh string `json:"refresh,omitempty"`
}

// Validate checks a host.health input.
func (in HostHealthInput) Validate() error {
	switch in.Refresh {
	case "", HealthRefreshSMART, HealthRefreshRAID:
		return nil
	}
	return invalid("host.health refresh must be empty, smart or raid")
}

// HostHealthOutput answers host.health.
type HostHealthOutput struct {
	// SampledAt is the agent clock when answering.
	SampledAt time.Time   `json:"sampledAt"`
	SMART     SMARTReport `json:"smart"`
	RAID      RAIDReport  `json:"raid"`
}

// SMARTReport is the agent's cached SMART state.
type SMARTReport struct {
	Status string `json:"status"`
	// Message explains status error or not_installed (no secrets, no
	// device contents).
	Message string `json:"message,omitempty"`
	// Checking: a requested read of every device is still running; ask
	// again for its result.
	Checking bool `json:"checking,omitempty"`
	// ScannedAt is when the device list was last scanned; CheckedAt when
	// the last read of every device ended.
	ScannedAt *time.Time    `json:"scannedAt,omitempty"`
	CheckedAt *time.Time    `json:"checkedAt,omitempty"`
	Devices   []SMARTDevice `json:"devices"`
}

// SMARTDevice is one disk's SMART data. Absent values are unknown (the
// device does not report them). Serial numbers are shown to users with
// environment.system.read and stored, but never logged.
type SMARTDevice struct {
	// Name is the device path (/dev/sda); Type the smartctl device type
	// (sat, nvme, scsi, usbjmicron, megaraid,N, ...). Together they
	// identify the device: disks behind one RAID controller share its
	// path.
	Name          string `json:"name"`
	Type          string `json:"type"`
	Protocol      string `json:"protocol,omitempty"`
	Model         string `json:"model,omitempty"`
	Serial        string `json:"serial,omitempty"`
	Firmware      string `json:"firmware,omitempty"`
	CapacityBytes int64  `json:"capacityBytes,omitempty"`
	// RotationRPM is the spindle speed; 0 for a solid-state device.
	RotationRPM    *int `json:"rotationRpm,omitempty"`
	SMARTSupported bool `json:"smartSupported"`
	// Passed is the drive's overall self-assessment.
	Passed       *bool  `json:"passed,omitempty"`
	TemperatureC *int   `json:"temperatureC,omitempty"`
	PowerOnHours *int64 `json:"powerOnHours,omitempty"`
	// ATA attributes (raw values): 5 reallocated sectors, 187 reported
	// uncorrectable errors, 197 pending sectors, 198 offline
	// uncorrectable sectors, and the attributes at or below their
	// threshold now or in the past.
	Reallocated           *int64           `json:"reallocatedSectors,omitempty"`
	ReportedUncorrectable *int64           `json:"reportedUncorrectable,omitempty"`
	Pending               *int64           `json:"pendingSectors,omitempty"`
	OfflineUncorrectable  *int64           `json:"offlineUncorrectable,omitempty"`
	FailingAttributes     []SMARTAttribute `json:"failingAttributes,omitempty"`
	// NVMe health log.
	CriticalWarning         *int   `json:"criticalWarning,omitempty"`
	AvailableSpare          *int   `json:"availableSpare,omitempty"`
	AvailableSpareThreshold *int   `json:"availableSpareThreshold,omitempty"`
	MediaErrors             *int64 `json:"mediaErrors,omitempty"`
	// PercentageUsed is the wear estimate (NVMe percentage used, SCSI
	// endurance indicator); it may exceed 100.
	PercentageUsed *int `json:"percentageUsed,omitempty"`
	// SCSI.
	GrownDefects      *int64 `json:"grownDefects,omitempty"`
	UncorrectedErrors *int64 `json:"uncorrectedErrors,omitempty"`
	// State is derived from the values (DiskOK, DiskWarning, DiskFailing),
	// DiskSleeping (in standby: the values are the previous read's) or
	// DiskError (ErrorCode says why; after a failed read the previous
	// values stay for reference).
	State     string `json:"state"`
	ErrorCode string `json:"errorCode,omitempty"`
	// ReadAt is when the values were read (absent when never).
	ReadAt *time.Time `json:"readAt,omitempty"`
}

// SMARTAttribute is an ATA attribute at or below its threshold.
type SMARTAttribute struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	// WhenFailed is "now" (failing) or "past" (was at or below the
	// threshold at some time).
	WhenFailed string `json:"whenFailed"`
}

// RAIDReport is the RAID state read when answering.
type RAIDReport struct {
	ReadAt time.Time `json:"readAt"`
	// Message: /proc/mdstat or the ZFS state could not be read (absent
	// files are not errors: no md driver, no ZFS).
	Message string    `json:"message,omitempty"`
	MD      []MDArray `json:"md"`
	ZFS     []ZFSPool `json:"zfs"`
}

// MDArray is one Linux software RAID array (/proc/mdstat).
type MDArray struct {
	Name string `json:"name"`
	// Level is raid0, raid1, raid4, raid5, raid6, raid10, linear, ...
	// (empty for an inactive array).
	Level    string `json:"level,omitempty"`
	State    string `json:"state"`
	ReadOnly bool   `json:"readOnly,omitempty"`
	// Devices and Active are [n/m]: the array's slots and the working
	// ones (both 0 for levels without redundancy).
	Devices   int        `json:"devices,omitempty"`
	Active    int        `json:"active,omitempty"`
	SizeBytes int64      `json:"sizeBytes,omitempty"`
	Members   []MDMember `json:"members"`
	// Action is the running (or pending) sync: recovery, resync, reshape,
	// check or repair. Progress is 0..100; FinishSeconds and
	// SpeedBytesPerSecond the kernel's estimate.
	Action              string   `json:"action,omitempty"`
	Pending             bool     `json:"pending,omitempty"`
	Progress            *float64 `json:"progress,omitempty"`
	FinishSeconds       *int64   `json:"finishSeconds,omitempty"`
	SpeedBytesPerSecond *int64   `json:"speedBytesPerSecond,omitempty"`
}

// MDMember is one member device of an md array.
type MDMember struct {
	Name        string `json:"name"`
	Slot        int    `json:"slot"`
	State       string `json:"state"`
	WriteMostly bool   `json:"writeMostly,omitempty"`
}

// ZFSPool is one ZFS pool's state.
type ZFSPool struct {
	Name string `json:"name"`
	// Health is the pool's own value (ONLINE, DEGRADED, ...); State its
	// RAID state (healthy, degraded, failed, inactive).
	Health string `json:"health"`
	State  string `json:"state"`
}

// Validate bounds a host.health answer (the manager calls it before
// keeping anything).
func (o HostHealthOutput) Validate() error {
	if o.SampledAt.IsZero() || o.RAID.ReadAt.IsZero() {
		return invalid("host.health output needs sampledAt and raid.readAt")
	}
	if err := o.SMART.validate(); err != nil {
		return err
	}
	return o.RAID.validate()
}

func (r SMARTReport) validate() error {
	switch r.Status {
	case SMARTOK, SMARTDisabled, SMARTNoAccess, SMARTNotInstalled, SMARTError:
	default:
		return invalid("host.health smart status %q unknown", r.Status)
	}
	if !healthText(r.Message, maxHealthMessage) || len(r.Devices) > MaxHealthDevices {
		return invalid("host.health smart report out of range")
	}
	for _, d := range r.Devices {
		if err := d.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (d SMARTDevice) validate() error {
	if d.Name == "" || !healthText(d.Name, maxHealthText) || !healthText(d.Type, 64) || !healthText(d.Model, maxHealthText) ||
		!healthText(d.Serial, maxHealthText) || !healthText(d.Firmware, maxHealthText) {
		return invalid("smart device text out of range")
	}
	switch d.Protocol {
	case "", DiskATA, DiskNVMe, DiskSCSI:
	default:
		return invalid("smart device protocol %q unknown", d.Protocol)
	}
	switch d.State {
	case DiskOK, DiskWarning, DiskFailing, DiskSleeping:
		if d.ErrorCode != "" {
			return invalid("smart device error code without state error")
		}
	case DiskError:
		switch d.ErrorCode {
		case DiskErrPermissionDenied, DiskErrOpenFailed, DiskErrUnsupported:
		default:
			return invalid("smart device error code %q unknown", d.ErrorCode)
		}
	default:
		return invalid("smart device state %q unknown", d.State)
	}
	if d.CapacityBytes < 0 || !intIn(d.RotationRPM, 0, 1_000_000) || !intIn(d.TemperatureC, -273, 1000) ||
		!intIn(d.CriticalWarning, 0, 255) || !intIn(d.AvailableSpare, 0, 255) || !intIn(d.AvailableSpareThreshold, 0, 255) ||
		!intIn(d.PercentageUsed, 0, 255) || !nonNegative(d.PowerOnHours, d.Reallocated, d.ReportedUncorrectable, d.Pending,
		d.OfflineUncorrectable, d.MediaErrors, d.GrownDefects, d.UncorrectedErrors) {
		return invalid("smart device value out of range")
	}
	if len(d.FailingAttributes) > MaxFailingAttributes {
		return invalid("smart device lists too many failing attributes")
	}
	for _, a := range d.FailingAttributes {
		if a.ID < 0 || a.ID > 255 || !healthText(a.Name, 64) || (a.WhenFailed != "now" && a.WhenFailed != "past") {
			return invalid("smart attribute out of range")
		}
	}
	return nil
}

func (r RAIDReport) validate() error {
	if !healthText(r.Message, maxHealthMessage) || len(r.MD) > MaxHealthArrays || len(r.ZFS) > MaxHealthArrays {
		return invalid("host.health raid report out of range")
	}
	for _, a := range r.MD {
		if a.Name == "" || !healthText(a.Name, 64) || !healthText(a.Level, 32) || !raidState(a.State) ||
			a.Devices < 0 || a.Active < 0 || a.Active > a.Devices || a.Devices > MaxHealthMembers*4 || a.SizeBytes < 0 ||
			len(a.Members) > MaxHealthMembers || !finite(a.Progress, 0, 100) || !nonNegative(a.FinishSeconds, a.SpeedBytesPerSecond) {
			return invalid("md array out of range")
		}
		switch a.Action {
		case "", MDRecovery, MDResync, MDReshape, MDCheck, MDRepair:
		default:
			return invalid("md array action %q unknown", a.Action)
		}
		for _, m := range a.Members {
			if m.Name == "" || !healthText(m.Name, 64) || m.Slot < 0 {
				return invalid("md member out of range")
			}
			switch m.State {
			case MemberActive, MemberSpare, MemberFailed, MemberReplacement, MemberJournal:
			default:
				return invalid("md member state %q unknown", m.State)
			}
		}
	}
	for _, p := range r.ZFS {
		if p.Name == "" || !healthText(p.Name, maxHealthText) || !slices.Contains(zfsHealth, p.Health) || !raidState(p.State) {
			return invalid("zfs pool out of range")
		}
	}
	return nil
}

func raidState(s string) bool {
	switch s {
	case RAIDHealthy, RAIDDegraded, RAIDRebuilding, RAIDChecking, RAIDFailed, RAIDInactive:
		return true
	}
	return false
}

func healthText(s string, n int) bool { return len(s) <= n && utf8.ValidString(s) }

func intIn(p *int, lo, hi int) bool { return p == nil || (*p >= lo && *p <= hi) }

// Worn is the percentage-used value from which a device counts as worn
// out (state warning).
const Worn = 90

// DeriveDiskState derives a read device's state from its values:
// failing when the drive reports a failed self-assessment, an attribute
// at or below its threshold now, or an NVMe critical warning; warning
// for reallocated, pending or uncorrectable sectors, NVMe media errors,
// SCSI grown defects or uncorrected errors, wear of Worn percent or
// more, spare below its threshold or an attribute that failed in the
// past; else ok.
func DeriveDiskState(d SMARTDevice) string {
	if d.Passed != nil && !*d.Passed {
		return DiskFailing
	}
	if d.CriticalWarning != nil && *d.CriticalWarning != 0 {
		return DiskFailing
	}
	past := false
	for _, a := range d.FailingAttributes {
		if a.WhenFailed == "now" {
			return DiskFailing
		}
		past = true
	}
	positive := func(ps ...*int64) bool {
		for _, p := range ps {
			if p != nil && *p > 0 {
				return true
			}
		}
		return false
	}
	switch {
	case past,
		positive(d.Reallocated, d.ReportedUncorrectable, d.Pending, d.OfflineUncorrectable, d.MediaErrors, d.GrownDefects, d.UncorrectedErrors),
		d.PercentageUsed != nil && *d.PercentageUsed >= Worn,
		d.AvailableSpare != nil && d.AvailableSpareThreshold != nil && *d.AvailableSpare < *d.AvailableSpareThreshold:
		return DiskWarning
	}
	return DiskOK
}
