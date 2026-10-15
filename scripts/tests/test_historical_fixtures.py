"""Exact historical adjudication; every detector-shaped value is runtime-only."""
from __future__ import annotations

import contextlib
import hashlib
import io
import json
import shutil
import subprocess
import unittest
from pathlib import Path
from unittest import mock

from test_prepublish_check import PC, TempGitRepo, _fake_aws_key, _run_git


class DisposableGitRepo(TempGitRepo):
    def __init__(self) -> None:
        super().__init__()
        self.path = self.root

    def close(self) -> None:
        self.cleanup()

    def git(self, *args: str) -> str:
        return _run_git(self.root, *args).strip()

    def commit_files(self, files: dict[str, str], message: str) -> str:
        for relative, text in files.items():
            self.write(relative, text)
        return self.commit(message)

    def empty_commit(self, message: str) -> str:
        self.git("commit", "--allow-empty", "-qm", message)
        return self.git("rev-parse", "HEAD")

    def ip_line(self) -> str:
        return "endpoint=" + ".".join(("1", "2", "3", "4"))

    def entry(self, commit, path, line, rule_id="internal.host_ip", content=None, line_sha256=None):
        if line_sha256 is None:
            if content is None:
                content = (self.path / path).read_text(encoding="utf-8")
            line_sha256 = hashlib.sha256(content.splitlines()[line - 1].encode("utf-8")).hexdigest()
        return {
            "commit": commit, "path": path, "line": line, "rule_id": rule_id,
            "line_sha256": line_sha256, "reason": "Synthetic historical detector fixture.",
        }

    def manifest_data(self, fixtures):
        return {
            "schema_version": 1,
            "review": {
                "method": "source-context-review", "date": "2026-10-07",
                "scope": "synthetic historical fixtures only",
            },
            "fixtures": fixtures,
        }

    def write_manifest(self, fixtures, path=None):
        target = path or self.path / ".prepublish-historical-fixtures.json"
        target.write_text(json.dumps(self.manifest_data(fixtures), indent=2), encoding="utf-8")
        return target


