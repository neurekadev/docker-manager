package agents

import (
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
	if !strings.Contains(run, label) {
		t.Errorf("docker run command lacks %q:\n%s", label, run)
	}
	if strings.Index(run, label) > strings.Index(run, DefaultAgentImage) {
		t.Errorf("label must come before the image:\n%s", run)
	}
}
