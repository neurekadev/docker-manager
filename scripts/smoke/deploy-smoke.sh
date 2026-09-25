#!/usr/bin/env bash
# Deploy smoke test (#29): runs deploy/caddy with the images under test
# and walks the first-run path an operator takes:
#
#   wait-images    edge images carry org.opencontainers.image.revision == SMOKE_REVISION
#   fresh-start    docker compose up on empty volumes; manager and agent healthy
#   health-ready   GET /api/v1/health/ready through the Caddy TLS proxy (verified CA)
#   owner-setup    first-run owner over HTTPS through Caddy, session cookie,
#                  sign-out/sign-in, second setup refused (#16)
#   enroll-agent   the owner creates a one-use enrollment token through the public
#                  API exactly as the PWA's "Add environment" screen does
#                  (POST /api/v1/agent-enrollments, #22 track B1), hands it to the
#                  co-located agent on stdin; it enrolls through the internal URL
#                  and its environment comes online; a reused token is refused.
#                  The headless path (`dockyard-manager enrollment create` in the
#                  manager container) is exercised too: its token is revoked
#                  through the API and then refused (#3)
#   api-token      curl as a script would: the owner creates an API token scoped
#                  to one environment; the token reads and renames only that
#                  environment, is refused elsewhere and on owner routes, the
#                  cookie is ignored on bearer requests, revocation gives a
#                  generic 401 and the value is in no log (#31)
#   deploy-stack   the owner creates a stack from test/smoke/sample-stack/compose.yaml
#                  (its html/ bind directory is copied next to it), deploys it
#                  through the API and waits for the job and a healthy web
#                  container; the applied revision equals the on-disk source and
#                  the compose.yaml bytes are unchanged; deleting the stack
#                  removes its containers (#7)
#   e2e-fixture    only with SMOKE_E2E_ENV: deploys the sample stack again as
#                  dockyard-smoke-e2e and leaves the deployment running for the
#                  Playwright terminal specs (e2e/tests/terminal.spec.ts,
#                  ui-terminal.spec.ts); writes E2E_BASE_URL, E2E_TERMINAL_USER,
#                  E2E_TERMINAL_ENV, E2E_TERMINAL_CONTAINER and E2E_CA_FILE (the
#                  Caddy root certificate) to that file (no secrets). Tear down
#                  afterwards with `bash scripts/smoke/deploy-smoke.sh teardown`.
#
# Every step is real: a step that cannot run fails the smoke test.
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
#   SMOKE_OWNER_PASSWORD owner password (default: random per run, never printed);
#                        the extended workflow passes a masked one to Playwright
#   SMOKE_E2E_ENV        file for the e2e-fixture step (implies SMOKE_KEEP=1)
#
#   bash scripts/smoke/deploy-smoke.sh            # run every step
#   bash scripts/smoke/deploy-smoke.sh teardown   # remove a kept deployment
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

# deploy/caddy with the smoke override; a dedicated project name keeps it
# apart from anything else on the host.
export COMPOSE_PROJECT_NAME=dockyard-smoke
export DOCKYARD_HOST=localhost DOCKYARD_TLS=internal
export DOCKYARD_HTTP_PORT=18080 DOCKYARD_HTTPS_PORT="$SMOKE_HTTPS_PORT"
compose=(docker compose -f deploy/caddy/compose.yaml -f test/smoke/compose.override.yaml)
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

SAMPLE_PROJECT=dockyard-smoke-sample
E2E_PROJECT=dockyard-smoke-e2e
[ -z "${SMOKE_E2E_ENV:-}" ] || SMOKE_KEEP=1

cleanup() {
	local code=$?
	# Containers the agent created for the sample stack are not part of the
	# smoke compose project.
	docker ps -aq --filter "label=com.docker.compose.project=${SAMPLE_PROJECT}" | xargs -r docker rm -f >/dev/null 2>&1 || true
	"${compose[@]}" ps -a >"${SMOKE_ARTIFACTS}/compose-ps.txt" 2>&1 || true
	"${compose[@]}" logs --no-color --timestamps >"${SMOKE_ARTIFACTS}/compose-logs.txt" 2>&1 || true
	if [ "${SMOKE_KEEP:-0}" != "1" ]; then
		teardown
	fi
	summary
	exit "$code"
}

