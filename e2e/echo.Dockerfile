# E2E stream fixture (test/e2e/echo): SSE and WebSocket echo behind the
# TLS proxy. Build context is the repository root (see e2e/compose.yaml).
# Base images match deploy/docker/manager.Dockerfile.
ARG GO_IMAGE=golang:1.27.1-alpine3.24@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:latest@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=readonly
COPY go.mod go.sum ./
COPY test/e2e/echo/ test/e2e/echo/
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=linux GOARCH="${TARGETARCH}" go build -trimpath -ldflags "-s -w" -o /out/echo ./test/e2e/echo

FROM ${RUNTIME_IMAGE}
COPY --from=build /out/echo /usr/local/bin/echo
USER 0:0
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/echo"]
