package testharness

// Fixture images, pinned by multi-arch index digest. Keep CaddyImage equal
// to deploy/caddy/compose.yaml (TestFixtureImagesPinned checks it).
const (
	// RegistryImage is the CNCF distribution registry.
	RegistryImage = "registry:3.1.1@sha256:325b4b29b041e82803abeb703e201655e4e23ab83264ec1a7c9ddb0a5b14a6e0"
	// MinIOImage: upstream stopped publishing minio/minio images in October
	// 2025; alpine/minio is a maintained multi-arch build of the last
	// upstream release (https://github.com/alpine-docker/minio).
	MinIOImage = "alpine/minio:RELEASE.2025-10-15T17-29-55Z@sha256:cf23643a6cf9ce159c57643ceb88279e431262282428c9e0bf3a7ef1a97e84b4"
	// CaddyImage serves the TLS reverse proxy and the Git server fixtures.
	CaddyImage = "caddy:2.11.4-alpine@sha256:6aeddd44c3078b0f9a35206472a11420648a79c184603ef95957d0a20044cb2b"
)
