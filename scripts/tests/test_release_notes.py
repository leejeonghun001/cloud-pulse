"""Unit tests for scripts/release-notes.sh, run against a throwaway git
repository fixture (never this repository's own history) so the tests
are hermetic and produce byte-identical, reviewable snapshot output.
"""

from __future__ import annotations

import shutil
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "release-notes.sh"


def run_git(repo: Path, *args: str, env: dict[str, str] | None = None) -> str:
    """Runs a git command inside repo, returning stripped stdout."""
    base_env = {
        "GIT_AUTHOR_NAME": "Test Author",
        "GIT_AUTHOR_EMAIL": "test@example.invalid",
        "GIT_COMMITTER_NAME": "Test Author",
        "GIT_COMMITTER_EMAIL": "test@example.invalid",
    }
    if env:
        base_env.update(env)
    result = subprocess.run(
        ["git", "-C", str(repo), *args],
        capture_output=True,
        text=True,
        env={**_base_os_env(), **base_env},
        check=True,
    )
    return result.stdout.strip()


def _base_os_env() -> dict[str, str]:
    import os

    return dict(os.environ)


def commit(repo: Path, message: str, *, date: str, filename: str = "file.txt") -> str:
    """Writes a unique file, commits with an explicit date, returns short hash."""
    target = repo / filename
    existing = target.read_text(encoding="utf-8") if target.exists() else ""
    target.write_text(existing + message + "\n", encoding="utf-8")
    run_git(repo, "add", filename)
    env = {"GIT_AUTHOR_DATE": date, "GIT_COMMITTER_DATE": date}
    run_git(repo, "commit", "--no-verify", "-m", message, env=env)
    return run_git(repo, "rev-parse", "--short", "HEAD")


def tag(repo: Path, name: str, *, date: str) -> None:
    env = {"GIT_COMMITTER_DATE": date}
    run_git(repo, "tag", name, env=env)


def run_release_notes(repo: Path, *args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["bash", str(SCRIPT), *args, "--repo-dir", str(repo), "--repo-url", "https://example.invalid/owner/repo"],
        capture_output=True,
        text=True,
        check=False,
    )


class ReleaseNotesFixture:
    """Builds a small, fully deterministic git repository with two tagged
    releases exercising every commit-type bucket plus a breaking change,
    for use by every test method below."""

    def __init__(self) -> None:
        self.dir = Path(tempfile.mkdtemp(prefix="cloud-pulse-release-notes-fixture-"))
        run_git(self.dir, "init", "-q", "-b", "main")
        run_git(self.dir, "config", "user.name", "Test Author")
        run_git(self.dir, "config", "user.email", "test@example.invalid")

        self.h1 = commit(self.dir, "chore: init project", date="2030-01-01T00:00:00")
        self.h2 = commit(self.dir, "feat: add widget support", date="2030-01-02T00:00:00")
        self.h3 = commit(self.dir, "fix: correct widget overflow", date="2030-01-03T00:00:00")
        tag(self.dir, "v0.1.0", date="2030-01-03T01:00:00")

        self.h4 = commit(self.dir, "feat(api)!: remove legacy /v0 endpoint", date="2030-02-01T00:00:00")
        self.h5 = commit(self.dir, "docs: document the new /v1 endpoint", date="2030-02-02T00:00:00")
        self.h6 = commit(self.dir, "test(api): cover /v1 edge cases", date="2030-02-03T00:00:00")
        self.h7 = commit(self.dir, "ci: run tests on windows too", date="2030-02-04T00:00:00")
        self.h8 = commit(
            self.dir,
            "fix(storage): reject invalid quota\n\nBREAKING CHANGE: the storage quota field is now required",
            date="2030-02-05T00:00:00",
        )
        tag(self.dir, "v0.2.0", date="2030-02-05T01:00:00")

    def cleanup(self) -> None:
        shutil.rmtree(self.dir, ignore_errors=True)


