#!/usr/bin/env python3
"""scripts/check-docs.py — documentation integrity checks for cloud-pulse.

Validates, across every tracked Markdown file in the repository:

1. Relative links (``[text](path)`` / ``[text](path#anchor)``) resolve to
   an existing file on disk.
2. In-document and cross-document anchor links (``#some-heading`` or
   ``other.md#some-heading``) resolve to a heading that actually exists
   in the target file, using GitHub's own heading-to-anchor slug rules.
3. Fenced code blocks (``` ``` ```) carry a language tag (or are
   deliberately untagged via ``text``/empty, which is allowed but noted)
   — this repo requires a *non-empty* info string on every fence so
   rendered docs get syntax highlighting.
4. README.md does not exceed a configurable line-count cap (default
   350; use ``--skip-length-cap`` while README.md is still being
   restructured by the docs stage, or ``--max-readme-lines N`` to
   override the cap).
5. Every ``CP_*`` environment variable and CLI flag (``-name``/``--name``)
   mentioned in Markdown documentation actually exists somewhere in the
   repository's source (Go, shell, Python, PowerShell, JS) — catching
   documentation that drifted from the code (a renamed/removed flag, a
   typo'd env var).

Exit status is non-zero if any check fails. Intended to run from the
repository root (matches every other ``scripts/*.py`` convention in
this project); also runnable via ``python3 scripts/check-docs.py`` from
elsewhere with ``--repo-root``.
"""

from __future__ import annotations

import argparse
import re
import sys
from dataclasses import dataclass, field
from pathlib import Path

LINK_RE = re.compile(r"(?<!!)\[[^\]]*\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)")
FENCE_RE = re.compile(r"^(`{3,}|~{3,})(.*)$")
HEADING_RE = re.compile(r"^(#{1,6})\s+(.*?)\s*$")
ENV_VAR_RE = re.compile(r"\bCP_[A-Z][A-Z0-9_]*\b")
FLAG_RE = re.compile(r"(?<![A-Za-z0-9_.-])(--?[a-z][a-z0-9-]*)\b")

# Words that look like CLI flags in prose but are not actual flags this
# repository defines (avoids false positives from grep-like doc text,
# shell idioms, or third-party tool flags quoted for context).
FLAG_ALLOWLIST = {
    "-c",
    "-r",
    "-w",
    "-i",
    "-l",
    "-n",
    "-p",
    "-y",
    "-a",
    "-e",
    "-o",
    "-f",
    "-d",
    "-m",
    "-s",
    "-t",
    "-u",
    "-x",
    "-v",
    "-y",
    "-z",
    "--yes",
    "--purge",
    "--dry-run",
    "--install",
    "--reinstall",
    "--uninstall",
    "--docker",
    "--hub-url",
    "--token",
    "--generate-ui-token",
    "--rotate-ui-token",
    "--help",
    "--apply",
    "--repo",
    "--repo-url",
    "--repo-dir",
    "--platform",
    "--credentials-file",
    "--timeout",
    "--cleanup",
    "--json",
    "--status-webhook-listen",
    "--max-readme-lines",
    "--skip-length-cap",
    "--check-flags",
    # Go toolchain's own flags (not this project's binaries):
    "-race",
    "-ldflags",
    "-dirty",
    # flag.Parse() auto-registers -h/--help even though no code calls
    # flag.Bool("help", ...) explicitly for it:
    "-h",
    "-help",
    "--help",
    # SQLite WAL-mode sidecar file suffixes (-wal, -shm appended to the
    # database filename), not CLI flags at all — coincidentally match
    # the "-word" flag-shaped regex when backtick-quoted in prose:
    "-wal",
    "-shm",
    # Other tools' own flags referenced in docs/decisions log, not this
    # project's cmd/* binaries:
    "--upgrade",
    "--eval",
    "--generate-notes",
    "--notes-file",
    "--verify-tag",
}

MARKDOWN_LINK_EXCLUDE_SCHEMES = ("http://", "https://", "mailto:", "//")


@dataclass
class Issue:
    path: Path
    line: int
    message: str

    def __str__(self) -> str:
        return f"{self.path}:{self.line}: {self.message}"


