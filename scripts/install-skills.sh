#!/usr/bin/env bash
# Install WisDev usage skills for Claude Code and Cursor.
#
# Symlinks skills from this checkout into:
#   ~/.claude/skills/<skill-name>
#   ~/.cursor/skills/<skill-name>
#   <repo>/.agents/skills/<skill-name>   (when run from a ScholarLM parent checkout)
#   <repo>/.claude/skills/<skill-name>   (same)
#
# Usage:
#   ./scripts/install-skills.sh
#   ./scripts/install-skills.sh --dry-run
#   WISDEV_SKILLS_USER_ONLY=1 ./scripts/install-skills.sh   # skip project .agents/.claude
set -euo pipefail

DRY_RUN=0
USER_ONLY="${WISDEV_SKILLS_USER_ONLY:-0}"
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --user-only) USER_ONLY=1 ;;
    -h|--help)
      sed -n '2,16p' "$0"
      exit 0
      ;;
  esac
done

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
arc_root="$(cd "${script_dir}/.." && pwd)"
skills_src="${arc_root}/.claude/skills/wisdev"

if [ ! -d "$skills_src" ]; then
  echo "error: skills source not found: ${skills_src}" >&2
  exit 1
fi

info() { printf '%s\n' "$*"; }
ok()   { printf '✓ %s\n' "$*"; }

link_skill() {
  local src="$1" dest="$2"
  if [ "$DRY_RUN" = "1" ]; then
    info "dry-run: ln -sfn ${src} ${dest}"
    return
  fi
  mkdir -p "$(dirname "$dest")"
  ln -sfn "$src" "$dest"
  ok "$dest"
}

install_into() {
  local dest_root="$1"
  [ -n "$dest_root" ] || return 0
  mkdir -p "$dest_root" 2>/dev/null || true
  local skill_dir skill_name
  for skill_dir in "${skills_src}"/wisdev-*; do
    [ -d "$skill_dir" ] || continue
    [ -f "${skill_dir}/SKILL.md" ] || continue
    skill_name="$(basename "$skill_dir")"
    link_skill "$skill_dir" "${dest_root}/${skill_name}"
  done
}

info "Installing WisDev skills from ${skills_src}"

install_into "${HOME}/.claude/skills"
install_into "${HOME}/.cursor/skills"

if [ "$USER_ONLY" != "1" ]; then
  # Parent ScholarLM checkout (…/scholarlm/wisdev-arc → …/scholarlm)
  parent="$(cd "${arc_root}/.." && pwd)"
  if [ -d "${parent}/.git" ] || [ -f "${parent}/package.json" ]; then
    install_into "${parent}/.agents/skills"
    install_into "${parent}/.claude/skills"
  fi
fi

info ""
info "Skills installed. Restart Claude Code / Cursor agents to pick them up."
info "MCP registration (separate):"
info "  wisdev setup --write ~/.cursor/mcp.json --binary"
info "  claude mcp add wisdev -- /path/to/wisdev mcp"
info "See docs/MCP_CLIENTS.md"