class ReleaseNotesScriptTests(unittest.TestCase):
    def setUp(self) -> None:
        self.fixture = ReleaseNotesFixture()

    def tearDown(self) -> None:
        self.fixture.cleanup()

    def test_first_tag_has_no_previous_tag_range(self) -> None:
        result = run_release_notes(self.fixture.dir, "v0.1.0")
        self.assertEqual(result.returncode, 0, result.stderr)
        out = result.stdout
        self.assertIn("## v0.1.0", out)
        self.assertIn("feat: add widget support", out)
        self.assertIn("fix: correct widget overflow", out)
        self.assertIn("chore: init project", out)
        self.assertIn("https://example.invalid/owner/repo/commits/v0.1.0", out)
        self.assertNotIn("compare/", out)

    def test_second_tag_auto_resolves_previous_tag_by_creation_order(self) -> None:
        result = run_release_notes(self.fixture.dir, "v0.2.0")
        self.assertEqual(result.returncode, 0, result.stderr)
        out = result.stdout
        self.assertIn("## v0.2.0", out)
        # Only commits after v0.1.0 must appear.
        self.assertNotIn("add widget support", out)
        self.assertIn("remove legacy /v0 endpoint", out)
        self.assertIn("https://example.invalid/owner/repo/compare/v0.1.0...v0.2.0", out)

    def test_explicit_previous_tag_matches_auto_resolution(self) -> None:
        auto = run_release_notes(self.fixture.dir, "v0.2.0")
        explicit = run_release_notes(self.fixture.dir, "v0.2.0", "v0.1.0")
        self.assertEqual(auto.returncode, 0, auto.stderr)
        self.assertEqual(explicit.returncode, 0, explicit.stderr)
        self.assertEqual(auto.stdout, explicit.stdout)

    def test_sections_group_by_conventional_commit_type(self) -> None:
        result = run_release_notes(self.fixture.dir, "v0.2.0")
        out = result.stdout
        self.assertIn("### Features", out)
        self.assertIn("### Fixes", out)
        self.assertIn("### Documentation", out)
        self.assertIn("### Tests", out)
        self.assertIn("### CI", out)
        # Ensure each commit landed under the right heading, not just
        # present somewhere in the output.
        features_idx = out.index("### Features")
        fixes_idx = out.index("### Fixes")
        docs_idx = out.index("### Documentation")
        self.assertIn("remove legacy /v0 endpoint", out[features_idx:fixes_idx])
        self.assertIn("reject invalid quota", out[fixes_idx:docs_idx])

    def test_breaking_changes_detected_from_bang_and_footer(self) -> None:
        result = run_release_notes(self.fixture.dir, "v0.2.0")
        out = result.stdout
        self.assertIn("### Breaking changes", out)
        self.assertIn("remove legacy /v0 endpoint", out.split("### Breaking changes")[1].split("###")[0])
        self.assertIn(
            "the storage quota field is now required",
            out.split("### Breaking changes")[1].split("###")[0],
        )

    def test_upgrade_and_checksum_blocks_present(self) -> None:
        result = run_release_notes(self.fixture.dir, "v0.1.0")
        out = result.stdout
        self.assertIn("### Upgrade", out)
        self.assertIn("sudo cloud-pulse-hub update", out)
        self.assertIn("sudo cloud-pulse-agent update", out)
        self.assertIn("### Checksums", out)
        self.assertIn("checksums.txt", out)

    def test_output_is_deterministic_across_repeated_runs(self) -> None:
        first = run_release_notes(self.fixture.dir, "v0.2.0")
        second = run_release_notes(self.fixture.dir, "v0.2.0")
        self.assertEqual(first.stdout, second.stdout)

    def test_unknown_tag_fails_with_nonzero_exit(self) -> None:
        result = run_release_notes(self.fixture.dir, "v9.9.9")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("v9.9.9", result.stderr)

    def test_unknown_previous_tag_fails_with_nonzero_exit(self) -> None:
        result = run_release_notes(self.fixture.dir, "v0.2.0", "v9.9.9")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("v9.9.9", result.stderr)

    def test_full_snapshot_matches_expected_text(self) -> None:
        result = run_release_notes(self.fixture.dir, "v0.2.0")
        self.assertEqual(result.returncode, 0, result.stderr)
        expected = textwrap.dedent(
            f"""\
            ## v0.2.0

            ### Breaking changes

            - the storage quota field is now required ({self.fixture.h8})
            - feat(api)!: remove legacy /v0 endpoint ({self.fixture.h4})

            ### Features

            - feat(api)!: remove legacy /v0 endpoint ({self.fixture.h4})

            ### Fixes

            - fix(storage): reject invalid quota ({self.fixture.h8})

            ### Documentation

            - docs: document the new /v1 endpoint ({self.fixture.h5})

            ### Tests

            - test(api): cover /v1 edge cases ({self.fixture.h6})

            ### CI

            - ci: run tests on windows too ({self.fixture.h7})

            ### Upgrade

            ```bash
            sudo cloud-pulse-hub update
            sudo cloud-pulse-agent update
            ```

            See `docs/upgrade.md` for the full upgrade/migration guide.

            ### Checksums

            Every release asset's sha256 is published in this release's `checksums.txt`;
            verify with `sha256sum -c checksums.txt` after download.

            ### Full changelog

            https://example.invalid/owner/repo/compare/v0.1.0...v0.2.0

            """
        )
        self.assertEqual(result.stdout, expected)

    def test_help_flag_exits_zero(self) -> None:
        result = subprocess.run(
            ["bash", str(SCRIPT), "--help"],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0)
        self.assertIn("Usage:", result.stdout)

    def test_missing_arguments_exit_nonzero(self) -> None:
        result = subprocess.run(
            ["bash", str(SCRIPT)],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertNotEqual(result.returncode, 0)


if __name__ == "__main__":
    unittest.main()