class HistoricalFixtureTests(unittest.TestCase):
    def setUp(self):
        self.repo = DisposableGitRepo()

    def tearDown(self):
        self.repo.close()

    def _committed_ip(self):
        line = self.repo.ip_line()
        return self.repo.commit_files({"sample.txt": line + "\n"}, "add synthetic IP fixture"), line

    def test_exact_match_is_separate_from_unresolved_findings(self):
        commit, _ = self._committed_ip()
        self.assertTrue(any(f.rule_id == "internal.host_ip" for f in PC.scan_history(self.repo.path, [])))
        fixture = self.repo.entry(commit, "sample.txt", 1)
        self.repo.write_manifest([fixture])
        accepted = PC.scan_history(self.repo.path, [], PC.ReviewedHistoricalFixtures.load(self.repo.path))
        self.assertEqual([f.severity for f in accepted], ["accepted_fixture"])
        summary = PC.render_summary([], accepted)
        self.assertIn("history findings=0 (fail=0, warn=0)", summary)
        self.assertIn("accepted_fixture_count=1", summary)
        self.assertIn("detector matches=1", summary)
        report = PC.render_markdown_report([], accepted)
        self.assertIn("## Unresolved history findings", report)
        self.assertIn("## Accepted historical fixtures", report)
        self.assertIn(fixture["reason"], report)

    def test_home_path_is_an_individually_eligible_rule(self):
        line = "fixture=" + "/" + "home" + "/" + "synthetic-user"
        commit = self.repo.commit_files({"home.txt": line + "\n"}, "add home fixture")
        self.repo.write_manifest([self.repo.entry(commit, "home.txt", 1, "internal.home_path")])
        findings = PC.scan_history(self.repo.path, [], PC.ReviewedHistoricalFixtures.load(self.repo.path))
        self.assertEqual([f.severity for f in findings], ["accepted_fixture"])

    def test_baseline_still_warns_ordinary_findings_not_accepted_fixtures(self):
        ordinary = PC.Finding("internal.host_ip", "fixture", "history:abc:path:1", "detected")
        accepted = PC.Finding("internal.host_ip", "fixture", "history:def:path:1", "reason", severity="accepted_fixture")
        class MatchingBaseline:
            def contains(self, finding):
                return True
        result = PC.classify_with_baseline([ordinary, accepted], MatchingBaseline(), True)
        self.assertEqual([f.severity for f in result], ["warn", "accepted_fixture"])

    def test_copy_to_new_commit_and_current_line_remains_ordinary(self):
        commit, line = self._committed_ip()
        fixture = self.repo.entry(commit, "sample.txt", 1)
        self.repo.commit_files({"sample.txt": line + "\n" + line + "\n"}, "copy fixture")
        self.repo.write_manifest([fixture])
        findings = PC.scan_history(self.repo.path, [], PC.ReviewedHistoricalFixtures.load(self.repo.path))
        self.assertEqual(sum(f.severity == "accepted_fixture" for f in findings), 1)
        self.assertEqual(sum(f.severity == "fail" for f in findings), 1)
        current = PC.scan_line(line, "current:sample.txt", 1, [], "sample.txt")
        self.assertTrue(current)
        self.assertTrue(all(f.severity == "fail" for f in current))

    def test_changed_commit_path_line_or_rule_cannot_match(self):
        commit, _ = self._committed_ip()
        text = self.repo.ip_line() + "\nordinary line\n"
        self.repo.commit_files({"sample.txt": text, "ordinary.txt": "ordinary\n"}, "add files")
        cases = (
            self.repo.entry(self.repo.empty_commit("non-added commit"), "sample.txt", 1),
            self.repo.entry(commit, "ordinary.txt", 1, content="ordinary\n"),
            self.repo.entry(commit, "sample.txt", 2, content=text),
            self.repo.entry(commit, "sample.txt", 1, "internal.home_path"),
        )
        for entry in cases:
            with self.subTest(identity=(entry["commit"], entry["path"], entry["line"], entry["rule_id"])):
                self.repo.write_manifest([entry])
                with self.assertRaises((ValueError, subprocess.CalledProcessError)):
                    PC.scan_history(self.repo.path, [], PC.ReviewedHistoricalFixtures.load(self.repo.path))

    def test_changed_hash_is_rejected_before_scanning(self):
        commit, _ = self._committed_ip()
        self.repo.write_manifest([self.repo.entry(commit, "sample.txt", 1, line_sha256="0" * 64)])
        with self.assertRaises(ValueError):
            PC.ReviewedHistoricalFixtures.load(self.repo.path)

    def test_same_line_secret_remains_a_failure_when_ip_is_accepted(self):
        line = self.repo.ip_line() + " " + _fake_aws_key()
        commit = self.repo.commit_files({"mixed.txt": line + "\n"}, "mixed fixture")
        self.repo.write_manifest([self.repo.entry(commit, "mixed.txt", 1)])
        findings = PC.scan_history(self.repo.path, [], PC.ReviewedHistoricalFixtures.load(self.repo.path))
        self.assertEqual([f.rule_id for f in findings if f.severity == "accepted_fixture"], ["internal.host_ip"])
        self.assertTrue(any(f.severity == "fail" and f.rule_id.startswith("secret.") for f in findings))

    def test_deleted_file_secret_remains_a_historical_failure(self):
        self.repo.commit_files({"deleted.txt": _fake_aws_key() + "\n"}, "synthetic secret")
        (self.repo.path / "deleted.txt").unlink()
        self.repo.git("add", "-A")
        self.repo.git("commit", "-qm", "delete synthetic secret")
        findings = PC.scan_history(self.repo.path, [])
        self.assertTrue(any(f.severity == "fail" and f.rule_id.startswith("secret.") for f in findings))

    def test_duplicate_json_unknown_fields_and_invalid_provenance_fail_closed(self):
        commit, _ = self._committed_ip()
        valid = self.repo.manifest_data([self.repo.entry(commit, "sample.txt", 1)])
        mutations = []
        for section, key, value in (
            ("top", "extra", True), ("fixture", "extra", True),
            ("fixture", "rule_id", "internal.secret"), ("fixture", "rule_id", "secret.example"),
            ("fixture", "rule_id", "internal.local_marker"), ("fixture", "rule_id", "hygiene.check_docs"),
            ("review", "date", "2026-02-30"), ("top", "schema_version", 2),
        ):
            data = json.loads(json.dumps(valid))
            target = data if section == "top" else data["fixtures"][0] if section == "fixture" else data["review"]
            target[key] = value
            mutations.append((f"{section}:{key}:{value}", data))
        for conflicting in (False, True):
            data = json.loads(json.dumps(valid))
            entry = dict(data["fixtures"][0])
            if conflicting:
                entry.update(reason="Conflicting review reason.", line_sha256="0" * 64)
            data["fixtures"].append(entry)
            mutations.append((f"duplicate:{conflicting}", data))
        for label, data in mutations:
            with self.subTest(label=label):
                manifest = self.repo.path / ".prepublish-historical-fixtures.json"
                manifest.write_text(json.dumps(data), encoding="utf-8")
                with self.assertRaises(ValueError):
                    PC.ReviewedHistoricalFixtures.load(self.repo.path)
        manifest = self.repo.path / ".prepublish-historical-fixtures.json"
        for text in ('{"schema_version":1,"schema_version":1,"review":{},"fixtures":[]}', "{"):
            manifest.write_text(text, encoding="utf-8")
            with self.assertRaises(ValueError):
                PC.ReviewedHistoricalFixtures.load(self.repo.path)

    def test_schema_types_and_review_provenance_fail_as_value_errors(self):
        commit, _ = self._committed_ip()
        valid = self.repo.manifest_data([self.repo.entry(commit, "sample.txt", 1)])
        cases = (
            ("top", "schema_version", True), ("fixture", "commit", []),
            ("fixture", "path", {}), ("fixture", "line", True),
            ("fixture", "rule_id", []), ("fixture", "rule_id", {}), ("fixture", "rule_id", True),
            ("fixture", "line_sha256", True), ("fixture", "reason", {}),
            ("review", "method", []), ("review", "date", True), ("review", "scope", {}),
            ("review", "reviewer", "unapproved"), ("review", "method", "other"), ("review", "scope", "other"),
        )
        for section, key, value in cases:
            with self.subTest(section=section, key=key):
                data = json.loads(json.dumps(valid))
                target = data if section == "top" else data["fixtures"][0] if section == "fixture" else data["review"]
                target[key] = value
                (self.repo.path / ".prepublish-historical-fixtures.json").write_text(json.dumps(data), encoding="utf-8")
                with self.assertRaises(ValueError):
                    PC.ReviewedHistoricalFixtures.load(self.repo.path)
        data = json.loads(json.dumps(valid))
        data["fixtures"][0]["rule_id"] = []
        invalid = self.repo.path / "invalid-types.json"
        invalid.write_text(json.dumps(data), encoding="utf-8")
        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr):
            result = PC.main(["--history-only", "--repo-root", str(self.repo.path), "--historical-fixtures", str(invalid)])
        self.assertEqual(result, 2)
        self.assertIn("fatal error", stderr.getvalue())
        self.assertNotIn(str(invalid), stderr.getvalue())

    def test_manifest_rejects_symlink_nonregular_and_missing_explicit_path(self):
        commit, _ = self._committed_ip()
        valid = self.repo.write_manifest([self.repo.entry(commit, "sample.txt", 1)], self.repo.path / "valid.json")
        missing = self.repo.path / "missing.json"
        directory = self.repo.path / "manifest-directory"
        directory.mkdir()
        symlink = self.repo.path / "manifest-symlink.json"
        symlink.symlink_to(valid)
        for target in (missing, directory, symlink):
            with self.subTest(target=target.name), self.assertRaises(ValueError):
                PC.ReviewedHistoricalFixtures.load(self.repo.path, target)

    def test_total_cached_blob_budget_is_checked_before_read(self):
        commit, _ = self._committed_ip()
        self.repo.write_manifest([self.repo.entry(commit, "sample.txt", 1)])
        with (
            mock.patch.object(PC.ReviewedHistoricalFixtures, "MAX_TOTAL_CACHED_TARGET_BYTES", 0),
            mock.patch.object(PC.ReviewedHistoricalFixtures, "_read_target_blob", side_effect=AssertionError("budget exceeded")),
        ):
            with self.assertRaises(ValueError):
                PC.ReviewedHistoricalFixtures.load(self.repo.path)

    def test_unsafe_path_unreachable_target_and_line_mismatch_fail(self):
        commit, original_line = self._committed_ip()
        for entry in (
            self.repo.entry(commit, "../sample.txt", 1, content=original_line + "\n"),
            self.repo.entry(commit, "sample.txt", 1, line_sha256="0" * 64),
        ):
            self.repo.write_manifest([entry])
            with self.assertRaises(ValueError):
                PC.ReviewedHistoricalFixtures.load(self.repo.path)
        old_branch = self.repo.git("branch", "--show-current")
        self.repo.git("checkout", "--orphan", "unrelated-history")
        for child in self.repo.path.iterdir():
            if child.name == ".git":
                continue
            if child.is_dir():
                shutil.rmtree(child)
            else:
                child.unlink()
        self.repo.commit_files({"unrelated.txt": "ordinary\n"}, "new unrelated root")
        self.repo.git("branch", "-D", old_branch)
        self.repo.write_manifest([self.repo.entry(commit, "sample.txt", 1, content=original_line + "\n")])
        with self.assertRaises(ValueError):
            PC.ReviewedHistoricalFixtures.load(self.repo.path)

    def test_missing_multiple_and_changed_content_matches_fail(self):
        line = self.repo.ip_line() + " synthetic local marker"
        commit = self.repo.commit_files({"marked.txt": line + "\n"}, "marker fixture")
        self.repo.write_manifest([self.repo.entry(commit, "marked.txt", 1)])
        with self.assertRaises(ValueError):
            PC.scan_history(self.repo.path, ["synthetic local marker"], PC.ReviewedHistoricalFixtures.load(self.repo.path))
        for rows in (
            [(commit, "marked.txt", 1, line + " changed")],
            [(commit, "marked.txt", 1, line), (commit, "marked.txt", 1, line)],
        ):
            reviewed = PC.ReviewedHistoricalFixtures.load(self.repo.path)
            with (
                mock.patch.object(PC, "iter_history_added_lines", return_value=iter(rows)),
                mock.patch.object(PC, "scan_history_large_and_binary_blobs", return_value=[]),
                self.assertRaises(ValueError),
            ):
                PC.scan_history(self.repo.path, [], reviewed)

    def test_tree_only_does_not_claim_full_history_validation(self):
        commit, _ = self._committed_ip()
        self.repo.write_manifest([self.repo.entry(commit, "sample.txt", 1)])
        with (
            mock.patch.object(PC, "load_markers", return_value=[]),
            mock.patch.object(PC, "scan_tree", return_value=[]),
            mock.patch.object(PC, "run_hygiene_checks", return_value=[]),
            contextlib.redirect_stdout(io.StringIO()) as output,
        ):
            result = PC.main(["--tree-only", "--repo-root", str(self.repo.path)])
        self.assertEqual(result, 0)
        self.assertIn("history scan=skipped", output.getvalue())
        report = PC.render_markdown_report([], [], history_skipped=True)
        history = report.split("## Unresolved history findings", 1)[1]
        self.assertIn("History scan skipped", history)
        self.assertNotIn("No findings.", history)

    def test_failed_git_log_is_nonzero_and_cli_errors_are_redacted(self):
        nonrepo = self.repo.path / "not-a-repository"
        nonrepo.mkdir()
        with self.assertRaises(RuntimeError):
            list(PC.iter_history_added_lines(nonrepo))
        stdout, stderr = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            result = PC.main(["--history-only", "--repo-root", str(nonrepo)])
        self.assertEqual(result, 2)
        self.assertIn("fatal error", stderr.getvalue())
        self.assertNotIn(str(nonrepo), stderr.getvalue())

    def test_invalid_cli_manifest_cannot_be_rescued_by_baseline(self):
        commit, _ = self._committed_ip()
        finding = next(f for f in PC.scan_history(self.repo.path, []) if f.rule_id == "internal.host_ip")
        baseline = self.repo.path / "baseline.json"
        PC.Baseline.load(baseline).save(baseline, [finding])
        invalid = self.repo.path / "invalid.json"
        invalid.write_text("{", encoding="utf-8")
        stdout, stderr = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            result = PC.main([
                "--history-only", "--repo-root", str(self.repo.path),
                "--baseline", str(baseline), "--historical-fixtures", str(invalid),
            ])
        self.assertEqual(result, 2)
        self.assertIn("fatal error", stderr.getvalue())
        self.assertNotIn(str(invalid), stderr.getvalue())
        self.assertNotIn(commit, stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
