//go:build !faultinject

package faultinject

import "context"

// Enabled reports whether this binary was built with -tags faultinject.
const Enabled = false

// Point marks a named fault point. Without the faultinject build tag it
// does nothing and returns nil.
func Point(context.Context, string) error { return nil }
