#!/usr/bin/env bash
# Web gate: npm ci (when needed), lint (prettier + eslint; prettier also on
# the e2e specs), svelte-check, vitest, production build into web/build/app, then the build checks
# (lazy-loaded chunks, service-worker precache list, manifest/icons, sizes).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../web"

if [ ! -d node_modules ] || [ package-lock.json -nt node_modules/.package-lock.json ]; then
	echo "==> npm ci"
	npm ci --no-audit --no-fund
fi

echo "==> lint"
npm run --silent lint
# The Playwright specs in e2e/ follow the web app's Prettier style (the e2e
# package has no toolchain of its own; format with the command below).
echo "==> e2e format (fix: cd web && npx prettier --write --config prettier.config.js '../e2e/**/*.ts')"
npx prettier --check --log-level warn --config prettier.config.js '../e2e/**/*.ts'
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
