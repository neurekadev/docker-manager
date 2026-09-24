#!/usr/bin/env bash
# Builds both executables for linux/amd64 and linux/arm64 with CGO disabled
# into dist/ and verifies each is a static binary.
#
# The manager embeds web/build/app when present (run scripts/web-check.sh or
# `npm --prefix web run build` first), otherwise the placeholder page.
#
# Environment: VERSION (default 0.0.0-edge), COMMIT, DATE (default: from git).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

VERSION="${VERSION:-0.0.0-edge}"
COMMIT="${COMMIT:-$(git rev-parse HEAD 2>/dev/null || echo unknown)}"
DATE="${DATE:-$(git log -1 --format=%cI 2>/dev/null || echo unknown)}"
pkg="github.com/neurekadev/dockyard/internal/buildinfo"
ldflags="-s -w -X ${pkg}.Version=${VERSION} -X ${pkg}.Commit=${COMMIT} -X ${pkg}.Date=${DATE}"

if [ -f web/build/app/index.html ]; then
	echo "UI: embedding web/build/app"
else
	echo "UI: web/build/app missing; the manager embeds the placeholder page"
fi

# The SDK graph pinned in go.mod (#21): the agent must link exactly these
# versions (Moby Engine client + API, Compose SDK with its docker/cli and
# BuildKit dependencies); the manager must link none of them.
sdk_modules=(github.com/moby/moby/client github.com/moby/moby/api github.com/docker/compose/v5 github.com/docker/cli github.com/moby/buildkit)
pinned() { # module -> version required in go.mod
	awk -v m="$1" '$1 == m { print $2; exit }' go.mod
}
check_sdk_graph() { # bin out meta
	local mod want have
	for mod in "${sdk_modules[@]}"; do
		have="$(awk -F'\t' -v m="$mod" '$2 == "dep" && $3 == m { print $4 }' <<<"$3")"
		if [ "$1" = dockyard-agent ]; then
			want="$(pinned "$mod")"
			if [ -z "$want" ] || [ "$have" != "$want" ]; then
				echo "$2: links ${mod} ${have:-(missing)}, go.mod pins ${want:-(nothing)}" >&2
				exit 1
			fi
			echo "    ${mod} ${have}"
		elif [ -n "$have" ]; then
			echo "$2: the manager must not link ${mod} (it never talks to Docker Engine)" >&2
			exit 1
		fi
	done
}

rm -rf dist
mkdir -p dist
for arch in amd64 arm64; do
	for bin in dockyard-manager dockyard-agent; do
		out="dist/${bin}-linux-${arch}"
		echo "==> ${out}"
		CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags "$ldflags" -o "$out" "./cmd/${bin}"

		meta="$(go version -m "$out")"
		grep -q $'\tbuild\tCGO_ENABLED=0' <<<"$meta" || {
			echo "$out: not built with CGO_ENABLED=0" >&2
			exit 1
		}
		grep -q $'\tbuild\tGOARCH='"$arch" <<<"$meta" || {
			echo "$out: wrong GOARCH" >&2
			exit 1
		}
		if grep -q 'mattn/go-sqlite3' <<<"$meta"; then
			echo "$out: links the cgo SQLite driver" >&2
			exit 1
		fi
		if grep -qE $'\tdep\tgithub\\.com/docker/docker\t' <<<"$meta"; then
			echo "$out: links the deprecated github.com/docker/docker module" >&2
			exit 1
		fi
		check_sdk_graph "$bin" "$out" "$meta"
		if command -v file >/dev/null 2>&1; then
			desc="$(file -b "$out")"
			case "$desc" in
			*"statically linked"*) ;;
			*)
				echo "$out: not statically linked: $desc" >&2
				exit 1
				;;
			esac
			case "$desc" in
			*interpreter*)
				echo "$out: has a dynamic interpreter: $desc" >&2
				exit 1
				;;
			esac
		else
			echo "warning: 'file' not available; skipped ELF interpreter check" >&2
		fi
	done
done
(cd dist && sha256sum -- * >SHA256SUMS)
ls -l dist
