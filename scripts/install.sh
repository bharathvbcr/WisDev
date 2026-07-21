#!/usr/bin/env bash
# WisDev CLI installer for Linux/macOS.
#
# One-liner (downloads the latest release binary, no Go required):
#   curl -fsSL https://raw.githubusercontent.com/bharathvbcr/WisDev/main/scripts/install.sh | bash
#
# From a repo checkout it builds from source when Go is available and no
# release asset matches, so the original `scripts/install.sh` flow still works.
#
# Environment overrides:
#   WISDEV_REPO         GitHub repo to fetch releases from (default: bharathvbcr/WisDev)
#   WISDEV_VERSION      Release tag to install (default: latest)
#   WISDEV_INSTALL_DIR  Target directory (default: ~/.local/bin)
#   WISDEV_FROM_SOURCE  Set to 1 to force a source build (requires Go + checkout)
set -euo pipefail

REPO="${WISDEV_REPO:-bharathvbcr/WisDev}"
VERSION="${WISDEV_VERSION:-latest}"
INSTALL_DIR="${WISDEV_INSTALL_DIR:-${HOME}/.local/bin}"

info()  { printf '\033[36m%s\033[0m\n' "$*"; }
step()  { printf '\033[90m%s\033[0m\n' "$*"; }
ok()    { printf '\033[32m%s\033[0m\n' "$*"; }
warn()  { printf '\033[33m%s\033[0m\n' "$*"; }
fail()  { printf '\033[31m%s\033[0m\n' "$*" >&2; exit 1; }

detect_platform() {
  local os arch
  case "$(uname -s)" in
    Linux)  os="linux" ;;
    Darwin) os="darwin" ;;
    *) fail "Unsupported OS: $(uname -s). Use scripts/install.ps1 on Windows." ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64)  arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) fail "Unsupported architecture: $(uname -m)" ;;
  esac
  printf '%s_%s' "$os" "$arch"
}

resolve_tag() {
  if [ "$VERSION" != "latest" ]; then
    printf '%s' "$VERSION"
    return
  fi
  curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null \
    | grep -m1 '"tag_name"' \
    | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/' || true
}

install_from_release() {
  local platform tag asset url tmp
  platform="$(detect_platform)"
  step "Step 1: Resolving release for ${platform}..."
  tag="$(resolve_tag)"
  if [ -z "$tag" ]; then
    return 1
  fi
  asset="wisdev_${tag}_${platform}.tar.gz"
  url="https://github.com/${REPO}/releases/download/${tag}/${asset}"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  step "Step 2: Downloading ${url}..."
  if ! curl -fsSL "$url" -o "${tmp}/${asset}"; then
    warn "No release asset ${asset} found for ${tag}."
    return 1
  fi
  tar -xzf "${tmp}/${asset}" -C "$tmp"
  mkdir -p "$INSTALL_DIR"
  install -m 0755 "${tmp}/wisdev" "${INSTALL_DIR}/wisdev"
  ok "Installed wisdev ${tag} to ${INSTALL_DIR}/wisdev"
  check_path "$INSTALL_DIR"
  return 0
}

install_from_source() {
  local script_dir repo_root orchestrator go_bin
  command -v go >/dev/null 2>&1 || fail "No release binary available and Go is not installed. Install Go 1.25+ or download a release manually from https://github.com/${REPO}/releases"
  # Resolve the checkout when running as a file; piped stdin has no path.
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-.}")" 2>/dev/null && pwd || true)"
  repo_root="$(cd "${script_dir}/.." 2>/dev/null && pwd || true)"
  orchestrator="${repo_root}/orchestrator"
  if [ ! -d "$orchestrator" ]; then
    fail "No release binary available and no repo checkout found. Clone https://github.com/${REPO} and rerun scripts/install.sh"
  fi
  if [ -n "${GOPATH:-}" ]; then
    go_bin="${GOPATH}/bin"
  else
    go_bin="${HOME}/go/bin"
  fi
  step "Building from source (go install ./cmd/wisdev)..."
  (cd "$orchestrator" && go install ./cmd/wisdev) || fail "Failed to compile/install wisdev binary."
  ok "Compiled and installed wisdev to ${go_bin}."
  check_path "$go_bin"
}

check_path() {
  local dir="$1"
  if [[ ":$PATH:" != *":$dir:"* ]]; then
    warn "Warning: ${dir} is not in your PATH."
    echo "To run 'wisdev' from anywhere, add this to your shell profile (~/.bashrc or ~/.zshrc):"
    info "  export PATH=\"\$PATH:${dir}\""
  fi
}

info "--- WisDev CLI Installer ---"
if [ "${WISDEV_FROM_SOURCE:-0}" = "1" ]; then
  install_from_source
elif ! install_from_release; then
  warn "Falling back to source build..."
  install_from_source
fi

ok "[Success] WisDev CLI installed!"
echo "Try running:"
info "  wisdev check"
info "  wisdev \"What evidence supports RAG for scientific literature?\""
