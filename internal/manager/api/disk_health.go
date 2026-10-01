package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/observe"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Disk health (#143): the SMART state of an environment's disks and its
// RAID arrays, part of the environment's system information, plus the
// "Check disks now" / "Check RAID now" route. Serial numbers are shown
// with environment.system.read (like host names), never logged.

// Disk health statuses beyond the agent's SMART statuses.
const (
	// DiskHealthAgentOutdated: the environment's agent predates disk
	// health (its capabilities lack host.health).
	DiskHealthAgentOutdated = "agent_outdated"
	// DiskHealthUnknown: no report yet (the agent has not answered since
	// it connected).
	DiskHealthUnknown = "unknown"
)

// DiskHealth is the SMART state of an environment's disks.
type DiskHealth struct {
	Status string `json:"status" enum:"ok,disabled,no_access,not_installed,error,agent_outdated,unknown" doc:"ok: devices were read (the list may be empty: no disk reports SMART data, e.g. virtual disks); disabled: turned off on the agent; no_access: the agent cannot open the host's disks (run it privileged); not_installed: the agent image has no smartctl; error: the disk scan failed; agent_outdated: the agent predates disk health; unknown: no report yet."`
	// Checking: a requested read of every disk is running.
	Checking   bool         `json:"checking" doc:"A fresh read of every disk is running (Check disks now); the result follows as an inventory event."`
	CheckedAt  *time.Time   `json:"checkedAt,omitempty" doc:"When the agent last finished reading every disk."`
	ReceivedAt *time.Time   `json:"receivedAt,omitempty" doc:"When the manager received the report."`
	Devices    []DiskDevice `json:"devices"`
}

// DiskDevice is one disk's SMART data; absent values are not reported by
// the disk.
type DiskDevice struct {
	Name           string `json:"name" example:"/dev/sda"`
	Type           string `json:"type" example:"sat" doc:"smartctl device type."`
	Protocol       string `json:"protocol,omitempty" enum:"ata,nvme,scsi"`
	Model          string `json:"model,omitempty" example:"WDC WD40EFZX-68AWUN0"`
	Serial         string `json:"serial,omitempty"`
	Firmware       string `json:"firmware,omitempty"`
	CapacityBytes  int64  `json:"capacityBytes,omitempty"`
	RotationRPM    *int   `json:"rotationRpm,omitempty" doc:"Spindle speed; 0 for a solid-state disk."`
	SMARTSupported bool   `json:"smartSupported"`
	Passed         *bool  `json:"passed,omitempty" doc:"The disk's overall self-assessment."`
	TemperatureC   *int   `json:"temperatureC,omitempty"`
	PowerOnHours   *int64 `json:"powerOnHours,omitempty"`
	// ATA.
	ReallocatedSectors    *int64          `json:"reallocatedSectors,omitempty"`
	ReportedUncorrectable *int64          `json:"reportedUncorrectable,omitempty"`
	PendingSectors        *int64          `json:"pendingSectors,omitempty"`
	OfflineUncorrectable  *int64          `json:"offlineUncorrectable,omitempty"`
	FailingAttributes     []DiskAttribute `json:"failingAttributes,omitempty" doc:"ATA attributes at or below their threshold now or in the past."`
	// NVMe.
	CriticalWarning         *int   `json:"criticalWarning,omitempty"`
	AvailableSpare          *int   `json:"availableSpare,omitempty" doc:"Percent."`
	AvailableSpareThreshold *int   `json:"availableSpareThreshold,omitempty" doc:"Percent."`
	MediaErrors             *int64 `json:"mediaErrors,omitempty"`
	PercentageUsed          *int   `json:"percentageUsed,omitempty" doc:"Wear estimate in percent (may exceed 100)."`
	// SCSI.
	GrownDefects      *int64     `json:"grownDefects,omitempty"`
	UncorrectedErrors *int64     `json:"uncorrectedErrors,omitempty"`
	State             string     `json:"state" enum:"ok,warning,failing,sleeping,error" doc:"sleeping: in standby, not woken (the values are the previous read's); error: see errorCode."`
	ErrorCode         string     `json:"errorCode,omitempty" enum:"permission_denied,open_failed,unsupported"`
	ReadAt            *time.Time `json:"readAt,omitempty" doc:"When the values were read."`
}

// DiskAttribute is an ATA attribute at or below its threshold.
type DiskAttribute struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	WhenFailed string `json:"whenFailed" enum:"now,past"`
}

// RAIDHealth is the state of an environment's RAID arrays: Linux software
// RAID (md) and ZFS pools.
type RAIDHealth struct {
	Status  string      `json:"status" enum:"ok,error,agent_outdated,unknown" doc:"error: the state could not be read (message)."`
	Message string      `json:"message,omitempty"`
	ReadAt  *time.Time  `json:"readAt,omitempty" doc:"When the agent read the state."`
	Arrays  []RAIDArray `json:"arrays"`
}

