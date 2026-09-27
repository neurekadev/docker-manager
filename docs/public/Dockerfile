# User documentation site: a static Fumadocs (Next.js) export served by
# nginx on port 3000.
#
#   docker build -t docker-manager-docs --build-arg GIT_TAG=0.0.0-edge \
#     --build-arg GIT_HASH=$(git rev-parse HEAD) docs/public
#
# Base images are pinned by digest; update tag and digest together.

ARG NODE_IMAGE=node:26.9.0-trixie-slim@sha256:3a771f83944bb763050c23c0225c260638c4b7899e7a72485ef75e5e570499e5
ARG RUNTIME_IMAGE=ubuntu:24.04@sha256:008173c23f95b170204355c12626cb5a965d779a7e1283b09e9cffbb1bf33ca3

# ---------------------------------------------------------------- site
FROM ${NODE_IMAGE} AS build
WORKDIR /src
ENV NEXT_TELEMETRY_DISABLED=1
COPY package.json package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY . .
RUN npm run build

# ---------------------------------------------------------------- runtime
FROM ${RUNTIME_IMAGE}
ARG GIT_TAG=0.0.0-edge
ARG GIT_HASH=unknown
LABEL org.opencontainers.image.title="docker-manager-docs" \
      org.opencontainers.image.description="Docker Manager user documentation" \
      org.opencontainers.image.source="https://code.neureka.dev/docker-manager/docker-manager" \
      org.opencontainers.image.version="${GIT_TAG}" \
      org.opencontainers.image.revision="${GIT_HASH}"
RUN apt-get update \
 && apt-get install -y --no-install-recommends nginx \
 && rm -rf /var/lib/apt/lists/* /etc/nginx/sites-enabled/default
ENV GIT_TAG=${GIT_TAG} \
    GIT_HASH=${GIT_HASH}
WORKDIR /app
COPY nginx.conf /app/nginx.conf
COPY --from=build /src/out/ /app/site/
RUN printf 'version=%s\ncommit=%s\n' "${GIT_TAG}" "${GIT_HASH}" > /app/build-info \
 && nginx -t -c /app/nginx.conf
EXPOSE 3000
CMD ["nginx", "-c", "/app/nginx.conf", "-g", "daemon off;"]
