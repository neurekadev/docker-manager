package smartctl

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/neurekadev/docker-manager/internal/protocol"
)

// ScanDevice is one device of smartctl --scan-open.
type ScanDevice struct {
	// Name is the device path (/dev/sda), Type the device type smartctl
	// detected (sat, nvme, scsi, ...), Protocol ATA, NVMe or SCSI.
	Name     string
	Type     string
	Protocol string
	// OpenError is set when smartctl could not open the device.
	OpenError string
}

// Reading is one device read.
type Reading struct {
	// Device holds the values and the derived state. For a device in
	// standby only its name, type and protocol are set.
	Device protocol.SMARTDevice
	// Standby: the device is in a low-power mode and was not read (not
	// woken); the caller keeps the previous values.
	Standby bool
}

type message struct {
	String   string `json:"string"`
	Severity string `json:"severity"`
}

type scanOutput struct {
	Smartctl struct {
		Messages []message `json:"messages"`
	} `json:"smartctl"`
	Devices []struct {
		Name      string `json:"name"`
		Type      string `json:"type"`
		Protocol  string `json:"protocol"`
		OpenError string `json:"open_error"`
	} `json:"devices"`
}

// parseScan reads --scan-open --json output. Devices whose name or type
// the runner would refuse are left out.
func parseScan(b []byte) ([]ScanDevice, error) {
	var out scanOutput
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, errors.New("unexpected scan output")
	}
	devs := make([]ScanDevice, 0, len(out.Devices))
	seen := map[string]bool{}
	for _, d := range out.Devices {
		if !validName(d.Name) || !validType(d.Type) || seen[d.Name] {
			continue
		}
		seen[d.Name] = true
		devs = append(devs, ScanDevice{Name: d.Name, Type: d.Type, Protocol: protocolOf(d.Protocol), OpenError: bound(d.OpenError, 512)})
	}
	return devs, nil
}

// protocolOf maps smartctl's protocol names (ATA, SCSI, NVMe, ATA+SCSI).
func protocolOf(p string) string {
	switch {
	case strings.Contains(p, "NVMe"):
		return protocol.DiskNVMe
	case strings.HasPrefix(p, "ATA"):
		return protocol.DiskATA
	case strings.Contains(p, "SCSI"):
		return protocol.DiskSCSI
	}
	return ""
}

// num is a JSON number that may exceed int64 (NVMe counters are 128-bit):
// larger values are clamped.
type num struct {
	set bool
	v   int64
}

func (n *num) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		return nil
	}
	s = strings.Trim(s, `"`)
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		*n = num{set: true, v: v}
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) {
		return nil // not a number: unknown
	}
	switch {
	case f >= math.MaxInt64:
		*n = num{set: true, v: math.MaxInt64}
	case f <= math.MinInt64:
		*n = num{set: true, v: math.MinInt64}
	default:
		*n = num{set: true, v: int64(f)}
	}
	return nil
}

func (n num) ptr() *int64 {
	if !n.set {
		return nil
	}
	v := n.v
	return &v
}

// intPtr returns the value as *int within [lo, hi] (nil when absent or
// out of range).
func (n num) intPtr(lo, hi int64) *int {
	if !n.set || n.v < lo || n.v > hi {
		return nil
	}
	v := int(n.v)
	return &v
}

func (n num) nonNegative() *int64 {
	if !n.set || n.v < 0 {
		return nil
	}
	return n.ptr()
}