@dataclass
class CheckResult:
    issues: list[Issue] = field(default_factory=list)

    def add(self, path: Path, line: int, message: str) -> None:
        self.issues.append(Issue(path, line, message))

    @property
    def ok(self) -> bool:
        return not self.issues


def find_markdown_files(repo_root: Path) -> list[Path]:
    """Returns every tracked-looking Markdown file, skipping vendored
    third-party docs and build/dependency directories."""
    skip_dirs = {
        ".git",
        "node_modules",
        "vendor",
        "dist",
        "bin",
        "data",
        "__pycache__",
    }
    results: list[Path] = []
    for path in sorted(repo_root.rglob("*.md")):
        if any(part in skip_dirs for part in path.relative_to(repo_root).parts):
            continue
        if "web/assets/vendor" in str(path.relative_to(repo_root)):
            continue
        results.append(path)
    return results


def github_slug(heading: str) -> str:
    """Reproduces GitHub's heading-to-anchor slug algorithm (matching
    the reference `github-slugger` behavior): strip Markdown emphasis
    markers and inline code backticks and links, lowercase, drop
    characters outside [a-z0-9 _-] WITHOUT first collapsing the
    resulting whitespace (so "services + listening" -> "services
    listening" with two adjacent spaces once '+' is dropped, then each
    space individually becomes its own hyphen, producing a double
    hyphen — this repo's own README anchors rely on exactly this, e.g.
    '#inventory-docker-services--listening-ports'), then strip
    leading/trailing hyphens."""
    text = heading
    text = re.sub(r"[`*]", "", text)
    text = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", text)  # [text](url) -> text
    text = text.lower()
    text = re.sub(r"[^a-z0-9 _-]", "", text)
    text = text.strip()
    text = text.replace(" ", "-")
    return text


def extract_headings(lines: list[str]) -> dict[str, int]:
    """Maps slug -> first 1-indexed line number defining it. Duplicate
    headings get GitHub's own '-1', '-2', ... suffixing, applied here
    too so repeated section titles (this repo has a few) don't produce
    false "anchor not found" reports."""
    slug_counts: dict[str, int] = {}
    slugs: dict[str, int] = {}
    in_fence = False
    fence_marker = ""
    for i, line in enumerate(lines, start=1):
        fence_match = FENCE_RE.match(line.strip())
        if fence_match:
            marker = fence_match.group(1)[0]
            if not in_fence:
                in_fence = True
                fence_marker = marker
            elif marker == fence_marker:
                in_fence = False
            continue
        if in_fence:
            continue
        m = HEADING_RE.match(line)
        if not m:
            continue
        base_slug = github_slug(m.group(2))
        if not base_slug:
            continue
        count = slug_counts.get(base_slug, 0)
        slug_counts[base_slug] = count + 1
        slug = base_slug if count == 0 else f"{base_slug}-{count}"
        slugs.setdefault(slug, i)
    return slugs


def extract_fences(lines: list[str]) -> list[tuple[int, str]]:
    """Returns (line_number, info_string) for every opening code fence."""
    fences: list[tuple[int, str]] = []
    in_fence = False
    fence_marker = ""
    for i, line in enumerate(lines, start=1):
        stripped = line.strip()
        m = FENCE_RE.match(stripped)
        if not m:
            continue
        marker = m.group(1)[0]
        info = m.group(2).strip()
        if not in_fence:
            in_fence = True
            fence_marker = marker
            fences.append((i, info))
        elif marker == fence_marker:
            in_fence = False
    return fences


