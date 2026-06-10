#!/usr/bin/env bash
# WisDev CLI Easy Installer for Unix/macOS
set -euo pipefail

ScriptDir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RepoRoot="$(cd "${ScriptDir}/.." && pwd)"
Orchestrator="${RepoRoot}/orchestrator"

# Determine Go bin directory
if [ -n "${GOPATH:-}" ]; then
  GoBin="${GOPATH}/bin"
else
  GoBin="${HOME}/go/bin"
fi

echo -e "\033[36m--- WisDev CLI Easy Installer ---\033[0m"
echo -e "\033[90mStep 1: Compiling and installing CLI binary...\033[0m"

# Compile and place in $GOPATH/bin
pushd "${Orchestrator}" > /dev/null
try_install() {
  go install ./cmd/wisdev
}
if try_install; then
  echo -e "\033[32mSuccessfully compiled and installed wisdev binary to ${GoBin}.\033[0m"
else
  echo -e "\033[31mFailed to compile/install wisdev binary.\033[0m"
  popd > /dev/null
  exit 1
fi
popd > /dev/null

echo -e "\033[90mStep 2: Checking environment PATH...\033[0m"

# Check if Go bin directory is in PATH
if [[ ":$PATH:" != *":$GoBin:"* ]]; then
  echo -e "\033[33mWarning: ${GoBin} is not in your PATH.\033[0m"
  echo -e "To run 'wisdev' from anywhere, add this to your shell profile (~/.bashrc or ~/.zshrc):"
  echo -e "  \033[36mexport PATH=\"\$PATH:${GoBin}\"\033[0m"
else
  echo -e "\033[32m${GoBin} is already in your PATH.\033[0m"
fi

echo -e "\n\033[32m[Success] WisDev CLI installed successfully!\033[0m"
echo -e "Try running: \033[36mwisdev tui\033[0m"
