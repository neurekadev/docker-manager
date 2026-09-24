#!/usr/bin/env bash
# Repository policy gate. Fails on:
#   - imports of the deprecated github.com/docker/docker module (use moby/moby via the #21 adapter)
#   - Docker / Compose CLI invocation from Go code
#   - files with CRLF line endings
#   - a project LICENSE/COPYING file (no project license for now, #25)
#   - the UI mockup image (lives only on issue #22)
# Extend the checks here (e.g. #21 adds Engine-client rules); keep each check
# a small function with a clear failure message.
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

check_docker_cli() {
	local hits
	hits="$(grep -nE 'exec\.Command(Context)?\([^)]*"(docker|docker-compose|compose|buildx)"' "${go_files[@]}" /dev/null || true)"
	[ -n "$hits" ] && fail "Docker CLI invocation from Go (use the Engine/Compose Go SDKs):"$'\n'"$hits"
	hits="$(grep -nE '"(/usr/(local/)?bin/)?docker(-compose)?"\s*,' "${go_files[@]}" /dev/null | grep -E 'exec\.|Cmd|Path:' || true)"
	[ -n "$hits" ] && fail "possible Docker CLI invocation:"$'\n'"$hits"
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
check_docker_cli
check_crlf
check_license_file
check_mockup

if [ "$failures" -gt 0 ]; then
	echo "policy-check: $failures problem(s)" >&2
	exit 1
fi
echo "policy-check: ok (${#files[@]} files, ${#go_files[@]} Go files)"