def check_links_and_anchors(repo_root: Path, md_files: list[Path], result: CheckResult) -> None:
    headings_cache: dict[Path, dict[str, int]] = {}

    def headings_for(path: Path) -> dict[str, int]:
        if path not in headings_cache:
            try:
                lines = path.read_text(encoding="utf-8").splitlines()
            except OSError:
                headings_cache[path] = {}
            else:
                headings_cache[path] = extract_headings(lines)
        return headings_cache[path]

    for md_path in md_files:
        text = md_path.read_text(encoding="utf-8")
        lines = text.splitlines()
        in_fence = False
        fence_marker = ""
        own_headings = headings_for(md_path)
        for lineno, line in enumerate(lines, start=1):
            fence_match = FENCE_RE.match(line.strip())
            if fence_match:
                marker = fence_match.group(1)[0]
                if not in_fence:
                    in_fence = True
                    fence_marker = marker
                elif marker == fence_marker:
                    in_fence = False
                continue
            if in_fence:
                continue
            for match in LINK_RE.finditer(line):
                target = match.group(1)
                if target.startswith(MARKDOWN_LINK_EXCLUDE_SCHEMES):
                    continue
                if target.startswith("#"):
                    anchor = target[1:]
                    if anchor and anchor not in own_headings:
                        result.add(
                            md_path,
                            lineno,
                            f"anchor not found in this file: #{anchor}",
                        )
                    continue
                file_part, _, anchor_part = target.partition("#")
                if not file_part:
                    continue
                resolved = (md_path.parent / file_part).resolve()
                try:
                    resolved.relative_to(repo_root.resolve())
                except ValueError:
                    result.add(md_path, lineno, f"link escapes repository root: {target}")
                    continue
                if not resolved.exists():
                    result.add(md_path, lineno, f"broken relative link: {target}")
                    continue
                if anchor_part and resolved.suffix == ".md":
                    target_headings = headings_for(resolved)
                    if anchor_part not in target_headings:
                        result.add(
                            md_path,
                            lineno,
                            f"anchor not found in {file_part}: #{anchor_part}",
                        )


def check_fence_languages(md_files: list[Path], result: CheckResult) -> None:
    for md_path in md_files:
        lines = md_path.read_text(encoding="utf-8").splitlines()
        for lineno, info in extract_fences(lines):
            if not info:
                result.add(
                    md_path,
                    lineno,
                    "fenced code block has no language tag (use e.g. ```bash, ```go, ```json, ```text)",
                )


def check_readme_length(repo_root: Path, max_lines: int, result: CheckResult) -> None:
    readme = repo_root / "README.md"
    if not readme.exists():
        return
    line_count = len(readme.read_text(encoding="utf-8").splitlines())
    if line_count > max_lines:
        result.add(
            readme,
            line_count,
            f"README.md has {line_count} lines, exceeding the {max_lines}-line cap "
            "(pass --skip-length-cap while the docs restructuring is in progress)",
        )


CODE_FILE_SUFFIXES = (".go", ".sh", ".py", ".ps1", ".mjs", ".js")


def collect_known_env_vars(repo_root: Path) -> set[str]:
    """Scans every source file in the repository (Go, shell, Python,
    PowerShell, JS) for CP_* identifiers actually referenced by code —
    this is intentionally broader than internal/config alone, since
    install scripts, internal/selfupdate, internal/notify's SSRF
    override, test harnesses, and Python verify scripts all read their
    own CP_* variables outside the main config loader."""
    skip_dirs = {".git", "node_modules", "vendor", "dist", "bin", "data", "__pycache__"}
    known: set[str] = set()
    extra_files = {repo_root / ".githooks" / "pre-commit", repo_root / ".githooks" / "commit-msg"}
    for path in repo_root.rglob("*"):
        if not path.is_file():
            continue
        if path.suffix not in CODE_FILE_SUFFIXES and path not in extra_files:
            continue
        if any(part in skip_dirs for part in path.relative_to(repo_root).parts):
            continue
        if "web/assets/vendor" in str(path.relative_to(repo_root)):
            continue
        try:
            text = path.read_text(encoding="utf-8")
        except (OSError, UnicodeDecodeError):
            continue
        known.update(ENV_VAR_RE.findall(text))
    return known