// RAIDArray is one md array or ZFS pool.
type RAIDArray struct {
	Kind  string `json:"kind" enum:"md,zfs"`
	Name  string `json:"name" example:"md0"`
	Level string `json:"level,omitempty" example:"raid1" doc:"md level (absent for inactive arrays and ZFS pools)."`
	State string `json:"state" enum:"healthy,degraded,rebuilding,checking,failed,inactive"`
	// Health is the ZFS pool's own value.
	Health   string `json:"health,omitempty" enum:"ONLINE,DEGRADED,FAULTED,OFFLINE,UNAVAIL,REMOVED,SUSPENDED" doc:"ZFS pool health."`
	ReadOnly bool   `json:"readOnly,omitempty"`
	// Devices and Active are md's [n/m].
	Devices   int          `json:"devices,omitempty"`
	Active    int          `json:"active,omitempty"`
	SizeBytes int64        `json:"sizeBytes,omitempty"`
	Members   []RAIDMember `json:"members"`
	// A running or pending sync.
	Action              string   `json:"action,omitempty" enum:"recovery,resync,reshape,check,repair"`
	Pending             bool     `json:"pending,omitempty" doc:"The action waits (DELAYED or PENDING)."`
	Progress            *float64 `json:"progress,omitempty" doc:"Percent done."`
	FinishSeconds       *int64   `json:"finishSeconds,omitempty"`
	SpeedBytesPerSecond *int64   `json:"speedBytesPerSecond,omitempty"`
}

// RAIDMember is one member device of an md array.
type RAIDMember struct {
	Name        string `json:"name" example:"sda1"`
	Slot        int    `json:"slot"`
	State       string `json:"state" enum:"active,spare,failed,replacement,journal"`
	WriteMostly bool   `json:"writeMostly,omitempty"`
}

// healthServed reports whether an agent advertised host.health; unknown
// (false, false) before its first capabilities report.
func healthServed(sys EnvironmentSystem) (served, known bool) {
	if sys.Agent == nil || sys.ReportedAt == nil {
		return false, false
	}
	return slices.Contains(sys.Requests, protocol.ReqHostHealth), true
}

// addHealth fills in the disk health and RAID sections of the system
// information.
func addHealth(out *EnvironmentSystem, obs ObserveService, envID string) {
	h, ok := obs.HostHealth(envID)
	served, known := healthServed(*out)
	var hp *observe.HostHealth
	if ok {
		hp = &h
	}
	d, r := healthDTOs(hp, served, known)
	out.DiskHealth, out.RAID = &d, &r
}

// healthDTOs converts a stored report (nil: none yet).
func healthDTOs(h *observe.HostHealth, served, known bool) (DiskHealth, RAIDHealth) {
	d := DiskHealth{Status: DiskHealthUnknown, Devices: []DiskDevice{}}
	r := RAIDHealth{Status: DiskHealthUnknown, Arrays: []RAIDArray{}}
	if known && !served {
		d.Status, r.Status = DiskHealthAgentOutdated, DiskHealthAgentOutdated
		return d, r
	}
	if h == nil {
		return d, r
	}
	s := h.SMART
	recv := h.ReceivedAt
	d.Status, d.Checking, d.CheckedAt, d.ReceivedAt = s.Status, s.Checking, s.CheckedAt, &recv
	for _, dev := range s.Devices {
		d.Devices = append(d.Devices, diskDevice(dev))
	}
	readAt := h.RAID.ReadAt
	r.Status, r.ReadAt, r.Message = "ok", &readAt, h.RAID.Message
	if h.RAID.Message != "" {
		r.Status = "error"
	}
	for _, a := range h.RAID.MD {
		arr := RAIDArray{Kind: "md", Name: a.Name, Level: a.Level, State: a.State, ReadOnly: a.ReadOnly, Devices: a.Devices, Active: a.Active,
			SizeBytes: a.SizeBytes, Members: []RAIDMember{}, Action: a.Action, Pending: a.Pending, Progress: a.Progress,
			FinishSeconds: a.FinishSeconds, SpeedBytesPerSecond: a.SpeedBytesPerSecond}
		for _, m := range a.Members {
			arr.Members = append(arr.Members, RAIDMember{Name: m.Name, Slot: m.Slot, State: m.State, WriteMostly: m.WriteMostly})
		}
		r.Arrays = append(r.Arrays, arr)
	}
	for _, p := range h.RAID.ZFS {
		r.Arrays = append(r.Arrays, RAIDArray{Kind: "zfs", Name: p.Name, State: p.State, Health: p.Health, Members: []RAIDMember{}})
	}
	return d, r
}

