#!/bin/sh
# interlinked installer for macOS and Linux.
#   curl -fsSL https://raw.githubusercontent.com/romanpert/ainterlinked/main/install.sh | sh
# Downloads the release binary to ~/.local/bin (override with INTERLINKED_BIN_DIR),
# verifies its checksum, then wires every detected AI harness (`interlinked install`).
# Pass --no-wire to only install the binary; other arguments go to `interlinked install`.
set -eu

REPO="romanpert/ainterlinked"
VERSION="${INTERLINKED_VERSION:-latest}"
BIN_DIR="${INTERLINKED_BIN_DIR:-$HOME/.local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in linux|darwin) ;; *) echo "unsupported OS: $os (on Windows use install.ps1)" >&2; exit 1 ;; esac
arch=$(uname -m)
case "$arch" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) echo "unsupported CPU: $arch" >&2; exit 1 ;; esac

asset="interlinked-$os-$arch"
if [ "$VERSION" = latest ]; then base="https://github.com/$REPO/releases/latest/download"; else base="https://github.com/$REPO/releases/download/$VERSION"; fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "Downloading $asset ($VERSION)…"
curl -fsSL "$base/$asset" -o "$tmp/interlinked"
curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS"
want=$(grep " $asset\$" "$tmp/SHA256SUMS" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then got=$(sha256sum "$tmp/interlinked" | cut -d' ' -f1); else got=$(shasum -a 256 "$tmp/interlinked" | cut -d' ' -f1); fi
if [ -z "$want" ] || [ "$want" != "$got" ]; then echo "checksum mismatch for $asset — aborting" >&2; exit 1; fi

mkdir -p "$BIN_DIR"
install -m 0755 "$tmp/interlinked" "$BIN_DIR/interlinked"
ln -sf interlinked "$BIN_DIR/ainterlinked" # the project's name; same program
echo "Installed $BIN_DIR/interlinked (also as ainterlinked)"
case ":$PATH:" in *":$BIN_DIR:"*) ;; *) echo "Note: add $BIN_DIR to your PATH (e.g. in ~/.profile)." ;; esac

if [ "${1:-}" = "--no-wire" ]; then exit 0; fi
"$BIN_DIR/interlinked" install "$@"
