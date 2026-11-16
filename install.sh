#!/bin/sh
# Anchor installer for macOS and Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/JingxuanC/anchor/main/install.sh | sh
#
# Environment overrides:
#   ANCHOR_VERSION      install a specific tag (e.g. v0.2.0) instead of latest
#   ANCHOR_INSTALL_DIR  install directory (default: ~/.local/bin)
set -eu

REPO="JingxuanC/anchor"
BINARY="anchor"
INSTALL_DIR="${ANCHOR_INSTALL_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "$*" >&2; }
die() { say "anchor install: $*"; exit 1; }

os="$(uname -s)"
case "$os" in
  Darwin) os="darwin" ;;
  Linux)  os="linux" ;;
  *) die "unsupported OS '$os' (macOS/Linux only; use the release archives on Windows)" ;;
esac
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) die "unsupported architecture '$arch' (need amd64 or arm64)" ;;
esac

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO "$2" "$1"; }
else
  die "curl or wget is required"
fi
if command -v sha256sum >/dev/null 2>&1; then
  checksum() { sha256sum -c "$1"; }
elif command -v shasum >/dev/null 2>&1; then
  checksum() { shasum -a 256 -c "$1"; }
else
  die "sha256sum or shasum is required to verify the download"
fi

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/anchor-install.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT INT TERM

tag="${ANCHOR_VERSION:-}"
if [ -z "$tag" ]; then
  say "anchor install: resolving the latest release of $REPO"
  fetch "https://api.github.com/repos/$REPO/releases/latest" "$tmp_dir/release.json" ||
    die "cannot query the latest release (set ANCHOR_VERSION to bypass)"
  tag="$(sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$tmp_dir/release.json" | head -n 1)"
  [ -n "$tag" ] || die "could not parse the latest release tag (set ANCHOR_VERSION to bypass)"
fi
version="${tag#v}"
say "anchor install: ${tag} (${os}/${arch})"

asset="${BINARY}_${version}_${os}_${arch}"
base="https://github.com/$REPO/releases/download/$tag"
fetch "$base/$asset.tar.gz" "$tmp_dir/$asset.tar.gz" || die "cannot download $asset.tar.gz"
fetch "$base/checksums.txt" "$tmp_dir/checksums.txt" || die "cannot download checksums.txt"
(cd "$tmp_dir" && grep " $asset.tar.gz\$" checksums.txt > "$asset.sha256") ||
  die "checksum entry for $asset.tar.gz is missing"
(cd "$tmp_dir" && checksum "$asset.sha256") || die "checksum verification failed"

tar -xzf "$tmp_dir/$asset.tar.gz" -C "$tmp_dir"
[ -f "$tmp_dir/$BINARY" ] || die "archive did not contain the $BINARY binary"
mkdir -p "$INSTALL_DIR"
mv "$tmp_dir/$BINARY" "$INSTALL_DIR/$BINARY"
chmod 755 "$INSTALL_DIR/$BINARY"
say "anchor install: installed $("$INSTALL_DIR/$BINARY" version 2>/dev/null || echo "$BINARY") to $INSTALL_DIR/$BINARY"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) say "anchor install: add $INSTALL_DIR to PATH, e.g. export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac
