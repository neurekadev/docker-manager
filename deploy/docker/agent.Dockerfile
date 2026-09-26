# Docker Agent image: static Go binary plus a pinned, checksum-verified
# restic. No web UI, no Docker/Compose CLI, no listening port.
#
# Every builder stage runs on $BUILDPLATFORM and cross-compiles, and the
# final stage has no RUN instructions, so multi-arch builds need no QEMU.
#
#   docker buildx build -f deploy/docker/agent.Dockerfile \
#     --platform linux/amd64,linux/arm64 .
#
# Base images are pinned by digest; update tag and digest together.

ARG GO_IMAGE=golang:1.27.1-alpine3.24@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414
ARG FETCH_IMAGE=alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
# Root variant of distroless static (CA certificates, tzdata, /etc/passwd; no shell).
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:latest@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2

# ---------------------------------------------------------------- restic
FROM --platform=$BUILDPLATFORM ${FETCH_IMAGE} AS restic
ARG TARGETARCH
# restic release and SHA-256 of the linux .bz2 assets (from the release's
# signed SHA256SUMS). Bump all three together, in both Dockerfiles.
ARG RESTIC_VERSION=0.19.1
ARG RESTIC_SHA256_AMD64=f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c
ARG RESTIC_SHA256_ARM64=a5f64aaab53d51e311fa3829124c5b703f2d14cf187d8640b6be3b2b49376465
RUN set -eu; \
    case "${TARGETARCH}" in \
        amd64) sum="${RESTIC_SHA256_AMD64}" ;; \
        arm64) sum="${RESTIC_SHA256_ARM64}" ;; \
        *) echo "unsupported TARGETARCH ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    wget -q -O /tmp/restic.bz2 "https://github.com/restic/restic/releases/download/v${RESTIC_VERSION}/restic_${RESTIC_VERSION}_linux_${TARGETARCH}.bz2"; \
    echo "${sum}  /tmp/restic.bz2" | sha256sum -c -; \
    bunzip2 -c /tmp/restic.bz2 > /restic; \
    chmod 0755 /restic

# ---------------------------------------------------------------- Go
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=readonly
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY internal/ internal/
ARG TARGETARCH
# Build metadata (CI passes the version, the commit and its date).
ARG GIT_TAG=0.0.0-edge
ARG GIT_HASH=unknown
ARG GIT_DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=linux GOARCH="${TARGETARCH}" go build -trimpath \
      -ldflags "-s -w -X code.neureka.dev/docker-manager/docker-manager/internal/buildinfo.Version=${GIT_TAG} -X code.neureka.dev/docker-manager/docker-manager/internal/buildinfo.Commit=${GIT_HASH} -X code.neureka.dev/docker-manager/docker-manager/internal/buildinfo.Date=${GIT_DATE}" \
      -o /out/docker-agent ./cmd/docker-agent

# ---------------------------------------------------------------- runtime
FROM ${RUNTIME_IMAGE}
ARG GIT_TAG=0.0.0-edge
ARG GIT_HASH=unknown
ARG GIT_DATE=unknown
LABEL org.opencontainers.image.title="docker-agent" \
      org.opencontainers.image.description="Docker Agent: outbound-only connector for a Docker Engine" \
      org.opencontainers.image.source="https://code.neureka.dev/docker-manager/docker-manager" \
      org.opencontainers.image.version="${GIT_TAG}" \
      org.opencontainers.image.revision="${GIT_HASH}" \
      org.opencontainers.image.created="${GIT_DATE}"
COPY --from=build /out/docker-agent /usr/local/bin/docker-agent
COPY --from=restic /restic /usr/local/bin/restic
ENV DOCKER_AGENT_STATE_DIR=/var/lib/docker-agent
# Docker Manager containers run as root (UID 0); the agent refuses to start otherwise (#28).
USER 0:0
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD ["/usr/local/bin/docker-agent", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/docker-agent"]
CMD ["run"]
