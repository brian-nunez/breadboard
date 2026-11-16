#!/bin/sh
# Builds the web UI's generated files: templ output (*_templ.go) and the
# compiled Tailwind stylesheet. Both are committed, because go install runs
# no generators.
#
#   scripts/web.sh generate   regenerate both
#   scripts/web.sh check      regenerate and fail if anything changed
#
# Tools: templ runs through go run at the version in go.mod; Tailwind's
# standalone CLI is downloaded once into .tools/, checked against its pinned
# SHA-256. No npm.
set -eu
cd "$(dirname "$0")/.."

TAILWIND_VERSION=v4.3.5
case "$(uname -s)-$(uname -m)" in
  Darwin-arm64) asset=tailwindcss-macos-arm64 sum=f3f4f5d1727e2b693b8f1c9a1492b5dd87008c86ea286bee1589cb458e6235fc ;;
  Darwin-x86_64) asset=tailwindcss-macos-x64 sum=a63702686a3ed048b3135d84a73be49ea9d47f880c9b20e54b3ef60934ad8cdf ;;
  Linux-aarch64 | Linux-arm64) asset=tailwindcss-linux-arm64 sum=ffcd0d21dbd7c605dc12743ff0c0b47debc963c86b399674084c1c8f2c96ecad ;;
  Linux-x86_64) asset=tailwindcss-linux-x64 sum=e47695cee3f0d90623a91f3939b08bdee610eb5ad99340adff9d4145b284d6cc ;;
  *) echo "web.sh: no pinned Tailwind CLI for $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac

tailwind=".tools/tailwindcss-$TAILWIND_VERSION"
if [ ! -x "$tailwind" ]; then
  mkdir -p .tools
  curl -fsSL -o "$tailwind.part" "https://github.com/tailwindlabs/tailwindcss/releases/download/$TAILWIND_VERSION/$asset"
  got=$(shasum -a 256 "$tailwind.part" | cut -d' ' -f1)
  if [ "$got" != "$sum" ]; then
    rm -f "$tailwind.part"
    echo "web.sh: $asset checksum mismatch: got $got, want $sum" >&2
    exit 1
  fi
  chmod +x "$tailwind.part"
  mv "$tailwind.part" "$tailwind"
fi

templ_version=$(go list -m -f '{{.Version}}' github.com/a-h/templ)

generate() {
  go run "github.com/a-h/templ/cmd/templ@$templ_version" generate -path internal/web >/dev/null
  "$tailwind" -i internal/web/styles/input.css -o internal/web/static/app.css --minify 2>/dev/null
}

case "${1:-generate}" in
  generate) generate ;;
  check)
    generate
    if ! git diff --quiet -- internal/web; then
      echo "web.sh: generated files are out of date; run scripts/web.sh generate and commit" >&2
      git diff --stat -- internal/web >&2
      exit 1
    fi
    ;;
  *) echo "usage: scripts/web.sh [generate|check]" >&2; exit 2 ;;
esac
