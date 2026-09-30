package agents

import (
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
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
// The docker run variant sets the agent role label like the compose files,
// so other agents on the host also see the container as Docker Manager's.
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
	run.WriteString("  --label " + protocol.LabelRole + "=agent \\\n")
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

// Moving Docker Manager to a new server (docs/internal/architecture/
// manager-move.md): the new server's compose.yaml and .env.
const (
	// DefaultManagerImage is the manager image of the generated compose.yaml.
	DefaultManagerImage = "code.neureka.dev/docker-manager/docker-manager:edge"
	// DefaultTrustedProxies is the Quickstart's DOCKER_MANAGER_TRUSTED_PROXIES
	// (a proxy on the same server).
	DefaultTrustedProxies = "172.16.0.0/12"
	// MoveAgentManagerURL is the new server's own manager for its agent
	// (the service name in the generated compose.yaml).
	MoveAgentManagerURL = "http://docker-manager:8080"
	// ImportOwnProject is where the agent of the Quickstart's compose.yaml
	// reads its own project's folder (read-only, below storage.ImportDir),
	// so Docker Manager's own project can be imported as a stack and then
	// upgraded from the app without editing files on the server.
	ImportOwnProject = "/import/docker-manager"
)

// MoveFilesInput is what the new server's files of a move need.
type MoveFilesInput struct {
	// ManagerImage and AgentImage default to DefaultManagerImage and
	// DefaultAgentImage.
	ManagerImage, AgentImage string
	PublicURL                string
	// TrustedProxies defaults to DefaultTrustedProxies.
	TrustedProxies string
	// OldManagerURL is http://<old server>:<port>: the waiting manager asks
	// it for the handoff and the new agent enrolls into it.
	OldManagerURL   string
	MoveCode        string
	EnrollmentToken string
	EnvironmentName string
}

// MoveFiles renders the new server's compose.yaml (the Quickstart's, with
// the move variables read from .env and defaults that give the
// Quickstart's setup once the move lines are removed) and its .env (the
// Quickstart's variables, then the move lines to remove after the move).
// The .env carries the move code and the enrollment token: show it once.
// Without an enrollment token (new setup files after the new server's
// agent enrolled) the .env has neither the token nor the environment
// name: the agent keeps its credential.
// Like the install commands, the agent mounts the Docker socket and
// Docker's volume directory at their identical paths.
func MoveFiles(in MoveFilesInput) (composeYAML, env string) {
	if in.ManagerImage == "" {
		in.ManagerImage = DefaultManagerImage
	}
	if in.AgentImage == "" {
		in.AgentImage = DefaultAgentImage
	}
	if in.TrustedProxies == "" {
		in.TrustedProxies = DefaultTrustedProxies
	}
	var c strings.Builder
	c.WriteString("name: docker-manager\n\n")
	c.WriteString("services:\n")
	c.WriteString("  docker-manager:\n")
	c.WriteString("    image: " + in.ManagerImage + "\n")
	c.WriteString("    restart: unless-stopped\n")
	c.WriteString("    ports:\n")
	c.WriteString("      - \"8080:8080\"\n")
	c.WriteString("    environment:\n")
	c.WriteString("      DOCKER_MANAGER_PUBLIC_URL: ${DOCKER_MANAGER_PUBLIC_URL}\n")
	c.WriteString("      DOCKER_MANAGER_TRUSTED_PROXIES: ${DOCKER_MANAGER_TRUSTED_PROXIES}\n")
	c.WriteString("      DOCKER_MANAGER_MOVE_FROM: ${DOCKER_MANAGER_MOVE_FROM:-}\n")
	c.WriteString("      DOCKER_MANAGER_MOVE_CODE: ${DOCKER_MANAGER_MOVE_CODE:-}\n")
	c.WriteString("    volumes:\n")
	c.WriteString("      - data:/var/lib/docker-manager\n")
	c.WriteString("    labels:\n")
	c.WriteString("      " + protocol.LabelRole + ": manager\n")
	c.WriteString("    networks:\n")
	c.WriteString("      - docker-manager\n\n")
	c.WriteString("  docker-agent:\n")
	c.WriteString("    image: " + in.AgentImage + "\n")
	c.WriteString("    restart: unless-stopped\n")
	c.WriteString("    depends_on:\n")
	c.WriteString("      - docker-manager\n")
	c.WriteString("    environment:\n")
	c.WriteString("      DOCKER_AGENT_MANAGER_URL: ${DOCKER_AGENT_MANAGER_URL:-" + MoveAgentManagerURL + "}\n")
	c.WriteString("      DOCKER_AGENT_MANAGER_ALLOW_HTTP: \"true\"\n")
	c.WriteString("      DOCKER_AGENT_ENROLLMENT_TOKEN: ${DOCKER_AGENT_ENROLLMENT_TOKEN:-}\n")
	c.WriteString("      DOCKER_AGENT_ENVIRONMENT_NAME: ${DOCKER_AGENT_ENVIRONMENT_NAME:-}\n")
	c.WriteString("    volumes:\n")
	c.WriteString("      - /var/run/docker.sock:/var/run/docker.sock\n")
	c.WriteString("      - /var/lib/docker/volumes:/var/lib/docker/volumes\n")
	c.WriteString("      - stacks:/var/lib/docker/volumes/docker-manager_stacks/_data\n")
	c.WriteString("      - agent:/var/lib/docker-agent\n")
	c.WriteString("      - .:" + ImportOwnProject + ":ro\n")
	c.WriteString("    labels:\n")
	c.WriteString("      " + protocol.LabelRole + ": agent\n")
	c.WriteString("    networks:\n")
	c.WriteString("      - docker-manager\n\n")
	c.WriteString("networks:\n")
	c.WriteString("  docker-manager:\n")
	c.WriteString("    name: docker-manager\n\n")
	c.WriteString("volumes:\n")
	c.WriteString("  data:\n")
	c.WriteString("  agent:\n")
	c.WriteString("  stacks:\n")

	var e strings.Builder
	e.WriteString("# The address you open Docker Manager at (the same as on the old server).\n")
	e.WriteString("DOCKER_MANAGER_PUBLIC_URL=" + in.PublicURL + "\n\n")
	e.WriteString("# The address of your reverse proxy (as on the old server).\n")
	e.WriteString("DOCKER_MANAGER_TRUSTED_PROXIES=" + in.TrustedProxies + "\n\n")
	e.WriteString("# Moving Docker Manager to this server. Remove these lines once the move\n")
	e.WriteString("# is complete, then run: docker compose up -d\n")
	e.WriteString("DOCKER_MANAGER_MOVE_FROM=" + in.OldManagerURL + "\n")
	e.WriteString("DOCKER_MANAGER_MOVE_CODE=" + in.MoveCode + "\n")
	e.WriteString("DOCKER_AGENT_MANAGER_URL=" + in.OldManagerURL + "\n")
	if in.EnrollmentToken == "" {
		// New setup files after the agent enrolled: it keeps its
		// credential (in its volume) and must not enroll again.
		e.WriteString("# The agent on this server is already connected: it needs no enrollment token.\n")
		return c.String(), e.String()
	}
	e.WriteString("DOCKER_AGENT_ENROLLMENT_TOKEN=" + in.EnrollmentToken + "\n")
	if in.EnvironmentName != "" {
		e.WriteString("DOCKER_AGENT_ENVIRONMENT_NAME=" + envFileValue(in.EnvironmentName) + "\n")
	}
	return c.String(), e.String()
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
