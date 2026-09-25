package transfer

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ParseRate parses a bandwidth cap in bytes per second: "0" or "" (no
// cap), a plain number of bytes, or a number with a decimal (KB, MB, GB)
// or binary (KiB, MiB, GiB) unit, optionally followed by "/s", e.g.
// "50MB", "100MiB/s", "1.5GB". Units are case-insensitive; "B" is bytes.
func ParseRate(s string) (int64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/s"), "/S")
	if s == "" || s == "0" {
		return 0, nil
	}
	i := strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	num, unit := s, ""
	if i >= 0 {
		num, unit = strings.TrimSpace(s[:i]), strings.TrimSpace(s[i:])
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || v < 0 || math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, fmt.Errorf("invalid rate %q: use a number of bytes per second with an optional unit (KB, MB, GB, KiB, MiB, GiB)", s)
	}
	mult := map[string]float64{
		"": 1, "b": 1,
		"kb": 1e3, "mb": 1e6, "gb": 1e9,
		"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30,
	}[strings.ToLower(unit)]
	if mult == 0 {
		return 0, fmt.Errorf("invalid rate %q: unknown unit %q (use KB, MB, GB, KiB, MiB or GiB)", s, unit)
	}
	r := v * mult
	if r > float64(math.MaxInt64/2) {
		return 0, fmt.Errorf("invalid rate %q: too large", s)
	}
	if r > 0 && r < 1024 {
		return 0, fmt.Errorf("invalid rate %q: at least 1 KiB/s", s)
	}
	return int64(r), nil
}
