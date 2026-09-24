#!/usr/bin/env bash
# Image contents check (#2): each image holds its own DockYard binary and the
# pinned restic, only the manager embeds the web UI, neither ships a Docker,
# Compose or buildx CLI, both run as UID 0 and only the manager exposes a port.
#
#   bash scripts/ci/image-contents.sh <manager-image> <agent-image>
#
# Needs a Docker CLI on the CI runner (never inside the images).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

manager="${1:?manager image}"
agent="${2:?agent image}"
restic_version="$(sed -n 's/^ARG RESTIC_VERSION=//p' deploy/docker/agent.Dockerfile)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

failures=0
fail() {
	echo "::error title=image contents::$*"
	failures=$((failures + 1))
}
ok() { echo "ok: $*"; }

# files IMAGE -> sorted file list of the image filesystem
files() {
	local cid
	cid="$(docker create "$1")"
	docker export "$cid" | tar -t | sed 's|^\./||' | sort
	docker rm -f "$cid" >/dev/null
}

# extract IMAGE PATH DEST
extract() {
	local cid
	cid="$(docker create "$1")"
	docker cp "$cid:$2" "$3" >/dev/null
	docker rm -f "$cid" >/dev/null
}

check_image() { # name image own-binary other-binary
	local name="$1" image="$2" own="$3" other="$4" list
	list="$(files "$image")"
	grep -qx "usr/local/bin/${own}" <<<"$list" && ok "$name contains /usr/local/bin/${own}" || fail "$name lacks /usr/local/bin/${own}"
	grep -qx "usr/local/bin/restic" <<<"$list" && ok "$name contains restic" || fail "$name lacks /usr/local/bin/restic"
	if grep -qx "usr/local/bin/${other}" <<<"$list"; then fail "$name also contains ${other}"; else ok "$name does not contain ${other}"; fi
	local cli
	cli="$(grep -E '(^|/)(docker|docker-compose|docker-buildx|buildx|docker-credential-[a-z0-9-]+)$|cli-plugins/' <<<"$list" || true)"
	if [ -n "$cli" ]; then fail "$name ships Docker/Compose CLI files: ${cli}"; else ok "$name ships no Docker/Compose/buildx CLI"; fi
	local rv
	rv="$(docker run --rm --entrypoint /usr/local/bin/restic "$image" version)"
	case "$rv" in
	"restic ${restic_version} "*) ok "$name restic: ${rv}" ;;
	*) fail "$name restic version: ${rv} (want ${restic_version})" ;;
	esac
	local user
	user="$(docker image inspect --format '{{.Config.User}}' "$image")"
	case "$user" in 0 | 0:0 | root | "") ok "$name runs as UID 0 (${user:-default})" ;; *) fail "$name runs as ${user}" ;; esac
}

check_image manager "$manager" dockyard-manager dockyard-agent
check_image agent "$agent" dockyard-agent dockyard-manager

# The UI is embedded in the manager binary (SvelteKit build with hashed
# _app/immutable assets and the app shell); the agent binary has no UI.
extract "$manager" /usr/local/bin/dockyard-manager "$work/manager"
extract "$agent" /usr/local/bin/dockyard-agent "$work/agent"
if grep -aq '_app/immutable/' "$work/manager" && grep -aq '<title>DockYard</title>' "$work/manager"; then
	ok "manager binary embeds the web UI"
else
	fail "manager binary does not embed the built web UI"
fi
if grep -aq '_app/immutable/' "$work/agent" || grep -aq '<title>DockYard</title>' "$work/agent"; then
	fail "agent binary contains web UI assets"
else
	ok "agent binary contains no web UI"
fi

ports="$(docker image inspect --format '{{json .Config.ExposedPorts}}' "$agent")"
case "$ports" in null | "{}") ok "agent exposes no port" ;; *) fail "agent image exposes ${ports}" ;; esac
ports="$(docker image inspect --format '{{json .Config.ExposedPorts}}' "$manager")"
case "$ports" in *8080/tcp*) ok "manager exposes 8080/tcp" ;; *) fail "manager image exposes ${ports}" ;; esac

if [ "$failures" -gt 0 ]; then
	echo "image contents: ${failures} problem(s)" >&2
	exit 1
fi
echo "image contents: ok"
