package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/envconfig"
)

func runCmd(args []string, vars map[string]string, uid int) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(args, envconfig.Map(vars, nil), &out, &errOut, func() int { return uid })
	return code, out.String(), errOut.String()
}

func TestRunRefusesPlainHTTPManager(t *testing.T) {
	code, _, stderr := runCmd(nil, map[string]string{"DOCKER_AGENT_MANAGER_URL": "http://docker-manager:8080"}, 0)
	if code != exitConfig || !strings.Contains(stderr, "refusing plain-HTTP") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}

func TestRunRefusesNonRoot(t *testing.T) {
	vars := map[string]string{
		"DOCKER_AGENT_MANAGER_URL": "https://docker.example.com",
		"DOCKER_AGENT_STATE_DIR":   t.TempDir(),
	}
	code, _, stderr := runCmd([]string{"run"}, vars, 1000)
	if code != exitFail || !strings.Contains(stderr, "must run as root") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}

func TestHealthcheckWithoutHealthFileFails(t *testing.T) {
	code, _, stderr := runCmd([]string{"healthcheck"}, map[string]string{"DOCKER_AGENT_STATE_DIR": t.TempDir()}, 0)
	if code != exitFail || !strings.Contains(stderr, "health file") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}

func TestVersionAndUnknown(t *testing.T) {
	if code, out, _ := runCmd([]string{"version"}, nil, 0); code != exitOK || !strings.HasPrefix(out, "docker-agent ") {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, _, _ := runCmd([]string{"serve"}, nil, 0); code != exitConfig {
		t.Fatalf("unknown command exit %d", code)
	}
}

func TestEnrollCommandHandsOverToken(t *testing.T) {
	dir := t.TempDir()
	vars := map[string]string{"DOCKER_AGENT_STATE_DIR": dir}
	withStdin := func(s string) {
		old := stdin
		stdin = strings.NewReader(s)
		t.Cleanup(func() { stdin = old })
	}
	withStdin("")
	if code, _, stderr := runCmd([]string{"enroll"}, vars, 0); code != exitConfig || !strings.Contains(stderr, "no token on stdin") {
		t.Fatalf("empty stdin: %d %q", code, stderr)
	}
	if code, _, stderr := runCmd([]string{"enroll", "dye_x_y"}, vars, 0); code != exitConfig || !strings.Contains(stderr, "not as an argument") {
		t.Fatalf("token argument: %d %q", code, stderr)
	}
	withStdin("dya_not_a_token\n")
	if code, _, stderr := runCmd([]string{"enroll", "-wait", "0"}, vars, 0); code != exitConfig || !strings.Contains(stderr, "not an enrollment token") {
		t.Fatalf("credential instead of token: %d %q", code, stderr)
	}
	withStdin("dye_0190a6e0-0000-7000-8000-000000000001_secret\n")
	code, stdout, stderr := runCmd([]string{"enroll", "-wait", "0"}, vars, 0)
	if code != exitOK || !strings.Contains(stdout, "handed over") {
		t.Fatalf("handover: %d %q %q", code, stdout, stderr)
	}
	b, err := os.ReadFile(filepath.Join(dir, "enrollment-token"))
	if err != nil || strings.TrimSpace(string(b)) != "dye_0190a6e0-0000-7000-8000-000000000001_secret" {
		t.Fatalf("token file %q %v", b, err)
	}
	if strings.Contains(stdout+stderr, "secret") {
		t.Fatal("token echoed")
	}
	tokenFile := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(tokenFile, []byte("dye_0190a6e0-0000-7000-8000-000000000002_other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCmd([]string{"enroll", "-wait", "0", "-token-file", tokenFile}, vars, 0); code != exitOK {
		t.Fatalf("token file: %d %q", code, stderr)
	}
}
