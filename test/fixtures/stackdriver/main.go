//go:build linux

// Command stackdriver deploys one stack the way the agent does, for the
// storage-layout integration tests (#28): it runs inside the agent image
// with the agent's mounts, performs the agent's startup storage check
// (internal/agent/storage), and deploys the project through the Compose
// adapter guarded by that check (internal/agent/compose). It prints one JSON
// object per step and exits 0 when the stack is up, 3 when the deploy was
// refused, 4 when it failed.
//
//	stackdriver NAME [DIR]   DIR defaults to <stacks volume mountpoint>/NAME
//
// Environment: DOCKER_HOST, DOCKYARD_STACKS_VOLUME, DOCKYARD_STACK_ROOTS.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/compose"
	"github.com/neurekadev/dockyard/internal/agent/config"
	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/storage"
)

// Report is one output line.
type Report struct {
	Step        string               `json:"step"`
	StacksOK    bool                 `json:"stacksOk,omitempty"`
	StacksDir   string               `json:"stacksDir,omitempty"`
	SelfID      string               `json:"selfContainerId,omitempty"`
	Roots       []storage.Root       `json:"roots,omitempty"`
	Diagnostics []storage.Diagnostic `json:"diagnostics,omitempty"`
	Dir         string               `json:"dir,omitempty"`
	Code        string               `json:"code,omitempty"`
	Error       string               `json:"error,omitempty"`
}

func emit(r Report) {
	b, _ := json.Marshal(r)
	fmt.Println(string(b))
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: stackdriver NAME [DIR]")
		os.Exit(2)
	}
	name := os.Args[1]
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		host = config.DefaultDockerHost
	}
	volume := os.Getenv("DOCKYARD_STACKS_VOLUME")
	if volume == "" {
		volume = config.DefaultStacksVolume
	}
	roots, err := config.ParseStackRoots(os.Getenv("DOCKYARD_STACK_ROOTS"))
	if err != nil {
		emit(Report{Step: "config", Error: err.Error()})
		os.Exit(2)
	}
	eng, err := engine.Connect(ctx, engine.Options{Host: host})
	if err != nil {
		emit(Report{Step: "connect", Code: string(engine.CodeOf(err)), Error: err.Error()})
		os.Exit(4)
	}
	defer eng.Close()

	res := storage.Verify(ctx, storage.Options{Engine: eng, StacksVolume: volume, StackRoots: roots})
	emit(Report{Step: "verify", StacksOK: res.StacksOK(), StacksDir: res.StacksDir, SelfID: res.SelfContainerID,
		Roots: res.Roots, Diagnostics: res.Diagnostics})

	comp, err := compose.New(ctx, compose.Options{Host: host, Engine: eng, Guard: res.Allows})
	if err != nil {
		emit(Report{Step: "compose", Code: string(engine.CodeOf(err)), Error: err.Error()})
		os.Exit(4)
	}
	defer comp.Close()
	dir := path.Join(res.StacksDir, name)
	if len(os.Args) > 2 {
		dir = os.Args[2]
	}
	p, err := comp.Load(ctx, compose.ProjectSpec{Dir: dir, Name: name})
	if err != nil {
		emit(Report{Step: "load", Dir: dir, Code: string(engine.CodeOf(err)), Error: err.Error()})
		if engine.CodeOf(err) == engine.CodeInvalidProject {
			os.Exit(4)
		}
		os.Exit(3)
	}
	if err := comp.Up(ctx, p, compose.UpOptions{Wait: true, WaitTimeout: 3 * time.Minute}); err != nil {
		emit(Report{Step: "up", Dir: dir, Code: string(engine.CodeOf(err)), Error: err.Error()})
		os.Exit(4)
	}
	emit(Report{Step: "up", Dir: dir})
}