# teardown removes the deployment, its volumes and the stacks the agent
# created (they are not part of the smoke compose project).
teardown() {
	local p
	for p in "$SAMPLE_PROJECT" "$E2E_PROJECT"; do
		docker ps -aq --filter "label=com.docker.compose.project=${p}" | xargs -r docker rm -f >/dev/null 2>&1 || true
	done
	"${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
}

fail() {
	record "$1" "FAILED" "$2"
	echo "::error title=deploy smoke: $1::$2"
	exit 1
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
	local deadline=$((SECONDS + SMOKE_WAIT_SECONDS)) img rev ok newer revs
	while :; do
		ok=1
		newer=""
		revs=()
		for img in "$SMOKE_MANAGER_IMAGE" "$SMOKE_AGENT_IMAGE"; do
			rev="$(image_revision "$img" || true)"
			revs+=("$rev")
			if [ "$rev" != "$SMOKE_REVISION" ]; then
				ok=0
				log "${img}: revision ${rev:-unknown}, waiting for ${SMOKE_REVISION}"
			fi
		done
		[ "$ok" = 1 ] && break
		# A later push to main may already have replaced :edge. If both
		# images carry the same newer commit that contains ours, test that
		# (it includes this change) instead of waiting for a tag that will
		# never come back.
		if [ -n "${revs[0]}" ] && [ "${revs[0]}" = "${revs[1]}" ] && commit_contains "${revs[0]}" "$SMOKE_REVISION"; then
			newer="${revs[0]}"
			log "edge moved on to ${newer}, which contains ${SMOKE_REVISION}; testing it"
			record wait-images PASSED "edge images carry ${newer}, a newer main commit containing ${SMOKE_REVISION}"
			SMOKE_REVISION="$newer"
			return
		fi
		if [ "$SECONDS" -ge "$deadline" ]; then
			fail wait-images "edge images did not reach revision ${SMOKE_REVISION} within ${SMOKE_WAIT_SECONDS}s (did the ci images job publish?)"
		fi
		sleep "$SMOKE_POLL_SECONDS"
	done
	record wait-images PASSED "edge images carry revision ${SMOKE_REVISION}"
}

# commit_contains HEAD BASE: HEAD is a descendant of BASE (GitHub compare API;
# needs GH_TOKEN and GITHUB_REPOSITORY, i.e. only in Actions).
commit_contains() {
	[ -n "${GITHUB_REPOSITORY:-}" ] && [ -n "${GH_TOKEN:-}" ] || return 1
	local status
	status="$(gh api "repos/${GITHUB_REPOSITORY}/compare/${2}...${1}" --jq .status 2>/dev/null)" || return 1
	[ "$status" = ahead ]
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
	local jar="${SMOKE_ARTIFACTS}/cookies.txt" out="${SMOKE_ARTIFACTS}/owner-setup.json" pw code
	# Random per run unless the workflow passes a masked one for Playwright;
	# never printed. (No "tr </dev/urandom | head" here: under pipefail tr's
	# SIGPIPE would fail the step.)
	pw="${SMOKE_OWNER_PASSWORD:-smoke-$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')}"
	OWNER_PW="$pw" # for later steps; shell variable only, never written out
	local body
	body="$(jq -nc --arg p "$pw" '{username: "smoke-owner", displayName: "Smoke Owner", password: $p}')"
	code="$(curl -sS -o "$out" -w '%{http_code}' --cacert "$CA" -c "$jar" -H 'Content-Type: application/json' \
		-d "$body" "${BASE_URL}/api/v1/setup/owner" || true)"
	[ "$code" = 201 ] || fail owner-setup "POST /api/v1/setup/owner through the proxy: HTTP ${code:-no response} $(jq -c '{code, message}' "$out" 2>/dev/null)"
	jq -e '.state == "authenticated" and .user.owner == true and .user.username == "smoke-owner"' "$out" >/dev/null ||
		fail owner-setup "unexpected setup response: $(jq -c '{state, user: .user.username}' "$out")"
	grep -q '__Host-dockyard_session' "$jar" || fail owner-setup "no __Host-dockyard_session cookie was set"
	curl -sS --fail --cacert "$CA" -b "$jar" "${BASE_URL}/api/v1/me" | jq -e '.owner == true' >/dev/null ||
		fail owner-setup "GET /api/v1/me with the session cookie failed"
	code="$(curl -sS -o "$out" -w '%{http_code}' --cacert "$CA" -H 'Content-Type: application/json' \
		-d "$body" "${BASE_URL}/api/v1/setup/owner" || true)"
	[ "$code" = 409 ] && jq -e '.code == "setup_complete"' "$out" >/dev/null ||
		fail owner-setup "a second setup was not refused (HTTP ${code})"
	code="$(curl -sS -o /dev/null -w '%{http_code}' --cacert "$CA" -b "$jar" -c "$jar" -X DELETE "${BASE_URL}/api/v1/auth/session" || true)"
	[ "$code" = 204 ] || fail owner-setup "sign-out: HTTP ${code}"
	code="$(curl -sS -o /dev/null -w '%{http_code}' --cacert "$CA" -b "$jar" "${BASE_URL}/api/v1/me" || true)"
	[ "$code" = 401 ] || fail owner-setup "session still valid after sign-out (HTTP ${code})"
	code="$(curl -sS -o "$out" -w '%{http_code}' --cacert "$CA" -c "$jar" -H 'Content-Type: application/json' \
		-d "$(jq -nc --arg p "$pw" '{username: "smoke-owner", password: $p}')" "${BASE_URL}/api/v1/auth/session" || true)"
	[ "$code" = 200 ] && jq -e '.state == "authenticated"' "$out" >/dev/null || fail owner-setup "sign-in: HTTP ${code}"
	rm -f "$jar"
	record owner-setup PASSED "owner created over HTTPS through Caddy; cookie session, sign-out/sign-in; second setup refused (409)"
}

