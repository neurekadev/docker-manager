#!/usr/bin/env bash
# Deploy smoke test (#29): runs deploy/compose with the images under test
# and walks the first-run path an operator takes:
#
#   wait-images    edge images carry org.opencontainers.image.revision == SMOKE_REVISION
#   fresh-start    docker compose up on empty volumes; manager and agent healthy
#   health-ready   GET /api/v1/health/ready through the Caddy TLS proxy (verified CA)
#   owner-setup    pending (#16)
#   enroll-agent   pending (#3)
#   deploy-stack   pending (#7): test/smoke/sample-stack/compose.yaml
#
# Pending steps print ::warning:: and are listed in the summary; they never
# count as passed. Enable a step by implementing its function below once
# the feature is merged (the warning says when the API has appeared).
#
# Environment:
#   SMOKE_IMAGES         edge (default) | local
#   SMOKE_REVISION       expected revision label / commit (default: git HEAD)
#   SMOKE_MANAGER_IMAGE  default ghcr.io/neurekadev/dockyard-manager:edge (edge)
#                        or dockyard-manager:smoke (local)
#   SMOKE_AGENT_IMAGE    likewise for the agent
#   SMOKE_WAIT_SECONDS   how long to wait for edge images (default 1800)
#   SMOKE_POLL_SECONDS   poll interval while waiting (default 30)
#   SMOKE_HTTPS_PORT     published HTTPS port (default 8443)
#   SMOKE_ARTIFACTS      directory for logs (default: a temp dir)
#   SMOKE_KEEP=1         leave the deployment running
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

SMOKE_IMAGES="${SMOKE_IMAGES:-edge}"
SMOKE_REVISION="${SMOKE_REVISION:-$(git rev-parse HEAD)}"
SMOKE_WAIT_SECONDS="${SMOKE_WAIT_SECONDS:-1800}"
SMOKE_POLL_SECONDS="${SMOKE_POLL_SECONDS:-30}"
SMOKE_HTTPS_PORT="${SMOKE_HTTPS_PORT:-8443}"
SMOKE_ARTIFACTS="${SMOKE_ARTIFACTS:-$(mktemp -d)}"
case "$SMOKE_IMAGES" in
edge)
	: "${SMOKE_MANAGER_IMAGE:=ghcr.io/neurekadev/dockyard-manager:edge}"
	: "${SMOKE_AGENT_IMAGE:=ghcr.io/neurekadev/dockyard-agent:edge}"
	export SMOKE_PULL_POLICY=always
	;;
local)
	: "${SMOKE_MANAGER_IMAGE:=dockyard-manager:smoke}"
	: "${SMOKE_AGENT_IMAGE:=dockyard-agent:smoke}"
	export SMOKE_PULL_POLICY=never
	;;
*)
	echo "SMOKE_IMAGES must be edge or local (got ${SMOKE_IMAGES})" >&2
	exit 2
	;;
esac
export SMOKE_MANAGER_IMAGE SMOKE_AGENT_IMAGE
mkdir -p "$SMOKE_ARTIFACTS"

# deploy/compose with the smoke override; a dedicated project name keeps it
# apart from anything else on the host.
export COMPOSE_PROJECT_NAME=dockyard-smoke
export DOCKYARD_HOST=localhost DOCKYARD_TLS=internal
export DOCKYARD_HTTP_PORT=18080 DOCKYARD_HTTPS_PORT="$SMOKE_HTTPS_PORT"
compose=(docker compose -f deploy/compose/compose.yaml -f test/smoke/compose.override.yaml)
BASE_URL="https://localhost:${SMOKE_HTTPS_PORT}"
CA="${SMOKE_ARTIFACTS}/caddy-root.crt"

results=()
record() { results+=("$1|$2|$3"); }
log() { echo "[smoke] $*"; }

summary() {
	{
		echo "### Deploy smoke (${SMOKE_IMAGES} images, revision \`${SMOKE_REVISION}\`)"
		echo
		echo "| step | status | detail |"
		echo "| --- | --- | --- |"
		for r in "${results[@]}"; do
			IFS='|' read -r step status detail <<<"$r"
			echo "| ${step} | ${status} | ${detail} |"
		done
	} | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
}

