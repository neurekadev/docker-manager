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
