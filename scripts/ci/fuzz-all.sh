#!/usr/bin/env bash
# Runs every FuzzXxx target in the module with real fuzzing (#29).
#
#   FUZZTIME=60s bash scripts/ci/fuzz-all.sh
#
# Targets are discovered with `go test -list`, so new fuzz tests are picked
# up without editing CI. A failing input is written by `go test` to the
# package's testdata/fuzz/<FuzzXxx>/ directory; the generated corpus is also
# copied to $RUNNER_TEMP/fuzz-cache (the workflow uploads both on failure).
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

FUZZTIME="${FUZZTIME:-60s}"
summary="${GITHUB_STEP_SUMMARY:-/dev/null}"
targets=()
failed=()

while IFS= read -r pkg; do
	while IFS= read -r fn; do
		[[ "$fn" == Fuzz* ]] && targets+=("${pkg} ${fn}")
	done < <(go test -list '^Fuzz' "$pkg" 2>/dev/null)
done < <(go list ./...)

if [ "${#targets[@]}" -eq 0 ]; then
	echo "::error title=fuzz::no FuzzXxx targets found"
	exit 1
fi

{
	echo "### Fuzzing (${FUZZTIME} per target)"
	echo
	echo "| package | target | result |"
	echo "| --- | --- | --- |"
} >>"$summary"

for t in "${targets[@]}"; do
	pkg="${t% *}"
	fn="${t#* }"
	echo "::group::${pkg} ${fn}"
	if go test -run '^$' -fuzz "^${fn}\$" -fuzztime "$FUZZTIME" "$pkg"; then
		result=pass
	else
		result="**FAIL**"
		failed+=("${pkg}.${fn}")
	fi
	echo "::endgroup::"
	echo "| \`${pkg}\` | ${fn} | ${result} |" >>"$summary"
done

if [ "${#failed[@]}" -gt 0 ]; then
	if [ -n "${RUNNER_TEMP:-}" ]; then
		mkdir -p "${RUNNER_TEMP}/fuzz-cache"
		cp -r "$(go env GOCACHE)/fuzz/." "${RUNNER_TEMP}/fuzz-cache/" 2>/dev/null || true
	fi
	echo "::error title=fuzz::failing targets: ${failed[*]} (inputs in testdata/fuzz, uploaded as the fuzz-corpus artifact)"
	exit 1
fi
echo "fuzzed ${#targets[@]} target(s) for ${FUZZTIME} each"
