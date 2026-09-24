#!/usr/bin/env bash
# Dependency license review for everything that ships:
#   - Go modules linked into the linux binaries (go-licenses)
#   - production npm dependencies of the web UI (license-checker-rseidelsohn)
# Fails on any license outside the allowlist, including unknown licenses.
# Changing the allowlist is a reviewed decision (docs/adr/0001-foundation.md).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

GO_LICENSES_VERSION="v2.0.1"
LICENSE_CHECKER_VERSION="5.0.1"

# Permissive licenses plus MPL-2.0 (file-level copyleft, fine for static linking).
GO_ALLOWED="Apache-2.0,BSD-2-Clause,BSD-3-Clause,ISC,MIT,MPL-2.0,0BSD,Unlicense,CC0-1.0,Zlib"
NPM_ALLOWED="Apache-2.0;BSD-2-Clause;BSD-3-Clause;ISC;MIT;MPL-2.0;0BSD;Unlicense;CC0-1.0;Zlib;BlueOak-1.0.0;Python-2.0;CC-BY-4.0"

report="${LICENSE_REPORT:-}"

echo "==> Go modules (linux build)"
bindir="$(mktemp -d)"
trap 'rm -rf "$bindir"' EXIT
GOBIN="$bindir" go install "github.com/google/go-licenses/v2@${GO_LICENSES_VERSION}"
golic=("$bindir/go-licenses")
# Analyse the linux build graph (what the images ship), whatever the host OS.
export GOOS=linux GOARCH=amd64 CGO_ENABLED=0

# Reviewed modules whose LICENSE file is only the short Apache-2.0 notice,
# which go-licenses' classifier reports as "Unknown" (pulled in by the
# Compose SDK / BuildKit graph, #21). Each entry is "module|phrase": the
# check verifies the phrase is still in the module's LICENSE, so a version
# bump that changes the license fails here and needs a new review.
reviewed_notices=(
	"gotest.tools/v3|Licensed under the Apache License, Version 2.0"
	"github.com/in-toto/attestation|Licensed under the Apache License, Version 2.0"
	"github.com/in-toto/in-toto-golang|Licensed under the Apache License, Version 2.0"
)
ignore=(--ignore github.com/neurekadev/dockyard)
for entry in "${reviewed_notices[@]}"; do
	mod="${entry%%|*}"
	phrase="${entry#*|}"
	go mod download "$mod" >/dev/null 2>&1 || true
	dir="$(go list -m -f '{{.Dir}}' "$mod" 2>/dev/null || true)"
	if [ -z "$dir" ] || ! grep -qF "$phrase" "$dir/LICENSE"; then
		echo "license-check: reviewed notice for ${mod} no longer matches (${dir:-module missing}/LICENSE); review again" >&2
		exit 1
	fi
	echo "reviewed: ${mod} (Apache-2.0 notice)"
	ignore+=(--ignore "$mod")
done

if [ -n "$report" ]; then
	{
		echo "### Go module licenses"
		echo
		echo '```'
		"${golic[@]}" report ./cmd/... "${ignore[@]}" 2>/dev/null
		for entry in "${reviewed_notices[@]}"; do
			echo "${entry%%|*},reviewed notice,Apache-2.0"
		done
		echo '```'
	} >>"$report"
fi
"${golic[@]}" check ./cmd/... "${ignore[@]}" --allowed_licenses="$GO_ALLOWED" 2> >(grep -v -e 'contains non-Go code' -e '\.s$' >&2)
unset GOOS GOARCH CGO_ENABLED

echo "==> npm production dependencies"
if [ ! -d web/node_modules ]; then
	npm --prefix web ci --no-audit --no-fund
fi
checker=(npx --yes "license-checker-rseidelsohn@${LICENSE_CHECKER_VERSION}" --start web --production --excludePrivatePackages)
if [ -n "$report" ]; then
	{
		echo "### npm production dependency licenses"
		echo
		echo '```'
		"${checker[@]}" --summary
		echo '```'
	} >>"$report"
fi
"${checker[@]}" --onlyAllow "$NPM_ALLOWED" >/dev/null
echo "license-check: ok"
