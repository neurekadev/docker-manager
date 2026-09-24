// Command dockyard-agent is the DockYard agent: an outbound-only connector
// that controls the local Docker Engine on behalf of the manager.
//
// Usage:
//
//	dockyard-agent [run]         run the agent (default)
//	dockyard-agent healthcheck   check the health file is fresh (image HEALTHCHECK)
//	dockyard-agent version       print build information
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata" // IANA zones without relying on the image

	"github.com/neurekadev/dockyard/internal/agent/config"
	"github.com/neurekadev/dockyard/internal/agent/runtime"
	"github.com/neurekadev/dockyard/internal/buildinfo"
	"github.com/neurekadev/dockyard/internal/envconfig"
	"github.com/neurekadev/dockyard/internal/logging"
)

const (
	exitOK     = 0
	exitFail   = 1
	exitConfig = 2
)

func main() {
	os.Exit(run(os.Args[1:], envconfig.OS(), os.Stdout, os.Stderr, os.Geteuid))
}

func run(args []string, env envconfig.Source, stdout, stderr io.Writer, geteuid func() int) int {
	cmd := "run"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "run":
		return runAgent(env, stderr, geteuid)
	case "healthcheck":
		stateDir := env.String(config.EnvStateDir, config.DefaultStateDir)
		if abs, err := filepath.Abs(stateDir); err == nil {
			stateDir = abs
		}
		if err := runtime.CheckHealth(stateDir, time.Now(), runtime.HealthMaxAge); err != nil {
			_, _ = fmt.Fprintln(stderr, "healthcheck:", err)
			return exitFail
		}
		return exitOK
	case "version", "--version", "-v":
		_, _ = fmt.Fprintln(stdout, "dockyard-agent", buildinfo.Get())
		return exitOK
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q\n\n", cmd)
		usage(stderr)
		return exitConfig
	}
}

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage: dockyard-agent [command]

Commands:
  run            run the agent (default)
  healthcheck    exit 0 if the agent health file is fresh
  version        print build information

Configuration is read from environment variables; see docs/configuration.md.
`)
}

func runAgent(env envconfig.Source, stderr io.Writer, geteuid func() int) int {
	cfg, err := config.Load(env)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "dockyard-agent: invalid configuration:\n%v\n", err)
		return exitConfig
	}
	logger := logging.New(stderr, cfg.LogLevel, cfg.LogFormat)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runtime.Run(ctx, runtime.Options{Config: cfg, Logger: logger, Geteuid: geteuid}); err != nil {
		logger.Error("dockyard-agent stopped with error", "error", err)
		return exitFail
	}
	return exitOK
}
