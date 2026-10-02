package alerts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
// path and smartctl type) and per md array or ZFS pool of an environment,
// and one per environment and kind while that monitoring does not work.
//
//   - A disk is critical while failing (SMART self-assessment failed, an
//     attribute failing now, an NVMe critical warning about the drive), a
//     warning while in state warning (bad sectors, wear, low spare, an
//     attribute that failed in the past, an NVMe temperature warning) or
//     when it can't be read (open failed, timed out, SMART turned off, no
//     health data) or is missing (a scan no longer finds it); a sleeping
//     disk never alerts (and keeps an alert it has). A disk without SMART
//     data (unsupported: virtual disks, USB sticks) never alerts: an alert
//     it had ends as removed. It resolves when the disk is ok again.
//   - A disk alert follows its disk: the fact diskId (a hash of its serial
//     number, never the number) names it. When the disk shows up under
//     another path (sdX names move after a reboot or a hot-swap), the
//     alert moves there; when another disk holds its path and its own disk
//     is not reported, it ends as removed.
//   - An md array is critical when failed or inactive, a warning while
//     degraded or rebuilding (progress only updates the alert); a ZFS pool
//     is a warning when DEGRADED, critical when FAULTED, UNAVAIL,
//     SUSPENDED or REMOVED. They resolve when healthy (ONLINE).
//   - Monitoring (warnings): disk_health/<env>/monitoring while the SMART
//     status is error, no_access or not_installed, while the agent's last
//     read of every disk is older than twice its interval plus
//     SMARTStaleSlack, or while an online environment's last report is
//     older than HealthReportStale; raid/<env>/monitoring while the RAID
//     state can't be read (or the report is that old). They resolve when
//     it works again.
//   - A disk or array no longer reported for DiskRemovedAfter resolves as
//     removed (no message), but never while its monitoring does not work
//     or there is no report: what can't be seen is not gone.

func diskKey(env, name, typ string) string { return "disk_health/" + env + "/" + name + "/" + typ }

func raidKey(env, kind, name string) string { return "raid/" + env + "/" + kind + "/" + name }

// monitorKey is the dedupe key of an environment's monitoring alert of
// kind (disk paths start with "/", array kinds are md and zfs: no clash).
func monitorKey(env string, kind domain.NotificationEventKind) string {
	return string(kind) + "/" + env + "/monitoring"
}

// Staleness of host health reports.
const (
	// HealthReportStale: an environment online for this long whose last
	// host health report is older is not monitored (the manager asks
	// about once a minute).
	HealthReportStale = 15 * time.Minute
	// SMARTStaleSlack is added to twice the agent's read interval before
	// its last read of every disk counts as out of date (a round of many
	// disks takes a while).
	SMARTStaleSlack = 30 * time.Minute
	// maxIntervalSeconds bounds a read interval worth judging by (agents
	// allow up to a day): larger values are not trusted.
	maxIntervalSeconds = int64(7 * 24 * time.Hour / time.Second)
)

// Monitoring problems (the fact reason).
const (
	reasonScanFailed   = "scan_failed"
	reasonNoAccess     = "no_access"
	reasonNotInstalled = "not_installed"
	reasonStale        = "stale"
	reasonNoReport     = "no_report"
	reasonRAIDRead     = "raid_read"
)

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
	var out []domain.Alert
	var touch []string
	var err error
	// Without a report nothing is known: nothing is removed.
	smartBlind, raidBlind := h == nil, h == nil
	if h != nil {
		stale := reportStale(e, h, now)
		so, sProblem := smartMonitoring(e, h, stale)
		ro, rProblem := raidMonitoring(e, h, stale)
		smartBlind, raidBlind = sProblem, rProblem
		for _, m := range []struct {
			o       Observation
			problem bool
		}{{so, sProblem}, {ro, rProblem}} {
			key := m.o.Key
			seen[key] = true
			if out, err = apply(ctx, db, out, firing[key], m.problem, m.o, now); err != nil {
				return nil, err
			}
		}
		if out, firing, err = followDisks(ctx, db, e, h.SMART.Devices, firing, now, out); err != nil {
			return nil, err
		}
		noAccess := h.SMART.Status == protocol.SMARTNoAccess
		if out, touch, err = evaluateDisks(ctx, db, e, h.SMART.Devices, noAccess, firing, seen, now, out); err != nil {
			return nil, err
		}
		for _, a := range h.RAID.MD {
			key := raidKey(e.ID, "md", a.Name)
			seen[key] = true
			o, problem := mdObservation(e, a, key)
			if out, err = apply(ctx, db, out, firing[key], problem, o, now); err != nil {
				return nil, err
			}
		}
		for _, p := range h.RAID.ZFS {
			key := raidKey(e.ID, "zfs", p.Name)
			seen[key] = true
			o, problem := zfsObservation(e, p, key)
			if out, err = apply(ctx, db, out, firing[key], problem, o, now); err != nil {
				return nil, err
			}
		}
	}
	// Disks and arrays no longer reported: removed after a day, unless
	// their monitoring does not work.
	for key, a := range firing {
		if seen[key] || now.Sub(a.LastSeenAt) < DiskRemovedAfter || a.Kind == domain.NotifyDiskHealth && smartBlind ||
			a.Kind == domain.NotifyRAID && raidBlind {
			continue
		}
		if out, err = collect(out)(resolve(ctx, db, a, domain.AlertResolvedRemoved, now)); err != nil {
			return nil, err
		}
	}
	return out, store.TouchAlerts(ctx, db, touch, now)
}

