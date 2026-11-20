#!/usr/bin/env bash
# Runs the Playwright web UI smoke test against a live `bb serve` instance in a
# throwaway experiment directory. Run with `make test-e2e`.

set -euo pipefail
cd "$(dirname "$0")/.."

if [[ ! -d tests/e2e/node_modules/playwright ]]; then
	echo "e2e.sh: installing tests/e2e Playwright dependencies..."
	(cd tests/e2e && npm ci)
fi

tmpdir=$(mktemp -d)
server_pid=""
cleanup() {
	if [[ -n "$server_pid" ]]; then
		kill "$server_pid" 2>/dev/null || true
		wait "$server_pid" 2>/dev/null || true
	fi
	rm -rf "$tmpdir"
}
trap cleanup EXIT

bb_bin="$tmpdir/bb"
go build -o "$bb_bin" ./bb

export BB_HOME="$tmpdir/home"
lab="$tmpdir/e2e-lab"
mkdir -p "$lab"

"$bb_bin" -C "$lab" init e2e-lab >/dev/null
"$bb_bin" -C "$lab" add postgres >/dev/null
"$bb_bin" -C "$lab" add redis >/dev/null
"$bb_bin" -C "$lab" add app api >/dev/null

log="$tmpdir/serve.log"
"$bb_bin" -C "$lab" serve --no-open --addr 127.0.0.1:0 >"$log" 2>&1 &
server_pid=$!

launch_url=""
for _ in $(seq 1 50); do
	if [[ -f "$log" ]]; then
		launch_url=$(grep -Eo 'http://127\.0\.0\.1:[0-9]+/auth\?token=[^[:space:]]+' "$log" | head -n 1 || true)
		if [[ -n "$launch_url" ]]; then
			break
		fi
	fi
	if ! kill -0 "$server_pid" 2>/dev/null; then
		echo "e2e.sh: bb serve exited early:" >&2
		cat "$log" >&2
		exit 1
	fi
	sleep 0.1
done

if [[ -z "$launch_url" ]]; then
	echo "e2e.sh: timed out waiting for bb serve launch URL:" >&2
	cat "$log" >&2
	exit 1
fi

BB_LAUNCH_URL="$launch_url" node tests/e2e/smoke.mjs