# owner_session JAR signs the owner in with a cookie jar (a fresh sign-in
# also counts as the recent authentication step-up-guarded routes need).
owner_session() {
	local code
	code="$(curl -sS -o /dev/null -w '%{http_code}' --cacert "$CA" -c "$1" -H 'Content-Type: application/json' \
		-d "$(jq -nc --arg p "$OWNER_PW" '{username: "smoke-owner", password: $p}')" "${BASE_URL}/api/v1/auth/session" || true)"
	[ "$code" = 200 ]
}

step_enroll_agent() {
	local jar out code token enrollment_id cli cli_token cli_id version body
	jar="$(mktemp)"
	out="${SMOKE_ARTIFACTS}/enrollment.json"
	owner_session "$jar" || fail enroll-agent "owner sign-in failed"
	# The request the PWA's "Add environment" screen sends
	# (web/src/routes/(app)/environments/add/+page.svelte). The token lives
	# in shell variables only; the response file is removed at once.
	code="$(curl -sS -o "$out" -w '%{http_code}' --cacert "$CA" -b "$jar" -H 'Content-Type: application/json' \
		-H "Idempotency-Key: smoke-enroll-${SMOKE_REVISION}" \
		-d '{"intent": "new", "environmentName": "smoke", "expiresInSeconds": 3600}' \
		"${BASE_URL}/api/v1/agent-enrollments" || true)"
	[ "$code" = 201 ] || fail enroll-agent "POST /api/v1/agent-enrollments: HTTP ${code} $(jq -c '{code, message}' "$out" 2>/dev/null)"
	token="$(jq -r '.token // empty' "$out")"
	enrollment_id="$(jq -r '.enrollment.id // empty' "$out")"
	if ! jq -e '.enrollment.state == "pending" and (.installCommands | map(.variant) | index("colocated") and index("remote"))' \
		"$out" >/dev/null; then
		rm -f "$out"
		fail enroll-agent "unexpected enrollment response (state or install commands)"
	fi
	rm -f "$out"
	case "$token" in dye_*) ;; *) fail enroll-agent "the API returned no dye_ token" ;; esac
	# Hand the token to the running co-located agent (internal plain-HTTP URL)
	# on stdin and wait until its environment is online.
	if ! printf '%s\n' "$token" | "${compose[@]}" exec -T dockyard-agent dockyard-agent enroll -wait 120s \
		>"${SMOKE_ARTIFACTS}/agent-enroll.out" 2>&1; then
		fail enroll-agent "dockyard-agent enroll failed: $(tail -5 "${SMOKE_ARTIFACTS}/agent-enroll.out")"
	fi
	grep -q "environment is online" "${SMOKE_ARTIFACTS}/agent-enroll.out" ||
		fail enroll-agent "agent did not report an online environment: $(cat "${SMOKE_ARTIFACTS}/agent-enroll.out")"
	"${compose[@]}" exec -T dockyard-agent dockyard-agent healthcheck >/dev/null 2>&1 ||
		fail enroll-agent "agent unhealthy after enrollment"
	# The owner sees the environment online and the enrollment used.
	curl -sS --fail --cacert "$CA" -b "$jar" "${BASE_URL}/api/v1/environments" >"$out" ||
		fail enroll-agent "GET /api/v1/environments failed"
	jq -e '(.items | length) == 1 and .items[0].name == "smoke" and .items[0].online == true' "$out" >/dev/null ||
		fail enroll-agent "the environment is not listed online: $(jq -c '[.items[] | {name, online}]' "$out")"
	curl -sS --fail --cacert "$CA" -b "$jar" "${BASE_URL}/api/v1/agent-enrollments" |
		jq -e --arg id "$enrollment_id" '[.items[] | select(.id == $id and .state == "used")] | length == 1' >/dev/null ||
		fail enroll-agent "the enrollment is not marked used"
	# The used token cannot enroll again, also through the public origin.
	version="$(curl -sS --fail --cacert "$CA" "${BASE_URL}/api/v1/health" | jq -r .version)"
	body="$(jq -nc --arg v "$version" '{protocol:"dockyard.agent/v1",agentVersion:$v,installId:"0190a6e0-0000-7000-8000-00000000cafe",engine:{id:"SMOKE:REUSE",version:"28.5.2",apiVersion:"1.51"}}')"
	code="$(curl -sS -o /dev/null -w '%{http_code}' --cacert "$CA" -X POST -H "Authorization: Bearer ${token}" \
		-H 'Content-Type: application/json' --data "$body" "${BASE_URL}/agent/v1/enroll" || true)"
	[ "$code" = 401 ] || fail enroll-agent "reused enrollment token answered HTTP ${code}, want 401"
	# Headless path (docs/guide/first-run.md): the manager CLI issues a
	# token; the owner revokes it through the API, after which it is refused.
	cli="$("${compose[@]}" exec -T dockyard-manager dockyard-manager enrollment create -name smoke-cli -json 2>"${SMOKE_ARTIFACTS}/enrollment-create.err")" ||
		fail enroll-agent "dockyard-manager enrollment create failed: $(tail -5 "${SMOKE_ARTIFACTS}/enrollment-create.err")"
	cli_token="$(jq -r '.token // empty' <<<"$cli")"
	cli_id="$(jq -r '.enrollmentId // empty' <<<"$cli")"
	case "$cli_token" in dye_*) ;; *) fail enroll-agent "enrollment create printed no dye_ token" ;; esac
	code="$(curl -sS -o /dev/null -w '%{http_code}' --cacert "$CA" -b "$jar" -X DELETE "${BASE_URL}/api/v1/agent-enrollments/${cli_id}" || true)"
	[ "$code" = 204 ] || fail enroll-agent "revoking the CLI enrollment: HTTP ${code}"
	code="$(curl -sS -o /dev/null -w '%{http_code}' --cacert "$CA" -X POST -H "Authorization: Bearer ${cli_token}" \
		-H 'Content-Type: application/json' --data "$body" "${BASE_URL}/agent/v1/enroll" || true)"
	[ "$code" = 401 ] || fail enroll-agent "revoked CLI enrollment token answered HTTP ${code}, want 401"
	# The manager saw the session come online; no log contains a token.
	local logs
	logs="$("${compose[@]}" logs --no-color dockyard-manager dockyard-agent 2>&1)"
	grep -q '"msg":"environment online"' <<<"$logs" || fail enroll-agent "manager did not log the environment online"
	if grep -qF "$token" <<<"$logs" || grep -qF "$cli_token" <<<"$logs"; then
		fail enroll-agent "an enrollment token appears in the container logs"
	fi
	rm -f "$jar" "$out"
	record enroll-agent PASSED "token from the API (the PWA's request); co-located agent enrolled through the internal URL, environment online, enrollment used, reused token refused (401); CLI token revoked through the API and refused"
}

