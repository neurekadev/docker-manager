# E2E test PKI (test/e2e/certgen): one throw-away root CA and a localhost
# certificate shared by the Caddy, Traefik and nginx example proxies in
# e2e/compose.yaml. Build context is the repository root. Base images match
# deploy/docker/manager.Dockerfile (TestFixtureImagesPinned).
ARG GO_IMAGE=golang:1.27.1-alpine3.24@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:latest@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=readonly
COPY go.mod go.sum ./
COPY test/e2e/certgen/ test/e2e/certgen/
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=linux GOARCH="${TARGETARCH}" go build -trimpath -ldflags "-s -w" -o /out/certgen ./test/e2e/certgen

FROM ${RUNTIME_IMAGE}
COPY --from=build /out/certgen /usr/local/bin/certgen
USER 0:0
ENTRYPOINT ["/usr/local/bin/certgen"]
CMD ["-dir", "/certs"]
