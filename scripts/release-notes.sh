#!/usr/bin/env bash
#
# scripts/release-notes.sh — generate deterministic release notes from
# Conventional Commits between two tags (or a tag and the repository
# root, if no previous tag exists).
#
# Usage:
#   scripts/release-notes.sh <tag> [<prev-tag>] [--repo-url URL] [--repo-dir DIR]
#
# <tag> must already exist as a git tag (annotated or lightweight).
# <prev-tag>, if omitted, defaults to the tag immediately preceding
# <tag> in tag-creation order (`git tag --sort=creatordate`); if <tag>
# is the very first tag, the notes cover every commit up to <tag>.
#
# Output (to stdout) groups commits by Conventional Commits type into
# fixed sections (Features / Fixes / Documentation / Tests / CI /
# Other), lists any commit whose subject carries a `!` before the `:`
# or whose body/footer contains a `BREAKING CHANGE:` line under a
# "Breaking changes" section, and appends an upgrade-command block, a
# checksum-verification note, and a compare link back to the repo.
#
# The script is pure (same tag range -> byte-identical output) and has
# no network access of its own — it only reads local git history. Pass
# --repo-url to control the link base (defaults to the origin remote's
# URL, normalized to an https://github.com/OWNER/REPO form); pass
# --repo-dir to run against a git repository other than the current
# working directory (used by the test fixture).

set -euo pipefail

REPO_DIR="."
REPO_URL_OVERRIDE=""
TAG=""
PREV_TAG=""
PREV_TAG_SET=0

usage() {
  cat <<'EOF'
Usage: release-notes.sh <tag> [<prev-tag>] [--repo-url URL] [--repo-dir DIR]

Generate Conventional-Commits-grouped release notes for <tag>, comparing
against <prev-tag> (or the previous tag by creation order, or the root
commit if <tag> is the first tag).

Options:
  --repo-url URL   Override the repository URL used for the compare link
                   (default: derived from the 'origin' remote).
  --repo-dir DIR   Run against the git repository at DIR instead of the
                   current directory.
  -h, --help       Show this help text.
EOF
}

# parse_args — populates TAG/PREV_TAG/REPO_DIR/REPO_URL_OVERRIDE from argv.
parse_args() {
  local positional=()
  while [ $# -gt 0 ]; do
    case "$1" in
      --repo-url)
        [ $# -ge 2 ] || { echo "release-notes.sh: --repo-url requires a value" >&2; exit 2; }
        REPO_URL_OVERRIDE="$2"
        shift 2
        ;;
      --repo-dir)
        [ $# -ge 2 ] || { echo "release-notes.sh: --repo-dir requires a value" >&2; exit 2; }
        REPO_DIR="$2"
        shift 2
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      --)
        shift
        while [ $# -gt 0 ]; do
          positional+=("$1")
          shift
        done
        ;;
      -*)
        echo "release-notes.sh: unknown option: $1" >&2
        usage >&2
        exit 2
        ;;
      *)
        positional+=("$1")
        shift
        ;;
    esac
  done

  if [ "${#positional[@]}" -lt 1 ] || [ "${#positional[@]}" -gt 2 ]; then
    usage >&2
    exit 2
  fi

  TAG="${positional[0]}"
  if [ "${#positional[@]}" -eq 2 ]; then
    PREV_TAG="${positional[1]}"
    PREV_TAG_SET=1
  fi
}

git_c() {
  git -C "$REPO_DIR" "$@"
}

# resolve_prev_tag — fills PREV_TAG (empty string means "no previous
# tag; range is the root commit through TAG") when the caller didn't
# pass one explicitly, using tag-creation order so an out-of-numeric-
# order tag name still resolves correctly.
resolve_prev_tag() {
  if [ "$PREV_TAG_SET" -eq 1 ]; then
    return
  fi
  local ordered
  ordered="$(git_c tag --sort=creatordate)"
  PREV_TAG="$(printf '%s\n' "$ordered" | grep -B1 -x -F "$TAG" | head -n1)"
  if [ "$PREV_TAG" = "$TAG" ]; then
    PREV_TAG=""
  fi
}

# repo_url — prints the https://github.com/OWNER/REPO URL to use for
# links, from --repo-url or by normalizing the 'origin' remote.
repo_url() {
  if [ -n "$REPO_URL_OVERRIDE" ]; then
    printf '%s\n' "${REPO_URL_OVERRIDE%/}"
    return
  fi
  local raw
  raw="$(git_c remote get-url origin 2>/dev/null || true)"
  if [ -z "$raw" ]; then
    printf '%s\n' ""
    return
  fi
  # git@github.com:OWNER/REPO.git -> https://github.com/OWNER/REPO
  # https://github.com/OWNER/REPO.git -> https://github.com/OWNER/REPO
  raw="${raw%.git}"
  case "$raw" in
    git@*:*)
      raw="${raw#git@}"
      raw="${raw/://}"
      raw="https://${raw}"
      ;;
  esac
  printf '%s\n' "$raw"
}

# commit_range — prints the git rev-range for `git log` covering
# PREV_TAG (exclusive) through TAG (inclusive); if PREV_TAG is empty,
# covers every ancestor of TAG.
commit_range() {
  if [ -n "$PREV_TAG" ]; then
    printf '%s..%s\n' "$PREV_TAG" "$TAG"
  else
    printf '%s\n' "$TAG"
  fi
}