// evaluateDisks applies each reported disk to its alert (firing: by key,
// after followDisks) and returns the alerts to touch. With noAccess (the
// SMART status no_access) the monitoring alert stands for the disks that
// refused to open.
func evaluateDisks(ctx context.Context, db bun.IDB, e domain.Environment, devices []protocol.SMARTDevice, noAccess bool,
	firing map[string]domain.Alert, seen map[string]bool, now time.Time, out []domain.Alert) ([]domain.Alert, []string, error) {
	var touch []string
	var err error
	for _, d := range devices {
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
			if d.ErrorCode == protocol.DiskErrUnsupported {
				// No SMART data at all (virtual disks, USB sticks): nothing
				// to watch, nothing to fix.
				if isFiring {
					out, err = collect(out)(resolve(ctx, db, cur, domain.AlertResolvedRemoved, now))
				}
				break
			}
			// A disk that can't be read keeps a stronger alert it has; with
			// no access the monitoring alert says it for every disk.
			if isFiring && (cur.Severity.Rank() > domain.AlertWarning.Rank() ||
				noAccess && d.ErrorCode == protocol.DiskErrPermissionDenied) {
				touch = append(touch, cur.ID)
				break
			}
			if noAccess && d.ErrorCode == protocol.DiskErrPermissionDenied {
				break
			}
			out, err = collect(out)(raise(ctx, db, diskObservation(e, d, key), now))
		case protocol.DiskWarning, protocol.DiskFailing:
			out, err = collect(out)(raise(ctx, db, diskObservation(e, d, key), now))
		}
		if err != nil {
			return nil, nil, err
		}
	}
	return out, touch, nil
}

// diskID names a physical disk across paths: a hash of the environment
// and the disk's serial number (never the number itself); "" without one.
func diskID(env string, d protocol.SMARTDevice) string {
	if d.Serial == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("docker-manager disk\x00" + env + "\x00" + d.Serial))
	return hex.EncodeToString(sum[:8])
}

