"""Keeps throwaway fixture repositories isolated from this repository.

Git sets GIT_DIR, GIT_INDEX_FILE and friends in the environment of every
hook it runs. The pre-commit hook runs this test suite, so a fixture that
only passes ``cwd=<tmpdir>`` or ``git -C <tmpdir>`` would still operate
on the repository being committed to: ``git init``/``git config`` in a
fixture then rewrote this repository's config (core.bare,
core.worktree, a fixture [user]) and fixture commits moved real branch
refs.

Two layers prevent that:

* ``clean_git_env()`` — every fixture git subprocess gets an environment
  with all ``GIT_*`` variables removed, so the explicit path is the only
  thing that selects the repository.
* ``RepoConfigGuard`` — snapshots this repository's config file(s)
  before a test module runs and, if anything changed them anyway,
  restores the original bytes and fails the run loudly.

The guard locates the config by reading ``.git`` on disk (never by
running git), so a poisoned environment cannot point it elsewhere.
"""

from __future__ import annotations

import atexit
import os
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def clean_git_env(extra: dict[str, str] | None = None) -> dict[str, str]:
    """Returns os.environ without any GIT_* variable, plus ``extra``."""
    env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    if extra:
        env.update(extra)
    return env


def _config_paths(repo_root: Path) -> list[Path]:
    """Returns the config files git would write for repo_root.

    Handles both a normal checkout (``.git`` is a directory) and a linked
    worktree (``.git`` is a file pointing at ``.git/worktrees/<name>``,
    whose ``commondir`` points back at the shared git directory).
    """
    dot_git = repo_root / ".git"
    if dot_git.is_dir():
        git_dir = dot_git
        common_dir = dot_git
    elif dot_git.is_file():
        text = dot_git.read_text(encoding="utf-8").strip()
        if not text.startswith("gitdir:"):
            return []
        git_dir = Path(text[len("gitdir:"):].strip())
        if not git_dir.is_absolute():
            git_dir = (repo_root / git_dir).resolve()
        commondir_file = git_dir / "commondir"
        if commondir_file.is_file():
            common = Path(commondir_file.read_text(encoding="utf-8").strip())
            common_dir = common if common.is_absolute() else (git_dir / common).resolve()
        else:
            common_dir = git_dir
    else:
        return []
    paths = [common_dir / "config"]
    worktree_config = git_dir / "config.worktree"
    if worktree_config != paths[0]:
        paths.append(worktree_config)
    return paths


class RepoConfigGuard:
    """Restores repo_root's git config files if they change, and reports it."""

    def __init__(self, repo_root: Path = ROOT) -> None:
        self.repo_root = repo_root
        self._snapshot: dict[Path, bytes | None] = {}
        self._active = False

    def start(self) -> "RepoConfigGuard":
        self._snapshot = {
            path: (path.read_bytes() if path.is_file() else None) for path in _config_paths(self.repo_root)
        }
        self._active = True
        atexit.register(self.restore)
        return self

    def restore(self) -> list[Path]:
        """Puts back any changed config file; returns the paths it restored."""
        if not self._active:
            return []
        self._active = False
        restored: list[Path] = []
        for path, original in self._snapshot.items():
            current = path.read_bytes() if path.is_file() else None
            if current == original:
                continue
            if original is None:
                path.unlink()
            else:
                path.write_bytes(original)
            restored.append(path)
        return restored

    def stop(self) -> None:
        """Restores, then raises if anything had to be restored."""
        restored = self.restore()
        if restored:
            names = ", ".join(path.name for path in restored)
            raise AssertionError(
                "a test fixture modified this repository's git config (" + names + "); "
                "the original was restored — find the git call missing clean_git_env()"
            )
