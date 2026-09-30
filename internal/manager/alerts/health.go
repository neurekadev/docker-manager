package alerts

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/humanize"
	"github.com/neurekadev/docker-manager/internal/manager/observe"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Disk health and RAID alerts (#143 reports): one alert per disk (its
// path and smartctl type) and per md array or ZFS pool of an environment.
//
//   - A disk is critical while failing (SMART self-assessment failed, an
//     attribute failing now, an NVMe critical warning), a warning while in
//     state warning (bad sectors, wear, low spare, an attribute that
//     failed in the past) or when it can't be read; a sleeping disk never
//     alerts (and keeps an alert it has). It resolves when the disk is ok
//     again.
//   - An md array is critical when failed or inactive, a warning while
//     degraded or rebuilding (progress only updates the alert); a ZFS pool
//     is a warning when DEGRADED, critical when FAULTED, UNAVAIL,
//     SUSPENDED or REMOVED. They resolve when healthy (ONLINE).
//   - A disk or array no longer reported for DiskRemovedAfter resolves as
//     removed (no message).

func diskKey(env, name, typ string) string { return "disk_health/" + env + "/" + name + "/" + typ }

func raidKey(env, kind, name string) string { return "raid/" + env + "/" + kind + "/" + name }

// evaluateAllHealth evaluates the disk and RAID alerts of every active
// environment.
func (s *Service) evaluateAllHealth(ctx context.Context) error {
	if s.opts.Health == nil {
		return nil
	}
	envs, err := store.ListEnvironments(ctx, s.db, domain.EnvironmentFilter{Statuses: []domain.EnvironmentStatus{domain.EnvironmentActive}})
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range envs {
		if err := s.EvaluateHealth(ctx, e.ID); err != nil {
			errs = append(errs, err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return errors.Join(errs...)
}

// EvaluateHealth raises, updates and resolves the disk and RAID alerts of
// one environment from its last host health report.
func (s *Service) EvaluateHealth(ctx context.Context, environmentID string) error {
	if s.opts.Health == nil {
		return nil
	}
	h, ok := s.opts.Health.HostHealth(environmentID)
	now := s.now()
	return s.inTx(ctx, func(ctx context.Context, tx bun.Tx) ([]domain.Alert, error) {
		e, err := store.GetEnvironment(ctx, tx, environmentID)
		if errors.Is(err, domain.ErrEnvironmentNotFound) {
			return nil, nil
		}
		if err != nil || e.Status != domain.EnvironmentActive {
			return nil, err
		}
		var report *observe.HostHealth
		if ok {
			report = &h
		}
		return evaluateHealth(ctx, tx, e, report, now)
	})
}

// evaluateHealth applies one report (nil: none yet) to the environment's
// disk and RAID alerts.
func evaluateHealth(ctx context.Context, db bun.IDB, e domain.Environment, h *observe.HostHealth, now time.Time) ([]domain.Alert, error) {
	firing := map[string]domain.Alert{}
	for _, kind := range []domain.NotificationEventKind{domain.NotifyDiskHealth, domain.NotifyRAID} {
		as, err := store.FiringAlerts(ctx, db, kind, e.ID)
		if err != nil {
			return nil, err
		}
		for _, a := range as {
			firing[a.DedupeKey] = a
		}
	}
	seen := map[string]bool{}
	var touch []string
	var out []domain.Alert
	var err error
	if h != nil && h.SMART.Status == protocol.SMARTOK {
		for _, d := range h.SMART.Devices {
			key := diskKey(e.ID, d.Name, d.Type)
			seen[key] = true
			cur, isFiring := firing[key]
			switch d.State {
			case protocol.DiskOK:
				if isFiring {
					out, err = collect(out)(resolve(ctx, db, cur, domain.AlertResolvedFixed, now))
				}
			case protocol.DiskSleeping:
				// Not woken: the values are the previous read's. The alert
				// stays as it is.
				if isFiring {
					touch = append(touch, cur.ID)
				}
			case protocol.DiskError:
				// A disk that can't be read keeps a stronger alert it has.
				if isFiring && cur.Severity.Rank() > domain.AlertWarning.Rank() {
					touch = append(touch, cur.ID)
					continue
				}
				out, err = collect(out)(raise(ctx, db, diskObservation(e, d, key), now))
			case protocol.DiskWarning, protocol.DiskFailing:
				out, err = collect(out)(raise(ctx, db, diskObservation(e, d, key), now))
			}
			if err != nil {
				return nil, err
			}
		}
	}
	if h != nil {
		for _, a := range h.RAID.MD {
			key := raidKey(e.ID, "md", a.Name)
			seen[key] = true
			o, problem := mdObservation(e, a, key)
			out, err = raidApply(ctx, db, out, firing[key], problem, o, now)
			if err != nil {
				return nil, err
			}
		}
		for _, p := range h.RAID.ZFS {
			key := raidKey(e.ID, "zfs", p.Name)
			seen[key] = true
			o, problem := zfsObservation(e, p, key)
			out, err = raidApply(ctx, db, out, firing[key], problem, o, now)
			if err != nil {
				return nil, err
			}
		}
	}
	// Disks and arrays no longer reported: removed after a day.
	for key, a := range firing {
		if seen[key] || now.Sub(a.LastSeenAt) < DiskRemovedAfter {
			continue
		}
		if out, err = collect(out)(resolve(ctx, db, a, domain.AlertResolvedRemoved, now)); err != nil {
			return nil, err
		}
	}
	return out, store.TouchAlerts(ctx, db, touch, now)
}

// raidApply raises a problem array's alert or resolves a healthy one's.
func raidApply(ctx context.Context, db bun.IDB, out []domain.Alert, cur domain.Alert, problem bool, o Observation, now time.Time) ([]domain.Alert, error) {
	if problem {
		return collect(out)(raise(ctx, db, o, now))
	}
	if cur.ID != "" {
		return collect(out)(resolve(ctx, db, cur, domain.AlertResolvedFixed, now))
	}
	return out, nil
}

// collect returns a function appending a changed alert to out.
func collect(out []domain.Alert) func(*domain.Alert, error) ([]domain.Alert, error) {
	return func(a *domain.Alert, err error) ([]domain.Alert, error) {
		if err != nil {
			return out, err
		}
		if a != nil {
			out = append(out, *a)
		}
		return out, nil
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// diskObservation describes a disk in state warning, failing or error.
// Never its serial number.
func diskObservation(e domain.Environment, d protocol.SMARTDevice, key string) Observation {
	facts := map[string]string{"device": d.Name, "deviceType": d.Type, "state": d.State}
	if d.Model != "" {
		facts["model"] = d.Model
	}
	if d.ErrorCode != "" {
		facts["errorCode"] = d.ErrorCode
	}
	var tokens []string
	count := func(name, token string, v *int64) {
		if v != nil && *v > 0 {
			facts[name] = itoa(*v)
			tokens = append(tokens, token)
		}
	}
	if d.Passed != nil && !*d.Passed {
		facts["selfAssessment"] = "failed"
		tokens = append(tokens, "self_assessment_failed")
	}
	if d.CriticalWarning != nil && *d.CriticalWarning != 0 {
		facts["criticalWarning"] = strconv.Itoa(*d.CriticalWarning)
		tokens = append(tokens, "critical_warning")
	}
	var attrs []string
	for _, a := range d.FailingAttributes {
		attrs = append(attrs, fmt.Sprintf("%d %s (%s)", a.ID, a.Name, a.WhenFailed))
		// One token per attribute: failing now rather than in the past
		// raises the severity; the reverse must not look like a new
		// problem.
		tokens = append(tokens, fmt.Sprintf("attribute_%d", a.ID))
	}
	if len(attrs) > 0 {
		facts["failingAttributes"] = strings.Join(attrs, ", ")
	}
	count("reallocatedSectors", "reallocated", d.Reallocated)
	count("pendingSectors", "pending", d.Pending)
	count("reportedUncorrectable", "uncorrectable", d.ReportedUncorrectable)
	count("offlineUncorrectable", "uncorrectable", d.OfflineUncorrectable)
	count("mediaErrors", "media_errors", d.MediaErrors)
	count("grownDefects", "grown_defects", d.GrownDefects)
	count("uncorrectedErrors", "uncorrectable", d.UncorrectedErrors)
	if d.PercentageUsed != nil && *d.PercentageUsed >= protocol.Worn {
		facts["percentageUsed"] = strconv.Itoa(*d.PercentageUsed)
		tokens = append(tokens, "worn")
	}
	if d.AvailableSpare != nil && d.AvailableSpareThreshold != nil && *d.AvailableSpare < *d.AvailableSpareThreshold {
		facts["availableSpare"] = strconv.Itoa(*d.AvailableSpare)
		facts["availableSpareThreshold"] = strconv.Itoa(*d.AvailableSpareThreshold)
		tokens = append(tokens, "spare_low")
	}
	sev, what := domain.AlertWarning, "needs attention"
	switch d.State {
	case protocol.DiskFailing:
		sev, what = domain.AlertCritical, "is failing"
	case protocol.DiskError:
		what = "can't be read"
		tokens = append(tokens, "unreadable")
	}
	return Observation{
		Key: key, Kind: domain.NotifyDiskHealth, Severity: sev, EnvironmentID: e.ID, ResourceType: domain.AlertResourceDisk,
		ResourceID: d.Name, Title: fmt.Sprintf("Disk %s on %s %s", d.Name, e.Name, what), Facts: facts,
		Fingerprint: domain.Fingerprint(tokens...),
	}
}

// mdObservation describes an md array; problem is false while it is
// healthy (or runs a check).
func mdObservation(e domain.Environment, a protocol.MDArray, key string) (Observation, bool) {
	facts := map[string]string{"array": a.Name, "arrayKind": "md", "state": a.State}
	if a.Level != "" {
		facts["level"] = a.Level
	}
	if a.Devices > 0 {
		facts["devices"] = strconv.Itoa(a.Devices)
		facts["active"] = strconv.Itoa(a.Active)
	}
	if a.Action != "" {
		facts["action"] = a.Action
	}
	if a.Progress != nil {
		facts["progress"] = humanize.Decimal(*a.Progress)
	}
	if a.FinishSeconds != nil {
		facts["finishSeconds"] = itoa(*a.FinishSeconds)
	}
	var tokens, failed []string
	for _, m := range a.Members {
		if m.State == protocol.MemberFailed {
			failed = append(failed, m.Name)
			tokens = append(tokens, "failed_member_"+m.Name)
		}
	}
	if len(failed) > 0 {
		facts["failedMembers"] = strings.Join(failed, ", ")
	}
	// "At least k disks missing" for every k up to the count: another
	// missing disk adds a token, a disk coming back removes one (never a
	// new token for an improvement).
	for k := 1; k <= a.Devices-a.Active; k++ {
		tokens = append(tokens, "missing_at_least_"+strconv.Itoa(k))
	}
	var sev domain.AlertSeverity
	var what string
	switch a.State {
	case protocol.RAIDFailed:
		sev, what = domain.AlertCritical, "has failed"
	case protocol.RAIDInactive:
		sev, what = domain.AlertCritical, "is inactive"
	case protocol.RAIDDegraded:
		sev, what = domain.AlertWarning, "is degraded"
	case protocol.RAIDRebuilding:
		sev, what = domain.AlertWarning, "is rebuilding"
	default:
		return Observation{}, false
	}
	return Observation{
		Key: key, Kind: domain.NotifyRAID, Severity: sev, EnvironmentID: e.ID, ResourceType: domain.AlertResourceRAID, ResourceID: a.Name,
		Title: fmt.Sprintf("RAID %s on %s %s", a.Name, e.Name, what), Facts: facts, Fingerprint: domain.Fingerprint(tokens...),
	}, true
}

// zfsObservation describes a ZFS pool; problem is false while it is
// ONLINE.
func zfsObservation(e domain.Environment, p protocol.ZFSPool, key string) (Observation, bool) {
	var sev domain.AlertSeverity
	var what string
	switch p.Health {
	case "ONLINE":
		return Observation{}, false
	case "DEGRADED":
		sev, what = domain.AlertWarning, "is degraded"
	case "FAULTED":
		sev, what = domain.AlertCritical, "is faulted"
	case "UNAVAIL":
		sev, what = domain.AlertCritical, "is unavailable"
	case "SUSPENDED":
		sev, what = domain.AlertCritical, "is suspended"
	case "REMOVED":
		sev, what = domain.AlertCritical, "was removed"
	default:
		// Other values follow the agent's RAID state.
		switch p.State {
		case protocol.RAIDHealthy, protocol.RAIDChecking:
			return Observation{}, false
		case protocol.RAIDDegraded, protocol.RAIDRebuilding:
			sev, what = domain.AlertWarning, "is degraded"
		default:
			sev, what = domain.AlertCritical, "is offline"
		}
	}
	return Observation{
		Key: key, Kind: domain.NotifyRAID, Severity: sev, EnvironmentID: e.ID, ResourceType: domain.AlertResourceZFS, ResourceID: p.Name,
		Title: fmt.Sprintf("ZFS pool %s on %s %s", p.Name, e.Name, what),
		Facts: map[string]string{"pool": p.Name, "arrayKind": "zfs", "health": p.Health, "state": p.State},
		// The pool's health replaces itself: the severity says whether it
		// got worse.
		Fingerprint: domain.Fingerprint("unhealthy"),
	}, true
}