step_api_token() {
	local jar out code env_id token token_id etag body
	local -a auth
	jar="$(mktemp)"
	out="${SMOKE_ARTIFACTS}/api-token.json"
	# The owner signs in (a fresh sign-in counts as the step-up creation
	# needs) and finds the enrolled environment.
	code="$(curl -sS -o "$out" -w '%{http_code}' --cacert "$CA" -c "$jar" -H 'Content-Type: application/json' \
		-d "$(jq -nc --arg p "$OWNER_PW" '{username: "smoke-owner", password: $p}')" "${BASE_URL}/api/v1/auth/session" || true)"
	[ "$code" = 200 ] || fail api-token "owner sign-in: HTTP ${code}"
	env_id="$(curl -sS --fail --cacert "$CA" -b "$jar" "${BASE_URL}/api/v1/environments" | jq -r '.items[0].id // empty')"
	[ -n "$env_id" ] || fail api-token "no environment listed for the owner"
	body="$(jq -nc --arg e "$env_id" --arg x "$(date -u -d '+1 day' +%Y-%m-%dT%H:%M:%SZ)" '{name: "smoke", expiresAt: $x, scopes: [
		{capability: "environment.read", scope: {kind: "environment", environmentId: $e}},
		{capability: "environment.manage", scope: {kind: "environment", environmentId: $e}}]}')"
	code="$(curl -sS -o "$out" -w '%{http_code}' --cacert "$CA" -b "$jar" -H 'Content-Type: application/json' -d "$body" \
		"${BASE_URL}/api/v1/me/api-tokens" || true)"
	[ "$code" = 201 ] || fail api-token "create token: HTTP ${code} $(jq -c '{code, message}' "$out" 2>/dev/null)"
	token="$(jq -r '.token // empty' "$out")"
	token_id="$(jq -r '.apiToken.id // empty' "$out")"
	rm -f "$out" # the only file that held the value
	case "$token" in "dy_${token_id}_"*) ;; *) fail api-token "the token value is not dy_<id>_<secret>" ;; esac
	auth=(-H "Authorization: Bearer ${token}")
	# Read and rename the one environment, without cookies or browser headers.
	curl -sS --fail --cacert "$CA" "${auth[@]}" "${BASE_URL}/api/v1/environments" |
		jq -e --arg e "$env_id" '(.items | length) == 1 and .items[0].id == $e and .items[0].view == "full"' >/dev/null ||
		fail api-token "the token does not list exactly its environment"
	etag="$(curl -sS --fail --cacert "$CA" "${auth[@]}" -D - -o /dev/null "${BASE_URL}/api/v1/environments/${env_id}" |
		tr -d '\r' | awk 'tolower($1) == "etag:" {print $2}')"
	[ -n "$etag" ] || fail api-token "no ETag for the environment"
	code="$(curl -sS -o "$out" -w '%{http_code}' --cacert "$CA" "${auth[@]}" -X PATCH -H 'Content-Type: application/json' \
		-H "If-Match: ${etag}" -d '{"name": "smoke-renamed"}' "${BASE_URL}/api/v1/environments/${env_id}" || true)"
	[ "$code" = 200 ] && jq -e '.name == "smoke-renamed"' "$out" >/dev/null || fail api-token "rename with the token: HTTP ${code}"
	# Outside its scope and on owner routes the token is refused, also
	# when the owner's session cookie rides along.
	code="$(curl -sS -o /dev/null -w '%{http_code}' --cacert "$CA" "${auth[@]}" "${BASE_URL}/api/v1/environments/${env_id}/system" || true)"
	[ "$code" = 403 ] || fail api-token "system information outside the token scope: HTTP ${code}"
	for path in /api/v1/users /api/v1/settings/security /api/v1/me/api-tokens; do
		code="$(curl -sS -o "$out" -w '%{http_code}' --cacert "$CA" -b "$jar" "${auth[@]}" "${BASE_URL}${path}" || true)"
		[ "$code" = 403 ] && jq -e '.code == "api_token_not_allowed"' "$out" >/dev/null ||
			fail api-token "${path} with the token (and the owner cookie): HTTP ${code}, want 403 api_token_not_allowed"
	done
	# Revoked: the generic 401.
	code="$(curl -sS -o /dev/null -w '%{http_code}' --cacert "$CA" -b "$jar" -X DELETE "${BASE_URL}/api/v1/me/api-tokens/${token_id}" || true)"
	[ "$code" = 204 ] || fail api-token "revoke: HTTP ${code}"
	code="$(curl -sS -o "$out" -w '%{http_code}' --cacert "$CA" "${auth[@]}" "${BASE_URL}/api/v1/me" || true)"
	[ "$code" = 401 ] && jq -e '.code == "unauthenticated"' "$out" >/dev/null || fail api-token "revoked token: HTTP ${code}, want 401"
	# The value is in no container log.
	local logs
	logs="$("${compose[@]}" logs --no-color dockyard-manager 2>&1)"
	grep -qF "$token" <<<"$logs" && fail api-token "the API token appears in the manager log"
	rm -f "$jar" "$out"
	record api-token PASSED "scoped token (one environment) read and renamed it via curl; system info and owner routes refused (403); revoked -> 401; not in logs"
}

