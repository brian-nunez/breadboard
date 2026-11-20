#!/bin/sh
# Installs and runs Breadboard's pinned developer and CI tools inside .tools/.
# Both local `make check` and GitHub Actions invoke this script so every run
# uses the exact same linter, vulnerability scanner, and analyzer versions
# without adding dev-only modules to go.mod.
#
#   scripts/tools.sh install                  ensure check tools are installed
#   scripts/tools.sh golangci-lint [args...]  run pinned golangci-lint
#   scripts/tools.sh govulncheck [args...]    run pinned govulncheck
#   scripts/tools.sh deadcode [args...]       run pinned deadcode
#   scripts/tools.sh gremlins [args...]       run pinned gremlins (mutation testing)
#   scripts/tools.sh versions                 print pinned tool versions
set -eu
cd "$(dirname "$0")/.."

GOLANGCI_LINT_VERSION=v2.15.0
GOVULNCHECK_VERSION=v1.10.0
DEADCODE_VERSION=v0.53.0
GREMLINS_VERSION=v0.6.0

ensure_golangci_lint() {
  ver_num="${GOLANGCI_LINT_VERSION#v}"
  case "$(uname -s)-$(uname -m)" in
    Darwin-arm64)
      asset="golangci-lint-${ver_num}-darwin-arm64.tar.gz"
      dir="golangci-lint-${ver_num}-darwin-arm64"
      sum=d1e409481a8ca846c4bc97e781eb5cb1bd5c23353a0b3977a1cbda1b1b6447d5
      ;;
    Darwin-x86_64)
      asset="golangci-lint-${ver_num}-darwin-amd64.tar.gz"
      dir="golangci-lint-${ver_num}-darwin-amd64"
      sum=767b899a28285e88ded2823d5dcc4bc5ef681173eb2d80f029a7b17ee4ece3b8
      ;;
    Linux-aarch64 | Linux-arm64)
      asset="golangci-lint-${ver_num}-linux-arm64.tar.gz"
      dir="golangci-lint-${ver_num}-linux-arm64"
      sum=b4d8f17068b35ee6060cc2f69a16fb48740d706fbba81661c318649c114a6998
      ;;
    Linux-x86_64)
      asset="golangci-lint-${ver_num}-linux-amd64.tar.gz"
      dir="golangci-lint-${ver_num}-linux-amd64"
      sum=9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea
      ;;
    *)
      echo "tools.sh: no pinned golangci-lint binary for $(uname -s)-$(uname -m)" >&2
      exit 1
      ;;
  esac

  bin=".tools/golangci-lint-${GOLANGCI_LINT_VERSION}"
  if [ -x "$bin" ]; then
    return 0
  fi

  mkdir -p .tools
  tmp_tar=".tools/${asset}.part"
  tmp_dir=".tools/${dir}.extract"
  rm -rf "$tmp_tar" "$tmp_dir"
  curl -fsSL -o "$tmp_tar" "https://github.com/golangci/golangci-lint/releases/download/${GOLANGCI_LINT_VERSION}/${asset}"
  got=$(shasum -a 256 "$tmp_tar" | cut -d' ' -f1)
  if [ "$got" != "$sum" ]; then
    rm -f "$tmp_tar"
    echo "tools.sh: ${asset} checksum mismatch: got ${got}, want ${sum}" >&2
    exit 1
  fi
  mkdir -p "$tmp_dir"
  tar -xzf "$tmp_tar" -C "$tmp_dir"
  mv "${tmp_dir}/${dir}/golangci-lint" "$bin"
  chmod +x "$bin"
  rm -rf "$tmp_tar" "$tmp_dir"
}

ensure_go_tool() {
  name="$1"
  pkg="$2"
  ver="$3"
  bin=".tools/${name}-${ver}"
  if [ -x "$bin" ]; then
    return 0
  fi

  mkdir -p .tools/tmpbin
  rm -f ".tools/tmpbin/${name}"
  GOBIN="$(pwd)/.tools/tmpbin" go install "${pkg}@${ver}"
  mv ".tools/tmpbin/${name}" "$bin"
  rmdir .tools/tmpbin 2>/dev/null || true
}

cmd="${1:-}"
if [ -z "$cmd" ]; then
  echo "usage: scripts/tools.sh [install|golangci-lint|govulncheck|deadcode|gremlins|versions]" >&2
  exit 2
fi
shift

case "$cmd" in
  install)
    ensure_golangci_lint
    ensure_go_tool govulncheck golang.org/x/vuln/cmd/govulncheck "$GOVULNCHECK_VERSION"
    ensure_go_tool deadcode golang.org/x/tools/cmd/deadcode "$DEADCODE_VERSION"
    ;;
  golangci-lint)
    ensure_golangci_lint
    exec ".tools/golangci-lint-${GOLANGCI_LINT_VERSION}" "$@"
    ;;
  govulncheck)
    ensure_go_tool govulncheck golang.org/x/vuln/cmd/govulncheck "$GOVULNCHECK_VERSION"
    exec ".tools/govulncheck-${GOVULNCHECK_VERSION}" "$@"
    ;;
  deadcode)
    ensure_go_tool deadcode golang.org/x/tools/cmd/deadcode "$DEADCODE_VERSION"
    exec ".tools/deadcode-${DEADCODE_VERSION}" "$@"
    ;;
  gremlins)
    ensure_go_tool gremlins github.com/go-gremlins/gremlins/cmd/gremlins "$GREMLINS_VERSION"
    exec ".tools/gremlins-${GREMLINS_VERSION}" "$@"
    ;;
  versions)
    echo "golangci-lint=${GOLANGCI_LINT_VERSION}"
    echo "govulncheck=${GOVULNCHECK_VERSION}"
    echo "deadcode=${DEADCODE_VERSION}"
    echo "gremlins=${GREMLINS_VERSION}"
    ;;
  *)
    echo "tools.sh: unknown command '$cmd'" >&2
    exit 2
    ;;
esac
