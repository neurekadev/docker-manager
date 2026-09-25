//go:build !race

package testutil

// RaceEnabled reports whether the race detector is compiled in.
const RaceEnabled = false

const raceBit = 0