# classify_type — maps a Conventional Commits type keyword to the
# section title it belongs under. Unrecognized types fall under
# "Other".
classify_type() {
  case "$1" in
    feat) echo "Features" ;;
    fix) echo "Fixes" ;;
    docs) echo "Documentation" ;;
    test|tests) echo "Tests" ;;
    ci) echo "CI" ;;
    *) echo "Other" ;;
  esac
}

# section_order — fixed, deterministic section ordering.
section_order() {
  printf '%s\n' "Features" "Fixes" "Documentation" "Tests" "CI" "Other"
}

# format_commit_line — given a subject and short hash, prints one
# Markdown list item with NO trailing newline (the caller appends
# $'\n' explicitly so callers can reason about separators uniformly).
format_commit_line() {
  local subject="$1" hash="$2"
  printf -- '- %s (%s)' "$subject" "$hash"
}

main() {
  parse_args "$@"

  if ! git_c rev-parse --verify --quiet "refs/tags/${TAG}" >/dev/null; then
    echo "release-notes.sh: tag not found: ${TAG}" >&2
    exit 1
  fi

  resolve_prev_tag

  if [ -n "$PREV_TAG" ] && ! git_c rev-parse --verify --quiet "refs/tags/${PREV_TAG}" >/dev/null; then
    echo "release-notes.sh: previous tag not found: ${PREV_TAG}" >&2
    exit 1
  fi

  local range
  range="$(commit_range)"

  # Each line: <short-hash>\x1f<subject>\x1f<body-with-\x1e-as-newline>
  local log_out
  log_out="$(git_c log --no-merges --date-order --format='%h%x1f%s%x1f%B%x1e' "$range" 2>/dev/null || true)"

  # Split records on the trailing 0x1e (record separator).
  local -a subjects=() hashes=() bodies=()
  if [ -n "$log_out" ]; then
    local IFS_OLD="$IFS"
    IFS=$'\x1e'
    local -a records=()
    read -r -d '' -a records < <(printf '%s\0' "$log_out") || true
    IFS="$IFS_OLD"
    local rec
    for rec in "${records[@]}"; do
      [ -n "$rec" ] || continue
      # Strip a single leading newline left by the record separator.
      rec="${rec#$'\n'}"
      local hash="${rec%%$'\x1f'*}"
      local rest="${rec#*$'\x1f'}"
      local subject="${rest%%$'\x1f'*}"
      local body="${rest#*$'\x1f'}"
      [ -n "$hash" ] || continue
      hashes+=("$hash")
      subjects+=("$subject")
      bodies+=("$body")
    done
  fi

  # Group commit indices by section, and collect breaking-change notes.
  declare -A section_lines
  local sec
  for sec in $(section_order); do
    section_lines["$sec"]=""
  done
  local breaking_lines=""

  local i
  for i in "${!hashes[@]}"; do
    local hash="${hashes[$i]}" subject="${subjects[$i]}" body="${bodies[$i]}"
    local type=""
    local conv_commit_re='^([A-Za-z]+)(\([^)]*\))?(!)?:'
    if [[ "$subject" =~ $conv_commit_re ]]; then
      type="${BASH_REMATCH[1]}"
      local bang="${BASH_REMATCH[3]}"
      if [ -n "$bang" ]; then
        breaking_lines+="$(format_commit_line "$subject" "$hash")"$'\n'
      fi
    fi
    if printf '%s\n' "$body" | grep -qE '^BREAKING CHANGE:'; then
      local detail
      detail="$(printf '%s\n' "$body" | grep -E '^BREAKING CHANGE:' | head -n1 | sed -E 's/^BREAKING CHANGE:[[:space:]]*//')"
      breaking_lines+="- ${detail} (${hash})"$'\n'
    fi
    local section
    section="$(classify_type "$type")"
    section_lines["$section"]+="$(format_commit_line "$subject" "$hash")"$'\n'
  done

  local repo
  repo="$(repo_url)"

  # --- Render ---
  echo "## ${TAG}"
  echo ""

  if [ -n "$breaking_lines" ]; then
    echo "### Breaking changes"
    echo ""
    printf '%s' "$breaking_lines"
    echo ""
  fi

  local any_section_emitted=0
  for sec in $(section_order); do
    local lines="${section_lines[$sec]:-}"
    if [ -n "$lines" ]; then
      any_section_emitted=1
      echo "### ${sec}"
      echo ""
      printf '%s' "$lines"
      echo ""
    fi
  done
  if [ "$any_section_emitted" -eq 0 ]; then
    echo "No user-facing changes recorded."
    echo ""
  fi

  echo "### Upgrade"
  echo ""
  echo '```bash'
  echo "sudo cloud-pulse-hub update"
  echo "sudo cloud-pulse-agent update"
  echo '```'
  echo ""
  echo "See \`docs/upgrade.md\` for the full upgrade/migration guide."
  echo ""

  echo "### Checksums"
  echo ""
  echo "Every release asset's sha256 is published in this release's \`checksums.txt\`;" 
  echo "verify with \`sha256sum -c checksums.txt\` after download."
  echo ""

  if [ -n "$repo" ]; then
    echo "### Full changelog"
    echo ""
    if [ -n "$PREV_TAG" ]; then
      echo "${repo}/compare/${PREV_TAG}...${TAG}"
    else
      echo "${repo}/commits/${TAG}"
    fi
    echo ""
  fi
}

main "$@"
