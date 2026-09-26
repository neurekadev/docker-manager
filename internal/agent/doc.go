// Package agent is the root of the docker-agent code:
//
//	internal/agent/config     environment configuration
//	internal/agent/runtime    main loop, root check, Engine connection, health file
//	internal/agent/transport  outbound HTTP/WebSocket client for the manager (#27)
//	internal/agent/engine     Moby Engine SDK adapter (#21): the ONLY package
//	                          that talks to Docker Engine directly
//	internal/agent/compose    Compose SDK adapter (#21): in-memory docker/cli,
//	                          builds through the Engine adapter's BuildKit
//	internal/agent/storage    host storage layout check (#28): identical-path
//	                          mounts, stack roots, volume access
//	internal/agent/jobs       job commands, fencing and journal (#26)
//
// Later workstreams add the manager session (#3), files and backups.
// docs/architecture/engine-integration.md describes the Engine boundary.
//
// The agent never opens a listening socket; nolisten_test.go enforces this
// for every in-module package the agent binary links, and
// TestEngineAgentImage checks the running image's network namespace.
package agent
