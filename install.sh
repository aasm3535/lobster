#!/bin/sh
# Lobster installer — one-liner:
#   curl -fsSL https://yutugyutugyutug.com/install | sh
# (the domain just redirects here, to raw.githubusercontent.com/aasm3535/lobster/main/install.sh)
#
# Downloads the prebuilt binary for your OS/arch from the latest GitHub release and puts it on
# your PATH, then offers to run `lobster setup`. Falls back to building from source with Go.
set -eu

REPO="aasm3535/lobster"
BIN="lobster"

# --- looks (coral palette, matching the TUI; plain text when piped / NO_COLOR) ---
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  CORAL="$(printf '\033[38;5;209m')"; GREY="$(printf '\033[38;5;245m')"
  GREEN="$(printf '\033[38;5;78m')";  RED="$(printf '\033[38;5;196m')"
  WHITE="$(printf '\033[38;5;231m')"; BOLD="$(printf '\033[1m')"; OFF="$(printf '\033[0m')"
else
  CORAL=; GREY=; GREEN=; RED=; WHITE=; BOLD=; OFF=
fi
say()  { printf '%b\n' "$*"; }
step() { say "  ${WHITE}●${OFF} $*"; }       # a white dot leads each step, like a TUI reply
ok()   { say "  ${GREEN}●${OFF} $*"; }
warn() { say "  ${RED}●${OFF} $*"; }
note() { say "    ${GREY}$*${OFF}"; }

ask() { # 0=yes, 1=no. Reads /dev/tty so it works under `curl | sh`.
  [ -e /dev/tty ] || return 1
  printf '%b' "  ${CORAL}›${OFF} $1 ${GREY}[Y/n]${OFF} " >/dev/tty
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

say ""
say "  ${CORAL}${BOLD}lobster${OFF}${GREY}  ·  installer${OFF}"
say "  ${GREY}────────────────────────${OFF}"
say ""

install_go() { # best-effort: fetch the latest stable Go into ~/.lobster/go and put it on PATH
  gv=$(curl -fsSL "https://go.dev/VERSION?m=text" 2>/dev/null | head -1)
  [ -n "$gv" ] || return 1
  step "downloading $gv…"
  curl -fsSL "https://go.dev/dl/${gv}.${os}-${arch}.tar.gz" -o "$HOME/.lobster-go.tgz" || return 1
  mkdir -p "$HOME/.lobster"; rm -rf "$HOME/.lobster/go"
  tar -C "$HOME/.lobster" -xzf "$HOME/.lobster-go.tgz" || return 1
  rm -f "$HOME/.lobster-go.tgz"
  export PATH="$HOME/.lobster/go/bin:$PATH"
  command -v go >/dev/null 2>&1
}

build_from_source() {
  if ! command -v go >/dev/null 2>&1; then
    if ask "No prebuilt binary for ${asset}. Install Go and build from source?"; then
      install_go || { warn "couldn't install Go — get it at https://go.dev/dl and retry."; exit 1; }
    else
      warn "no prebuilt binary, and Go isn't installed."
      note "install Go (https://go.dev/dl) or wait for a release, then retry."
      exit 1
    fi
  fi
  step "building from source…"
  go install "github.com/$REPO/cmd/lobster@latest"
  dir="$(go env GOBIN)"; [ -z "$dir" ] && dir="$(go env GOPATH)/bin"
}

url="https://github.com/$REPO/releases/latest/download/$asset"
step "fetching ${BOLD}${asset}${OFF}…"
if curl -fsSL "$url" -o "$dir/$BIN.tmp" 2>/dev/null && [ -s "$dir/$BIN.tmp" ]; then
  mv "$dir/$BIN.tmp" "$dir/$BIN"; chmod +x "$dir/$BIN"
  ok "installed to ${BOLD}$dir/$BIN${OFF}"
else
  rm -f "$dir/$BIN.tmp"
  build_from_source
  ok "built to ${BOLD}$dir/$BIN${OFF}"
fi

case ":$PATH:" in
  *":$dir:"*) ;;
  *) warn "add ${BOLD}$dir${OFF} to your PATH"; note "echo 'export PATH=\"$dir:\$PATH\"' >> ~/.profile" ;;
esac

say ""
if ask "Run setup now (configure + run in the background)?"; then
  "$dir/$BIN" setup </dev/tty
else
  say ""
  ok "done. next:"
  note "$BIN setup     configure + run in the background"
  note "$BIN tui       chat in your terminal"
fi
