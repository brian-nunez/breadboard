#!/usr/bin/env bash
# Audits a git diff for changes to deterministic guardrails (linter/formatter
# config, CI workflows, build/tool scripts, architectural boundary tests,
# design-doc table tests, CODEOWNERS, AGENTS.md) or newly introduced lint/test
# bypasses (//nolint, t.Skip).
#
# Usage:
#   scripts/guardrail-check.sh [--base <ref>] [--head <ref>] [--markdown]
#
# Exit codes:
#   0  no guardrail changes found, or GUARDRAIL_APPROVED=true is set
#   1  guardrail change or bypass detected without explicit approval
#   2  usage error

set -euo pipefail

base=""
head="HEAD"
markdown=false

while [[ $# -gt 0 ]]; do
	case "$1" in
		--base)
			base="${2:-}"
			shift 2
			;;
		--head)
			head="${2:-}"
			shift 2
			;;
		--markdown)
			markdown=true
			shift
			;;
		-h | --help)
			sed -n '2,14p' "$0"
			exit 0
			;;
		*)
			echo "guardrail-check: unknown argument: $1" >&2
			exit 2
			;;
	esac
done

if [[ -z "$base" ]]; then
	if git rev-parse --verify -q origin/main >/dev/null 2>&1; then
		base=$(git merge-base origin/main "$head")
	elif git rev-parse --verify -q HEAD~1 >/dev/null 2>&1; then
		base="HEAD~1"
	else
		echo "guardrail-check: specify --base <ref>" >&2
		exit 2
	fi
fi

is_guardrail_file() {
	local f="$1"
	case "$f" in
		.golangci.yml | Makefile | AGENTS.md | .github/* | scripts/* | internal/core/boundary_test.go | internal/designdocs/*)
			return 0
			;;
		*)
			return 1
			;;
	esac
}

changed_guardrails=()
while IFS= read -r file; do
	[[ -z "$file" ]] && continue
	if is_guardrail_file "$file"; then
		changed_guardrails+=("$file")
	fi
done < <(git diff --name-only "$base" "$head")

nolint_hits=()
skip_hits=()
current_file=""

while IFS= read -r line; do
	if [[ "$line" == "+++ b/"* ]]; then
		current_file="${line#+++ b/}"
		continue
	fi
	if [[ "$line" == "+++ /dev/null" ]]; then
		current_file=""
		continue
	fi
	[[ -z "$current_file" ]] && continue
	[[ "$current_file" != *.go ]] && continue
	[[ "$current_file" == *_templ.go ]] && continue
	[[ "$current_file" == "internal/core/boundary_test.go" ]] && continue
	[[ "$line" != "+"* || "$line" == "+++"* ]] && continue

	added="${line#+}"
	if [[ "$added" =~ //[[:space:]]*nolint ]]; then
		nolint_hits+=("${current_file}: ${added}")
	fi
	if [[ "$current_file" == *_test.go ]] && [[ "$added" =~ [[:space:]\.]t\.Skip(f|Now)?\( ]]; then
		skip_hits+=("${current_file}: ${added}")
	fi
done < <(git diff --unified=0 "$base" "$head" -- '*.go')

if ((${#changed_guardrails[@]} == 0 && ${#nolint_hits[@]} == 0 && ${#skip_hits[@]} == 0)); then
	if [[ "$markdown" == true ]]; then
		echo "No guardrail changes or lint/test bypasses detected."
	else
		echo "guardrail-check: ok (no deterministic guardrail changes or bypasses)"
	fi
	exit 0
fi

print_report() {
	if [[ "$markdown" == true ]]; then
		cat <<'EOF'
<!-- guardrail-alarm -->
## Guardrail Change Alarm

This change modifies Breadboard's deterministic guardrails or introduces a lint/test bypass.
**Do not merge without verifying why the check is changing and how it affects the verification loop.**

EOF
		if ((${#changed_guardrails[@]} > 0)); then
			echo "### Protected guardrail files modified"
			for f in "${changed_guardrails[@]}"; do
				echo "- \`$f\`"
			done
			echo ""
		fi
		if ((${#nolint_hits[@]} > 0)); then
			echo "### \`//nolint\` directives added"
			for h in "${nolint_hits[@]}"; do
				echo "- \`$h\`"
			done
			echo ""
		fi
		if ((${#skip_hits[@]} > 0)); then
			echo "### \`t.Skip\` calls added"
			for h in "${skip_hits[@]}"; do
				echo "- \`$h\`"
			done
			echo ""
		fi
		cat <<'EOF'
### Reviewer checklist
1. **Why** is this deterministic rule, linter, boundary, workflow, or bypass changing?
2. Does it **tighten** or **loosen** enforcement?
3. If approved, add the `guardrail-approved` label to clear this alarm.
EOF
	else
		echo "ALARM: deterministic guardrail change or bypass detected!"
		if ((${#changed_guardrails[@]} > 0)); then
			echo "  Protected guardrail files modified:"
			for f in "${changed_guardrails[@]}"; do
				echo "    - $f"
			done
		fi
		if ((${#nolint_hits[@]} > 0)); then
			echo "  //nolint directives added:"
			for h in "${nolint_hits[@]}"; do
				echo "    - $h"
			done
		fi
		if ((${#skip_hits[@]} > 0)); then
			echo "  t.Skip calls added:"
			for h in "${skip_hits[@]}"; do
				echo "    - $h"
			done
		fi
		echo "  Explain why this guardrail change is necessary and set GUARDRAIL_APPROVED=true (or add the 'guardrail-approved' PR label) after review."
	fi
}

print_report

if [[ "${GUARDRAIL_APPROVED:-}" == "true" ]]; then
	exit 0
fi

exit 1