# wait_job polls a job until it is terminal and prints its state.
wait_job() {
	local jar="$1" id="$2" deadline state
	deadline=$((SECONDS + 300))
	while [ "$SECONDS" -lt "$deadline" ]; do
		state="$(curl -sS --fail --cacert "$CA" -b "$jar" "${BASE_URL}/api/v1/jobs/${id}" | jq -r .state)"
		case "$state" in succeeded | failed | partial | cancelled | interrupted)
			echo "$state"
			return 0
			;;
		esac
		sleep 3
	done
	echo "timeout"
}

step_deploy_stack() {
	local jar out code env_id body stack_id job_id state health applied source
	jar="$(mktemp)"
	out="${SMOKE_ARTIFACTS}/deploy-stack.json"
	code="$(curl -sS -o "$out" -w '%{http_code}' --cacert "$CA" -c "$jar" -H 'Content-Type: application/json' \
		-d "$(jq -nc --arg p "$OWNER_PW" '{username: "smoke-owner", password: $p}')" "${BASE_URL}/api/v1/auth/session" || true)"
	[ "$code" = 200 ] || fail deploy-stack "owner sign-in: HTTP ${code}"
	env_id="$(curl -sS --fail --cacert "$CA" -b "$jar" "${BASE_URL}/api/v1/environments" | jq -r '.items[0].id // empty')"
	[ -n "$env_id" ] || fail deploy-stack "no environment listed for the owner"

	# Create: the manager validates on the agent and writes compose.yaml into
	# a new project directory of the stacks volume.
	body="$(jq -nc --arg e "$env_id" --arg n "$SAMPLE_PROJECT" --rawfile c test/smoke/sample-stack/compose.yaml \
		'{environmentId: $e, name: $n, compose: $c, description: "deploy smoke sample"}')"
	code="$(curl -sS -o "$out" -w '%{http_code}' --cacert "$CA" -b "$jar" -H 'Content-Type: application/json' -d "$body" \
		"${BASE_URL}/api/v1/stacks" || true)"
	[ "$code" = 201 ] || fail deploy-stack "create stack: HTTP ${code} $(jq -c '{code, message, details}' "$out" 2>/dev/null)"
	stack_id="$(jq -r '.stack.id' "$out")"
	jq -e '.stack.status == "undeployed" and .stack.undeployedChanges == true and .validation.valid == true' "$out" >/dev/null ||
		fail deploy-stack "unexpected new stack: $(jq -c '{status: .stack.status, v: .validation.valid}' "$out")"
	# The relative bind source (./html) lives next to compose.yaml.
	"${compose[@]}" cp test/smoke/sample-stack/html \
		"dockyard-agent:/var/lib/docker/volumes/dockyard_stacks/_data/${SAMPLE_PROJECT}/html" >/dev/null ||
		fail deploy-stack "could not copy html/ into the project directory"

	# Deploy (202 + job) and wait for it.
	code="$(curl -sS -o "$out" -w '%{http_code}' --cacert "$CA" -b "$jar" -H 'Content-Type: application/json' \
		-H "Idempotency-Key: smoke-deploy-1" -d '{}' "${BASE_URL}/api/v1/stacks/${stack_id}/deployments" || true)"
	[ "$code" = 202 ] || fail deploy-stack "deploy: HTTP ${code} $(jq -c '{code, message}' "$out" 2>/dev/null)"
	job_id="$(jq -r .id "$out")"
	state="$(wait_job "$jar" "$job_id")"
	[ "$state" = succeeded ] || fail deploy-stack "deploy job ${state}: $(curl -sS --cacert "$CA" -b "$jar" "${BASE_URL}/api/v1/jobs/${job_id}" | jq -c .error)"
	local deadline=$((SECONDS + 120))
	health=""
	while [ "$SECONDS" -lt "$deadline" ]; do
		health="$(docker inspect --format '{{.State.Health.Status}}' "${SAMPLE_PROJECT}-web-1" 2>/dev/null || true)"
		[ "$health" = healthy ] && break
		sleep 3
	done
	[ "$health" = healthy ] || fail deploy-stack "web is ${health:-missing}, want healthy"

	# The applied revision is the on-disk source: no undeployed changes, and
	# the deployed compose.yaml bytes are the ones submitted.
	curl -sS --fail --cacert "$CA" -b "$jar" "${BASE_URL}/api/v1/stacks/${stack_id}" >"$out" ||
		fail deploy-stack "GET stack failed"
	applied="$(jq -r '.appliedRevision.id // empty' "$out")"
	source="$(jq -r '.sourceRevision.hash // empty' "$out")"
	jq -e '.status == "deployed" and .undeployedChanges != true and .appliedRevision.hash == .sourceRevision.hash and .engine.state == "running"' "$out" >/dev/null ||
		fail deploy-stack "stack after deploy: $(jq -c '{status, undeployedChanges, appliedRevision, sourceRevision, engine}' "$out")"
	curl -sS --fail --cacert "$CA" -b "$jar" "${BASE_URL}/api/v1/stacks/${stack_id}/revisions/${applied}" |
		jq -r '.files[] | select(.path == "compose.yaml") | .content' >"${SMOKE_ARTIFACTS}/applied-compose.yaml" ||
		fail deploy-stack "could not read the applied revision"
	cmp -s <(printf '%s\n' "$(cat "${SMOKE_ARTIFACTS}/applied-compose.yaml")") <(printf '%s\n' "$(cat test/smoke/sample-stack/compose.yaml)") ||
		fail deploy-stack "the applied compose.yaml differs from the submitted one"
	[ -n "$source" ] || fail deploy-stack "no source revision"

	# Delete: stack.remove takes it down and forgets it.
	code="$(curl -sS -o "$out" -w '%{http_code}' --cacert "$CA" -b "$jar" -X DELETE -H "Idempotency-Key: smoke-delete-1" \
		"${BASE_URL}/api/v1/stacks/${stack_id}" || true)"
	[ "$code" = 202 ] || fail deploy-stack "delete: HTTP ${code}"
	state="$(wait_job "$jar" "$(jq -r .id "$out")")"
	[ "$state" = succeeded ] || fail deploy-stack "delete job ${state}"
	[ -z "$(docker ps -aq --filter "label=com.docker.compose.project=${SAMPLE_PROJECT}")" ] ||
		fail deploy-stack "containers left after deleting the stack"
	code="$(curl -sS -o /dev/null -w '%{http_code}' --cacert "$CA" -b "$jar" "${BASE_URL}/api/v1/stacks/${stack_id}" || true)"
	[ "$code" = 404 ] || fail deploy-stack "deleted stack answers HTTP ${code}"
	rm -f "$jar"
	record deploy-stack PASSED "created, deployed (web healthy), applied revision == on-disk source, compose.yaml unchanged, deleted"
}

