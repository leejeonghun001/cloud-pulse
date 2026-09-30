#!/usr/bin/env bash
#
# scripts/github-setup.sh — apply repository labels, topics, and the
# description to the real GitHub repository via the `gh` CLI.
#
# SAFETY: this script defaults to --dry-run behavior (prints every
# action it WOULD take, changes nothing) and only mutates the real
# repository when explicitly passed --apply. It is meant to be run by
# a human (the repository owner) from their own authenticated `gh`
# session — it is never invoked by CI or any other automation in this
# repository.
#
# Usage:
#   scripts/github-setup.sh [--repo OWNER/REPO] [--apply]
#
# Requires: gh (GitHub CLI), already authenticated (`gh auth status`).

set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LABELS_FILE="${REPO_DIR}/.github/labels.yml"

REPO=""
APPLY=0

DESCRIPTION="Ultra-lightweight, CGO-free server monitor for small fleets — hub + agent, one static binary each, embedded SQLite, dashboard baked into the binary."

# Keep this list in sync with the "Topics" a maintainer would set from
# the GitHub UI's repository settings page.
TOPICS=(
  golang
  monitoring
  server-monitoring
  sqlite
  self-hosted
  dashboard
  tailscale
  systemd
  observability
)

usage() {
  cat <<'EOF'
Usage: github-setup.sh [--repo OWNER/REPO] [--apply]

Applies labels (from .github/labels.yml), topics, and the repository
description to a GitHub repository via the gh CLI.

Options:
  --repo OWNER/REPO   Target repository (default: the current directory's
                       'origin' remote, resolved via `gh repo view`).
  --apply             Actually perform the changes. Without this flag,
                       every action is only printed (dry run), and gh's
                       own state-changing commands are never invoked.
  -h, --help          Show this help text.

This script never runs automatically as part of CI or any other
automation in this repository — it is provided for the repository
owner to run by hand, deliberately, from an authenticated `gh` session.
EOF
}

log() {
  echo "[github-setup] $*"
}

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --repo)
        [ $# -ge 2 ] || { echo "github-setup.sh: --repo requires a value" >&2; exit 2; }
        REPO="$2"
        shift 2
        ;;
      --apply)
        APPLY=1
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        echo "github-setup.sh: unknown argument: $1" >&2
        usage >&2
        exit 2
        ;;
    esac
  done
}

require_gh() {
  if ! command -v gh >/dev/null 2>&1; then
    echo "github-setup.sh: the 'gh' CLI is required but not found on PATH" >&2
    exit 1
  fi
  if ! gh auth status >/dev/null 2>&1; then
    echo "github-setup.sh: 'gh' is not authenticated; run 'gh auth login' first" >&2
    exit 1
  fi
}

resolve_repo() {
  if [ -n "$REPO" ]; then
    return
  fi
  REPO="$(gh repo view --json nameWithOwner --jq .nameWithOwner 2>/dev/null || true)"
  if [ -z "$REPO" ]; then
    echo "github-setup.sh: could not resolve a repository; pass --repo OWNER/REPO" >&2
    exit 1
  fi
}

run_or_print() {
  if [ "$APPLY" -eq 1 ]; then
    log "RUN: $*"
    "$@"
  else
    log "DRY RUN (pass --apply to execute): $*"
  fi
}

# apply_labels reads .github/labels.yml (a small fixed schema: a list
# of {name, color, description} maps) with a minimal line-based parser
# — no YAML library dependency, matching CODING_CONVENTIONS.md's
# stdlib-first policy for scripts. Each label is created if missing,
# or edited in place (color/description) if it already exists.
apply_labels() {
  if [ ! -f "$LABELS_FILE" ]; then
    echo "github-setup.sh: labels file not found: $LABELS_FILE" >&2
    exit 1
  fi

  local name="" color="" description=""
  local have_entry=0

  flush_entry() {
    if [ "$have_entry" -eq 1 ]; then
      if gh label list --repo "$REPO" --json name --jq '.[].name' 2>/dev/null | grep -qxF "$name"; then
        run_or_print gh label edit "$name" --repo "$REPO" --color "$color" --description "$description"
      else
        run_or_print gh label create "$name" --repo "$REPO" --color "$color" --description "$description"
      fi
    fi
    name=""
    color=""
    description=""
    have_entry=0
  }

  while IFS= read -r line; do
    case "$line" in
      "- name: "*)
        flush_entry
        name="${line#- name: }"
        name="${name%\"}"
        name="${name#\"}"
        have_entry=1
        ;;
      "  color: "*)
        color="${line#  color: }"
        color="${color%\"}"
        color="${color#\"}"
        ;;
      "  description: "*)
        description="${line#  description: }"
        description="${description%\"}"
        description="${description#\"}"
        ;;
      *) ;;
    esac
  done < "$LABELS_FILE"
  flush_entry
}

apply_topics() {
  run_or_print gh repo edit "$REPO" --add-topic "$(
    local IFS=,
    echo "${TOPICS[*]}"
  )"
}

apply_description() {
  run_or_print gh repo edit "$REPO" --description "$DESCRIPTION"
}

main() {
  parse_args "$@"
  require_gh
  resolve_repo

  log "target repository: $REPO"
  if [ "$APPLY" -eq 0 ]; then
    log "dry run mode (no changes will be made); pass --apply to apply"
  fi

  apply_labels
  apply_topics
  apply_description

  if [ "$APPLY" -eq 0 ]; then
    log "dry run complete. Re-run with --apply to make these changes."
  else
    log "done."
  fi
}

main "$@"
