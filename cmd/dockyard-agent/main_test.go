package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/envconfig"
)

func runCmd(args []string, vars map[string]string, uid int) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(args, envconfig.Map(vars, nil), &out, &errOut, func() int { return uid })
	return code, out.String(), errOut.String()
}

func TestRunRefusesPlainHTTPManager(t *testing.T) {
	code, _, stderr := runCmd(nil, map[string]string{"DOCKYARD_MANAGER_URL": "http://dockyard-manager:8080"}, 0)
	if code != exitConfig || !strings.Contains(stderr, "refusing plain-HTTP") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}

func TestRunRefusesNonRoot(t *testing.T) {
	vars := map[string]string{
		"DOCKYARD_MANAGER_URL":     "https://docker.example.com",
		"DOCKYARD_AGENT_STATE_DIR": t.TempDir(),
	}
	code, _, stderr := runCmd([]string{"run"}, vars, 1000)
	if code != exitFail || !strings.Contains(stderr, "must run as root") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}

func TestHealthcheckWithoutHealthFileFails(t *testing.T) {
	code, _, stderr := runCmd([]string{"healthcheck"}, map[string]string{"DOCKYARD_AGENT_STATE_DIR": t.TempDir()}, 0)
	if code != exitFail || !strings.Contains(stderr, "health file") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}

func TestVersionAndUnknown(t *testing.T) {
	if code, out, _ := runCmd([]string{"version"}, nil, 0); code != exitOK || !strings.HasPrefix(out, "dockyard-agent ") {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, _, _ := runCmd([]string{"serve"}, nil, 0); code != exitConfig {
		t.Fatalf("unknown command exit %d", code)
	}
}
