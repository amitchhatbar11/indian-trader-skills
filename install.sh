#!/bin/sh
# Installs the desk CLI (Indian Trader Skills) from the latest GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/amitchhatbar11/indian-trader-skills/main/install.sh | sh
#
# No Go, no Python, no sudo: it downloads the binary for this machine,
# checks its SHA-256 against the release's checksums.txt and puts it in
# ~/.local/bin (override with DESK_INSTALL_DIR; pin a version with
# DESK_VERSION=v0.3.0).
set -eu

REPO="amitchhatbar11/indian-trader-skills"
DIR="${DESK_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${DESK_VERSION:-latest}"

say() { printf '%s\n' "$*"; }
die() { printf 'desk install: %s\n' "$*" >&2; exit 1; }

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported system $(uname -s) — on Windows use install.ps1" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported CPU $(uname -m)" ;;
esac

if [ "$VERSION" = latest ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/$VERSION"
fi
name="desk_${os}_${arch}"

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO "$2" "$1"; }
else
  die "needs curl or wget"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

say "Downloading $name ($VERSION)…"
fetch "$base/$name.tar.gz" "$tmp/$name.tar.gz" || die "download failed: $base/$name.tar.gz"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "download failed: $base/checksums.txt"

want="$(grep " $name.tar.gz\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
[ -n "$want" ] || die "no checksum for $name.tar.gz"
if command -v sha256sum >/dev/null 2>&1; then
  got="$(sha256sum "$tmp/$name.tar.gz" | cut -d' ' -f1)"
else
  got="$(shasum -a 256 "$tmp/$name.tar.gz" | cut -d' ' -f1)"
fi
[ "$got" = "$want" ] || die "checksum mismatch for $name.tar.gz — not installing"

tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
mkdir -p "$DIR"
install -m 0755 "$tmp/$name/desk" "$DIR/desk" 2>/dev/null || { cp "$tmp/$name/desk" "$DIR/desk"; chmod 0755 "$DIR/desk"; }

say "✓ $("$DIR/desk" version) installed to $DIR/desk"
case ":$PATH:" in
  *":$DIR:"*) ;;
  *) say "Add it to your PATH:  echo 'export PATH=\"$DIR:\$PATH\"' >> ~/.profile  (then open a new terminal)" ;;
esac
say "Next: desk clock    (and see the README to set up a broker)"