# step_e2e_fixture leaves a deployed stack with a shell (busybox in the
# nginx alpine image) for the Playwright terminal specs, and writes their
# non-secret settings to SMOKE_E2E_ENV. The owner password reaches
# Playwright from the workflow (masked SMOKE_OWNER_PASSWORD), never from
# this file.
step_e2e_fixture() {
	local jar out code env_id body stack_id state container deadline
	jar="$(mktemp)"
	out="${SMOKE_ARTIFACTS}/e2e-fixture.json"
	owner_session "$jar" || fail e2e-fixture "owner sign-in failed"
	env_id="$(curl -sS --fail --cacert "$CA" -b "$jar" "${BASE_URL}/api/v1/environments" | jq -r '.items[0].id // empty')"
	[ -n "$env_id" ] || fail e2e-fixture "no environment listed for the owner"
	body="$(sed "s/^name: .*/name: ${E2E_PROJECT}/" test/smoke/sample-stack/compose.yaml |
		jq -Rsc --arg e "$env_id" --arg n "$E2E_PROJECT" '{environmentId: $e, name: $n, compose: ., description: "Playwright terminal fixture"}')"
	code="$(curl -sS -o "$out" -w '%{http_code}' --cacert "$CA" -b "$jar" -H 'Content-Type: application/json' -d "$body" \
		"${BASE_URL}/api/v1/stacks" || true)"
	[ "$code" = 201 ] || fail e2e-fixture "create stack: HTTP ${code} $(jq -c '{code, message}' "$out" 2>/dev/null)"
	stack_id="$(jq -r '.stack.id' "$out")"
	"${compose[@]}" cp test/smoke/sample-stack/html \
		"dockyard-agent:/var/lib/docker/volumes/dockyard_stacks/_data/${E2E_PROJECT}/html" >/dev/null ||
		fail e2e-fixture "could not copy html/ into the project directory"
	code="$(curl -sS -o "$out" -w '%{http_code}' --cacert "$CA" -b "$jar" -H 'Content-Type: application/json' \
		-H "Idempotency-Key: smoke-e2e-deploy-1" -d '{}' "${BASE_URL}/api/v1/stacks/${stack_id}/deployments" || true)"
	[ "$code" = 202 ] || fail e2e-fixture "deploy: HTTP ${code}"
	state="$(wait_job "$jar" "$(jq -r .id "$out")")"
	[ "$state" = succeeded ] || fail e2e-fixture "deploy job ${state}"
	deadline=$((SECONDS + 120))
	container=""
	while [ "$SECONDS" -lt "$deadline" ]; do
		container="$(docker ps --filter "label=com.docker.compose.project=${E2E_PROJECT}" \
			--filter "label=com.docker.compose.service=web" --filter health=healthy --format '{{.Names}}' | head -1)"
		[ -n "$container" ] && break
		sleep 3
	done
	[ -n "$container" ] || fail e2e-fixture "no healthy web container of ${E2E_PROJECT}"
	{
		echo "E2E_BASE_URL=${BASE_URL}"
		echo "E2E_TERMINAL_USER=smoke-owner"
		echo "E2E_TERMINAL_ENV=${env_id}"
		echo "E2E_TERMINAL_CONTAINER=${container}"
		echo "E2E_CA_FILE=${CA}"
	} >"$SMOKE_E2E_ENV"
	rm -f "$jar" "$out"
	record e2e-fixture PASSED "stack ${E2E_PROJECT} deployed (${container} healthy) and left running for the Playwright terminal specs"
}

trap cleanup EXIT
if [ "${1:-}" = teardown ]; then
	trap - EXIT
	"${compose[@]}" logs --no-color --timestamps >"${SMOKE_ARTIFACTS}/compose-logs-teardown.txt" 2>&1 || true
	teardown
	exit 0
fi
log "images: ${SMOKE_MANAGER_IMAGE}, ${SMOKE_AGENT_IMAGE} (want revision ${SMOKE_REVISION})"
step_wait_images
step_fresh_start
step_health_ready
step_owner_setup
step_enroll_agent
step_api_token
step_deploy_stack
[ -z "${SMOKE_E2E_ENV:-}" ] || step_e2e_fixture
log "done"
