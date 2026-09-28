package agents

import (
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// Install command variants.
const (
	InstallColocated     = "colocated"
	InstallRemote        = "remote"
	InstallRemoteCompose = "remote_compose"
)

// InstallCommands renders the install commands for an enrollment token.
// managerURL is DOCKER_MANAGER_PUBLIC_URL; name is the optional preset display
// name.
//
// The remote variants mount the Docker socket and Docker's volume
// directory at its identical path (#28), as the agent's compose.yaml in the
// user documentation (Quickstart, "Add more servers") does; the
// socket path literal is the documented policy-check exception for this
// file (docs/internal/architecture/engine-integration.md).
func InstallCommands(managerURL, image, token, name string) []domain.InstallCommand {
	colocated := "printf '%s\\n' " + shellQuote(token) + " | docker compose exec -T docker-agent docker-agent enroll"

	var run strings.Builder
	run.WriteString("docker run -d --name docker-agent --restart unless-stopped \\\n")
	run.WriteString("  -e DOCKER_AGENT_MANAGER_URL=" + shellQuote(managerURL) + " \\\n")
	if name != "" {
		run.WriteString("  -e DOCKER_AGENT_ENVIRONMENT_NAME=" + shellQuote(name) + " \\\n")
	}
	run.WriteString("  -v /var/run/docker.sock:/var/run/docker.sock \\\n")
	run.WriteString("  -v /var/lib/docker/volumes:/var/lib/docker/volumes \\\n")
	run.WriteString("  -v docker-manager_stacks:/var/lib/docker/volumes/docker-manager_stacks/_data \\\n")
	run.WriteString("  -v docker-manager_agent:/var/lib/docker-agent \\\n")
	run.WriteString("  " + image + "\n")
	run.WriteString("printf '%s\\n' " + shellQuote(token) + " | docker exec -i docker-agent docker-agent enroll")

	var env strings.Builder
	env.WriteString("# .env next to the agent's compose.yaml\n")
	env.WriteString("DOCKER_AGENT_MANAGER_URL=" + managerURL + "\n")
	env.WriteString("DOCKER_AGENT_ENROLLMENT_TOKEN=" + token + "\n")
	if name != "" {
		env.WriteString("DOCKER_AGENT_ENVIRONMENT_NAME=" + envFileValue(name) + "\n")
	}
	env.WriteString("# then, in the same directory:\n")
	env.WriteString("docker compose up -d")

	return []domain.InstallCommand{
		{
			Variant: InstallColocated, Title: "Agent next to the manager",
			Description: "Run in the directory of the manager's compose.yaml. " +
				"The co-located agent already runs on the internal URL; it enrolls within seconds and the command prints the result.",
			Command: colocated,
		},
		{
			Variant: InstallRemote, Title: "Agent on another Docker host",
			Description: "Starts the agent with the manager's public HTTPS origin and hands it the token on stdin, " +
				"so the token never appears in the container configuration. Only this host's Docker socket and its volume directory are mounted; " +
				"Docker socket access confers host-level authority.",
			Command: run.String(),
		},
		{
			Variant: InstallRemoteCompose, Title: "Agent on another Docker host (Compose)",
			Description: "The same agent with Compose: put these lines in the .env file next to its compose.yaml. " +
				"Remove DOCKER_AGENT_ENROLLMENT_TOKEN after the agent enrolled; the used token cannot enroll again.",
			Command: env.String(),
		},
	}
}

// shellQuote quotes s for POSIX shells.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// envFileValue quotes a value for a Compose .env file: single quotes keep
// it literal (no interpolation); a value containing a single quote uses
// double quotes with escapes.
func envFileValue(s string) string {
	switch {
	case !strings.ContainsAny(s, " #'\"\\$`"):
		return s
	case !strings.Contains(s, "'"):
		return "'" + s + "'"
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(s) + `"`
}
