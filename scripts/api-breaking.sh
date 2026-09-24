#!/usr/bin/env bash
# Breaking-change check for the public API contract (#4, docs/api/versioning.md).
#
# Compares api/openapi.json with its version at the merge base of HEAD and
# the base ref (default origin/main) using a pinned oasdiff release binary.
# Prints the changelog, then fails on breaking changes (oasdiff level ERR)
# unless API_BREAKING_ALLOWED=true (CI sets it when the pull request carries
# the api-breaking-change label).
#
#   bash scripts/api-breaking.sh [base-ref]
#
# Environment:
#   OASDIFF              use this oasdiff binary instead of the pinned download
#   API_BREAKING_ALLOWED true: report breaking changes but exit 0
#   API_BASE_SPEC        compare against this file instead of the merge base
#   API_SPEC             the new spec (default api/openapi.json); with
#                        API_BASE_SPEC used by the CI self-test
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

OASDIFF_VERSION="1.32.1"
declare -A OASDIFF_SHA256=(
	[linux_amd64]="7c8939fc49b75ee11fec66a5b83b37a2fca6aee109fed85013b1ba2ac2a1ee7f"
	[linux_arm64]="32fff58a120f75a723d6c2422444691c37fa6813fed61d23f53dbcb604b30f6d"
	[darwin_all]="e4d74b7e2dfb9d4819e7fc720c905ec86547e4637ac270a2b0187c0f1fb7187e"
	[windows_amd64]="4d0758b32d454e6011e59db93884af1ca27ae2b212d990f36738ea5efb5d7f28"
	[windows_arm64]="7937f4506954676c495188b5c0a221019079801857fb7ba29dd2bc2a767cacab"
)

base_ref="${1:-origin/main}"
spec="${API_SPEC:-api/openapi.json}"

platform() {
	local os arch
	case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) echo darwin_all && return ;;
	MINGW* | MSYS* | CYGWIN*) os=windows ;;
	*) echo "unsupported OS $(uname -s)" >&2 && return 1 ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) echo "unsupported architecture $(uname -m)" >&2 && return 1 ;;
	esac
	echo "${os}_${arch}"
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

install_oasdiff() {
	local plat want dir exe archive url got
	plat="$(platform)"
	want="${OASDIFF_SHA256[$plat]}"
	dir="${XDG_CACHE_HOME:-$HOME/.cache}/dockyard/oasdiff-${OASDIFF_VERSION}-${plat}"
	exe="$dir/oasdiff"
	[[ "$plat" == windows_* ]] && exe="$dir/oasdiff.exe"
	if [ -x "$exe" ]; then
		echo "$exe"
		return
	fi
	mkdir -p "$dir"
	archive="$dir/oasdiff.tar.gz"
	url="https://github.com/oasdiff/oasdiff/releases/download/v${OASDIFF_VERSION}/oasdiff_${OASDIFF_VERSION}_${plat}.tar.gz"
	echo "==> downloading oasdiff ${OASDIFF_VERSION} (${plat})" >&2
	curl -fsSL --retry 3 -o "$archive" "$url"
	got="$(sha256 "$archive")"
	if [ "$got" != "$want" ]; then
		echo "oasdiff archive checksum mismatch: got $got, want $want" >&2
		rm -f "$archive"
		return 1
	fi
	tar -xzf "$archive" -C "$dir"
	rm -f "$archive"
	chmod +x "$exe"
	echo "$exe"
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

if [ -n "${API_BASE_SPEC:-}" ]; then
	cp "$API_BASE_SPEC" "$tmp/base.json"
	base_desc="$API_BASE_SPEC"
else
	if ! git rev-parse --verify --quiet "${base_ref}^{commit}" >/dev/null; then
		echo "api-breaking: base ref ${base_ref} not found (fetch it first, e.g. git fetch origin main)" >&2
		exit 2
	fi
	merge_base="$(git merge-base HEAD "$base_ref")"
	if ! git cat-file -e "${merge_base}:api/openapi.json" 2>/dev/null; then
		echo "api-breaking: api/openapi.json does not exist at the merge base ${merge_base}; nothing to compare"
		exit 0
	fi
	git show "${merge_base}:api/openapi.json" >"$tmp/base.json"
	base_desc="merge base ${merge_base:0:12} (${base_ref})"
fi

oasdiff="${OASDIFF:-}"
if [ -z "$oasdiff" ]; then
	oasdiff="$(install_oasdiff)"
fi

echo "==> API changes since ${base_desc}"
"$oasdiff" changelog "$tmp/base.json" "$spec"

echo "==> breaking changes"
if "$oasdiff" breaking "$tmp/base.json" "$spec" --fail-on ERR; then
	echo "api-breaking: no breaking changes"
	exit 0
fi
if [ "${API_BREAKING_ALLOWED:-false}" = "true" ]; then
	echo "api-breaking: breaking changes approved by the api-breaking-change label (see docs/api/versioning.md)"
	exit 0
fi
echo "api-breaking: the API contract has breaking changes (above)." >&2
echo "Make the change compatible, or follow docs/api/versioning.md and add the api-breaking-change label." >&2
exit 1
