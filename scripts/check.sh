#!/usr/bin/env bash
# Local gate: the same lint, unit-test and build classes as the lint,
# unit-tests and build jobs of .github/workflows/CI.yaml (images are built
# in CI only). Fails fast and prints a summary.
#
#   bash scripts/check.sh                   # lint, unit-tests, build
#   bash scripts/check.sh lint unit-tests   # selected classes
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

export CGO_ENABLED=0
GOLANGCI_LINT_VERSION="2.13.2"

web_deps() {
	if [ ! -d web/node_modules ] || [ web/package-lock.json -nt web/node_modules/.package-lock.json ]; then
		npm ci --prefix web --no-audit --no-fund
	fi
}

check_lint() {
	web_deps || return 1
	echo "==> gofmt"
	local unformatted
	unformatted="$(git ls-files -z -- '*.go' | xargs -0 gofmt -l)" || return 1
	if [ -n "$unformatted" ]; then
		echo "gofmt: these files need formatting (run: gofmt -w <file>):" >&2
		echo "$unformatted" >&2
		return 1
	fi
	echo "==> prettier"
	npm --prefix web run --silent format:check || return 1
	echo "==> golangci-lint"
	if ! command -v golangci-lint >/dev/null 2>&1; then
		echo "golangci-lint not found; install v${GOLANGCI_LINT_VERSION} (see docs/development.md)" >&2
		return 1
	fi
	local have
	have="$(golangci-lint version --short 2>/dev/null || golangci-lint --version)"
	case "$have" in
	*"$GOLANGCI_LINT_VERSION"*) ;;
	*) echo "warning: golangci-lint ${have} found; CI uses v${GOLANGCI_LINT_VERSION}" >&2 ;;
	esac
	golangci-lint run ./... || return 1
	# CI lints on Linux: files behind a linux build constraint are invisible
	# to a lint run on another host OS, so lint them for GOOS=linux as well.
	if [ "$(go env GOOS)" != "linux" ]; then
		echo "==> golangci-lint (GOOS=linux)"
		GOOS=linux golangci-lint run ./... || return 1
	fi
	echo "==> policy-check"
	bash scripts/policy-check.sh || return 1
	echo "==> eslint"
	npm --prefix web run --silent lint
}

check_unit_tests() {
	web_deps || return 1
	echo "==> go test"
	go test ./... || return 1
	echo "==> vitest"
	npm --prefix web run --silent test
}

check_build() {
	web_deps || return 1
	echo "==> web build"
	npm --prefix web run --silent build || return 1
	node web/scripts/verify-build.mjs || return 1
	echo "==> go build"
	go build ./... || return 1
	echo "==> release binaries"
	bash scripts/build-static.sh
}

classes=("$@")
[ ${#classes[@]} -gt 0 ] || classes=(lint unit-tests build)

results=()
summary() {
	echo
	echo "==================== check summary ===================="
	for r in "${results[@]}"; do
		echo "  $r"
	done
	echo "======================================================="
}

for class in "${classes[@]}"; do
	case "$class" in
	lint | unit-tests | build) ;;
	*)
		echo "unknown class ${class} (lint, unit-tests, build)" >&2
		exit 2
		;;
	esac
	echo
	echo "######## ${class}"
	start=$SECONDS
	if "check_${class//-/_}"; then
		results+=("PASS  ${class} ($((SECONDS - start))s)")
	else
		results+=("FAIL  ${class} ($((SECONDS - start))s)")
		summary
		exit 1
	fi
done
summary
echo "all checks passed"
