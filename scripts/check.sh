#!/usr/bin/env bash
# Local gate: everything CI's PR suite checks that does not need Docker.
# Fails fast and prints a summary. Run from anywhere: bash scripts/check.sh
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

steps=(
	"policy:bash scripts/policy-check.sh"
	"generate:bash scripts/generate.sh --check"
	"go:bash scripts/go-check.sh"
	"web:bash scripts/web-check.sh"
)

results=()
summary() {
	echo
	echo "==================== check summary ===================="
	for r in "${results[@]}"; do
		echo "  $r"
	done
	echo "======================================================="
}

for step in "${steps[@]}"; do
	name="${step%%:*}"
	cmd="${step#*:}"
	echo
	echo "######## ${name}: ${cmd}"
	start=$SECONDS
	if $cmd; then
		results+=("PASS  ${name} ($((SECONDS - start))s)")
	else
		results+=("FAIL  ${name} ($((SECONDS - start))s)")
		summary
		exit 1
	fi
done
summary
echo "all checks passed"
