#!/bin/sh
# greybeard launcher for the Claude Code plugin: resolves the binary,
# bootstrapping it from GitHub Releases on first use so installing the
# plugin is the only setup step. The hook and the MCP server entry both
# route through this script.
#
# Resolution order:
#   1. `greybeard` on PATH (go install, package manager, manual) — always wins
#   2. ~/.greybeard/bin/greybeard — our bootstrapped copy
#   3. download the release asset for this OS/arch, verify its sha256
#      against the release's checksums.txt, install to ~/.greybeard/bin
#
# Windows has no /bin/sh by default — Windows users install the binary
# manually (see the README); this script serves macOS and Linux.
set -e

REPO="deepaksinghcs14/greybeard"

if command -v greybeard >/dev/null 2>&1; then
  exec greybeard "$@"
fi

BIN_DIR="$HOME/.greybeard/bin"
BIN="$BIN_DIR/greybeard"

if [ ! -x "$BIN" ]; then
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  arch=$(uname -m)
  case "$arch" in
    x86_64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) echo "greybeard bootstrap: unsupported arch $arch" >&2; exit 1 ;;
  esac
  asset="greybeard_${os}_${arch}"
  base="https://github.com/$REPO/releases/latest/download"

  mkdir -p "$BIN_DIR"
  tmp="$BIN_DIR/.$asset.download"
  trap 'rm -f "$tmp" "$tmp.sums"' EXIT

  curl -fsSL "$base/$asset" -o "$tmp"
  curl -fsSL "$base/checksums.txt" -o "$tmp.sums"

  expected=$(grep " $asset\$" "$tmp.sums" | awk '{print $1}')
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$tmp" | awk '{print $1}')
  else
    actual=$(shasum -a 256 "$tmp" | awk '{print $1}')
  fi
  if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
    echo "greybeard bootstrap: checksum mismatch for $asset — refusing to install" >&2
    exit 1
  fi

  chmod +x "$tmp"
  mv "$tmp" "$BIN" # rename, not copy-over: macOS kills binaries rewritten in place
  rm -f "$tmp.sums" # explicit: the EXIT trap never fires through exec below
fi

exec "$BIN" "$@"
