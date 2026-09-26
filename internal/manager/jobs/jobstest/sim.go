package jobstest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
)

// Effects records simulated side effects as "<jobID>:<step>" and
// "<jobID>:compensate:<name>". With File set, every effect is also appended
// (and fsync'd) to the file, so subprocess tests can inspect effects of a
// killed process.
type Effects struct {
	File string

	mu   sync.Mutex
	list []string
}

// Add records an effect.
func (e *Effects) Add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, s)
	if e.File == "" {
		return
	}
	f, err := os.OpenFile(e.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		panic(err) // test helper: a lost effect would invalidate the test
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(s + "\n"); err != nil {
		panic(err)
	}
	if err := f.Sync(); err != nil {
		panic(err)
	}
}

// List returns the effects recorded by this process.
func (e *Effects) List() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.list)
}

// ReadEffects reads an effects file.
func ReadEffects(path string) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

// SimOptions configures SimExecutor.
type SimOptions struct {
	Effects *Effects
	// Before runs before a step's effect (keyed by step name); an error
	// fails the step. Use it to block, observe or fail steps.
	Before map[string]func(ctx context.Context, sc *jobexec.StepContext) error
}

// SimExecutor implements every declared step of kind by recording an
// effect, mirroring the shape of the real features: a step named
// stop_containers registers the start_containers compensation before
// "stopping", a step named start_containers releases it after "starting".
func SimExecutor(kind domain.JobKind, o SimOptions) jobexec.Executor {
	spec, ok := jobspec.Lookup(kind)
	if !ok {
		panic("jobstest: unknown kind " + string(kind))
	}
	steps := map[string]jobexec.StepFunc{}
	for _, s := range spec.Steps {
		name := s.Name
		steps[name] = func(ctx context.Context, sc *jobexec.StepContext) error {
			if name == "stop_containers" && spec.HasCompensation(jobspec.CompStartContainers) {
				if err := sc.AddCompensation(ctx, jobspec.CompStartContainers, map[string]string{"job": sc.JobID}); err != nil {
					return err
				}
			}
			if fn := o.Before[name]; fn != nil {
				if err := fn(ctx, sc); err != nil {
					return err
				}
			}
			sc.Progress(ctx, -1, "simulated "+name)
			o.Effects.Add(sc.JobID + ":" + name)
			if name == "start_containers" && spec.HasCompensation(jobspec.CompStartContainers) {
				return sc.ReleaseCompensation(ctx, jobspec.CompStartContainers)
			}
			return nil
		}
	}
	comps := map[string]jobexec.CompensationFunc{}
	for _, c := range spec.Compensations {
		name := c.Name
		comps[name] = func(_ context.Context, args json.RawMessage) error {
			var a map[string]string
			_ = json.Unmarshal(args, &a)
			o.Effects.Add(a["job"] + ":compensate:" + name)
			return nil
		}
	}
	return jobexec.Executor{Kind: kind, Steps: steps, Compensations: comps}
}
