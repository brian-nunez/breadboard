#!/bin/bash
# Regenerates ../breadboard-ui.figcraft and ../renders/*.png from the Python screen definitions.
# Requires figcraft-cli and python3. Pass screen function names to build a subset.
set -euo pipefail
SRC="$(cd "$(dirname "$0")" && pwd)"
UI="$(dirname "$SRC")"
python3 "$SRC/screens2.py" "$@" >/dev/null
rm -f "$UI/breadboard-ui.figcraft"
figcraft-cli run "$SRC/out.json" --out "$UI/breadboard-ui.figcraft" | grep -v '"ok":true' || true
rm -rf "$UI/renders"
figcraft-cli export "$UI/breadboard-ui.figcraft" -o "$UI/renders" --format png >/dev/null
rm -f "$SRC/out.json"
