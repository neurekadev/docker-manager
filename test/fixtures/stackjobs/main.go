//go:build linux

// Command stackjobs runs one stack job the way the agent does, for the
// Compose stack integration tests (#7): inside the agent image with the
// agent's mounts it performs the startup storage check (#28), then runs the
// stack.* job through the agent's executors (internal/agent/stacks) and the
// shared step runner (internal/jobexec), exactly as the agent's job runner
// would after receiving the command. It prints the job's result payload
// (outcome, error, output) as one JSON line and exits 0 when the job
// succeeded, 1 otherwise.
//
//	stackjobs KIND NAME [SERVICE...]   KIND: stack.deploy, stack.start, stack.stop, stack.restart, stack.down
//	stackjobs read NAME                 print compose.read's output
//
// Environment: DOCKER_HOST, DOCKYARD_STACKS_VOLUME, STACKJOBS_BUILD=1
// (rebuild build sections), STACKJOBS_WAIT_TIMEOUT (dependency waits).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/compose"
	"github.com/neurekadev/dockyard/internal/agent/config"
	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/stacks"
	"github.com/neurekadev/dockyard/internal/agent/storage"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/protocol"
)

type deps struct {
	c   *compose.Adapter
	eng engine.Engine
	st  *storage.Result
}

func (d deps) Composer() stacks.Composer { return d.c }
func (d deps) Engine() engine.Engine     { return d.eng }
func (d deps) Storage() *storage.Result  { return d.st }

type journal struct{}

func (journal) Save(context.Context, *jobexec.State) error { return nil }

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 3 {
		fail("usage: stackjobs KIND NAME [SERVICE...]")
	}
	kind, name := os.Args[1], os.Args[2]
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		host = config.DefaultDockerHost
	}
	volume := os.Getenv("DOCKYARD_STACKS_VOLUME")
	if volume == "" {
		volume = config.DefaultStacksVolume
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	eng, err := engine.Connect(ctx, engine.Options{Host: host, Logger: log})
	if err != nil {
		fail("connect: %v", err)
	}
	defer eng.Close()
	res := storage.Verify(ctx, storage.Options{Engine: eng, StacksVolume: volume})
	if !res.StacksOK() {
		fail("storage check: %+v", res.Diagnostics)
	}
	comp, err := compose.New(ctx, compose.Options{Host: host, Engine: eng, Guard: res.Allows, Logger: log})
	if err != nil {
		fail("compose: %v", err)
	}
	defer comp.Close()
	wait := time.Minute
	if v := os.Getenv("STACKJOBS_WAIT_TIMEOUT"); v != "" {
		if wait, err = time.ParseDuration(v); err != nil {
			fail("STACKJOBS_WAIT_TIMEOUT: %v", err)
		}
	}
	svc := stacks.New(stacks.Options{Deps: deps{c: comp, eng: eng, st: &res}, Logger: log, WaitTimeout: wait})
	ref := protocol.ProjectRef{Root: protocol.RootStacks, Dir: name, ProjectName: name}

	if kind == "read" {
		out, err := svc.Requests()[protocol.ReqComposeRead](ctx, mustJSON(protocol.ComposeReadInput{Stack: ref}))
		if err != nil {
			fail("read: %v", err)
		}
		fmt.Println(string(mustJSON(out)))
		return
	}
	in := protocol.StackJobInput{StackID: name, Stack: ref, Services: os.Args[3:], Build: os.Getenv("STACKJOBS_BUILD") == "1"}
	var exec *jobexec.Executor
	for _, x := range svc.Executors() {
		if string(x.Kind) == kind {
			exec = &x
		}
	}
	if exec == nil {
		fail("unknown kind %s", kind)
	}
	st := &jobexec.State{JobID: "stackjobs-" + name, Attempt: 1, FencingToken: 1, Kind: domain.JobKind(kind), Input: mustJSON(in)}
	result, err := jobexec.Run(ctx, *exec, st, jobexec.Options{Journal: journal{}, FaultPrefix: "agent"})
	if err != nil {
		fail("run: %v", err)
	}
	fmt.Println(string(mustJSON(result)))
	if result.Outcome != jobexec.OutcomeSucceeded {
		os.Exit(1)
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		fail("encode: %v", err)
	}
	return b
}