// followDisks moves a disk alert to the path its disk is reported under
// now, and ends (removed) one whose path another disk holds while its own
// disk is not reported. It returns firing updated.
func followDisks(ctx context.Context, db bun.IDB, e domain.Environment, devices []protocol.SMARTDevice, firing map[string]domain.Alert,
	now time.Time, out []domain.Alert) ([]domain.Alert, map[string]domain.Alert, error) {
	at := map[string]string{}    // key: the disk ID reported there
	where := map[string]string{} // disk ID: the first key it is reported at
	for _, d := range devices {
		k, id := diskKey(e.ID, d.Name, d.Type), diskID(e.ID, d)
		at[k] = id
		if _, ok := where[id]; id != "" && !ok {
			where[id] = k
		}
	}
	type move struct {
		a  domain.Alert
		to string
	}
	var moves []move
	var gone []domain.Alert
	claimed := map[string]bool{}
	for key, a := range firing {
		id := a.Facts["diskId"]
		if a.Kind != domain.NotifyDiskHealth || id == "" {
			continue
		}
		cur, present := at[key]
		if present && (cur == id || cur == "") {
			continue // still its disk, or nothing says otherwise
		}
		if to, ok := where[id]; ok {
			if b, held := firing[to]; held && b.Facts["diskId"] == id || claimed[to] {
				gone = append(gone, a) // its disk's alert is there already
			} else {
				claimed[to] = true
				moves = append(moves, move{a, to})
			}
			continue
		}
		if present {
			gone = append(gone, a) // another disk took its path
		}
	}
	var err error
	for _, a := range gone {
		if out, err = collect(out)(resolve(ctx, db, a, domain.AlertResolvedRemoved, now)); err != nil {
			return nil, nil, err
		}
		delete(firing, a.DedupeKey)
	}
	moving := map[string]bool{}
	for _, m := range moves {
		moving[m.a.ID] = true
	}
	// An alert at a target that is neither moving nor gone was raised for
	// that path before its disk was known: the arriving one replaces it.
	for _, m := range moves {
		if b, held := firing[m.to]; held && !moving[b.ID] {
			if out, err = collect(out)(resolve(ctx, db, b, domain.AlertResolvedRemoved, now)); err != nil {
				return nil, nil, err
			}
			delete(firing, m.to)
		}
	}
	// Two steps: alerts may swap paths, and a key is unique while firing.
	for _, m := range moves {
		if err := store.RekeyAlert(ctx, db, m.a.ID, m.a.DedupeKey+"#moving#"+m.a.ID); err != nil {
			return nil, nil, err
		}
		delete(firing, m.a.DedupeKey)
	}
	for _, m := range moves {
		if err := store.RekeyAlert(ctx, db, m.a.ID, m.to); err != nil {
			return nil, nil, err
		}
		m.a.DedupeKey = m.to
		firing[m.to] = m.a
	}
	return out, firing, nil
}

