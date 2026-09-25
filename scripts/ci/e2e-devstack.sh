#!/usr/bin/env bash
# Playwright UI specs against the Docker-free devstack (#22, #23, #29).
#
# The proxy E2E stack (e2e/compose.yaml) has no agents, so the UI track
# specs skip their seeded steps there. This runner starts test/devstack (a
# real manager with in-process agents over fake Engines, seeded with the
# homelab of docs/development.md) and runs each spec against a FRESH
# devstack, because several specs deploy, delete or edit seeded data:
#
#   ui-setup    ui.spec.ts on a -setup devstack (first-run setup in the browser)
#   ui, b1-environments, stacks, resources, ui-files, ui-logs,
#   admin-access, admin-automation, admin-settings, auth-factors, live
#               the spec of that name on a seeded devstack (owner admin)
#   import      a -setup devstack that keeps the seeded run's backups:
#               admin-automation.spec.ts "fresh-manager import (#24)"
#
# Specs needing a real Engine (terminal.spec.ts, ui-terminal.spec.ts: exec)
# run in the extended `smoke` job against the deploy smoke deployment.
#
#   bash scripts/ci/e2e-devstack.sh                 # every group
#   bash scripts/ci/e2e-devstack.sh stacks live     # selected groups
#
# Environment:
#   E2E_DEVSTACK_PORT        listen port (default 8090)
#   E2E_DEVSTACK_WORK        work directory (default: a temp dir)
#   E2E_DEVSTACK_SKIP_BUILD  1 = reuse web/build/app and the devstack binary
#   E2E_DEVSTACK_ARTIFACTS   directory for devstack logs (default: work dir)
# Needs Go, Node (npm ci in web/ and e2e/ done) and Playwright's Chromium.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
root="$(pwd)"

port="${E2E_DEVSTACK_PORT:-8090}"
work="${E2E_DEVSTACK_WORK:-$(mktemp -d)}"
artifacts="${E2E_DEVSTACK_ARTIFACTS:-$work}"
base="http://localhost:${port}"
mkdir -p "$work" "$artifacts"
data="${work}/data"
backups="${work}/backups"
bin="${work}/devstack"
case "$(go env GOOS)" in windows) bin="${bin}.exe" ;; esac

# Seeded accounts of test/devstack (published, localhost-only test
# credentials; see test/devstack/main.go).
owner=admin
owner_pw=dockyard-devstack-owner
guest=guest
guest_pw=dockyard-devstack-guest

all_groups=(ui-setup ui b1-environments stacks resources ui-files ui-logs admin-access admin-automation admin-settings auth-factors live import)
groups=("$@")
[ "${#groups[@]}" -gt 0 ] || groups=("${all_groups[@]}")

if [ "${E2E_DEVSTACK_SKIP_BUILD:-0}" != 1 ]; then
	echo "==> build web/build/app and the devstack"
	npm --prefix web run build >"${artifacts}/web-build.log" 2>&1 || {
		tail -40 "${artifacts}/web-build.log"
		exit 1
	}
	go build -o "$bin" ./test/devstack
fi

pid=""
log=""
stop_devstack() {
	if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
		kill "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
	fi
	pid=""
}
trap stop_devstack EXIT

# start_devstack NAME [flags...]: starts a devstack and waits until it
# printed its summary (seeding done).
start_devstack() {
	local name="$1"
	shift
	log="${artifacts}/devstack-${name}.log"
	"$bin" -addr "127.0.0.1:${port}" -data "$data" -backups "$backups" -log-level warn "$@" >"$log" 2>&1 &
	pid=$!
	local deadline=$((SECONDS + 180))
	until grep -q "DockYard devstack is running" "$log"; do
		if ! kill -0 "$pid" 2>/dev/null; then
			echo "devstack (${name}) exited early:" >&2
			tail -40 "$log" >&2
			return 1
		fi
		if [ "$SECONDS" -ge "$deadline" ]; then
			echo "devstack (${name}) did not become ready within 180 s" >&2
			tail -40 "$log" >&2
			return 1
		fi
		sleep 1
	done
}

