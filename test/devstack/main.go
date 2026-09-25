// Command devstack runs a real DockYard manager on localhost with two
// in-process agents over in-memory fake Docker Engines, seeded with a small
// homelab, for UI work and Playwright WITHOUT Docker (#22).
//
//	go run ./test/devstack               # seeded, owner "admin" signed up
//	go run ./test/devstack -setup        # first-run setup still open
//	go run ./test/devstack -addr 127.0.0.1:8090 -keep
//
// It is a test tool: it lives under test/, is never built into images or
// the build-static release binaries, and must never be exposed beyond
// localhost (plain-HTTP local development mode, printed credentials).
// Everything between the public API and the Engine adapter is production
// code; only the Docker Engines, host metrics, Compose reads and container
// logs are simulated. Documentation: docs/development.md, "UI devstack".
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	_ "time/tzdata" // IANA zones for schedules

	"github.com/neurekadev/dockyard/internal/envconfig"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/app"
	"github.com/neurekadev/dockyard/internal/manager/config"
	"github.com/neurekadev/dockyard/web"
)

// Printed credentials of the seeded accounts (localhost only).
const (
	ownerUser     = "admin"
	ownerPassword = "dockyard-devstack-owner" //nolint:gosec // G101: a published test-only credential of the local devstack
	guestUser     = "guest"
	guestPassword = "dockyard-devstack-guest" //nolint:gosec // G101: a published test-only credential of the local devstack
)

type options struct {
	addr     string
	dataDir  string
	uiDir    string
	keep     bool
	setup    bool
	logLevel string
}

func main() {
	var o options
	flag.StringVar(&o.addr, "addr", "127.0.0.1:8080", "listen address (loopback only)")
	flag.StringVar(&o.dataDir, "data", filepath.Join(os.TempDir(), "dockyard-devstack"), "data directory")
	flag.StringVar(&o.uiDir, "ui", "", "built UI directory (default: web/build/app of this checkout, else the embedded UI)")
	flag.BoolVar(&o.keep, "keep", false, "keep the data directory of a previous run (default: start fresh)")
	flag.BoolVar(&o.setup, "setup", false, "leave first-run setup open: no accounts and no jobs are seeded")
	flag.StringVar(&o.logLevel, "log-level", "warn", "manager log level (debug, info, warn, error)")
	flag.Parse()
	if err := run(o); err != nil {
		fmt.Fprintln(os.Stderr, "devstack:", err)
		os.Exit(1)
	}
}

func run(o options) error {
	host, port, err := net.SplitHostPort(o.addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("refusing to listen on %s: the devstack is for localhost only", o.addr)
	}
	if !o.keep {
		if err := os.RemoveAll(o.dataDir); err != nil {
			return fmt.Errorf("reset %s: %w", o.dataDir, err)
		}
	}
	cfg, err := config.Load(envconfig.Map(map[string]string{
		"DOCKYARD_PUBLIC_URL":  "http://localhost:" + port,
		"DOCKYARD_LISTEN_ADDR": o.addr,
		"DOCKYARD_DATA_DIR":    filepath.Join(o.dataDir, "manager"),
		"DOCKYARD_LOG_LEVEL":   o.logLevel,
		"DOCKYARD_LOG_FORMAT":  "text",
	}, nil))
	if err != nil {
		return err
	}
	logger := logging.New(os.Stderr, cfg.LogLevel, cfg.LogFormat)
	ui, err := uiAssets(o.uiDir)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	started := make(chan *app.Manager, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Run(ctx, app.Options{Config: cfg, Logger: logger, UI: ui, UIBuilt: true,
			OnStarted: func(m *app.Manager) {
				select {
				case started <- m:
				default:
				}
			}})
	}()
	var m *app.Manager
	select {
	case m = <-started:
	case err := <-errCh:
		return err
	}
	base := "http://localhost:" + port
	s := &seeder{m: m, base: base, dataDir: o.dataDir, log: logger.With("component", "devstack")}
	if err := s.seed(ctx, !o.setup); err != nil {
		stop()
		<-errCh
		return fmt.Errorf("seed: %w", err)
	}
	s.printSummary(os.Stdout, !o.setup)
	err = <-errCh
	s.stopAgents()
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// uiAssets serves the UI from disk when it was built in this checkout
// (so `npm --prefix web run build` needs no Go rebuild), else the
// embedded assets.
func uiAssets(dir string) (fs.FS, error) {
	if dir == "" {
		if root, err := repoRoot(); err == nil {
			cand := filepath.Join(root, "web", "build", "app")
			if _, err := os.Stat(filepath.Join(cand, "index.html")); err == nil {
				dir = cand
			}
		}
	}
	if dir == "" {
		ui, _ := web.Assets()
		slog.Warn("devstack: web/build/app not found; serving the embedded UI (run npm --prefix web run build)")
		return ui, nil
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		return nil, fmt.Errorf("no index.html in %s: build the UI first (npm --prefix web run build)", dir)
	}
	return os.DirFS(dir), nil
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found")
		}
		dir = parent
	}
}
