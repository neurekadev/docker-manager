package compose

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/pkg/authconfig"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/engine/enginetest"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func newAdapter(t *testing.T, fake *enginetest.Engine) *Adapter {
	t.Helper()
	ctx := testutil.Context(t)
	eng, err := engine.Connect(ctx, engine.Options{Host: fake.Host, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	a, err := New(ctx, Options{Host: fake.Host, Engine: eng, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

const stackYAML = `
version: "3.9"
name: shop
services:
  db:
    image: registry.example:5000/db:${DB_TAG}
    env_file: [db.env]
    volumes:
      - ./data:/var/lib/db
    healthcheck:
      test: ["CMD", "/check"]
  migrate:
    image: registry.example:5000/migrate:1
    depends_on:
      db:
        condition: service_healthy
  web:
    build:
      context: ./web
      args:
        LEAK: ${DOCKYARD_ENROLLMENT_TOKEN:-not-from-agent-env}
    depends_on:
      db:
        condition: service_healthy
        restart: true
      migrate:
        condition: service_completed_successfully
      cache:
        condition: service_started
        required: false
  cache:
    image: registry.example:5000/cache:1
    profiles: [extras]
`

func TestLoadProject(t *testing.T) {
	// The agent's own environment must never reach interpolation.
	t.Setenv("DOCKYARD_ENROLLMENT_TOKEN", "agent-secret-token")
	t.Setenv("DB_TAG", "from-agent-env")
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"compose.yaml":          stackYAML,
		"compose.override.yaml": "services:\n  web:\n    environment:\n      MODE: override\n",
		".env":                  "DB_TAG=16\n",
		"db.env":                "POSTGRES_PASSWORD=pw\n",
		"web/Dockerfile":        "FROM scratch\n",
	})
	a := newAdapter(t, enginetest.Start(t, enginetest.Options{}))
	p, err := a.Load(testutil.Context(t), ProjectSpec{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "shop" || len(p.ConfigFiles) != 2 || !strings.HasSuffix(p.ConfigFiles[1], "compose.override.yaml") {
		t.Errorf("project %s files %v", p.Name, p.ConfigFiles)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "version") {
		t.Errorf("warnings %v", p.Warnings)
	}
	m := p.model
	if img := m.Services["db"].Image; img != "registry.example:5000/db:16" {
		t.Errorf("db image %q: .env must drive interpolation, not the agent environment", img)
	}
	if v := m.Services["web"].Build.Args["LEAK"]; v == nil || *v != "not-from-agent-env" {
		t.Errorf("agent environment leaked into interpolation: %v", v)
	}
	if env := m.Services["db"].Environment["POSTGRES_PASSWORD"]; env == nil || *env != "pw" {
		t.Error("env_file not loaded")
	}
	if src := m.Services["db"].Volumes[0].Source; src != filepath.Join(dir, "data") {
		t.Errorf("relative bind resolved to %q, want %q", src, filepath.Join(dir, "data"))
	}
	if m.Services["web"].Build.Context != filepath.Join(dir, "web") {
		t.Errorf("build context %q", m.Services["web"].Build.Context)
	}
	if _, ok := m.Services["cache"]; ok {
		t.Error("service of a disabled profile loaded")
	}
	labels := m.Services["web"].CustomLabels
	if labels[api.ProjectLabel] != "shop" || labels[api.ServiceLabel] != "web" || labels[api.WorkingDirLabel] != dir {
		t.Errorf("compose labels %v", labels)
	}
	var web ServiceInfo
	for _, s := range p.Services {
		if s.Name == "web" {
			web = s
		}
	}
	want := []Dependency{
		{Service: "cache", Condition: "service_started", Required: false},
		{Service: "db", Condition: "service_healthy", Required: true, Restart: true},
		{Service: "migrate", Condition: "service_completed_successfully", Required: true},
	}
	if !web.Build || web.Image != "shop-web" || len(web.DependsOn) != 3 {
		t.Fatalf("web %+v", web)
	}
	for i, d := range want {
		if web.DependsOn[i] != d {
			t.Errorf("dependency %d = %+v, want %+v", i, web.DependsOn[i], d)
		}
	}

	// Operations work on copies: adjusting a service for one operation
	// never changes the loaded project.
	sel, err := selected(p, []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Services["web"]
	w.PullPolicy = types.PullPolicyNever
	sel.Services["web"] = w
	if p.model.Services["web"].PullPolicy == types.PullPolicyNever {
		t.Error("selected() returned the loaded model instead of a copy")
	}

	// Profiles enable services; explicit files and env files stay inside Dir.
	p, err = a.Load(testutil.Context(t), ProjectSpec{Dir: dir, Name: "shop2", ConfigFiles: []string{"compose.yaml"}, Profiles: []string{"extras"}})
	if err != nil || p.Name != "shop2" || p.model.Services["cache"].Name != "cache" {
		t.Fatalf("with profile: %v", err)
	}
	for _, spec := range []ProjectSpec{
		{Dir: dir, ConfigFiles: []string{"../compose.yaml"}},
		{Dir: dir, EnvFiles: []string{"/etc/passwd"}},
		{Dir: "relative"},
		{Dir: filepath.Join(dir, "web")},
		{Dir: dir, Name: "Bad Name"},
	} {
		if _, err := a.Load(testutil.Context(t), spec); engine.CodeOf(err) != engine.CodeInvalidProject {
			t.Errorf("Load(%+v) = %v, want invalid_project", spec, err)
		}
	}
}

func TestLoadRejectsUnsupportedFeatures(t *testing.T) {
	a := newAdapter(t, enginetest.Start(t, enginetest.Options{}))
	for name, svc := range map[string]string{
		"build secrets":   "build:\n      context: .\n      secrets: [token]",
		"build ssh":       "build:\n      context: .\n      ssh: [default]",
		"extra contexts":  "build:\n      context: .\n      additional_contexts:\n        base: ../base",
		"cache_to":        "build:\n      context: .\n      cache_to: [type=local,dest=/tmp/c]",
		"multi-platform":  "build:\n      context: .\n      platforms: [linux/amd64, linux/arm64]",
		"privileged":      "build:\n      context: .\n      privileged: true",
		"git inline":      "build:\n      context: https://example.com/r.git\n      dockerfile_inline: FROM scratch",
		"ssh git":         "build:\n      context: git@github.com:x/y.git",
		"use_api_socket":  "image: alpine\n    use_api_socket: true",
		"provider":        "provider:\n      type: awesomecloud",
		"service models":  "image: alpine\n    models: [llm]",
		"build.network":   "build:\n      context: .\n      network: mynet",
		"attestations":    "build:\n      context: .\n      provenance: mode=max",
		"no_cache_filter": "build:\n      context: .\n      no_cache_filter: [x]",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			yaml := "services:\n  app:\n    " + svc + "\n"
			if strings.Contains(svc, "secrets:") {
				yaml += "secrets:\n  token:\n    file: ./token.txt\n"
			}
			if strings.Contains(svc, "models:") {
				yaml += "models:\n  llm:\n    model: ai/smollm2\n"
			}
			writeFiles(t, dir, map[string]string{"compose.yaml": yaml, "token.txt": "x", "Dockerfile": "FROM scratch\n"})
			_, err := a.Load(testutil.Context(t), ProjectSpec{Dir: dir, Name: "p"})
			if engine.CodeOf(err) != engine.CodeUnsupportedFeature {
				t.Fatalf("Load = %v (%s), want unsupported_compose_feature", err, engine.CodeOf(err))
			}
		})
	}
}

func TestBuildSpecMapping(t *testing.T) {
	arg := "1.2"
	s := types.ServiceConfig{Name: "web", Build: &types.BuildConfig{
		Context: "/stacks/shop/web", Dockerfile: "docker/Dockerfile", Args: types.MappingWithEquals{"VERSION": &arg, "UNSET": nil},
		Target: "prod", Labels: types.Labels{"a": "b"}, Tags: []string{"shop/web:extra"}, Platforms: []string{"linux/amd64"},
		Network: "host", Pull: true,
	}}
	spec, err := buildSpec(s, "shop-web", "linux/amd64", buildRequest{noCache: true})
	if err != nil {
		t.Fatal(err)
	}
	if spec.ContextDir != "/stacks/shop/web" || spec.Dockerfile != "docker/Dockerfile" || spec.Target != "prod" ||
		spec.BuildArgs["VERSION"] != "1.2" || len(spec.BuildArgs) != 1 || !spec.NoCache || !spec.Pull ||
		strings.Join(spec.Tags, ",") != "shop-web,shop/web:extra" || spec.NetworkMode != "host" || spec.Labels["a"] != "b" {
		t.Errorf("spec %+v", spec)
	}
	if _, err := buildSpec(s, "shop-web", "linux/arm64", buildRequest{}); err == nil {
		t.Error("foreign build platform accepted")
	}
	s.Build = &types.BuildConfig{Context: "https://git.example/app.git#main", DockerfileInline: ""}
	spec, err = buildSpec(s, "x", "linux/amd64", buildRequest{})
	if err != nil || spec.RemoteContext != "https://git.example/app.git#main" || spec.ContextDir != "" {
		t.Errorf("git context: %+v %v", spec, err)
	}
}

// TestCredentialsNeverTouchDisk runs a Compose pull through the SDK with a
// per-operation credential and proves that (a) the Engine receives exactly
// that credential, (b) the Docker config on disk (which configures a
// credential helper and a wrong password as traps) is neither read nor
// written, and (c) nothing else is written below HOME/DOCKER_CONFIG.
func TestCredentialsNeverTouchDisk(t *testing.T) {
	home, dockerConfig, xdg := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DOCKER_CONFIG", dockerConfig)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("DOCKER_AUTH_CONFIG", `{"auths":{"registry.example:5000":{"auth":"`+base64.StdEncoding.EncodeToString([]byte("env:wrong"))+`"}}}`)
	trap := []byte(`{"credsStore":"dockyard-trap","credHelpers":{"registry.example:5000":"dockyard-trap"},` +
		`"auths":{"registry.example:5000":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("disk:wrong")) + `"}}}`)
	if err := os.WriteFile(filepath.Join(dockerConfig, "config.json"), trap, 0o600); err != nil {
		t.Fatal(err)
	}

	canaries := canary.New()
	password := canaries.New(canary.RegistryCredential, "registry password")
	hubToken := canaries.New(canary.RegistryCredential, "docker hub token")

	fake := enginetest.Start(t, enginetest.Options{})
	fake.Handle(http.MethodGet, "/images/json", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.JSON(w, http.StatusOK, []any{})
	})
	fake.Handle(http.MethodGet, "/images/.*/json", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.Error(w, http.StatusNotFound, "No such image")
	})
	fake.Handle(http.MethodPost, "/images/create", func(w http.ResponseWriter, _ *http.Request) {
		enginetest.Stream(w, map[string]any{"status": "Pull complete"})
	})
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"compose.yaml": `
services:
  app:
    image: registry.example:5000/team/app:1
  hub:
    image: library/busybox:1.37
  other:
    image: other.example/x:1
`})
	var sdkOut bytes.Buffer
	logger := canaries.CaptureLogger(t)
	ctx := testutil.Context(t)
	eng, err := engine.Connect(ctx, engine.Options{Host: fake.Host, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	a, err := New(ctx, Options{Host: fake.Host, Engine: eng, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if os.Getenv("DOCKER_AUTH_CONFIG") != "" {
		t.Error("DOCKER_AUTH_CONFIG must be ignored (unset) by the adapter")
	}
	p, err := a.Load(ctx, ProjectSpec{Dir: dir, Name: "creds"})
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	err = a.Pull(ctx, p, RunOptions{
		Auth: []engine.RegistryAuth{
			{ServerAddress: "registry.example:5000", Username: "dockyard", Password: logging.Secret(password)},
			{ServerAddress: "docker.io", Username: "hubuser", Password: logging.Secret(hubToken)},
		},
		Output: &sdkOut,
		Events: func(e Event) { events = append(events, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, r := range fake.Find(http.MethodPost, "/images/create") {
		ac, err := authconfig.Decode(r.Header.Get("X-Registry-Auth"))
		if err != nil {
			t.Fatal(err)
		}
		seen[r.Query.Get("fromImage")] = ac.Username + ":" + ac.Password
	}
	if seen["registry.example:5000/team/app"] != "dockyard:"+password {
		t.Errorf("private registry pull used %q", seen["registry.example:5000/team/app"])
	}
	if seen["docker.io/library/busybox"] != "hubuser:"+hubToken {
		t.Errorf("Docker Hub pull used %q (all: %v)", seen["docker.io/library/busybox"], seen)
	}
	if seen["other.example/x"] != ":" {
		t.Errorf("a registry without a credential got %q", seen["other.example/x"])
	}
	if len(events) == 0 {
		t.Error("no progress events")
	}

	// The on-disk config is untouched and nothing else was written.
	if b, _ := os.ReadFile(filepath.Join(dockerConfig, "config.json")); !bytes.Equal(b, trap) {
		t.Error("the Docker config file was modified")
	}
	for _, d := range []string{home, dockerConfig, xdg} {
		_ = filepath.WalkDir(d, func(path string, e fs.DirEntry, err error) error {
			if err == nil && path != d && path != filepath.Join(dockerConfig, "config.json") {
				t.Errorf("written to disk: %s", path)
			}
			return nil
		})
	}
	canaries.AssertClean(t, "SDK output", sdkOut.String())
	canaries.AssertClean(t, "progress events", events)

	// The in-memory config can never be saved.
	cli := newMemoryCLI(nil, fake.Host, "linux", []engine.RegistryAuth{{ServerAddress: "r", Password: "x"}}, &sdkOut)
	if err := cli.ConfigFile().Save(); err == nil {
		t.Error("in-memory config file saved")
	}
	if on, _ := cli.BuildKitEnabled(); on {
		t.Error("BuildKitEnabled must be false so the SDK never looks for buildx")
	}
	if cli.ConfigFile().CredentialsStore != "" || len(cli.ConfigFile().CredentialHelpers) != 0 {
		t.Error("credential helpers configured")
	}
}

func TestMapsComposeErrors(t *testing.T) {
	for msg, want := range map[string]engine.Code{
		"dependency failed to start: container shop-db-1 is unhealthy":      engine.CodeDependencyFailed,
		`service "migrate" didn't complete successfully: exit 1`:            engine.CodeDependencyFailed,
		"Error response from daemon: No such container: x (not found)":      engine.CodeEngineError,
		"toomanyrequests: You have reached your unauthenticated pull limit": engine.CodeRateLimited,
	} {
		if got := engine.CodeOf(composeError("compose.up", errors.New(msg))); got != want {
			t.Errorf("%q -> %s, want %s", msg, got, want)
		}
	}
	if composeError("x", nil) != nil {
		t.Error("nil error mapped")
	}
}
