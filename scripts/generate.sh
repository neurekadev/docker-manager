#!/usr/bin/env bash
# Regenerates committed artifacts:
#   api/openapi.json               from the registered Huma operations
#   web/src/lib/api/schema.d.ts    TypeScript types via openapi-typescript
#   docs/architecture/job-engine.md  lock-matrix table from internal/jobspec
#
#   bash scripts/generate.sh           regenerate in place
#   bash scripts/generate.sh --check   fail if anything is stale (CI)
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

mode="write"
if [ "${1:-}" = "--check" ]; then
	mode="check"
elif [ $# -gt 0 ]; then
	echo "usage: $0 [--check]" >&2
	exit 2
fi

if [ ! -x web/node_modules/.bin/openapi-typescript ] && [ ! -f web/node_modules/.bin/openapi-typescript ]; then
	echo "==> npm ci (web)"
	npm --prefix web ci --no-audit --no-fund
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "==> OpenAPI spec"
go run ./cmd/dockyard-manager openapi >"$tmp/openapi.json"

echo "==> TypeScript client types"
(cd web && npx --no-install openapi-typescript "$tmp/openapi.json" --output "$tmp/schema.d.ts" >/dev/null)

stale=0
compare() { # generated committed
	if ! cmp -s "$1" "$2"; then
		stale=1
		echo "stale: $2" >&2
		diff -u "$2" "$1" | head -40 >&2 || true
	fi
}

if [ "$mode" = "check" ]; then
	compare "$tmp/openapi.json" api/openapi.json
	compare "$tmp/schema.d.ts" web/src/lib/api/schema.d.ts
	echo "==> job lock matrix"
	if ! go run ./tools/jobdoc -check; then
		stale=1
	fi
	if [ "$stale" -ne 0 ]; then
		echo "Generated artifacts are out of date. Run: bash scripts/generate.sh and commit the result." >&2
		exit 1
	fi
	echo "generate: up to date"
else
	mkdir -p api
	cp "$tmp/openapi.json" api/openapi.json
	cp "$tmp/schema.d.ts" web/src/lib/api/schema.d.ts
	echo "==> job lock matrix"
	go run ./tools/jobdoc
	echo "generate: wrote api/openapi.json, web/src/lib/api/schema.d.ts and the lock matrix in docs/architecture/job-engine.md"
fi
