"""Regression tests for fixture git isolation (scripts/tests/_git_isolation.py).

Reproduces the hook environment that once corrupted this repository: git
runs the pre-commit hook with GIT_DIR/GIT_INDEX_FILE/GIT_WORK_TREE set,
and fixture code that only used ``cwd=``/``git -C`` then initialised,
reconfigured and committed into the hook's repository. Here the "hook's
repository" is a throwaway sentinel repo, so a regression damages only
the sentinel and fails the test.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from _git_isolation import RepoConfigGuard, clean_git_env

import test_prepublish_check as tpc
import test_release_notes as trn


def _git(repo: Path, *args: str) -> str:
    return subprocess.run(
        ["git", "-C", str(repo), *args],
        env=clean_git_env(
            {
                "GIT_AUTHOR_NAME": "Sentinel",
                "GIT_AUTHOR_EMAIL": "sentinel@example.invalid",
                "GIT_COMMITTER_NAME": "Sentinel",
                "GIT_COMMITTER_EMAIL": "sentinel@example.invalid",
            }
        ),
        capture_output=True,
        text=True,
        check=True,
    ).stdout


class SentinelRepo:
    """A repository standing in for 'the repository whose hook is running'."""

    def __init__(self) -> None:
        self.root = Path(tempfile.mkdtemp(prefix="cp-git-sentinel-"))
        _git(self.root, "init", "-q")
        (self.root / "README").write_text("sentinel\n", encoding="utf-8")
        _git(self.root, "add", "README")
        _git(self.root, "commit", "-q", "-m", "sentinel")
        _git(self.root, "tag", "sentinel-tag")

    def state(self) -> tuple[bytes, str, str]:
        config = (self.root / ".git" / "config").read_bytes()
        refs = _git(self.root, "for-each-ref", "--format=%(refname) %(objectname)")
        head = _git(self.root, "rev-parse", "HEAD")
        return config, refs, head

    def hook_env(self) -> dict[str, str]:
        git_dir = str(self.root / ".git")
        return {
            "GIT_DIR": git_dir,
            "GIT_INDEX_FILE": str(self.root / ".git" / "index"),
            "GIT_WORK_TREE": str(self.root),
        }

    def cleanup(self) -> None:
        shutil.rmtree(self.root, ignore_errors=True)


class FixtureIsolationTest(unittest.TestCase):
    def setUp(self) -> None:
        self.sentinel = SentinelRepo()
        self.addCleanup(self.sentinel.cleanup)
        self.before = self.sentinel.state()

    def assertSentinelUntouched(self) -> None:
        config, refs, head = self.sentinel.state()
        self.assertEqual(config, self.before[0], "fixture rewrote the hook repository's config")
        self.assertEqual(refs, self.before[1], "fixture moved the hook repository's refs")
        self.assertEqual(head, self.before[2], "fixture committed into the hook repository")

    def test_prepublish_fixture_and_scanner_ignore_inherited_git_dir(self) -> None:
        with mock.patch.dict(os.environ, self.sentinel.hook_env()):
            repo = tpc.TempGitRepo()
            try:
                repo.write("main.go", "package main\n")
                commit = repo.commit("fixture commit")
                findings = tpc.PC.scan_history(repo.root, set())
                tracked = tpc.PC.list_tracked_files(repo.root)
            finally:
                repo.cleanup()
        self.assertEqual(len(commit), 40)
        self.assertEqual(tracked, ["main.go"])
        self.assertIsInstance(findings, list)
        self.assertSentinelUntouched()

    def test_release_notes_fixture_ignores_inherited_git_dir(self) -> None:
        with mock.patch.dict(os.environ, self.sentinel.hook_env()):
            fixture = trn.ReleaseNotesFixture()
            try:
                result = trn.run_release_notes(fixture.dir, "v0.2.0")
            finally:
                fixture.cleanup()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertSentinelUntouched()


class RepoConfigGuardTest(unittest.TestCase):
    def test_restores_changed_config_and_reports_it(self) -> None:
        sentinel = SentinelRepo()
        self.addCleanup(sentinel.cleanup)
        original = (sentinel.root / ".git" / "config").read_bytes()
        guard = RepoConfigGuard(sentinel.root).start()
        _git(sentinel.root, "config", "core.bare", "true")
        with self.assertRaises(AssertionError):
            guard.stop()
        self.assertEqual((sentinel.root / ".git" / "config").read_bytes(), original)

    def test_unchanged_config_passes(self) -> None:
        sentinel = SentinelRepo()
        self.addCleanup(sentinel.cleanup)
        RepoConfigGuard(sentinel.root).start().stop()


if __name__ == "__main__":
    unittest.main()