cleanup() {
	local code=$?
	"${compose[@]}" ps -a >"${SMOKE_ARTIFACTS}/compose-ps.txt" 2>&1 || true
	"${compose[@]}" logs --no-color --timestamps >"${SMOKE_ARTIFACTS}/compose-logs.txt" 2>&1 || true
	if [ "${SMOKE_KEEP:-0}" != "1" ]; then
		"${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
	fi
	summary
	exit "$code"
}

fail() {
	record "$1" "FAILED" "$2"
	echo "::error title=deploy smoke: $1::$2"
	exit 1
}

pending() {
	local step="$1" issue="$2" what="$3" op_pattern="$4" detail
	detail="pending (feature ${issue} not merged): ${what}"
	if [ -n "$op_pattern" ] && [ -s "${SMOKE_ARTIFACTS}/openapi.json" ] &&
		jq -e --arg re "$op_pattern" '[.paths[][] | objects | .operationId // empty | select(test($re))] | length > 0' \
			"${SMOKE_ARTIFACTS}/openapi.json" >/dev/null 2>&1; then
		detail="pending, but operations matching /${op_pattern}/ now exist in the API: enable this step (${issue})"
	fi
	record "$step" "PENDING" "$detail"
	echo "::warning title=deploy smoke: ${step}::${detail}"
}

# image_revision prints the revision label of an image for this host's
# platform, from the registry manifest (no pull).
image_revision() {
	local img="$1" arch
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) arch="$(uname -m)" ;;
	esac
	docker buildx imagetools inspect "$img" --format '{{json .Image}}' 2>/dev/null |
		jq -r --arg p "linux/${arch}" '
			(if has("config") then . else (.[$p] // (to_entries | map(select(.key | startswith("linux/"))) | .[0].value)) end)
			| .config.Labels["org.opencontainers.image.revision"] // empty'
}

step_wait_images() {
	if [ "$SMOKE_IMAGES" != "edge" ]; then
		for img in "$SMOKE_MANAGER_IMAGE" "$SMOKE_AGENT_IMAGE"; do
			local rev
			rev="$(docker image inspect "$img" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' 2>/dev/null)" ||
				fail wait-images "local image ${img} not found (build it first)"
			[ "$rev" = "$SMOKE_REVISION" ] || fail wait-images "local image ${img} has revision ${rev:-none}, want ${SMOKE_REVISION}"
		done
		record wait-images PASSED "local images carry revision ${SMOKE_REVISION}"
		return
	fi
	local deadline=$((SECONDS + SMOKE_WAIT_SECONDS)) img rev ok
	while :; do
		ok=1
		for img in "$SMOKE_MANAGER_IMAGE" "$SMOKE_AGENT_IMAGE"; do
			rev="$(image_revision "$img" || true)"
			if [ "$rev" != "$SMOKE_REVISION" ]; then
				ok=0
				log "${img}: revision ${rev:-unknown}, waiting for ${SMOKE_REVISION}"
			fi
		done
		[ "$ok" = 1 ] && break
		if [ "$SECONDS" -ge "$deadline" ]; then
			fail wait-images "edge images did not reach revision ${SMOKE_REVISION} within ${SMOKE_WAIT_SECONDS}s (did the ci images job publish?)"
		fi
		sleep "$SMOKE_POLL_SECONDS"
	done
	record wait-images PASSED "edge images carry revision ${SMOKE_REVISION}"
}

container_health() {
	local id
	id="$("${compose[@]}" ps -q "$1")"
	[ -n "$id" ] || return 1
	docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$id"
}

step_fresh_start() {
	"${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
	if [ "$SMOKE_IMAGES" = "edge" ]; then
		"${compose[@]}" pull --quiet dockyard-manager dockyard-agent || fail fresh-start "pull failed"
	fi
	"${compose[@]}" up -d || fail fresh-start "docker compose up failed"
	local deadline=$((SECONDS + 240)) m a
	while :; do
		m="$(container_health dockyard-manager || echo missing)"
		a="$(container_health dockyard-agent || echo missing)"
		[ "$m" = healthy ] && [ "$a" = healthy ] && break
		if [ "$SECONDS" -ge "$deadline" ]; then
			fail fresh-start "containers not healthy after 240s (manager=${m}, agent=${a})"
		fi
		sleep 5
	done
	# The containers run as root (#28), and the agent reports that it waits
	# for enrollment instead of crashing.
	for svc in dockyard-manager dockyard-agent; do
		local user
		user="$(docker inspect --format '{{.Config.User}}' "$("${compose[@]}" ps -q "$svc")")"
		case "$user" in 0 | 0:0 | root | "") ;; *) fail fresh-start "${svc} runs as ${user}, want UID 0" ;; esac
	done
	# Only the agent gets the Docker socket; the manager never does (#12, #28).
	local mounts
	mounts="$(docker inspect --format '{{range .Mounts}}{{.Source}} {{end}}' "$("${compose[@]}" ps -q dockyard-manager)")"
	case "$mounts" in *docker.sock*) fail fresh-start "the manager mounts the Docker socket" ;; esac
	mounts="$(docker inspect --format '{{range .Mounts}}{{.Source}} {{end}}' "$("${compose[@]}" ps -q dockyard-agent)")"
	case "$mounts" in *docker.sock*) ;; *) fail fresh-start "the agent lacks the Docker socket mount" ;; esac
	record fresh-start PASSED "manager and agent healthy on empty volumes, UID 0, socket only on the agent"
}