type readOutput struct {
	Smartctl struct {
		Messages []message `json:"messages"`
	} `json:"smartctl"`
	Device *struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Protocol string `json:"protocol"`
	} `json:"device"`
	ModelName       string `json:"model_name"`
	SCSIModelName   string `json:"scsi_model_name"`
	SCSIVendor      string `json:"scsi_vendor"`
	SCSIProduct     string `json:"scsi_product"`
	SerialNumber    string `json:"serial_number"`
	FirmwareVersion string `json:"firmware_version"`
	SCSIRevision    string `json:"scsi_revision"`
	UserCapacity    struct {
		Bytes num `json:"bytes"`
	} `json:"user_capacity"`
	NVMeTotalCapacity num `json:"nvme_total_capacity"`
	RotationRate      num `json:"rotation_rate"`
	SmartSupport      *struct {
		Available *bool `json:"available"`
		Enabled   *bool `json:"enabled"`
	} `json:"smart_support"`
	SmartStatus *struct {
		Passed *bool `json:"passed"`
	} `json:"smart_status"`
	Temperature struct {
		Current num `json:"current"`
	} `json:"temperature"`
	PowerOnTime struct {
		Hours num `json:"hours"`
	} `json:"power_on_time"`
	PowerMode          json.RawMessage `json:"power_mode"`
	ATASmartAttributes *struct {
		Table []struct {
			ID         int    `json:"id"`
			Name       string `json:"name"`
			WhenFailed string `json:"when_failed"`
			Raw        struct {
				Value num `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	NVMeLog *struct {
		CriticalWarning         num `json:"critical_warning"`
		Temperature             num `json:"temperature"`
		AvailableSpare          num `json:"available_spare"`
		AvailableSpareThreshold num `json:"available_spare_threshold"`
		PercentageUsed          num `json:"percentage_used"`
		MediaErrors             num `json:"media_errors"`
		PowerOnHours            num `json:"power_on_hours"`
	} `json:"nvme_smart_health_information_log"`
	SCSIGrownDefects    num `json:"scsi_grown_defect_list"`
	SCSIErrorCounterLog *struct {
		Read   *scsiCounter `json:"read"`
		Write  *scsiCounter `json:"write"`
		Verify *scsiCounter `json:"verify"`
	} `json:"scsi_error_counter_log"`
	SCSIPercentageUsed num `json:"scsi_percentage_used_endurance_indicator"`
}

type scsiCounter struct {
	TotalUncorrected num `json:"total_uncorrected_errors"`
}

// parseRead turns one read's output into a Reading. Bits 2 to 7 of the
// exit status (a failing disk, error logs) still come with complete JSON:
// the state is derived from the values. Without device data the device
// is in state error with a code from smartctl's messages.
func parseRead(scan ScanDevice, b []byte, exit ExitBits, stderr string) Reading {
	base := protocol.SMARTDevice{Name: scan.Name, Type: scan.Type, Protocol: scan.Protocol}
	var out readOutput
	parsed := json.Unmarshal(b, &out) == nil
	msgs := messagesText(out.Smartctl.Messages) + "\n" + stderr
	if int(exit) == StandbyExit && (len(out.PowerMode) > 0 || standbyMessage(msgs)) {
		base.State = protocol.DiskSleeping
		return Reading{Device: base, Standby: true}
	}
	if !parsed || out.Device == nil {
		// Nothing was read: open failed, the command line was refused
		// (unknown USB bridge) or smartctl printed no JSON.
		base.State, base.ErrorCode = protocol.DiskError, classify(msgs, exit)
		return Reading{Device: base}
	}
	d := base
	if p := protocolOf(out.Device.Protocol); p != "" {
		d.Protocol = p
	}
	if out.Device.Type != "" && validType(out.Device.Type) {
		d.Type = out.Device.Type
	}
	d.Model = firstNonEmpty(out.ModelName, out.SCSIModelName, strings.TrimSpace(out.SCSIVendor+" "+out.SCSIProduct))
	d.Serial = bound(strings.TrimSpace(out.SerialNumber), 255)
	d.Firmware = bound(firstNonEmpty(out.FirmwareVersion, out.SCSIRevision), 255)
	d.Model = bound(d.Model, 255)
	if c := out.UserCapacity.Bytes.nonNegative(); c != nil {
		d.CapacityBytes = *c
	} else if c := out.NVMeTotalCapacity.nonNegative(); c != nil {
		d.CapacityBytes = *c
	}
	d.RotationRPM = out.RotationRate.intPtr(0, 1_000_000)
	if out.SmartStatus != nil && out.SmartStatus.Passed != nil {
		passed := *out.SmartStatus.Passed
		d.Passed = &passed
	}
	d.SMARTSupported = (out.SmartSupport != nil && out.SmartSupport.Available != nil && *out.SmartSupport.Available) ||
		d.Passed != nil || out.NVMeLog != nil
	d.TemperatureC = out.Temperature.Current.intPtr(-273, 1000)
	d.PowerOnHours = out.PowerOnTime.Hours.nonNegative()
	if a := out.ATASmartAttributes; a != nil {
		for _, attr := range a.Table {
			switch attr.ID {
			case 5:
				d.Reallocated = attr.Raw.Value.nonNegative()
			case 187:
				d.ReportedUncorrectable = attr.Raw.Value.nonNegative()
			case 197:
				d.Pending = attr.Raw.Value.nonNegative()
			case 198:
				d.OfflineUncorrectable = attr.Raw.Value.nonNegative()
			}
			if (attr.WhenFailed == "now" || attr.WhenFailed == "past") && len(d.FailingAttributes) < protocol.MaxFailingAttributes &&
				attr.ID >= 0 && attr.ID <= 255 {
				d.FailingAttributes = append(d.FailingAttributes, protocol.SMARTAttribute{ID: attr.ID, Name: bound(attr.Name, 64),
					WhenFailed: attr.WhenFailed})
			}
		}
	}
	if n := out.NVMeLog; n != nil {
		d.CriticalWarning = n.CriticalWarning.intPtr(0, 255)
		d.AvailableSpare = n.AvailableSpare.intPtr(0, 255)
		d.AvailableSpareThreshold = n.AvailableSpareThreshold.intPtr(0, 255)
		d.PercentageUsed = n.PercentageUsed.intPtr(0, 255)
		d.MediaErrors = n.MediaErrors.nonNegative()
		if d.TemperatureC == nil {
			d.TemperatureC = n.Temperature.intPtr(-273, 1000)
		}
		if d.PowerOnHours == nil {
			d.PowerOnHours = n.PowerOnHours.nonNegative()
		}
	}
	d.GrownDefects = out.SCSIGrownDefects.nonNegative()
	if l := out.SCSIErrorCounterLog; l != nil {
		var sum int64
		found := false
		for _, c := range []*scsiCounter{l.Read, l.Write, l.Verify} {
			if c == nil {
				continue
			}
			if v := c.TotalUncorrected.nonNegative(); v != nil {
				found = true
				if sum > math.MaxInt64-*v {
					sum = math.MaxInt64
				} else {
					sum += *v
				}
			}
		}
		if found {
			d.UncorrectedErrors = &sum
		}
	}
	if d.PercentageUsed == nil {
		d.PercentageUsed = out.SCSIPercentageUsed.intPtr(0, 255)
	}
	if !d.SMARTSupported {
		d.State, d.ErrorCode = protocol.DiskError, protocol.DiskErrUnsupported
		return Reading{Device: d}
	}
	d.State = protocol.DeriveDiskState(d)
	return Reading{Device: d}
}

// ScanOpenError is the state error code of a device the scan could not
// open.
func ScanOpenError(dev ScanDevice) string { return classify(dev.OpenError, BitOpenFailed) }

// classify names why a device could not be read.
func classify(msgs string, exit ExitBits) string {
	s := strings.ToLower(msgs)
	switch {
	case strings.Contains(s, "permission denied"), strings.Contains(s, "operation not permitted"):
		return protocol.DiskErrPermissionDenied
	case strings.Contains(s, "unknown usb bridge"), strings.Contains(s, "please specify device type"),
		strings.Contains(s, "unable to detect device type"), strings.Contains(s, "unsupported"),
		strings.Contains(s, "lacks smart capability"), strings.Contains(s, "not supported"):
		return protocol.DiskErrUnsupported
	case exit.Has(BitCommandLine) && !exit.Has(BitOpenFailed):
		return protocol.DiskErrUnsupported
	}
	return protocol.DiskErrOpenFailed
}

func standbyMessage(msgs string) bool {
	s := strings.ToLower(msgs)
	return strings.Contains(s, "device is in") && strings.Contains(s, "mode, exit(")
}

func messagesText(ms []message) string {
	parts := make([]string, 0, len(ms))
	for _, m := range ms {
		parts = append(parts, m.String)
	}
	return strings.Join(parts, "\n")
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// bound cuts s to at most n bytes of valid UTF-8 (a rune cut in half is
// dropped).
func bound(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	return strings.ToValidUTF8(s, "")
}