// apply raises a problem's alert or resolves the alert (cur) of one that
// is gone.
func apply(ctx context.Context, db bun.IDB, out []domain.Alert, cur domain.Alert, problem bool, o Observation, now time.Time) ([]domain.Alert, error) {
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

// reportStale reports an online environment whose last host health report
// is older than HealthReportStale although it has been online that long.
func reportStale(e domain.Environment, h *observe.HostHealth, now time.Time) bool {
	return e.Online && e.ConnectionChangedAt != nil && now.Sub(*e.ConnectionChangedAt) >= HealthReportStale &&
		!h.ReceivedAt.IsZero() && now.Sub(h.ReceivedAt) >= HealthReportStale
}

// smartMonitoring describes an environment whose disks are not watched:
// the SMART status error, no_access or not_installed, the last read of
// every disk out of date, or no recent report (stale). problem is false
// while it works (and when SMART is turned off on purpose).
func smartMonitoring(e domain.Environment, h *observe.HostHealth, stale bool) (Observation, bool) {
	key := monitorKey(e.ID, domain.NotifyDiskHealth)
	facts := map[string]string{"monitoring": "smart"}
	var reason, title string
	s := h.SMART
	switch {
	case stale:
		reason, title = reasonNoReport, "Disk health is out of date"
		facts["since"] = h.ReceivedAt.UTC().Format(time.RFC3339)
	case s.Status == protocol.SMARTError:
		reason, title = reasonScanFailed, "Disks can't be scanned"
	case s.Status == protocol.SMARTNoAccess:
		reason, title = reasonNoAccess, "Disks can't be opened"
	case s.Status == protocol.SMARTNotInstalled:
		reason, title = reasonNotInstalled, "Disk health tool is missing"
	case s.Status == protocol.SMARTOK && s.IntervalSeconds > 0 && s.IntervalSeconds <= maxIntervalSeconds && s.CheckedAt != nil &&
		h.SampledAt.Sub(*s.CheckedAt) > 2*time.Duration(s.IntervalSeconds)*time.Second+SMARTStaleSlack:
		reason, title = reasonStale, "Disk health is out of date"
		facts["since"] = s.CheckedAt.UTC().Format(time.RFC3339)
	default:
		return Observation{Key: key}, false
	}
	facts["reason"] = reason
	return Observation{
		Key: key, Kind: domain.NotifyDiskHealth, Severity: domain.AlertWarning, EnvironmentID: e.ID,
		ResourceType: domain.AlertResourceEnvironment, ResourceID: e.ID, Title: title, Facts: facts,
		// The reasons replace each other: none is worse than another.
		Fingerprint: domain.Fingerprint("unmonitored"),
	}, true
}

// raidMonitoring describes an environment whose RAID state can't be read
// (or whose report is stale); problem is false while it can.
func raidMonitoring(e domain.Environment, h *observe.HostHealth, stale bool) (Observation, bool) {
	key := monitorKey(e.ID, domain.NotifyRAID)
	facts := map[string]string{"monitoring": "raid"}
	var title string
	switch {
	case stale:
		facts["reason"], facts["since"] = reasonNoReport, h.ReceivedAt.UTC().Format(time.RFC3339)
		title = "RAID state is out of date"
	case h.RAID.Message != "":
		facts["reason"] = reasonRAIDRead
		title = "RAID state can't be read"
	default:
		return Observation{Key: key}, false
	}
	return Observation{
		Key: key, Kind: domain.NotifyRAID, Severity: domain.AlertWarning, EnvironmentID: e.ID,
		ResourceType: domain.AlertResourceEnvironment, ResourceID: e.ID, Title: title, Facts: facts,
		Fingerprint: domain.Fingerprint("unmonitored"),
	}, true
}

// diskObservation describes a disk in state warning, failing or error.
// Never its serial number (diskId is a hash of it).
func diskObservation(e domain.Environment, d protocol.SMARTDevice, key string) Observation {
	facts := map[string]string{"device": d.Name, "deviceType": d.Type, "state": d.State}
	if id := diskID(e.ID, d); id != "" {
		facts["diskId"] = id
	}
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
	hot := protocol.OverTemperatureOnly(d)
	if d.Passed != nil && !*d.Passed && !hot {
		facts["selfAssessment"] = "failed"
		tokens = append(tokens, "self_assessment_failed")
	}
	if d.CriticalWarning != nil && *d.CriticalWarning != 0 {
		facts["criticalWarning"] = strconv.Itoa(*d.CriticalWarning)
		if hot {
			facts["overTemperature"] = "true"
			tokens = append(tokens, "over_temperature")
		} else {
			tokens = append(tokens, "critical_warning")
		}
	}
	if protocol.OverTemperatureLimit(d) {
		// At or above the disk's own limit (#212).
		facts["temperatureC"] = strconv.Itoa(*d.TemperatureC)
		limit := d.TemperatureLimitC
		if limit == nil || *d.TemperatureC < *limit {
			limit = d.TemperatureCriticalC
		}
		facts["temperatureLimitC"] = strconv.Itoa(*limit)
		tokens = append(tokens, "over_temperature")
	}
	// Time above the limit is the same problem as being above it now, one
	// token: a lifetime count keeps it once the disk ran hot, so a reading
	// that crosses its limit back and forth never looks like a new problem
	// (no re-send, no cleared dismissal). Fingerprint drops the repeat.
	count("overTemperatureMinutes", "over_temperature", d.OverTemperatureMinutes)
	count("criticalTemperatureMinutes", "ran_critically_hot", d.CriticalTemperatureMinutes)
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
	count("endToEndErrors", "end_to_end", d.EndToEndErrors)
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
		if d.ErrorCode == protocol.DiskErrMissing {
			// Gone from the bus: worse than unreadable.
			what = "is missing"
			tokens = append(tokens, "missing")
		}
	}
	return Observation{
		Key: key, Kind: domain.NotifyDiskHealth, Severity: sev, EnvironmentID: e.ID, ResourceType: domain.AlertResourceDisk,
		ResourceID: d.Name, Title: "Disk " + d.Name + " " + what, Facts: facts,
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
		Title: "RAID " + a.Name + " " + what, Facts: facts, Fingerprint: domain.Fingerprint(tokens...),
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
		Title: "ZFS pool " + p.Name + " " + what,
		Facts: map[string]string{"pool": p.Name, "arrayKind": "zfs", "health": p.Health, "state": p.State},
		// The pool's health replaces itself: the severity says whether it
		// got worse.
		Fingerprint: domain.Fingerprint("unhealthy"),
	}, true
}
