package domain

import "time"

// Alert thresholds: when a host runs hot, out of disk space or out of
// memory. One set of defaults for every environment, and an optional
// override per environment. 0 turns a level off.

// AlertThresholds are the warning and critical levels of the host alerts:
// temperatures in °C, disk space and memory in percent used; 0 is off.
type AlertThresholds struct {
	TemperatureWarning  int
	TemperatureCritical int
	DiskSpaceWarning    int
	DiskSpaceCritical   int
	MemoryWarning       int
	MemoryCritical      int
}

// DefaultAlertThresholds are the thresholds of a new instance.
func DefaultAlertThresholds() AlertThresholds {
	return AlertThresholds{
		TemperatureWarning: 80, TemperatureCritical: 90,
		DiskSpaceWarning: 85, DiskSpaceCritical: 95,
		MemoryWarning: 90, MemoryCritical: 95,
	}
}

// Threshold limits.
const (
	MaxTemperatureThreshold = 150
	MaxPercentThreshold     = 100
)

// AlertThresholdOverride changes some thresholds of one environment; a nil
// level keeps the default, 0 turns it off there.
type AlertThresholdOverride struct {
	EnvironmentID       string
	TemperatureWarning  *int
	TemperatureCritical *int
	DiskSpaceWarning    *int
	DiskSpaceCritical   *int
	MemoryWarning       *int
	MemoryCritical      *int
}

// Apply returns the thresholds of the override's environment.
func (o AlertThresholdOverride) Apply(t AlertThresholds) AlertThresholds {
	pick := func(v *int, d int) int {
		if v != nil {
			return *v
		}
		return d
	}
	return AlertThresholds{
		TemperatureWarning: pick(o.TemperatureWarning, t.TemperatureWarning), TemperatureCritical: pick(o.TemperatureCritical, t.TemperatureCritical),
		DiskSpaceWarning: pick(o.DiskSpaceWarning, t.DiskSpaceWarning), DiskSpaceCritical: pick(o.DiskSpaceCritical, t.DiskSpaceCritical),
		MemoryWarning: pick(o.MemoryWarning, t.MemoryWarning), MemoryCritical: pick(o.MemoryCritical, t.MemoryCritical),
	}
}

// AlertSettings are the instance's alert thresholds.
type AlertSettings struct {
	Thresholds AlertThresholds
	// Overrides by environment, in environment ID order.
	Overrides []AlertThresholdOverride
	Revision  int64
	UpdatedAt time.Time
}

// For returns the thresholds of an environment.
func (s AlertSettings) For(environmentID string) AlertThresholds {
	for _, o := range s.Overrides {
		if o.EnvironmentID == environmentID {
			return o.Apply(s.Thresholds)
		}
	}
	return s.Thresholds
}
