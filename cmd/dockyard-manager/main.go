// Command dockyard-manager is the DockYard manager: web UI, public API,
// agent endpoint, jobs and persistence.
//
// Usage:
//
//	dockyard-manager [serve]          run the manager (default)
//	dockyard-manager healthcheck      probe the local /api/v1/health (image HEALTHCHECK)
//	dockyard-manager openapi [-format json|yaml]   print the OpenAPI 3.1 spec
//	dockyard-manager enrollment create [flags]     create an agent enrollment token
//	dockyard-manager version          print build information
//	dockyard-manager owner-recovery   issue a one-time owner recovery code (#16)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // IANA zones for schedules without relying on the image

	"github.com/neurekadev/dockyard/internal/buildinfo"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/envconfig"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/agents"
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
	case "enrollment":
		return enrollment(args, env, stdout, stderr)
	case "version", "--version", "-v":
		_, _ = fmt.Fprintln(stdout, "dockyard-manager", buildinfo.Get())
		return exitOK
	case "owner-recovery":
		return ownerRecovery(env, stdout, stderr)
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
  enrollment create
                   create a one-use agent enrollment token and print the
                   install commands (run inside the manager container:
                   docker compose exec dockyard-manager dockyard-manager enrollment create)
                   flags: -name NAME, -intent new|replace:<agentId>|reattach:<environmentId>,
                          -ttl 1h, -allow-duplicate-engine-id, -json
  version          print build information
  owner-recovery   issue a one-time owner recovery code (run inside the
                   manager container; signs the owner out everywhere)

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

// ownerRecovery is the owner lockout break-glass (#16): it prints a
// one-time recovery code for the instance owner and signs the owner out.
// Run it where the manager's data volume is mounted:
//
//	docker exec dockyard-manager dockyard-manager owner-recovery
func ownerRecovery(env envconfig.Source, stdout, stderr io.Writer) int {
	cfg, err := config.Load(env)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "dockyard-manager: invalid configuration:\n%v\n", err)
		return exitConfig
	}
	logger := logging.New(stderr, cfg.LogLevel, cfg.LogFormat)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	code, err := app.OwnerRecovery(ctx, cfg, logger, nil)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "owner-recovery:", err)
		return exitFail
	}
	_, _ = fmt.Fprintf(stdout, `Owner recovery code (single use, valid until %s):

  %s

Open this link and choose a new password:

  %s

(or POST {"code": "...", "newPassword": "..."} to /api/v1/auth/password-resets/redemptions).
Every owner session has been signed out. Redeeming the code sets a new password and
removes the owner's TOTP, passkeys and recovery codes; sign in and enroll them again.
`, code.ExpiresAt.Format(time.RFC3339), code.Code, code.URL)
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

// enrollmentJSON is the -json output of `enrollment create` (the same
// member names as the API's CreatedAgentEnrollment).
type enrollmentJSON struct {
	EnrollmentID    string               `json:"enrollmentId"`
	Intent          string               `json:"intent"`
	Token           string               `json:"token"`
	ExpiresAt       time.Time            `json:"expiresAt"`
	ManagerURL      string               `json:"managerUrl"`
	InstallCommands []installCommandJSON `json:"installCommands"`
}

type installCommandJSON struct {
	Variant     string `json:"variant"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Command     string `json:"command"`
}

// enrollment implements `dockyard-manager enrollment create`.
func enrollment(args []string, env envconfig.Source, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "create" {
		_, _ = fmt.Fprintln(stderr, "usage: dockyard-manager enrollment create [-name NAME] [-intent new|replace:<agentId>|reattach:<environmentId>] [-ttl 1h] [-allow-duplicate-engine-id] [-json]")
		return exitConfig
	}
	fs := flag.NewFlagSet("enrollment create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "preset display name of the new environment")
	intent := fs.String("intent", "new", "new, replace:<agentId> or reattach:<environmentId>")
	ttl := fs.Duration("ttl", agents.DefaultEnrollmentTTL, "token lifetime (1m to 24h)")
	dup := fs.Bool("allow-duplicate-engine-id", false, "the host is a distinct machine sharing an enrolled Engine ID (cloned VM)")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return exitConfig
	}
	kind, target, err := agents.ParseIntent(*intent)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "enrollment create:", err)
		return exitConfig
	}
	cfg, err := config.Load(env)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "dockyard-manager: invalid configuration:\n%v\n", err)
		return exitConfig
	}
	logger := logging.New(stderr, cfg.LogLevel, cfg.LogFormat)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	created, err := app.CreateEnrollment(ctx, cfg, logger, domain.EnrollmentSpec{Intent: kind, TargetID: target, EnvironmentName: *name,
		TTL: *ttl, AllowDuplicateEngineID: *dup, CreatedBy: "cli"})
	if err != nil {
		var in *domain.InputError
		if errors.As(err, &in) {
			_, _ = fmt.Fprintf(stderr, "enrollment create: %s: %s\n", in.Field, in.Message)
			return exitConfig
		}
		_, _ = fmt.Fprintln(stderr, "enrollment create:", err)
		return exitFail
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		out := enrollmentJSON{EnrollmentID: created.Enrollment.ID, Intent: agents.FormatIntent(created.Enrollment),
			Token: created.Token, ExpiresAt: created.Enrollment.ExpiresAt, ManagerURL: created.ManagerURL, InstallCommands: []installCommandJSON{}}
		for _, c := range created.Install {
			out.InstallCommands = append(out.InstallCommands, installCommandJSON(c))
		}
		if err := enc.Encode(out); err != nil {
			return exitFail
		}
		return exitOK
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Enrollment %s (intent %s), valid until %s.\n", created.Enrollment.ID, agents.FormatIntent(created.Enrollment),
		created.Enrollment.ExpiresAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "One-use token (shown only now): %s\n", created.Token)
	for _, c := range created.Install {
		fmt.Fprintf(&b, "\n# %s\n# %s\n%s\n", c.Title, c.Description, c.Command)
	}
	_, _ = io.WriteString(stdout, b.String())
	return exitOK
}