def collect_known_flags(repo_root: Path) -> set[str]:
    cmd_dir = repo_root / "cmd"
    known: set[str] = set()
    if not cmd_dir.exists():
        return known
    # Matches both the top-level `flag.String("name", ...)` form and a
    # FlagSet variable's `fs.String("name", ...)` form (subcommands like
    # `update`, `systemd-unit`, `reset-password` each build their own
    # flag.NewFlagSet rather than using the package-level flag.* funcs).
    flag_def_re = re.compile(
        r'\b[A-Za-z_][A-Za-z0-9_]*\.(?:String|Bool|Int|Int64|Uint|Duration|Float64)\(\s*"([a-zA-Z0-9-]+)"'
    )
    for go_file in cmd_dir.rglob("*.go"):
        text = go_file.read_text(encoding="utf-8")
        for name in flag_def_re.findall(text):
            known.add(f"-{name}")
            known.add(f"--{name}")
    return known


def check_env_and_flags(
    repo_root: Path,
    md_files: list[Path],
    result: CheckResult,
    *,
    enabled: bool,
) -> None:
    if not enabled:
        return
    known_env = collect_known_env_vars(repo_root)
    known_flags = collect_known_flags(repo_root)
    if not known_env and not known_flags:
        return

    for md_path in md_files:
        lines = md_path.read_text(encoding="utf-8").splitlines()
        in_fence = False
        fence_marker = ""
        for lineno, line in enumerate(lines, start=1):
            fence_match = FENCE_RE.match(line.strip())
            if fence_match:
                marker = fence_match.group(1)[0]
                if not in_fence:
                    in_fence = True
                    fence_marker = marker
                elif marker == fence_marker:
                    in_fence = False
                continue
            for env_var in ENV_VAR_RE.findall(line):
                if known_env and env_var not in known_env:
                    result.add(
                        md_path,
                        lineno,
                        f"documented env var not referenced anywhere in source: {env_var}",
                    )
            if in_fence:
                continue
            for flag_match in FLAG_RE.finditer(line):
                flag = flag_match.group(1)
                if flag in FLAG_ALLOWLIST:
                    continue
                if not known_flags or flag in known_flags:
                    continue
                # A flag-looking token not in FLAG_ALLOWLIST and not
                # defined by any cmd/*/main.go flag.String/Bool/...
                # call: this is a real signal (an actually-removed or
                # typo'd flag), but free-form prose also contains many
                # incidental "-word" sequences (compound adjectives,
                # command-line examples for other tools) that would
                # make this noisy without a much larger allowlist or a
                # markdown-aware parser distinguishing `--flag` in code
                # spans from "-hyphenated" prose. Only flag it when the
                # token appears inside inline code (single backticks)
                # on the same line, which is how this repo always
                # formats a real flag reference.
                backtick_re = re.compile(r"`" + re.escape(flag) + r"`")
                if backtick_re.search(line):
                    result.add(
                        md_path,
                        lineno,
                        f"documented flag not defined by any cmd/*/main.go flag.*(...) call: {flag}",
                    )


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--repo-root",
        type=Path,
        default=Path(__file__).resolve().parents[1],
        help="Repository root to scan (default: parent of scripts/)",
    )
    parser.add_argument(
        "--max-readme-lines",
        type=int,
        default=350,
        help="Maximum allowed line count for README.md (default: 350)",
    )
    parser.add_argument(
        "--skip-length-cap",
        action="store_true",
        help="Skip the README.md line-count cap (use while the docs stage is still restructuring README.md)",
    )
    parser.add_argument(
        "--check-flags",
        action="store_true",
        help="Also cross-reference CLI flags mentioned in docs against cmd/* (env vars are always checked)",
    )
    args = parser.parse_args(argv)

    repo_root = args.repo_root.resolve()
    md_files = find_markdown_files(repo_root)
    if not md_files:
        print("check-docs: no Markdown files found", file=sys.stderr)
        return 1

    result = CheckResult()
    check_links_and_anchors(repo_root, md_files, result)
    check_fence_languages(md_files, result)
    if not args.skip_length_cap:
        check_readme_length(repo_root, args.max_readme_lines, result)
    check_env_and_flags(repo_root, md_files, result, enabled=True)

    if result.issues:
        print(f"check-docs: {len(result.issues)} issue(s) found:", file=sys.stderr)
        for issue in result.issues:
            print(f"  {issue}", file=sys.stderr)
        return 1

    print(f"check-docs: OK ({len(md_files)} Markdown file(s) checked)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
