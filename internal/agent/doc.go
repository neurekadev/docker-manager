// Package agent is the root of the dockyard-agent code:
//
//	internal/agent/config   environment configuration
//	internal/agent/runtime  main loop, root check, health file
//
// Later workstreams add the manager session (#3), the Moby Engine adapter
// (#21, the ONLY place that talks to Docker), Compose, files and backups.
//
// The agent never opens a listening socket; nolisten_test.go enforces this
// for every in-module package the agent binary links.
package agent
