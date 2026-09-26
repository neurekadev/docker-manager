package jobexec

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// deployExec sets output in its first step and fails in failStep.
func deployExec(failStep string) Executor {
	steps := map[string]StepFunc{}
	spec, _ := jobspec.Lookup(jobspec.StackDeploy)
	for _, s := range spec.Steps {
		name := s.Name
		steps[name] = func(ctx context.Context, sc *StepContext) error {
			var out map[string][]string
			_ = json.Unmarshal(sc.Output(), &out)
			if out == nil {
				out = map[string][]string{}
			}
			out["steps"] = append(out["steps"], name)
			if err := sc.SetOutput(ctx, out); err != nil {
				return err
			}
			if name == failStep {
				return errors.New("boom")
			}
			return nil
		}
	}
	return Executor{Kind: jobspec.StackDeploy, Steps: steps}
}

func TestOutputIsJournaledAndSentWithEveryOutcome(t *testing.T) {
	for _, fail := range []string{"", "build_images"} {
		j := &memJournal{}
		st := &State{JobID: "j1", Attempt: 1, Kind: jobspec.StackDeploy, Input: json.RawMessage(`{}`)}
		res, err := Run(context.Background(), deployExec(fail), st, Options{Journal: j})
		if err != nil {
			t.Fatal(err)
		}
		want := `{"steps":["resolve_sources","pull_images","build_images","apply"]}`
		if fail != "" {
			want = `{"steps":["resolve_sources","pull_images","build_images"]}`
		}
		if string(res.Output) != want || string(st.Outcome.Output) != want {
			t.Errorf("fail=%q: output %s", fail, res.Output)
		}
		// Every SetOutput was journaled before the step went on.
		found := false
		for _, s := range j.saves {
			if s.StepInFlight && s.CurrentStep == "resolve_sources" && strings.Contains(string(s.Output), "resolve_sources") {
				found = true
			}
		}
		if !found {
			t.Errorf("fail=%q: output of the in-flight step not journaled", fail)
		}
		// Clone keeps the output.
		c := st.Clone()
		if string(c.Output) != string(st.Output) || string(c.Outcome.Output) != want {
			t.Error("clone lost the output")
		}
	}
}

func TestOutputBound(t *testing.T) {
	sc := &StepContext{Kind: jobspec.StackDeploy, st: &State{}, opts: Options{Journal: &memJournal{}}}
	err := sc.SetOutput(context.Background(), map[string]string{"x": strings.Repeat("a", protocol.MaxResultOutput)})
	if !errors.Is(err, ErrOutputTooLarge) || sc.Output() != nil {
		t.Errorf("oversized output: %v %s", err, sc.Output())
	}
}

func TestRecoverSendsJournaledOutput(t *testing.T) {
	st := &State{JobID: "j1", Attempt: 1, Kind: jobspec.StackDeploy, CurrentStep: "apply", StepInFlight: true,
		Completed: []string{"resolve_sources", "pull_images", "build_images"}, Output: json.RawMessage(`{"before":[]}`)}
	res, err := Recover(context.Background(), nil, st, Options{Journal: &memJournal{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeInterrupted || !res.Resumable || string(res.Output) != `{"before":[]}` {
		t.Errorf("recovered %+v", res)
	}
}
