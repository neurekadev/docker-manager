// Package humanize formats measured values for text people read (job
// progress and result messages, refusals, preflight findings): up to two
// decimal places with trailing zeros dropped, the same rule as the web
// UI's formatters (#147). Machine-readable values (JSON numbers) stay raw.
// It is shared by the manager and the agent.
package humanize

import (
	"math"
	"strconv"
)

// Decimal formats v with up to two decimal places, rounding half away
// from zero and dropping trailing zeros: 2, 1.5, 1.25, -0.07. A value on
// the half at the third decimal as written (1.005) rounds up, although
// its binary form is slightly below it.
func Decimal(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	// Clean the float noise of the scaling (1.005 * 100 = 100.49999...)
	// before rounding.
	scaled, err := strconv.ParseFloat(strconv.FormatFloat(v*100, 'g', 15, 64), 64)
	if err != nil {
		scaled = v * 100
	}
	r := math.Round(scaled) / 100
	if r == 0 {
		r = 0 // no "-0"
	}
	return strconv.FormatFloat(r, 'f', -1, 64)
}

var byteUnits = [...]string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}

// Bytes formats a byte count in binary units with up to two decimal
// places: "512 B", "1.5 KiB", "1.25 GiB", "2 MiB". Counts below 1 KiB
// stay whole bytes.
func Bytes(n int64) string {
	v := math.Abs(float64(n))
	u := 0
	for v >= 1024 && u < len(byteUnits)-1 {
		v /= 1024
		u++
	}
	// 1023.999 KiB rounds to 1024 KiB: read it as 1 MiB.
	if u > 0 && u < len(byteUnits)-1 && math.Round(v*100)/100 >= 1024 {
		v /= 1024
		u++
	}
	s := Decimal(v)
	if n < 0 && s != "0" {
		s = "-" + s
	}
	return s + " " + byteUnits[u]
}

// Sizes formats byte counts one message compares (needed and free, done
// and expected) with Bytes, unless two different counts would read the
// same after rounding: then every count is written in whole bytes
// ("1048575 B of 1048576 B"), so a difference never hides behind it.
func Sizes(ns ...int64) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = Bytes(n)
	}
	for i := range ns {
		for j := i + 1; j < len(ns); j++ {
			if ns[i] != ns[j] && out[i] == out[j] {
				for k, n := range ns {
					out[k] = strconv.FormatInt(n, 10) + " B"
				}
				return out
			}
		}
	}
	return out
}
