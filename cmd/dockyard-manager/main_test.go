package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/neurekadev/dockyard/internal/envconfig"
	"github.com/neurekadev/dockyard/internal/manager/api"
)

func runCmd(args []string, vars map[string]string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(args, envconfig.Map(vars, nil), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestServeRejectsInvalidConfig(t *testing.T) {
	code, _, stderr := runCmd([]string{"serve"}, map[string]string{})
	if code != exitConfig || !strings.Contains(stderr, "DOCKYARD_PUBLIC_URL is required") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	code, _, stderr = runCmd(nil, map[string]string{"DOCKYARD_PUBLIC_URL": "http://docker.example.com"})
	if code != exitConfig || !strings.Contains(stderr, "must use https") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}

func TestOpenAPIAndVersion(t *testing.T) {
	code, out, _ := runCmd([]string{"openapi"}, nil)
	want, _ := api.SpecJSON()
	if code != exitOK || out != string(want) {
		t.Fatalf("openapi: code %d, output differs from SpecJSON", code)
	}
	code, out, _ = runCmd([]string{"openapi", "-format", "yaml"}, nil)
	if code != exitOK || !strings.Contains(out, "openapi: 3.1") {
		t.Fatalf("yaml: %d %.60q", code, out)
	}
	if code, _, _ := runCmd([]string{"openapi", "-format", "xml"}, nil); code == exitOK {
		t.Fatal("xml accepted")
	}
	code, out, _ = runCmd([]string{"version"}, nil)
	if code != exitOK || !strings.HasPrefix(out, "dockyard-manager ") {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, _, _ := runCmd([]string{"owner-recovery"}, nil); code != exitConfig {
		t.Fatalf("owner-recovery placeholder exit %d", code)
	}
	if code, _, _ := runCmd([]string{"frobnicate"}, nil); code != exitConfig {
		t.Fatalf("unknown command exit %d", code)
	}
}

func TestHealthURL(t *testing.T) {
	for in, want := range map[string]string{
		":8080":          "http://127.0.0.1:8080/api/v1/health",
		"0.0.0.0:9000":   "http://127.0.0.1:9000/api/v1/health",
		"[::]:8080":      "http://127.0.0.1:8080/api/v1/health",
		"10.0.0.5:8080":  "http://10.0.0.5:8080/api/v1/health",
		"[::1]:8080":     "http://[::1]:8080/api/v1/health",
		"localhost:8080": "http://localhost:8080/api/v1/health",
	} {
		got, err := healthURL(in)
		if err != nil || got != want {
			t.Errorf("healthURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := healthURL("8080"); err == nil {
		t.Error("expected error")
	}
}

func TestHealthcheckCommand(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	vars := map[string]string{"DOCKYARD_LISTEN_ADDR": ":" + port}
	if code, _, stderr := runCmd([]string{"healthcheck"}, vars); code != exitOK {
		t.Fatalf("healthy: %d %s", code, stderr)
	}
	status.Store(http.StatusServiceUnavailable)
	if code, _, _ := runCmd([]string{"healthcheck"}, vars); code != exitFail {
		t.Fatalf("unhealthy exit %d", code)
	}
}
