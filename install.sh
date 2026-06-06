#!/bin/sh
# Lobster installer — one-liner:
#   curl -fsSL https://raw.githubusercontent.com/aasm3535/lobster/main/install.sh | sh
#
# Downloads the prebuilt binary for your OS/arch from the latest GitHub release and drops it
# on your PATH. Falls back to `go install` from source when no prebuilt binary is available.
set -eu

REPO="aasm3535/lobster"
BIN="lobster"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
esac
asset="lobster_${os}_${arch}"

# Install dir: $LOBSTER_INSTALL_DIR, else /usr/local/bin if writable, else ~/.local/bin.
dir="${LOBSTER_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ]; then dir=/usr/local/bin; else dir="$HOME/.local/bin"; fi
fi
mkdir -p "$dir"

url="https://github.com/$REPO/releases/latest/download/$asset"
echo "🦞 Installing lobster ($asset)…"
if curl -fsSL "$url" -o "$dir/$BIN.tmp" 2>/dev/null && [ -s "$dir/$BIN.tmp" ]; then
  mv "$dir/$BIN.tmp" "$dir/$BIN"
  chmod +x "$dir/$BIN"
  echo "✅ Installed to $dir/$BIN"
else
  rm -f "$dir/$BIN.tmp"
  echo "No prebuilt binary for $asset — trying to build from source…"
  if command -v go >/dev/null 2>&1; then
    go install "github.com/$REPO/cmd/lobster@latest"
    dir="$(go env GOBIN)"
    [ -z "$dir" ] && dir="$(go env GOPATH)/bin"
    echo "✅ Installed via go install to $dir/$BIN"
  else
    echo "❌ No release binary and Go isn't installed. Install Go (https://go.dev/dl) and retry." >&2
    exit 1
  fi
fi

case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "⚠️  Add $dir to your PATH (e.g. echo 'export PATH=\"$dir:\$PATH\"' >> ~/.profile)" ;;
esac
echo "Next: lobster setup   (or: lobster tui)"
