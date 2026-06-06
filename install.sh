#!/bin/sh
# Lobster installer — one-liner:
#   curl -fsSL https://yutugyutugyutug.com/install | sh
# (the domain just redirects here, to raw.githubusercontent.com/aasm3535/lobster/main/install.sh)
#
# Downloads the prebuilt binary for your OS/arch from the latest GitHub release and puts it on
# your PATH, then offers to run `lobster setup` (which configures it and can run it in the
# background, surviving reboots). If there's no prebuilt binary it offers to install Go and
# build from source.
set -eu

REPO="aasm3535/lobster"
BIN="lobster"

ask() { # ask "question" -> returns 0 for yes (default yes). Uses /dev/tty so it works under `curl | sh`.
  [ -e /dev/tty ] || return 0
  printf "%s [Y/n] " "$1" >/dev/tty
  ans=""; read -r ans </dev/tty || ans=""
  case "$ans" in [Nn]*) return 1 ;; *) return 0 ;; esac
}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
esac
asset="lobster_${os}_${arch}"

dir="${LOBSTER_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ]; then dir=/usr/local/bin; else dir="$HOME/.local/bin"; fi
fi
mkdir -p "$dir"

install_go() { # best-effort: fetch the latest stable Go into ~/.lobster/go and put it on PATH
  gv=$(curl -fsSL "https://go.dev/VERSION?m=text" 2>/dev/null | head -1)
  [ -n "$gv" ] || return 1
  echo "Downloading $gv…"
  curl -fsSL "https://go.dev/dl/${gv}.${os}-${arch}.tar.gz" -o "$HOME/.lobster-go.tgz" || return 1
  mkdir -p "$HOME/.lobster"
  rm -rf "$HOME/.lobster/go"
  tar -C "$HOME/.lobster" -xzf "$HOME/.lobster-go.tgz" || return 1
  rm -f "$HOME/.lobster-go.tgz"
  export PATH="$HOME/.lobster/go/bin:$PATH"
  command -v go >/dev/null 2>&1
}

build_from_source() {
  if ! command -v go >/dev/null 2>&1; then
    if ask "No prebuilt binary for $asset. Install Go and build from source?"; then
      install_go || { echo "❌ couldn't install Go — get it at https://go.dev/dl and retry." >&2; exit 1; }
    else
      echo "Aborted. Install Go (https://go.dev/dl) or wait for a release, then retry." >&2
      exit 1
    fi
  fi
  echo "Building from source…"
  go install "github.com/$REPO/cmd/lobster@latest"
  dir="$(go env GOBIN)"; [ -z "$dir" ] && dir="$(go env GOPATH)/bin"
}

url="https://github.com/$REPO/releases/latest/download/$asset"
echo "🦞 Installing lobster ($asset)…"
if curl -fsSL "$url" -o "$dir/$BIN.tmp" 2>/dev/null && [ -s "$dir/$BIN.tmp" ]; then
  mv "$dir/$BIN.tmp" "$dir/$BIN"
  chmod +x "$dir/$BIN"
  echo "✅ Installed to $dir/$BIN"
else
  rm -f "$dir/$BIN.tmp"
  build_from_source
  echo "✅ Built to $dir/$BIN"
fi

case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "⚠️  Add $dir to your PATH (e.g. echo 'export PATH=\"$dir:\$PATH\"' >> ~/.profile)" ;;
esac

# Offer to configure + run in the background right now (interactive only).
if ask "Run setup now (configure + run in the background)?"; then
  "$dir/$BIN" setup
else
  echo "Next: $BIN setup   (or: $BIN tui)"
fi
