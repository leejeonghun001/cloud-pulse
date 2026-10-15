#!/usr/bin/env python3
"""next-version.py — decide whether HEAD needs a new stable release.

Prints the next tag (e.g. ``v0.8.0``) when a commit since the latest
stable ``vX.Y.Z`` tag changes what ships in the hub or agent binaries,
and prints nothing otherwise. ``.github/workflows/auto-release.yml``
runs this on every push to ``main`` and tags + releases whatever it
prints, so ``cloud-pulse-hub update`` / ``cloud-pulse-agent update``
(which follow GitHub's latest stable release) always pick up new work.

A commit counts when its Conventional Commits type is ``feat``, ``fix``
or ``perf`` (or it is marked breaking with ``!`` / ``BREAKING CHANGE:``)
and it touches a shipped path: ``cmd/``, ``internal/``, ``web/`` (the
embedded dashboard), ``go.mod`` or ``go.sum`` — test files excluded.

Bump: breaking -> major (minor while the major version is 0), feat ->
minor, fix/perf -> patch; the largest bump wins. Pre-release tags
(``v0.8.0-rc.1``) are ignored as a base, since ``update`` never
installs them.

Usage: next-version.py [--repo-dir DIR]
"""

from __future__ import annotations

import argparse
import re
import subprocess
import sys

STABLE_TAG = re.compile(r"^v(\d+)\.(\d+)\.(\d+)$")
SUBJECT = re.compile(r"^(?P<type>[a-z]+)(?:\([^)]*\))?(?P<bang>!)?:")
RELEASE_TYPES = {"feat": 2, "fix": 1, "perf": 1}
SHIPPED_PREFIXES = ("cmd/", "internal/", "web/")
SHIPPED_FILES = {"go.mod", "go.sum"}

PATCH, MINOR, MAJOR = 1, 2, 3


def git(repo: str, *args: str) -> str:
    return subprocess.run(["git", "-C", repo, *args], capture_output=True, text=True, check=True).stdout


def latest_stable_tag(repo: str) -> tuple[str, tuple[int, int, int]] | None:
    best = None
    for name in git(repo, "tag", "--list", "v*", "--merged", "HEAD").split():
        m = STABLE_TAG.match(name)
        if m:
            version = (int(m[1]), int(m[2]), int(m[3]))
            if best is None or version > best[1]:
                best = (name, version)
    return best


def is_shipped(path: str) -> bool:
    if path.endswith("_test.go") or path.startswith("web/test/"):
        return False
    return path in SHIPPED_FILES or path.startswith(SHIPPED_PREFIXES)


def commit_bump(repo: str, sha: str, subject: str, body: str, major: int) -> int:
    m = SUBJECT.match(subject)
    breaking = bool(m and m["bang"]) or re.search(r"^BREAKING[ -]CHANGE:", body, re.M) is not None
    if not breaking and (not m or m["type"] not in RELEASE_TYPES):
        return 0
    files = git(repo, "diff-tree", "--no-commit-id", "--name-only", "-r", "--root", sha).split("\n")
    if not any(is_shipped(f) for f in files if f):
        return 0
    if breaking:
        return MAJOR if major >= 1 else MINOR
    return RELEASE_TYPES[m["type"]]


def next_version(repo: str) -> str:
    base = latest_stable_tag(repo)
    version = base[1] if base else (0, 0, 0)
    rev_range = f"{base[0]}..HEAD" if base else "HEAD"
    log = git(repo, "log", "--no-merges", "--format=%H%x1f%s%x1f%b%x1e", rev_range)

    bump = 0
    for record in log.split("\x1e"):
        record = record.strip("\n")
        if not record:
            continue
        sha, subject, body = (record.split("\x1f") + ["", ""])[:3]
        bump = max(bump, commit_bump(repo, sha, subject, body, version[0]))

    major, minor, patch = version
    if bump == MAJOR:
        return f"v{major + 1}.0.0"
    if bump == MINOR:
        return f"v{major}.{minor + 1}.0"
    if bump == PATCH:
        return f"v{major}.{minor}.{patch + 1}"
    return ""


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    parser.add_argument("--repo-dir", default=".")
    args = parser.parse_args()
    tag = next_version(args.repo_dir)
    if tag:
        print(tag)
    else:
        print("next-version: no shipped feat/fix/perf change since the last stable tag", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