func diskDevice(d protocol.SMARTDevice) DiskDevice {
	out := DiskDevice{Name: d.Name, Type: d.Type, Protocol: d.Protocol, Model: d.Model, Serial: d.Serial, Firmware: d.Firmware,
		CapacityBytes: d.CapacityBytes, RotationRPM: d.RotationRPM, SMARTSupported: d.SMARTSupported, Passed: d.Passed,
		TemperatureC: d.TemperatureC, PowerOnHours: d.PowerOnHours, ReallocatedSectors: d.Reallocated,
		ReportedUncorrectable: d.ReportedUncorrectable, PendingSectors: d.Pending, OfflineUncorrectable: d.OfflineUncorrectable,
		CriticalWarning: d.CriticalWarning, AvailableSpare: d.AvailableSpare, AvailableSpareThreshold: d.AvailableSpareThreshold,
		MediaErrors: d.MediaErrors, PercentageUsed: d.PercentageUsed, GrownDefects: d.GrownDefects, UncorrectedErrors: d.UncorrectedErrors,
		State: d.State, ErrorCode: d.ErrorCode, ReadAt: d.ReadAt}
	for _, a := range d.FailingAttributes {
		out.FailingAttributes = append(out.FailingAttributes, DiskAttribute{ID: a.ID, Name: a.Name, WhenFailed: a.WhenFailed})
	}
	return out
}

// DiskHealthCheck answers a check: the fresh disk health and RAID state.
type DiskHealthCheck struct {
	EnvironmentID string     `json:"environmentId"`
	Scope         string     `json:"scope" enum:"smart,raid" example:"smart"`
	DiskHealth    DiskHealth `json:"diskHealth"`
	RAID          RAIDHealth `json:"raid"`
}

type diskHealthCheckInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	Body          struct {
		Scope string `json:"scope" enum:"smart,raid" example:"smart" doc:"smart: read every disk's SMART data now (never a self-test; a disk in standby is not woken); raid: read the RAID state now (never a scrub)."`
	}
}

type diskHealthCheckOutput struct{ Body DiskHealthCheck }

func (h *agentsAPI) checkDiskHealth(ctx context.Context, in *diskHealthCheckInput) (*diskHealthCheckOutput, error) {
	_, env, v, err := h.visibleEnvironment(ctx, in.EnvironmentID)
	if err != nil {
		return nil, err
	}
	if !v.Has(string(CapEnvironmentSystemRead)) {
		return nil, Forbidden("not permitted to read this environment's system information")
	}
	if env.Status == domain.EnvironmentArchived {
		return nil, Conflict(CodeEnvironmentArchived, "the environment is archived; re-attach it to operate it")
	}
	obs := h.deps.Observe
	if obs == nil {
		return nil, Unavailable(CodeUnavailable, "disk health is not available on this manager")
	}
	audit.SetDetail(ctx, "scope", in.Body.Scope)
	rep, err := obs.CheckHealth(ctx, env.ID, in.Body.Scope)
	if err != nil {
		return nil, healthCheckErr(err, env.Name)
	}
	d, r := healthDTOs(&rep, true, true)
	return &diskHealthCheckOutput{Body: DiskHealthCheck{EnvironmentID: env.ID, Scope: in.Body.Scope, DiskHealth: d, RAID: r}}, nil
}

// healthCheckErr maps the observe errors of a check.
func healthCheckErr(err error, envName string) error {
	var rl *observe.HealthRateLimitError
	switch {
	case errors.As(err, &rl):
		secs := int(math.Ceil(rl.RetryAfter.Seconds()))
		return RateLimited("checked moments ago; try again in "+strconv.Itoa(secs)+" s").WithHeader("Retry-After", strconv.Itoa(secs))
	case errors.Is(err, observe.ErrHealthScope):
		return Invalid("unknown scope", Field("body.scope", "must be smart or raid"))
	case errors.Is(err, observe.ErrHealthOffline):
		return NewError(http.StatusServiceUnavailable, CodeEnvironmentOffline, envName+" is offline; check again when it is back")
	case errors.Is(err, observe.ErrHealthUnsupported):
		return NewError(http.StatusNotImplemented, CodeAgentUnsupported, "the agent on "+envName+" is older than disk health; update it")
	case errors.Is(err, observe.ErrHealthTimeout):
		return NewError(http.StatusGatewayTimeout, CodeTimeout, "the agent on "+envName+" did not answer in time")
	}
	return Internal(err)
}

func registerDiskHealth(a huma.API, h *agentsAPI) {
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-environment-disk-health-check", Method: http.MethodPost,
			Path:    BasePath + "/environments/{environmentId}/disk-health/checks",
			Summary: "Check an environment's disks or RAID now",
			Description: "Asks the agent for a fresh read (#143): scope smart reads every disk's SMART data now (never a self-test; a " +
				"disk in standby is not woken), scope raid the md arrays and ZFS pools. Answers the fresh disk health and RAID state; a " +
				"SMART read that takes longer answers with diskHealth.checking and the result follows as an inventory event. Nothing " +
				"changes on the host. At most one check per environment and scope every 30 s (smart) or 5 s (raid): 429 with " +
				"Retry-After before; a check the agent did not answer (timeout, offline) does not count. 503 environment_offline, 501 agent_unsupported for an agent that predates disk health.",
			Tags: []string{tagEnvironments},
			Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity,
				http.StatusTooManyRequests, http.StatusNotImplemented, http.StatusServiceUnavailable, http.StatusGatewayTimeout},
		},
		Capability: CapEnvironmentSystemRead, Scope: ScopeEnvironment, AuditAction: "environment.disk_health.check",
	}, h.checkDiskHealth)
}
