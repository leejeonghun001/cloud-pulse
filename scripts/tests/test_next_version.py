"""Unit tests for scripts/next-version.py against throwaway git fixtures."""

from __future__ import annotations

import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

from _git_isolation import RepoConfigGuard, clean_git_env

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "next-version.py"
_CONFIG_GUARD = RepoConfigGuard(ROOT)

IDENTITY = {
    "GIT_AUTHOR_NAME": "Test Author",
    "GIT_AUTHOR_EMAIL": "test@example.invalid",
    "GIT_COMMITTER_NAME": "Test Author",
    "GIT_COMMITTER_EMAIL": "test@example.invalid",
}


def setUpModule() -> None:
    _CONFIG_GUARD.start()


def tearDownModule() -> None:
    _CONFIG_GUARD.stop()


class NextVersionTests(unittest.TestCase):
    def setUp(self) -> None:
        self.repo = Path(tempfile.mkdtemp(prefix="cloud-pulse-next-version-"))
        self.addCleanup(shutil.rmtree, self.repo)
        self.git("init", "-q", "-b", "main")
        self.commit("chore: initial", "README.md")

    def git(self, *args: str) -> str:
        return subprocess.run(
            ["git", "-C", str(self.repo), *args],
            capture_output=True, text=True, check=True, env=clean_git_env(IDENTITY),
        ).stdout.strip()

    def commit(self, message: str, *paths: str) -> None:
        for p in paths:
            target = self.repo / p
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(target.read_text() + message + "\n" if target.exists() else message + "\n")
            self.git("add", p)
        self.git("commit", "-q", "--no-verify", "-m", message)

    def next_version(self) -> str:
        return subprocess.run(
            ["python3", str(SCRIPT), "--repo-dir", str(self.repo)],
            capture_output=True, text=True, check=True, env=clean_git_env(),
        ).stdout.strip()

    def test_feat_in_shipped_code_bumps_minor(self) -> None:
        self.git("tag", "v0.7.0")
        self.commit("fix(hub): handle x", "internal/hub/x.go")
        self.commit("feat(agent): add y", "internal/agent/y.go")
        self.assertEqual(self.next_version(), "v0.8.0")

    def test_fix_bumps_patch(self) -> None:
        self.git("tag", "v0.7.0")
        self.commit("fix(web): button", "web/assets/js/main.js")
        self.assertEqual(self.next_version(), "v0.7.1")

    def test_no_release_for_docs_ci_tests_or_unshipped_paths(self) -> None:
        self.git("tag", "v0.7.0")
        self.commit("docs: explain", "docs/a.md")
        self.commit("feat(scripts): new helper", "scripts/helper.py")
        self.commit("fix(hub): flaky test", "internal/hub/x_test.go")
        self.commit("fix(web): test only", "web/test/a.test.mjs")
        self.commit("chore(hub): tidy", "internal/hub/x.go")
        self.assertEqual(self.next_version(), "")

    def test_prerelease_tags_are_not_a_base(self) -> None:
        self.git("tag", "v0.7.0")
        self.commit("feat(hub): a", "cmd/hub/a.go")
        self.git("tag", "v0.8.0-rc.1")
        self.commit("fix(hub): b", "cmd/hub/b.go")
        self.assertEqual(self.next_version(), "v0.8.0")

    def test_already_released_head_needs_nothing(self) -> None:
        self.commit("feat(hub): a", "cmd/hub/a.go")
        self.git("tag", "v0.8.0")
        self.assertEqual(self.next_version(), "")

    def test_breaking_bumps_minor_before_1_and_major_after(self) -> None:
        self.git("tag", "v0.7.0")
        self.commit("refactor(hub)!: change api", "internal/hub/api.go")
        self.assertEqual(self.next_version(), "v0.8.0")
        self.git("tag", "v1.2.3")
        self.commit("fix(hub): x\n\nBREAKING CHANGE: removed y", "internal/hub/y.go")
        self.assertEqual(self.next_version(), "v2.0.0")

    def test_no_tags_starts_from_zero(self) -> None:
        self.commit("feat: first", "go.mod")
        self.assertEqual(self.next_version(), "v0.1.0")


if __name__ == "__main__":
    unittest.main()
