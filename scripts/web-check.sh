#!/usr/bin/env bash
# Web gate: npm ci (when needed), lint (prettier + eslint), svelte-check,
# vitest, production build into web/build/app, then the build checks
# (lazy-loaded chunks, service-worker precache list, manifest/icons, sizes).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../web"

if [ ! -d node_modules ] || [ package-lock.json -nt node_modules/.package-lock.json ]; then
	echo "==> npm ci"
	npm ci --no-audit --no-fund
fi

echo "==> lint"
npm run --silent lint
echo "==> svelte-check"
npm run --silent check
echo "==> vitest"
npm run --silent test
echo "==> build"
npm run --silent build
test -f build/app/index.html || {
	echo "web build did not produce build/app/index.html" >&2
	exit 1
}
echo "==> verify build"
node scripts/verify-build.mjs
