package alerts

import (
	"context"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Host usage alerts: an environment's hottest temperature sensor, each
// filesystem the agent reports (Docker data, stacks, bind mounts) and its
// memory, against the thresholds of Settings → Notifications (defaults,
// and an environment's override). One alert per environment for the
// temperature and memory, one per filesystem.
//
//   - A level is reached when the value stayed at or above it for
//     ThresholdSustain (a short spike raises nothing); critical counts on
//     its own, so a warning becomes critical only when that holds too.
//   - It resolves once the value is below the warning level by
//     ThresholdHysteresis (no flapping around the threshold); a value
//     between the two keeps the alert as it is. Thresholds turned off, a
//     filesystem or sensor no longer reported, end it silently.
//   - The facts keep the peak: the alert changes when the value gets
//     higher (or the thresholds change), not with every sample.
//   - Samples older than ThresholdStale change nothing (an offline
//     environment keeps its alerts until it reports again).

// Host usage timing and margins.
const (
	ThresholdSustain = 5 * time.Minute
	ThresholdStale   = 2 * time.Minute
	// ThresholdHysteresis is in °C for temperatures and in percentage
	// points for disk space and memory.
	ThresholdHysteresis = 3
)

// overSince is when a value was first seen at or above each level (zero:
// not now).
type overSince struct {
	warning, critical time.Time
}

func temperatureKey(env string) string { return "temperature/" + env }

func diskSpaceKey(env, mount string) string { return "disk_space/" + env + "/" + mount }

func memoryKey(env string) string { return "memory/" + env }

// measure is one value checked against its thresholds.
type measure struct {
	key, resourceType, resourceID string
	kind                          domain.NotificationEventKind
	value                         float64
	warning, critical             int
	title                         string
	// facts describe the value now (the peak is kept, see peakFacts).
	facts map[string]string
}

// evaluateAllThresholds evaluates the host usage alerts of every active
// environment.
func (s *Service) evaluateAllThresholds(ctx context.Context) error {
	if s.opts.Metrics == nil {
		return nil
	}
	envs, err := store.ListEnvironments(ctx, s.db, domain.EnvironmentFilter{Statuses: []domain.EnvironmentStatus{domain.EnvironmentActive}})
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range envs {
		if err := s.EvaluateThresholds(ctx, e.ID); err != nil {
			errs = append(errs, err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return errors.Join(errs...)
}

// percent is used of total in percent (ok false: unknown).
func percent(used, total int64) (float64, bool) {
	if total <= 0 || used < 0 {
		return 0, false
	}
	return float64(used) / float64(total) * 100, true
}

// round formats a value as a whole number.
func round(v float64) string { return strconv.Itoa(int(math.Round(v))) }

// EvaluateThresholds raises, updates and resolves the host usage alerts of
// one environment from its latest samples.
func (s *Service) EvaluateThresholds(ctx context.Context, environmentID string) error {
	if s.opts.Metrics == nil {
		return nil
	}
	now := s.now()
	latest, hostOK, err := s.opts.Metrics.Latest(ctx, environmentID)
	if err != nil {
		return err
	}
	temps, tempsAt, tempsOK, err := s.opts.Metrics.LatestTemperatures(ctx, environmentID)
	if err != nil {
		return err
	}
	hostFresh := hostOK && now.Sub(latest.At) <= ThresholdStale
	tempsFresh := tempsOK && now.Sub(tempsAt) <= ThresholdStale
	// This runs with every sample: when nothing is at a level and nothing
	// fires, it writes nothing (no transaction).
	if quiet, err := s.quiet(ctx, environmentID, latest, hostFresh, temps, tempsFresh); err != nil || quiet {
		return err
	}
	return s.inTx(ctx, func(ctx context.Context, tx bun.Tx) ([]domain.Alert, error) {
		e, err := store.GetEnvironment(ctx, tx, environmentID)
		if errors.Is(err, domain.ErrEnvironmentNotFound) {
			return nil, nil
		}
		if err != nil || e.Status != domain.EnvironmentActive {
			return nil, err
		}
		set, err := store.GetAlertSettings(ctx, tx)
		if err != nil {
			return nil, err
		}
		t := set.For(environmentID)
		var ms []measure
		// seen are the keys reported now; firing ones not among them
		// (a filesystem or the sensors gone) end.
		seen := map[string]bool{}
		if tempsFresh {
			var hottest *domain.TemperatureValues
			for i := range temps {
				if hottest == nil || temps[i].Celsius > hottest.Celsius {
					hottest = &temps[i]
				}
			}
			if hottest != nil {
				seen[temperatureKey(environmentID)] = true
				ms = append(ms, measure{key: temperatureKey(environmentID), kind: domain.NotifyTemperature,
					resourceType: domain.AlertResourceEnvironment, resourceID: environmentID, value: hottest.Celsius,
					warning: t.TemperatureWarning, critical: t.TemperatureCritical, title: e.Name + " is running hot",
					facts: map[string]string{"sensor": hottest.Sensor, "celsius": round(hottest.Celsius)}})
			}
		}
		if hostFresh {
			for _, d := range latest.Disks {
				p, ok := percent(d.UsedBytes, d.TotalBytes)
				if !ok {
					continue
				}
				key := diskSpaceKey(environmentID, d.Mount)
				seen[key] = true
				ms = append(ms, measure{key: key, kind: domain.NotifyDiskSpace, resourceType: domain.AlertResourceFilesystem,
					resourceID: d.Mount, value: p, warning: t.DiskSpaceWarning, critical: t.DiskSpaceCritical,
					title: mountWords(d.Mount) + " disk is almost full",
					facts: map[string]string{"mount": d.Mount, "usedPercent": round(p),
						"freeBytes": strconv.FormatInt(max(d.TotalBytes-d.UsedBytes, 0), 10), "totalBytes": strconv.FormatInt(d.TotalBytes, 10)}})
			}
			if h := latest.Host; h.MemoryUsedBytes != nil && h.MemoryTotalBytes != nil {
				if p, ok := percent(*h.MemoryUsedBytes, *h.MemoryTotalBytes); ok {
					seen[memoryKey(environmentID)] = true
					ms = append(ms, measure{key: memoryKey(environmentID), kind: domain.NotifyMemory,
						resourceType: domain.AlertResourceEnvironment, resourceID: environmentID, value: p,
						warning: t.MemoryWarning, critical: t.MemoryCritical, title: e.Name + " is running out of memory",
						facts: map[string]string{"usedPercent": round(p), "usedBytes": strconv.FormatInt(*h.MemoryUsedBytes, 10),
							"totalBytes": strconv.FormatInt(*h.MemoryTotalBytes, 10)}})
				}
			}
		}
		var out []domain.Alert
		for _, m := range ms {
			if out, err = collect(out)(s.evaluateMeasure(ctx, tx, environmentID, m, now)); err != nil {
				return nil, err
			}
		}
		// Firing alerts of this environment whose value is no longer
		// reported (with fresh samples of that kind) end silently.
		for _, k := range []struct {
			kind  domain.NotificationEventKind
			fresh bool
		}{{domain.NotifyTemperature, tempsFresh}, {domain.NotifyDiskSpace, hostFresh}, {domain.NotifyMemory, hostFresh}} {
			if !k.fresh {
				continue
			}
			as, err := store.FiringAlerts(ctx, tx, k.kind, environmentID)
			if err != nil {
				return nil, err
			}
			for _, a := range as {
				if seen[a.DedupeKey] {
					continue
				}
				s.forget(a.DedupeKey)
				if out, err = collect(out)(resolve(ctx, tx, a, domain.AlertResolvedRemoved, now)); err != nil {
					return nil, err
				}
			}
		}
		return out, nil
	})
}

// lowest is the lowest level that is on (0: both off).
func lowest(warning, critical int) int {
	switch {
	case warning > 0 && critical > 0:
		return min(warning, critical)
	case warning > 0:
		return warning
	}
	return max(critical, 0)
}

// quiet reports whether no value of the environment is at a level and no
// host usage alert of it fires (and forgets the levels tracked for it).
func (s *Service) quiet(ctx context.Context, env string, latest domain.LatestMetrics, hostFresh bool,
	temps []domain.TemperatureValues, tempsFresh bool) (bool, error) {
	set, err := store.GetAlertSettings(ctx, s.db)
	if err != nil {
		return false, err
	}
	t := set.For(env)
	at := func(v float64, warning, critical int) bool {
		l := lowest(warning, critical)
		return l > 0 && v >= float64(l)
	}
	keys := []string{temperatureKey(env), memoryKey(env)}
	if tempsFresh {
		for _, r := range temps {
			if at(r.Celsius, t.TemperatureWarning, t.TemperatureCritical) {
				return false, nil
			}
		}
	}
	if hostFresh {
		for _, d := range latest.Disks {
			keys = append(keys, diskSpaceKey(env, d.Mount))
			if p, ok := percent(d.UsedBytes, d.TotalBytes); ok && at(p, t.DiskSpaceWarning, t.DiskSpaceCritical) {
				return false, nil
			}
		}
		if h := latest.Host; h.MemoryUsedBytes != nil && h.MemoryTotalBytes != nil {
			if p, ok := percent(*h.MemoryUsedBytes, *h.MemoryTotalBytes); ok && at(p, t.MemoryWarning, t.MemoryCritical) {
				return false, nil
			}
		}
	}
	as, err := store.FiringAlerts(ctx, s.db, "", env)
	if err != nil {
		return false, err
	}
	for _, a := range as {
		switch a.Kind {
		case domain.NotifyTemperature, domain.NotifyDiskSpace, domain.NotifyMemory:
			return false, nil
		}
	}
	for _, k := range keys {
		s.forget(k)
	}
	return true, nil
}

// forget drops what is known of a key's levels.
func (s *Service) forget(key string) {
	s.mu.Lock()
	delete(s.over, key)
	s.mu.Unlock()
}

// sustained tracks since when m is at or above each level and returns the
// level held for ThresholdSustain ("" for none).
func (s *Service) sustained(m measure, now time.Time) domain.AlertSeverity {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.over[m.key]
	track := func(since *time.Time, level int) bool {
		if level <= 0 || m.value < float64(level) {
			*since = time.Time{}
			return false
		}
		if since.IsZero() {
			*since = now
		}
		return now.Sub(*since) >= ThresholdSustain
	}
	warn := track(&o.warning, m.warning)
	crit := track(&o.critical, m.critical)
	if o.warning.IsZero() && o.critical.IsZero() {
		delete(s.over, m.key)
	} else {
		s.over[m.key] = o
	}
	switch {
	case crit:
		return domain.AlertCritical
	case warn:
		return domain.AlertWarning
	}
	return ""
}

// peakFacts keeps the facts of the highest value seen while the alert
// fires (cur: the firing alert's facts, nil for a new one), with the
// thresholds now in force.
func peakFacts(m measure, cur map[string]string, valueKey string) map[string]string {
	facts := m.facts
	if cur != nil {
		if prev, err := strconv.ParseFloat(cur[valueKey], 64); err == nil {
			if now, err := strconv.ParseFloat(m.facts[valueKey], 64); err == nil && now <= prev {
				facts = map[string]string{}
				for k, v := range cur {
					facts[k] = v
				}
			}
		}
	}
	facts["warningAt"], facts["criticalAt"] = strconv.Itoa(m.warning), strconv.Itoa(m.critical)
	return facts
}

// evaluateMeasure raises, updates or resolves the alert of one value.
func (s *Service) evaluateMeasure(ctx context.Context, db bun.IDB, env string, m measure, now time.Time) (*domain.Alert, error) {
	cur, firing, err := store.FiringAlert(ctx, db, m.key)
	if err != nil {
		return nil, err
	}
	if m.warning <= 0 && m.critical <= 0 {
		// Turned off.
		s.forget(m.key)
		if firing {
			return resolve(ctx, db, cur, domain.AlertResolvedRemoved, now)
		}
		return nil, nil
	}
	level := s.sustained(m, now)
	if level == "" {
		if !firing {
			return nil, nil
		}
		// Resolve below the lowest level by the margin; in between the
		// alert stays as it is.
		low := m.warning
		if low <= 0 {
			low = m.critical
		}
		if m.value < float64(low-ThresholdHysteresis) {
			return resolve(ctx, db, cur, domain.AlertResolvedFixed, now)
		}
		return nil, store.TouchAlerts(ctx, db, []string{cur.ID}, now)
	}
	valueKey := "usedPercent"
	if m.kind == domain.NotifyTemperature {
		valueKey = "celsius"
	}
	var prev map[string]string
	if firing {
		prev = cur.Facts
	}
	return raise(ctx, db, Observation{
		Key: m.key, Kind: m.kind, Severity: level, EnvironmentID: env, ResourceType: m.resourceType, ResourceID: m.resourceID,
		Title: m.title, Facts: peakFacts(m, prev, valueKey),
		// The level replaces itself: the severity says whether it got
		// worse.
		Fingerprint: domain.Fingerprint("over"),
	}, now)
}
