// Package testharness provides reproducible environments for DockYard's
// extended test suites (#29): Docker-in-Docker Engines from the Engine
// matrix, a private registry with a fault-injecting proxy, a Git server,
// MinIO, a pinned restic binary and a Caddy TLS reverse proxy.
//
// Two layers:
//
//   - Docker-free helpers (no build tag): the Engine matrix loader
//     (test/matrix/engines.json), the registry FaultProxy, htpasswd and
//     S3 SigV4 helpers, bare Git repository seeding and the restic fetcher.
//     They have unit tests that run in `go test ./...`.
//   - Container fixtures (build tag `integration`, testcontainers-go):
//     StartEngine/StartEngines, StartRegistry, StartGitServer, StartMinIO
//     and StartTLSProxy. Each has a self-test under the same tag; they run
//     in .github/workflows/extended.yaml, never in the PR suite.
//
// Environment variables (see docs/testing/harness.md):
//
//	DOCKYARD_TEST_ENGINE  Engine version from the matrix to use, or
//	                      "default" / "minimum" / "latest" (default: the
//	                      matrix default).
//	DOCKYARD_TEST_CACHE   download cache (restic); default: the user cache
//	                      directory.
//
// Tests that need an Engine call StartEngine with the version returned by
// SelectEngine, so the engine-matrix job can run them against every matrix
// entry by setting DOCKYARD_TEST_ENGINE. Name such tests TestEngine... so
// the job's -run filter picks them up.
package testharness