# json EXPR: evaluates a JS expression over the JSON on stdin (as `j`);
# node instead of jq so the script also runs on development machines.
json() { node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);const v=('"$1"');process.stdout.write(v===undefined||v===null?"":String(v))})'; }

# silo_location prints "<stack id> <project directory>" of the seeded stack
# Silo, read through the API as the owner.
silo_location() {
	local jar="${work}/cookies.txt" id
	curl -sS --fail -c "$jar" -H 'Content-Type: application/json' \
		-d "{\"username\":\"${owner}\",\"password\":\"${owner_pw}\"}" "${base}/api/v1/auth/session" >/dev/null
	id="$(curl -sS --fail -b "$jar" "${base}/api/v1/stacks?limit=100" | json 'j.items.find(s=>s.name==="silo")?.id')"
	[ -n "$id" ] || {
		echo "no seeded stack silo" >&2
		return 1
	}
	printf '%s %s\n' "$id" "$(curl -sS --fail -b "$jar" "${base}/api/v1/stacks/${id}" | json 'j.location.hostPath')"
	rm -f "$jar"
}

playwright() {
	(cd e2e && E2E_BASE_URL="$base" npx playwright test --workers=1 "$@")
}

failed=()
passed=()
# run_group runs one group. It is called in an `if`, where errexit does not
# apply, so every step returns explicitly on failure.
run_group() {
	local g="$1" spec
	echo "==> ${g}"
	case "$g" in
	ui-setup)
		# First-run setup in the browser on a fresh manager (the spec's
		# default owner), then the rest of ui.spec.ts as that owner.
		start_devstack "$g" -setup || return 1
		playwright tests/ui.spec.ts
		;;
	import)
		# Needs the backups and Recovery Key of a seeded run: run one
		# (admin-automation also exercises the backup screens), then a
		# -setup devstack that keeps them.
		start_devstack "${g}-seed" || return 1
		local key
		key="$(sed -n 's/^ *recovery key *//p' "$log" | head -1)"
		stop_devstack
		[ -n "$key" ] || {
			echo "the seeded devstack printed no Recovery Key" >&2
			return 1
		}
		start_devstack "$g" -setup || return 1
		E2E_IMPORT_DIR="${backups}/manager" E2E_IMPORT_KEY="$key" \
			playwright tests/admin-automation.spec.ts -g "setup imports a backup set"
		;;
	live)
		start_devstack "$g" || return 1
		local loc
		loc="$(silo_location)" || return 1
		E2E_LIVE_USER="$owner" E2E_LIVE_PASSWORD="$owner_pw" E2E_LIVE_STACK="${loc%% *}" E2E_LIVE_STACK_DIR="${loc#* }" \
			playwright tests/live.spec.ts
		;;
	ui-files)
		start_devstack "$g" || return 1
		local loc
		loc="$(silo_location)" || return 1
		E2E_UI_OWNER="$owner" E2E_UI_PASSWORD="$owner_pw" E2E_FILES_STACK_DIR="${loc#* }" \
			playwright tests/ui-files.spec.ts
		;;
	*)
		spec="tests/${g}.spec.ts"
		[ -f "e2e/${spec}" ] || {
			echo "unknown group ${g} (no e2e/${spec})" >&2
			return 1
		}
		start_devstack "$g" || return 1
		# E2E_FACTORS: auth-factors.spec.ts may change the owner's sign-in
		# factors on this dedicated devstack.
		E2E_UI_OWNER="$owner" E2E_UI_PASSWORD="$owner_pw" E2E_UI_GUEST="$guest" E2E_UI_GUEST_PASSWORD="$guest_pw" E2E_FACTORS=1 \
			playwright "$spec"
		;;
	esac
}

for g in "${groups[@]}"; do
	if run_group "$g"; then
		passed+=("$g")
	else
		failed+=("$g")
	fi
	stop_devstack
done

{
	echo "### Playwright against the devstack"
	echo
	echo "- passed: ${passed[*]:-none}"
	echo "- failed: ${failed[*]:-none}"
} | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
cd "$root"
[ "${#failed[@]}" -eq 0 ]
