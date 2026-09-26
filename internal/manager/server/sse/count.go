package sse

import (
	"strings"
	"sync/atomic"
)

// open counts the event streams being served (the diagnostics metric
// docker_manager_sse_streams, #34). The HTTP layer tracks every response whose
// Content-Type is ContentType, so both the Huma adapter and plain handlers
// are counted without their cooperation.
var open atomic.Int64

// IsStream reports whether a response Content-Type is an event stream.
func IsStream(contentType string) bool { return strings.HasPrefix(contentType, ContentType) }

// Track counts one open stream; call the returned function when it ended.
func Track() (done func()) {
	open.Add(1)
	var once atomic.Bool
	return func() {
		if once.CompareAndSwap(false, true) {
			open.Add(-1)
		}
	}
}

// Open returns the number of event streams being served.
func Open() int64 { return open.Load() }
