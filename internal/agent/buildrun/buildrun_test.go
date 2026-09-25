package buildrun

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

var secrets = &protocol.CommandSecrets{
	Registries: []protocol.RegistryCredential{{Username: "bot", Secret: "registry-secret-1"}},
	Git:        []protocol.GitCredential{{Username: "builder", Secret: "git-token-2"}},
}

func TestScrubberCoversCommonEncodings(t *testing.T) {
	s := Scrubber(secrets)
	in := strings.Join([]string{"registry-secret-1", base64.StdEncoding.EncodeToString([]byte("bot:registry-secret-1")),
		base64.StdEncoding.EncodeToString([]byte("git-token-2")), base64.URLEncoding.EncodeToString([]byte("git-token-2"))}, " ")
	if got := s(in); strings.Count(got, "[redacted]") != 4 || strings.Contains(got, "secret") || strings.Contains(got, "token") {
		t.Errorf("%q", got)
	}
	if Scrubber(nil)("x") != "x" {
		t.Error("nil secrets changed text")
	}
}

func TestScrubErrorKeepsTheEngineCode(t *testing.T) {
	err := ScrubError(secrets, &engine.Error{Code: engine.CodeUnauthorized, Op: "image.build", Message: "denied for git-token-2"})
	if engine.CodeOf(err) != engine.CodeUnauthorized || strings.Contains(err.Error(), "git-token-2") {
		t.Errorf("%v (%s)", err, engine.CodeOf(err))
	}
	plain := errors.New("nothing secret")
	if ScrubError(secrets, plain) != plain {
		t.Error("an error without secrets was replaced")
	}
	if err := ScrubError(secrets, errors.New("x registry-secret-1")); err.Error() != "x [redacted]" || engine.CodeOf(err) != engine.CodeEngineError {
		t.Errorf("%v", err)
	}
}

type reporter struct {
	mu   sync.Mutex
	msgs []string
}

func (r *reporter) Progress(_ context.Context, _ *jobexec.State, p protocol.ProgressPayload) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, p.Message)
}

type nopJournal struct{}

func (nopJournal) Save(context.Context, *jobexec.State) error { return nil }

// step runs fn as the single step of an image.build attempt.
func step(t *testing.T, fn jobexec.StepFunc, rep *reporter) protocol.ResultPayload {
	t.Helper()
	exec := jobexec.Executor{Kind: jobspec.ImageBuild, Steps: map[string]jobexec.StepFunc{
		"fetch_context": func(context.Context, *jobexec.StepContext) error { return nil }, "build": fn}}
	res, err := jobexec.Run(testutil.Context(t), exec, &jobexec.State{JobID: "j", Attempt: 1, Kind: jobspec.ImageBuild, Secrets: secrets},
		jobexec.Options{Journal: nopJournal{}, Reporter: rep})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestProgressBatchesAndBoundsOutput(t *testing.T) {
	rep := &reporter{}
	res := step(t, func(ctx context.Context, sc *jobexec.StepContext) error {
		p := NewProgress(ctx, sc)
		p.SetPrefix("shop-web")
		p.Event(engine.BuildEvent{Step: "[1/2] FROM base", Status: "started"})
		p.Event(engine.BuildEvent{Step: "[2/2] RUN make", Status: "log", Log: []byte("a registry-secret-1\n")})
		p.Event(engine.BuildEvent{Step: "[2/2] RUN make", Status: "log", Log: []byte("b\n")})
		p.Event(engine.BuildEvent{Step: "[2/2] RUN make", Status: "error", Error: "exit 1"})
		for range maxLogMessages + 5 {
			p.Event(engine.BuildEvent{Step: "x", Status: "log", Log: []byte(strings.Repeat("y", logFlushBytes))})
		}
		p.Flush()
		return nil
	}, rep)
	if res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatal(res)
	}
	all := strings.Join(rep.msgs, "\n")
	for _, want := range []string{"shop-web: [1/2] FROM base: started", "shop-web: [2/2] RUN make\na [redacted]\nb",
		"shop-web: [2/2] RUN make: error: exit 1", "build output truncated"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(all, "registry-secret-1") || strings.Count(all, "shop-web: x\n") != maxLogMessages-1 {
		t.Errorf("%d output messages", strings.Count(all, "shop-web: x\n"))
	}
}

func TestRunReportsCancellationAndTimeout(t *testing.T) {
	clk := testutil.FakeClock()
	blocked := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

	cancelled := make(chan protocol.ResultPayload, 1)
	var mu sync.Mutex
	requested := false
	go func() {
		exec := jobexec.Executor{Kind: jobspec.ImageBuild, Steps: map[string]jobexec.StepFunc{
			"fetch_context": func(context.Context, *jobexec.StepContext) error { return nil },
			"build": func(ctx context.Context, sc *jobexec.StepContext) error {
				return Run(ctx, sc, Options{Clock: clk, Timeout: jobspec.DefaultBuildTimeout}, blocked)
			}}}
		res, _ := jobexec.Run(testutil.Context(t), exec, &jobexec.State{JobID: "j", Attempt: 1, Kind: jobspec.ImageBuild},
			jobexec.Options{Journal: nopJournal{}, CancelRequested: func() bool { mu.Lock(); defer mu.Unlock(); return requested }})
		cancelled <- res
	}()
	if err := clk.BlockUntilWaiters(testutil.Context(t), 2); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	requested = true
	mu.Unlock()
	clk.Advance(DefaultCancelPoll)
	if res := <-cancelled; res.Outcome != jobexec.OutcomeCancelled || res.ErrorClass != domain.ErrorCancelled {
		t.Errorf("cancel: %+v", res)
	}

	clk = testutil.FakeClock()
	timedOut := make(chan error, 1)
	go func() {
		step(t, func(ctx context.Context, sc *jobexec.StepContext) error {
			err := Run(ctx, sc, Options{Clock: clk, Timeout: 90 * time.Second}, blocked)
			timedOut <- err
			return err
		}, &reporter{})
	}()
	if err := clk.BlockUntilWaiters(testutil.Context(t), 2); err != nil {
		t.Fatal(err)
	}
	clk.Advance(90 * time.Second)
	if err := <-timedOut; err == nil || errors.Is(err, jobexec.ErrStepCancelled) || !strings.Contains(err.Error(), "within 1m30s") {
		t.Errorf("timeout: %v", err)
	}

	// A build that ends on its own returns its own error.
	want := errors.New("boom")
	step(t, func(ctx context.Context, sc *jobexec.StepContext) error {
		if err := Run(ctx, sc, Options{Clock: testutil.FakeClock(), Timeout: 90 * time.Second}, func(context.Context) error { return want }); err != want {
			t.Errorf("own error: %v", err)
		}
		return nil
	}, &reporter{})
}
