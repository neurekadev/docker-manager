package compose

import (
	"testing"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/engine/enginetest"
	"github.com/neurekadev/dockyard/internal/agent/storage"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// No project is loaded or deployed from a directory the storage check did
// not verify (#28); the refusal carries the storage diagnostic code.
func TestGuardRefusesUnverifiedDirectories(t *testing.T) {
	a := newAdapter(t, enginetest.Start(t, enginetest.Options{}))
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"compose.yaml": "services:\n  app:\n    image: app\n"})
	p, err := a.Load(testutil.Context(t), ProjectSpec{Dir: dir, Name: "guarded"})
	if err != nil {
		t.Fatal(err)
	}

	var checked []string
	a.opts.Guard = func(d string) error {
		checked = append(checked, d)
		return storage.Diagnostic{Code: storage.CodePathMismatch, Message: "mounted from elsewhere", Path: d}
	}
	if _, err := a.Load(testutil.Context(t), ProjectSpec{Dir: dir, Name: "guarded"}); engine.CodeOf(err) != storage.CodePathMismatch {
		t.Errorf("load: %v (%s)", err, engine.CodeOf(err))
	}
	for name, run := range map[string]func() error{
		"up":      func() error { return a.Up(testutil.Context(t), p, UpOptions{}) },
		"start":   func() error { return a.Start(testutil.Context(t), p, RunOptions{}) },
		"restart": func() error { return a.Restart(testutil.Context(t), p, nil, nil, RunOptions{}) },
		"build":   func() error { return a.Build(testutil.Context(t), p, BuildOptions{}) },
	} {
		if err := run(); engine.CodeOf(err) != storage.CodePathMismatch {
			t.Errorf("%s: %v (%s)", name, err, engine.CodeOf(err))
		}
	}
	if len(checked) != 5 || checked[0] != dir {
		t.Errorf("guard saw %v", checked)
	}
	a.opts.Guard = func(string) error { return errPlain }
	if err := a.Up(testutil.Context(t), p, UpOptions{}); engine.CodeOf(err) != CodeStorageUnverified {
		t.Errorf("plain guard error: %v", err)
	}
}

type plainErr struct{}

func (plainErr) Error() string { return "not verified" }

var errPlain error = plainErr{}
