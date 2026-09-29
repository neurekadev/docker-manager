package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// TestInstallCommandsRemoteRunLabelsTheAgent: the docker run variant marks
// its container with the agent role label, like the documented compose
// files, so other agents on the host protect it too (#32).
func TestInstallCommandsRemoteRunLabelsTheAgent(t *testing.T) {
	cmds := InstallCommands("https://docker.example.com", DefaultAgentImage, "dye_token", "")
	var run string
	for _, c := range cmds {
		if c.Variant == InstallRemote {
			run = c.Command
		}
	}
	if run == "" {
		t.Fatalf("no %s variant in %+v", InstallRemote, cmds)
	}
	label := "--label " + protocol.LabelRole + "=agent"
	if label != "--label docker-manager.role=agent" || !strings.Contains(run, label) {
		t.Errorf("docker run command lacks %q:\n%s", label, run)
	}
	// The label prefix changed: new installations get only the new key.
	if strings.Contains(run, protocol.LegacyLabelPrefix) {
		t.Errorf("docker run command writes a legacy label:\n%s", run)
	}
	if strings.Index(run, label) > strings.Index(run, DefaultAgentImage) {
		t.Errorf("label must come before the image:\n%s", run)
	}
}

// TestMoveFilesAreTheQuickstartsPlusTheMoveLines: the new server's
// compose.yaml of a move is the Quickstart's with the move variables read
// from .env (and defaults that leave the Quickstart's setup once the move
// lines are removed); the .env carries the code, the old manager's address
// for the waiting manager and the agent, and the token.
func TestMoveFilesAreTheQuickstartsPlusTheMoveLines(t *testing.T) {
	compose, env := MoveFiles(MoveFilesInput{PublicURL: "https://docker.example.com", OldManagerURL: "http://192.168.1.10:8080",
		MoveCode: "dmm_move_code", EnrollmentToken: "dye_token", EnvironmentName: "new server"})
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "public", "content", "docs", "quickstart.mdx"))
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.ReplaceAll(string(raw), "\r\n", "\n")
	start := strings.Index(doc, "```yaml title=\"compose.yaml\"\n")
	if start < 0 {
		t.Fatal("the Quickstart has no compose.yaml block")
	}
	block := doc[start+len("```yaml title=\"compose.yaml\"\n"):]
	block = block[:strings.Index(block, "```")]
	var kept []string
	for _, line := range strings.Split(compose, "\n") {
		switch {
		case strings.Contains(line, "DOCKER_MANAGER_MOVE_"), strings.Contains(line, "DOCKER_AGENT_ENROLLMENT_TOKEN"),
			strings.Contains(line, "DOCKER_AGENT_ENVIRONMENT_NAME"):
			continue
		}
		kept = append(kept, strings.Replace(line, "${DOCKER_AGENT_MANAGER_URL:-"+MoveAgentManagerURL+"}", MoveAgentManagerURL, 1))
	}
	if got := strings.Join(kept, "\n"); got != block {
		t.Errorf("the move's compose.yaml without the move lines differs from the Quickstart's:\n%s\n--- Quickstart ---\n%s", got, block)
	}
	for _, want := range []string{"DOCKER_MANAGER_PUBLIC_URL=https://docker.example.com\n", "DOCKER_MANAGER_TRUSTED_PROXIES=" + DefaultTrustedProxies + "\n",
		"DOCKER_MANAGER_MOVE_FROM=http://192.168.1.10:8080\n", "DOCKER_MANAGER_MOVE_CODE=dmm_move_code\n",
		"DOCKER_AGENT_MANAGER_URL=http://192.168.1.10:8080\n", "DOCKER_AGENT_ENROLLMENT_TOKEN=dye_token\n",
		"DOCKER_AGENT_ENVIRONMENT_NAME='new server'\n"} {
		if !strings.Contains(env, want) {
			t.Errorf(".env lacks %q:\n%s", want, env)
		}
	}
	_, env = MoveFiles(MoveFilesInput{PublicURL: "https://docker.example.com", TrustedProxies: "10.0.0.5/32", OldManagerURL: "http://a:8080",
		MoveCode: "c", EnrollmentToken: "t"})
	if !strings.Contains(env, "DOCKER_MANAGER_TRUSTED_PROXIES=10.0.0.5/32\n") || strings.Contains(env, "DOCKER_AGENT_ENVIRONMENT_NAME") {
		t.Errorf(".env with the old manager's proxies:\n%s", env)
	}
	// New setup files after the agent enrolled: no token, no name; the
	// agent still dials the old manager (its credential is there).
	_, env = MoveFiles(MoveFilesInput{PublicURL: "https://docker.example.com", OldManagerURL: "http://a:8080", MoveCode: "c2",
		EnvironmentName: "new server"})
	if strings.Contains(env, "DOCKER_AGENT_ENROLLMENT_TOKEN") || strings.Contains(env, "DOCKER_AGENT_ENVIRONMENT_NAME") ||
		!strings.Contains(env, "DOCKER_AGENT_MANAGER_URL=http://a:8080\n") || !strings.Contains(env, "DOCKER_MANAGER_MOVE_CODE=c2\n") {
		t.Errorf(".env of an enrolled agent:\n%s", env)
	}
}
