// Command dockyard-manager is the DockYard manager: web UI, public API,
// agent endpoint, jobs and persistence.
//
// Usage:
//
//	dockyard-manager [serve]          run the manager (default)
//	dockyard-manager healthcheck      probe the local /api/v1/health (image HEALTHCHECK)
//	dockyard-manager openapi [-format json|yaml]   print the OpenAPI 3.1 spec
//	dockyard-manager version          print build information
//	dockyard-manager owner-recovery   reserved for owner account recovery (#16)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // IANA zones for schedules without relying on the image

	"github.com/neurekadev/dockyard/internal/buildinfo"
	"github.com/neurekadev/dockyard/internal/envconfig"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/api"
	"github.com/neurekadev/dockyard/internal/manager/app"
	"github.com/neurekadev/dockyard/internal/manager/config"
	"github.com/neurekadev/dockyard/web"
)

// Exit codes.
const (
	exitOK     = 0
	exitFail   = 1
	exitConfig = 2
)

func main() {
	os.Exit(run(os.Args[1:], envconfig.OS(), os.Stdout, os.Stderr))
}

func run(args []string, env envconfig.Source, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return serve(env, stderr)
	case "healthcheck":
		return healthcheck(env, stderr)
	case "openapi":
		return openapi(args, stdout, stderr)
	case "version", "--version", "-v":
		_, _ = fmt.Fprintln(stdout, "dockyard-manager", buildinfo.Get())
		return exitOK
	case "owner-recovery":
		// TODO(#16): offline owner recovery against the data volume.
		_, _ = fmt.Fprintln(stderr, "owner-recovery is not implemented yet (see issue #16)")
		return exitConfig
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
	_, _ = fmt.Fprint(w, `Usage: dockyard-manager [command]

Commands:
  serve            run the manager (default)
  healthcheck      exit 0 if the local manager reports healthy
  openapi          print the OpenAPI 3.1 spec (-format json|yaml)
  version          print build information
  owner-recovery   reserved (#16)

Configuration is read from environment variables; see docs/configuration.md.
`)
}

func serve(env envconfig.Source, stderr io.Writer) int {
	cfg, err := config.Load(env)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "dockyard-manager: invalid configuration:\n%v\n", err)
		return exitConfig
	}
	logger := logging.New(stderr, cfg.LogLevel, cfg.LogFormat)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ui, built := web.Assets()
	if err := app.Run(ctx, app.Options{Config: cfg, Logger: logger, UI: ui, UIBuilt: built}); err != nil {
		logger.Error("dockyard-manager stopped with error", "error", err)
		return exitFail
	}
	logger.Info("dockyard-manager stopped")
	return exitOK
}

// healthcheck probes the liveness endpoint on the loopback interface.
func healthcheck(env envconfig.Source, stderr io.Writer) int {
	target, err := healthURL(env.String(config.EnvListenAddr, config.DefaultListenAddr))
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "healthcheck:", err)
		return exitFail
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "healthcheck:", err)
		return exitFail
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "healthcheck:", err)
		return exitFail
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintln(stderr, "healthcheck: status", resp.StatusCode)
		return exitFail
	}
	return exitOK
}

// healthURL maps a listen address to a loopback URL for the health probe.
func healthURL(listenAddr string) (string, error) {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "", fmt.Errorf("invalid %s %q: %w", config.EnvListenAddr, listenAddr, err)
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + api.BasePath + "/health", nil
}

func openapi(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("openapi", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "json", "output format: json or yaml")
	if err := fs.Parse(args); err != nil {
		return exitConfig
	}
	var (
		b   []byte
		err error
	)
	switch *format {
	case "json":
		b, err = api.SpecJSON()
	case "yaml":
		b, err = api.SpecYAML()
	default:
		err = errors.New("format must be json or yaml")
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "openapi:", err)
		return exitFail
	}
	if _, err := stdout.Write(b); err != nil {
		return exitFail
	}
	return exitOK
}
