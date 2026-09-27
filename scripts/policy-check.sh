#!/usr/bin/env bash
# Repository policy gate. Fails on:
#   - imports of the deprecated github.com/docker/docker module, directly or
#     anywhere in the build graph of the binaries (use moby/moby via the #21 adapter)
#   - Docker / Compose / buildx / credential-helper CLI invocation from Go code
#   - direct Engine HTTP outside the Engine adapter: Docker socket literals
#     and raw Engine API paths (#21)
#   - Engine / Compose SDK imports outside the two agent adapters (#21)
#   - files with CRLF line endings
#   - a project LICENSE/COPYING file (no project license for now, #25)
#   - the UI mockup image (lives only on issue #22)
# Keep each check a small function with a clear failure message. The
# narrowly documented exceptions are listed next to each check and in
# docs/internal/architecture/engine-integration.md.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

failures=0
fail() {
	echo "policy: $*" >&2
	failures=$((failures + 1))
}

# Tracked plus untracked-but-not-ignored files (so violations are caught
# before they are committed).
mapfile -t files < <(git ls-files -co --exclude-standard | sort -u)
go_files=()
for f in "${files[@]}"; do
	[[ "$f" == *.go && -f "$f" ]] && go_files+=("$f")
done

check_legacy_docker() {
	local hits
	hits="$(grep -nE '"github\.com/docker/docker(/[^"]*)?"' "${go_files[@]}" /dev/null || true)"
	[ -n "$hits" ] && fail "legacy github.com/docker/docker import (use github.com/moby/moby/client via the agent adapter, #21):"$'\n'"$hits"
	if grep -qE '^\s*github\.com/docker/docker ' go.mod; then
		fail "go.mod requires github.com/docker/docker"
	fi
}

# The deprecated module must not enter the build graph transitively either.
check_legacy_docker_graph() {
	command -v go >/dev/null 2>&1 || return 0
	local deps
	if ! deps="$(GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go list -deps ./cmd/... 2>&1)"; then
		fail "go list -deps ./cmd/... failed:"$'\n'"$deps"
		return 0
	fi
	deps="$(grep -E '^github\.com/docker/docker(/|$)' <<<"$deps" || true)"
	[ -n "$deps" ] && fail "github.com/docker/docker is linked into a binary:"$'\n'"$deps"
	return 0
}

check_docker_cli() {
	local hits
	hits="$(grep -nE 'exec\.(Command(Context)?|LookPath)\([^)]*"(docker|docker-compose|compose|buildx|docker-buildx|docker-credential-[a-z0-9-]+)"' "${go_files[@]}" /dev/null || true)"
	[ -n "$hits" ] && fail "Docker CLI invocation from Go (use the Engine/Compose Go SDKs):"$'\n'"$hits"
	hits="$(grep -nE '"(/usr/(local/)?(bin|lib/docker/cli-plugins)/)?docker(-compose|-buildx|-credential-[a-z0-9-]+)?"\s*,' "${go_files[@]}" /dev/null | grep -E 'exec\.|Cmd|Path:' || true)"
	[ -n "$hits" ] && fail "possible Docker CLI invocation:"$'\n'"$hits"
	return 0
}

# in_list PATH REGEX...: PATH matches one of the (extended) regexes.
in_list() {
	local f="$1" re
	shift
	for re in "$@"; do
		[[ "$f" =~ $re ]] && return 0
	done
	return 1
}

# Direct Engine HTTP (#21): only the Moby SDK talks to the Engine. A Docker
# socket literal or a raw Engine API path elsewhere means someone dials the
# Engine by hand. Exceptions (keep narrow; documented in
# docs/internal/architecture/engine-integration.md):
#   internal/agent/engine/            the adapter and its fake Engine for unit tests
#   internal/agent/compose/*_test.go  tests scripting that fake Engine
#   internal/agent/config/config.go   the DOCKER_HOST default value only
#   internal/testutil/fscorpus/       a path-traversal test string
#   test/deploy/*_test.go             tests asserting the deploy examples mount the socket
#   internal/manager/agents/install.go  the agent install command's socket bind mount (text shown to operators)
#   internal/protocol/docker{,_test}.go  refuses binding the Docker socket into containers created through Docker Manager (#6)
engine_http_exceptions=(
	'^internal/agent/engine/'
	'^internal/agent/compose/[^/]+_test\.go$'
	'^internal/agent/config/config\.go$'
	'^internal/testutil/fscorpus/'
	'^test/deploy/[^/]+_test\.go$'
	'^internal/manager/agents/install\.go$'
	'^internal/protocol/docker(_test)?\.go$'
)

check_direct_engine_http() {
	local f h hits=""
	for f in "${go_files[@]}"; do
		in_list "$f" "${engine_http_exceptions[@]}" && continue
		h="$(grep -nHE 'docker\.sock|"(/v1\.[0-9]+)?/(_ping|containers/(json|create)|images/(json|create)|volumes/create|networks/create|exec/[^"]*)"|"/v1\.[0-9]+/' "$f" || true)"
		[ -n "$h" ] && hits+="${h}"$'\n'
	done
	[ -n "$hits" ] && fail "direct Engine HTTP outside internal/agent/engine (use the Moby SDK adapter, #21):"$'\n'"$hits"
	return 0
}

# SDK boundary (#21): Engine, Compose, BuildKit and docker/cli packages are
# imported only by the two agent adapters.
sdk_exceptions=(
	'^internal/agent/engine/'
	'^internal/agent/compose/'
)

check_sdk_boundary() {
	local f h hits=""
	for f in "${go_files[@]}"; do
		in_list "$f" "${sdk_exceptions[@]}" && continue
		h="$(grep -nHE '"github\.com/(moby/moby/(client|api)|docker/compose/v[0-9]+|docker/cli|moby/buildkit|compose-spec/compose-go)(/[^"]*)?"' "$f" || true)"
		[ -n "$h" ] && hits+="${h}"$'\n'
	done
	[ -n "$hits" ] && fail "Engine/Compose SDK imported outside internal/agent/{engine,compose} (#21):"$'\n'"$hits"
	return 0
}

check_crlf() {
	local bad
	# i/ = index content, w/ = working tree content.
	bad="$(git ls-files --eol -co --exclude-standard | awk '$1 ~ /crlf|mixed/ || $2 ~ /crlf|mixed/ {print $NF}')"
	[ -n "$bad" ] && fail "CRLF line endings (the repo is LF-only; see .gitattributes):"$'\n'"$bad"
	return 0
}

check_license_file() {
	local f base
	for f in "${files[@]}"; do
		base="$(basename "$f")"
		if [[ "$base" =~ ^(LICEN[CS]E|COPYING|UNLICENSE)(\..*)?$ ]]; then
			fail "project license file $f is not allowed (no project license for now, #25)"
		fi
	done
}

# SHA-256 of the mockup attached to issue #22.
MOCKUP_SHA256="e2f73b82d45c7c3f05d72a8ce393b3b5ca94de886cd2a1a597015149a7784491"

check_mockup() {
	local f lower
	for f in "${files[@]}"; do
		lower="$(echo "$f" | tr '[:upper:]' '[:lower:]')"
		case "$lower" in
		web/*mockup*.png | docs/*mockup*.png | web/*mockup*.jpg | docs/*mockup*.jpg | web/*mockup*.webp | docs/*mockup*.webp)
			fail "mockup image $f must not be committed (it lives on issue #22)"
			;;
		esac
		case "$lower" in
		*.png | *.jpg | *.jpeg | *.webp)
			if [ -f "$f" ] && [ "$(sha256sum "$f" | cut -d' ' -f1)" = "$MOCKUP_SHA256" ]; then
				fail "$f is the UI mockup (it lives on issue #22)"
			fi
			;;
		esac
	done
}

check_legacy_docker
check_legacy_docker_graph
check_docker_cli
check_direct_engine_http
check_sdk_boundary
check_crlf
check_license_file
check_mockup

if [ "$failures" -gt 0 ]; then
	echo "policy-check: $failures problem(s)" >&2
	exit 1
fi
echo "policy-check: ok (${#files[@]} files, ${#go_files[@]} Go files)"
