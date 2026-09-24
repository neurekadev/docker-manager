//go:build faultinject

package faultinject

import (
	"context"
	"fmt"
	"os"
	"sync"
)

// Enabled reports whether this binary was built with -tags faultinject.
const Enabled = true

var traceMu sync.Mutex

// Point marks a named fault point and performs the armed action, if any.
// The environment is read on every call (fault builds are test-only), so
// tests may re-arm points with t.Setenv.
func Point(ctx context.Context, name string) error {
	armed, err := ParseSpec(os.Getenv(EnvVar))
	if err != nil {
		return err
	}
	trace(name)
	switch armed[name] {
	case Crash:
		// Like a kill -9 at this line: no deferred functions, no flushing.
		os.Exit(ExitCode)
	case Error:
		return fmt.Errorf("%w at %s", ErrInjected, name)
	case Block:
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func trace(name string) {
	path := os.Getenv(TraceEnv)
	if path == "" {
		return
	}
	traceMu.Lock()
	defer traceMu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // test-only trace file named by the test harness
	if err != nil {
		return
	}
	_, _ = f.WriteString(name + "\n")
	_ = f.Close()
}
