#!/usr/bin/env bash
# Tests for guardrail-check.sh against throwaway Git repositories. Run with `make test-scripts`.

set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/guardrail-check.sh"
failures=0

repo() {
	dir=$(mktemp -d)
	git -C "$dir" init -q -b main
	git -C "$dir" config user.name test
	git -C "$dir" config user.email test@example.com
	git -C "$dir" config commit.gpgsign false
	git -C "$dir" config tag.gpgsign false
	echo "package foo" >"$dir/foo.go"
	git -C "$dir" add foo.go
	git -C "$dir" commit -q -m "chore: base"
	base=$(git -C "$dir" rev-parse HEAD)
}

expect_exit() {
	local name=$1 want_code=$2 want_substr=${3:-} approved=${4:-}
	local out code=0
	out=$(cd "$dir" && GUARDRAIL_APPROVED="$approved" bash "$script" --base "$base" --head HEAD 2>&1) || code=$?

	if [[ "$code" -eq "$want_code" ]] && { [[ -z "$want_substr" ]] || [[ "$out" == *"$want_substr"* ]]; }; then
		echo "ok   $name"
	else
		echo "FAIL $name: want exit $want_code containing '$want_substr', got exit $code:"
		echo "$out"
		failures=$((failures + 1))
	fi

	rm -rf "$dir"
}

repo
echo "// normal change" >>"$dir/foo.go"
git -C "$dir" add foo.go && git -C "$dir" commit -q -m "feat: normal"
expect_exit "normal code change passes" 0 "ok"

repo
echo "version: 2" >"$dir/.golangci.yml"
git -C "$dir" add .golangci.yml && git -C "$dir" commit -q -m "chore: edit lint"
expect_exit "golangci.yml change trips alarm" 1 ".golangci.yml"

repo
mkdir -p "$dir/internal/core"
echo "package core" >"$dir/internal/core/boundary_test.go"
git -C "$dir" add internal/core/boundary_test.go && git -C "$dir" commit -q -m "test: edit boundary"
expect_exit "boundary_test.go change trips alarm" 1 "boundary_test.go"

repo
mkdir -p "$dir/.github/workflows"
echo "name: CI" >"$dir/.github/workflows/ci.yml"
git -C "$dir" add .github/workflows/ci.yml && git -C "$dir" commit -q -m "ci: edit workflow"
expect_exit "workflow change trips alarm" 1 ".github/workflows/ci.yml"

repo
echo "func x() {} //nolint:gosec // bypass" >>"$dir/foo.go"
git -C "$dir" add foo.go && git -C "$dir" commit -q -m "fix: add nolint"
expect_exit "added nolint directive trips alarm" 1 "nolint"

repo
cat >"$dir/foo_test.go" <<'EOF'
package foo
import "testing"
func TestX(t *testing.T) {
	t.Skip("flaky")
}
EOF
git -C "$dir" add foo_test.go && git -C "$dir" commit -q -m "test: skip"
expect_exit "added t.Skip trips alarm" 1 "t.Skip"

repo
echo "version: 2" >"$dir/.golangci.yml"
git -C "$dir" add .golangci.yml && git -C "$dir" commit -q -m "chore: edit lint"
expect_exit "approved guardrail change passes" 0 ".golangci.yml" "true"

if ((failures > 0)); then
	echo "$failures failed"
	exit 1
fi
