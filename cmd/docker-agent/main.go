// Command docker-agent is the Docker Agent: an outbound-only connector
// that controls the local Docker Engine on behalf of the manager.
//
// Usage:
//
//	docker-agent [run]         run the agent (default)
//	docker-agent enroll        hand an enrollment token (stdin) to the running agent
//	docker-agent healthcheck   check the health file is fresh (image HEALTHCHECK)
//	docker-agent version       print build information
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // IANA zones without relying on the image

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/config"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/runtime"
	"code.neureka.dev/docker-manager/docker-manager/internal/buildinfo"
	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/envconfig"
	"code.neureka.dev/docker-manager/docker-manager/internal/logging"
)

const (
	exitOK      = 0
	exitFail    = 1
	exitConfig  = 2
	exitTimeout = 3
)

// stdin is the enroll command's token source (tests replace it).
var stdin io.Reader = os.Stdin

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
	case "enroll":
		return enrollCmd(args[1:], env, stdout, stderr)
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
		_, _ = fmt.Fprintln(stdout, "docker-agent", buildinfo.Get())
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
	_, _ = io.WriteString(w, `Usage: docker-agent [command]

Commands:
  run            run the agent (default)
  enroll         hand an enrollment token to the running agent:
                   printf '%s\n' "$TOKEN" | docker exec -i docker-agent docker-agent enroll
                 flags: -token-file PATH (instead of stdin), -wait 90s (0: do not wait)
  healthcheck    exit 0 if the agent health file is fresh
  version        print build information

Configuration is read from environment variables; see docs/configuration.md.
`)
}

func runAgent(env envconfig.Source, stderr io.Writer, geteuid func() int) int {
	cfg, err := config.Load(env)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "docker-agent: invalid configuration:\n%v\n", err)
		return exitConfig
	}
	logger := logging.New(stderr, cfg.LogLevel, cfg.LogFormat)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runtime.Run(ctx, runtime.Options{Config: cfg, Logger: logger, Geteuid: geteuid, Observe: true, Files: true, ContainerIO: true, Backups: true}); err != nil {
		logger.Error("docker-agent stopped with error", "error", err)
		return exitFail
	}
	return exitOK
}

// enrollCmd hands a token to the running agent through its state directory
// and waits for the outcome. The token is read from stdin (or -token-file),
// never from the command line, so it does not appear in process lists.
func enrollCmd(args []string, env envconfig.Source, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	fs.SetOutput(stderr)
	tokenFile := fs.String("token-file", "", "read the token from this file instead of stdin")
	wait := fs.Duration("wait", 90*time.Second, "how long to wait for the enrollment and the session (0: do not wait)")
	if err := fs.Parse(args); err != nil {
		return exitConfig
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintln(stderr, "enroll: pass the token on stdin or with -token-file, not as an argument")
		return exitConfig
	}
	r := stdin
	if *tokenFile != "" {
		f, err := os.Open(*tokenFile)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "enroll:", err)
			return exitConfig
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	line, err := bufio.NewReader(io.LimitReader(r, 4096)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		_, _ = fmt.Fprintln(stderr, "enroll: cannot read the token:", err)
		return exitConfig
	}
	token := strings.TrimSpace(line)
	if token == "" {
		_, _ = io.WriteString(stderr, "enroll: no token on stdin (printf '%s\\n' \"$TOKEN\" | docker-agent enroll)\n")
		return exitConfig
	}
	stateDir := env.String(config.EnvStateDir, config.DefaultStateDir)
	if abs, err := filepath.Abs(stateDir); err == nil {
		stateDir = abs
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := runtime.HandOver(ctx, stateDir, token, *wait, time.Second, clock.Real())
	switch {
	case err != nil && res.Enrollment == nil && !errors.Is(err, runtime.ErrHandoverTimeout):
		_, _ = fmt.Fprintln(stderr, "enroll:", err)
		return exitConfig
	case *wait <= 0:
		_, _ = fmt.Fprintln(stdout, "token handed over; the agent enrolls within a few seconds (see its logs)")
		return exitOK
	case res.Enrollment == nil:
		_, _ = fmt.Fprintln(stderr, "enroll:", runtime.ErrHandoverTimeout)
		return exitTimeout
	case res.Enrollment.Status != "enrolled":
		_, _ = fmt.Fprintf(stderr, "enrollment failed (%s): %s\n", res.Enrollment.Code, res.Enrollment.Message)
		return exitFail
	case res.Online:
		_, _ = fmt.Fprintf(stdout, "enrolled: agent %s, environment %s; the environment is online\n", res.Enrollment.AgentID, res.Enrollment.EnvironmentID)
		return exitOK
	}
	status := "unknown"
	if res.Health != nil {
		status = res.Health.Status
	}
	_, _ = fmt.Fprintf(stdout, "enrolled: agent %s, environment %s; the session is not online yet (status %s)\n",
		res.Enrollment.AgentID, res.Enrollment.EnvironmentID, status)
	return exitTimeout
}