step_health_ready() {
	local deadline=$((SECONDS + 120))
	until "${compose[@]}" cp caddy:/data/caddy/pki/authorities/local/root.crt "$CA" >/dev/null 2>&1 && [ -s "$CA" ]; do
		[ "$SECONDS" -ge "$deadline" ] && fail health-ready "Caddy root CA not available"
		sleep 2
	done
	local body code
	while :; do
		code="$(curl -sS -o "${SMOKE_ARTIFACTS}/ready.json" -w '%{http_code}' --cacert "$CA" "${BASE_URL}/api/v1/health/ready" 2>/dev/null || true)"
		[ "$code" = 200 ] && break
		[ "$SECONDS" -ge "$deadline" ] && fail health-ready "readiness through the TLS proxy: HTTP ${code:-no response}"
		sleep 2
	done
	jq -e '.status == "ready" and (.checks | length > 0) and all(.checks[]; .ok)' "${SMOKE_ARTIFACTS}/ready.json" >/dev/null ||
		fail health-ready "unexpected readiness body: $(cat "${SMOKE_ARTIFACTS}/ready.json")"
	body="$(curl -sS --fail --cacert "$CA" "${BASE_URL}/api/v1/health")" || fail health-ready "GET /api/v1/health failed"
	local commit
	commit="$(jq -r .commit <<<"$body")"
	[ "$commit" = "$SMOKE_REVISION" ] || fail health-ready "manager reports commit ${commit}, want ${SMOKE_REVISION}"
	# UI shell and OpenAPI through the same origin.
	curl -sS --fail --cacert "$CA" "${BASE_URL}/" | grep -q '<title>DockYard</title>' || fail health-ready "UI shell not served through the proxy"
	curl -sS --fail --cacert "$CA" -o "${SMOKE_ARTIFACTS}/openapi.json" "${BASE_URL}/api/v1/openapi.json" || fail health-ready "OpenAPI not served"
	record health-ready PASSED "ready through ${BASE_URL} (Caddy internal CA verified), commit ${commit}"
}

step_owner_setup() {
	pending owner-setup "#16" "create the first owner and log in" 'setup|owner|bootstrap'
}

step_enroll_agent() {
	pending enroll-agent "#3" "create an enrollment token, restart the agent with it, wait for the Environment to be online" 'enroll'
}

step_deploy_stack() {
	pending deploy-stack "#7" "deploy test/smoke/sample-stack/compose.yaml and wait until web is healthy" 'stack'
}

trap cleanup EXIT
log "images: ${SMOKE_MANAGER_IMAGE}, ${SMOKE_AGENT_IMAGE} (want revision ${SMOKE_REVISION})"
step_wait_images
step_fresh_start
step_health_ready
step_owner_setup
step_enroll_agent
step_deploy_stack
log "done (pending steps are listed in the summary)"
