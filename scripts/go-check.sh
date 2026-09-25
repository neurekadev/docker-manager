#!/usr/bin/env bash
# Go gate: gofmt, go vet, golangci-lint (pinned v2.13.2), go test.
#
# Environment:
#   GO_CHECK_SKIP_LINT=1   skip golangci-lint (CI runs it via the pinned action)
#   GO_TEST_FLAGS="..."    extra flags for go test (e.g. -race, -coverprofile=coverage.out)
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

GOLANGCI_LINT_VERSION="2.13.2"

echo "==> gofmt"
go_dirs=()
for d in cmd internal test tools; do
	[ -d "$d" ] && go_dirs+=("$d")
done
unformatted="$(gofmt -l "${go_dirs[@]}" web/*.go)"
if [ -n "$unformatted" ]; then
	echo "gofmt: these files need formatting (run: gofmt -w <file>):" >&2
	echo "$unformatted" >&2
	exit 1
fi

echo "==> go vet"
go vet ./...
# Tagged test code (Docker-backed harness, fault injection) must keep
# compiling even though only the extended workflow runs it.
go vet -tags integration,e2e,faultinject ./...

if [ "${GO_CHECK_SKIP_LINT:-0}" != "1" ]; then
	echo "==> golangci-lint"
	if ! command -v golangci-lint >/dev/null 2>&1; then
		echo "golangci-lint not found; install v${GOLANGCI_LINT_VERSION} (see docs/development.md)" >&2
		exit 1
	fi
	have="$(golangci-lint version --short 2>/dev/null || golangci-lint --version)"
	case "$have" in
	*"$GOLANGCI_LINT_VERSION"*) ;;
	*) echo "warning: golangci-lint ${have} found; CI uses v${GOLANGCI_LINT_VERSION}" >&2 ;;
	esac
	golangci-lint run ./...
	# CI lints on Linux: files behind a linux build constraint (agent
	# fixtures, inotify watcher, procfs sampler) are invisible to a lint run
	# on another host OS, so lint them for GOOS=linux as well.
	if [ "$(go env GOOS)" != "linux" ]; then
		echo "==> golangci-lint (GOOS=linux)"
		GOOS=linux golangci-lint run ./...
	fi
fi

echo "==> go test"
# shellcheck disable=SC2086 # GO_TEST_FLAGS is intentionally word-split
go test ${GO_TEST_FLAGS:-} ./...
