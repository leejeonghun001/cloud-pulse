"""Unit tests for scripts/prepublish-check.py.

Every secret-shaped fixture value used below is assembled at *runtime*
via string concatenation/formatting rather than written as a literal,
so this test file itself never contains a string that would trip the
very rules it's testing (verified indirectly: running
prepublish-check.py --tree-only against this repository, which
includes this file, must still report zero tree findings).
"""

from __future__ import annotations

import contextlib
import hashlib
import importlib.util
import io
import os
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from types import ModuleType

ROOT = Path(__file__).resolve().parents[2]


def load_script(filename: str, module_name: str) -> ModuleType:
    spec = importlib.util.spec_from_file_location(module_name, ROOT / "scripts" / filename)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"could not load {filename}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    spec.loader.exec_module(module)
    return module


PC = load_script("prepublish-check.py", "cloud_pulse_prepublish_check_test")


def _run_git(repo: Path, *args: str) -> str:
    result = subprocess.run(
        ["git", *args],
        cwd=repo,
        capture_output=True,
        text=True,
        check=True,
    )
    return result.stdout


class TempGitRepo:
    """A throwaway git repository used as a scan target, so tests never
    touch this project's own tree or history."""

    def __init__(self) -> None:
        self.root = Path(tempfile.mkdtemp(prefix="cloud-pulse-prepublish-fixture-"))
        _run_git(self.root, "init", "-q")
        _run_git(self.root, "config", "user.email", "test@example.invalid")
        _run_git(self.root, "config", "user.name", "Test")

    def write(self, relpath: str, content: str) -> Path:
        path = self.root / relpath
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
        return path

    def commit(self, message: str = "commit") -> str:
        _run_git(self.root, "add", "-A")
        _run_git(self.root, "commit", "-q", "-m", message, "--allow-empty")
        return _run_git(self.root, "rev-parse", "HEAD").strip()

    def cleanup(self) -> None:
        import shutil

        shutil.rmtree(self.root, ignore_errors=True)


def _fake_aws_key() -> str:
    # AKIA + 16 alphanumerics, assembled at runtime.
    return "AKIA" + "".join(["Q" if i % 2 == 0 else "7" for i in range(16)])


def _fake_github_token() -> str:
    return "gh" + "p_" + ("A" * 36)


def _fake_slack_token() -> str:
    return "xox" + "b-" + "1234567890-" + "abcdefghij"


def _fake_discord_webhook() -> str:
    return "https://discord.com/api/webhooks/" + ("1" * 18) + "/" + ("a" * 68)


def _fake_telegram_token() -> str:
    return "123456789:" + ("A" * 35)


def _fake_google_oauth_secret() -> str:
    return "GOCSPX-" + ("b" * 28)


def _fake_dropbox_token() -> str:
    return "sl." + ("c" * 70)


def _fake_jwt() -> str:
    seg = "eyJ" + ("x" * 20)
    return seg + "." + seg + "." + ("y" * 20)


def _pem_private_key_header(kind: str = "RSA") -> str:
    # Assembled at runtime (never a literal in this file) so this fixture
    # itself doesn't trip the repo's own blunt pre-commit secret grep,
    # which — unlike prepublish-check.py's own smarter rule engine — has
    # no allow-secret-scan/marker mechanism and matches on the literal
    # "-----BEGIN ... PRIVATE KEY-----" header text alone.
    dashes = "-" * 5
    return f"{dashes}BEGIN {kind} PRIVATE KEY{dashes}"


def _fake_high_entropy() -> str:
    # 32 chars mixing case/digits/symbols -> high Shannon entropy.
    return "Zk9" + "qP2mN" + "8vR1x" + "Tw4bH" + "s7Yc" + "!Lm3"


class SecretRuleTests(unittest.TestCase):
    def setUp(self) -> None:
        self.markers: set[str] = set()

    def test_aws_key_detected(self) -> None:
        line = f'access_key = "{_fake_aws_key()}"  # allow-secret-scan: unit test fixture'
        findings = list(PC.scan_line_for_secrets(line, "tree:x.go", 1, self.markers))
        self.assertEqual(findings, [])  # allow-secret-scan comment suppresses it

    def test_aws_key_detected_without_allow_comment(self) -> None:
        value = _fake_aws_key()
        # Replace the "AKIA"-derived fake marker so it isn't treated as
        # an obvious fake value by _is_fake_value (it contains neither
        # "test"/"fake"/etc. nor an allow comment here).
        line = f'access_key = "{value}"'
        findings = list(PC.scan_line_for_secrets(line, "tree:x.go", 1, self.markers))
        self.assertEqual(len(findings), 1)
        self.assertEqual(findings[0].rule_id, "secret.aws_access_key")
        # The raw value must never appear in the finding's detail.
        self.assertNotIn(value, findings[0].detail)

    def test_github_token_detected(self) -> None:
        value = _fake_github_token()
        line = f'token := "{value}"'
        findings = list(PC.scan_line_for_secrets(line, "tree:x.go", 1, self.markers))
        self.assertEqual(len(findings), 1)
        self.assertEqual(findings[0].rule_id, "secret.github_token")
        self.assertNotIn(value, findings[0].detail)

    def test_slack_token_detected(self) -> None:
        value = _fake_slack_token()
        line = f"SLACK={value}"
        findings = list(PC.scan_line_for_secrets(line, "tree:x.env", 1, self.markers))
        self.assertEqual(len(findings), 1)
        self.assertEqual(findings[0].rule_id, "secret.slack_token")

    def test_discord_webhook_detected(self) -> None:
        value = _fake_discord_webhook()
        line = f'webhook = "{value}"'
        findings = list(PC.scan_line_for_secrets(line, "tree:x.go", 1, self.markers))
        self.assertEqual(len(findings), 1)
        self.assertEqual(findings[0].rule_id, "secret.discord_webhook")

    def test_telegram_bot_token_detected(self) -> None:
        value = _fake_telegram_token()
        line = f"bot_token={value}"
        findings = list(PC.scan_line_for_secrets(line, "tree:x.env", 1, self.markers))
        self.assertEqual(len(findings), 1)
        self.assertEqual(findings[0].rule_id, "secret.telegram_bot_token")

    def test_google_oauth_secret_detected(self) -> None:
        value = _fake_google_oauth_secret()
        line = f'client_secret = "{value}"'
        findings = list(PC.scan_line_for_secrets(line, "tree:x.go", 1, self.markers))
        self.assertEqual(len(findings), 1)
        self.assertEqual(findings[0].rule_id, "secret.google_oauth_client_secret")

    def test_dropbox_token_detected(self) -> None:
        value = _fake_dropbox_token()
        line = f'token = "{value}"'
        findings = list(PC.scan_line_for_secrets(line, "tree:x.go", 1, self.markers))
        rule_ids = {f.rule_id for f in findings}
        self.assertIn("secret.dropbox_token", rule_ids)

    def test_private_key_block_detected(self) -> None:
        line = _pem_private_key_header("RSA")
        findings = list(PC.scan_line_for_secrets(line, "tree:x.pem", 1, self.markers))
        self.assertEqual(len(findings), 1)
        self.assertEqual(findings[0].rule_id, "secret.private_key_block")

    def test_jwt_detected(self) -> None:
        value = _fake_jwt()
        line = f'token = "{value}"'
        findings = list(PC.scan_line_for_secrets(line, "tree:x.go", 1, self.markers))
        rule_ids = {f.rule_id for f in findings}
        self.assertIn("secret.jwt", rule_ids)

    def test_generic_high_entropy_assignment_detected(self) -> None:
        value = _fake_high_entropy()
        line = f'password = "{value}"'
        findings = list(PC.scan_line_for_secrets(line, "tree:x.go", 1, self.markers))
        rule_ids = {f.rule_id for f in findings}
        self.assertIn("secret.generic_high_entropy_assignment", rule_ids)

    def test_generic_low_entropy_assignment_not_flagged(self) -> None:
        line = 'password = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
        findings = list(PC.scan_line_for_secrets(line, "tree:x.go", 1, self.markers))
        self.assertEqual(findings, [])

    def test_fake_marker_in_value_suppresses_finding(self) -> None:
        value = "FAKEghp_" + ("A" * 36)
        line = f'token := "{value}"'
        findings = list(PC.scan_line_for_secrets(line, "tree:x.go", 1, self.markers))
        self.assertEqual(findings, [])

    def test_allow_secret_scan_comment_suppresses_finding(self) -> None:
        value = _fake_github_token()
        line = f'token := "{value}"  // allow-secret-scan: CI-only placeholder, rotated per run'
        findings = list(PC.scan_line_for_secrets(line, "tree:x.go", 1, self.markers))
        self.assertEqual(findings, [])

    def test_local_marker_suppresses_finding(self) -> None:
        value = _fake_github_token()
        line = f'token := "{value}"'
        findings = list(PC.scan_line_for_secrets(line, "tree:x.go", 1, {value}))
        self.assertEqual(findings, [])

    def test_negative_no_secret_in_ordinary_code_line(self) -> None:
        line = "func main() { fmt.Println(\"hello world\") }"
        findings = list(PC.scan_line_for_secrets(line, "tree:x.go", 1, self.markers))
        self.assertEqual(findings, [])

    def test_redacted_never_contains_raw_value(self) -> None:
        value = _fake_aws_key()
        redacted = PC._redact(value)
        self.assertNotIn(value, redacted)
        self.assertIn(value[:4], redacted)  # prefix4 is allowed to appear
        self.assertIn(str(len(value)), redacted)


class InternalInfoRuleTests(unittest.TestCase):
    def setUp(self) -> None:
        self.markers: list[PC.Marker] = []

    def _findings(self, line: str) -> list[PC.Finding]:
        return list(PC.scan_line_for_internal_info(line, "tree:x.go", 1, self.markers, "x.go"))

    def test_home_path_detected(self) -> None:
        findings = self._findings('path := "/home/alice/.config/cloud-pulse"')
        self.assertEqual([f.rule_id for f in findings], ["internal.home_path"])

    def test_home_path_allowlisted_account_not_flagged(self) -> None:
        self.assertEqual(self._findings('path := "/home/runneradmin/work"'), [])

    def test_macos_and_windows_home_paths_detected(self) -> None:
        self.assertEqual(len(self._findings('path := "/Users/alice/Library"')), 1)
        self.assertEqual(len(self._findings(r'path := "C:\Users\alice\AppData"')), 1)

    def test_orchestrator_dir_name_detected(self) -> None:
        name = "." + "cloud-pulse-orchestrator"
        findings = self._findings(f'notes_dir := "/some/path/{name}/notes"')
        self.assertIn("internal.orchestrator_dir", {f.rule_id for f in findings})

    def test_public_ipv4_and_ipv6_detected(self) -> None:
        ipv4 = ".".join(["8", "8", "4", "4"])
        ipv6 = "2606" + ":4700::1111"
        self.assertIn("internal.host_ip", {f.rule_id for f in self._findings(f'addr := "{ipv4}"')})
        self.assertIn("internal.host_ip", {f.rule_id for f in self._findings(f'addr := "{ipv6}"')})

    def test_private_documentation_and_network_cidr_literals_allowed(self) -> None:
        private = ".".join(["10", "1", "2", "3"])
        doc_ipv4 = ".".join(["192", "0", "2", "1"])
        self.assertEqual(self._findings(f'addr := "{private}"'), [])
        self.assertEqual(self._findings(f'addr := "{doc_ipv4}"'), [])
        self.assertEqual(self._findings('addr := "2001:db8::1"'), [])
        self.assertEqual(self._findings('cidr := "8.8.4.0/24"'), [])
        self.assertEqual(self._findings('addr := "fd00::1234"'), [])

    def test_loopback_and_non_address_text_not_flagged(self) -> None:
        self.assertEqual(self._findings('addr := "127.0.0.1"'), [])
        self.assertEqual(self._findings('addr := "::1"'), [])
        self.assertEqual(self._findings('// fixed at 00:20:16'), [])


class MarkersTests(unittest.TestCase):
    @staticmethod
    def _marker_values() -> tuple[str, str, str, str, str, str]:
        exact_ipv4 = ".".join(["10", "77", "88", "99"])
        ipv4_prefix = ".".join(["172", "20", "30", ""]) 
        ipv6 = "fd" + "12:3456::7"
        hostname = "operator" + "-laptop"
        email = "operator" + "@example.invalid"
        arbitrary = "private" + "/label"
        return exact_ipv4, ipv4_prefix, ipv6, hostname, email, arbitrary

    def test_load_markers_from_env_supports_all_required_forms(self) -> None:
        repo = TempGitRepo()
        try:
            values = self._marker_values()
            env_value = ",".join(values[:3]) + "\n" + ",".join(values[3:])
            markers = PC.load_markers(repo.root, env={"CP_PREPUBLISH_MARKERS": env_value})
            self.assertEqual([marker.value for marker in markers], list(values))
            lines = [
                f'addr = "{values[0]}"',
                f'addr = "{values[1]}42"',
                f'addr = "{values[2]}"',
                f'host = "{values[3]}"',
                f'email = "{values[4]}"',
                f'label = "{values[5]}"',
            ]
            for line in lines:
                findings = PC.scan_line(line, "tree:x", 1, markers, "x")
                self.assertIn("internal.local_marker", {f.rule_id for f in findings})
            absent = PC.scan_line(f'host = "not{values[3]}"', "tree:x", 1, markers, "x")
            self.assertNotIn("internal.local_marker", {f.rule_id for f in absent})
        finally:
            repo.cleanup()

    def test_load_markers_from_local_file(self) -> None:
        repo = TempGitRepo()
        try:
            values = self._marker_values()
            repo.write(".prepublish-markers.local", f"# local-only\n{values[0]}\n{values[4]}\n")
            markers = PC.load_markers(repo.root, env={})
            self.assertEqual([marker.value for marker in markers], [values[0], values[4]])
        finally:
            repo.cleanup()

    def test_marker_values_never_appear_in_stdout_or_report(self) -> None:
        repo = TempGitRepo()
        try:
            value = self._marker_values()[4]
            repo.write(".prepublish-markers.local", value + "\n")
            repo.write("marker.txt", f"contact={value}\n")
            repo.commit("marker fixture")
            report = repo.root / "report.md"
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout):
                result = PC.main([
                    "--tree-only", "--skip-check-docs", "--repo-root", str(repo.root), "--report", str(report)
                ])
            self.assertEqual(result, 1)
            rendered = stdout.getvalue() + report.read_text(encoding="utf-8")
            self.assertNotIn(value, rendered)
            self.assertIn("marker #1", rendered)
            self.assertIn("<redacted", rendered)
        finally:
            repo.cleanup()


class BaselineTests(unittest.TestCase):
    def test_load_missing_file_returns_empty(self) -> None:
        baseline = PC.Baseline.load(Path("/nonexistent/does/not/exist.json"))
        self.assertEqual(baseline.entries, set())

    def test_save_and_reload_roundtrip(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "baseline.json"
            finding = PC.Finding(
                rule_id="internal.host_ip",
                description="d",
                location="history:abc:file.go:1",
                detail="detail",
                baseline_key="deadbeef",
            )
            baseline = PC.Baseline()
            baseline.save(path, [finding])
            reloaded = PC.Baseline.load(path)
            self.assertIn("deadbeef", reloaded.entries)

    def test_baseline_file_never_contains_raw_matched_text(self) -> None:
        # The baseline stores only rule id + location + a sha256 hash —
        # confirm no plaintext secret-shaped value sneaks into the file
        # by checking a representative fake secret value never appears.
        secret_value = _fake_github_token()
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "baseline.json"
            finding = PC.Finding(
                rule_id="secret.github_token",
                description="d",
                location="history:abc:file.go:1",
                detail=PC._redact(secret_value),
                baseline_key=PC._baseline_hash("secret.github_token", "commit", "file.go", 1),
            )
            baseline = PC.Baseline()
            baseline.save(path, [finding])
            raw = path.read_text(encoding="utf-8")
            self.assertNotIn(secret_value, raw)

    def test_baseline_key_uses_exact_location_formula(self) -> None:
        expected = hashlib.sha256(("rule\0commit\0path/file\0" + "12").encode("utf-8")).hexdigest()
        self.assertEqual(PC._baseline_hash("rule", "commit", "path/file", 12), expected)

    def test_baseline_key_is_independent_of_matched_value(self) -> None:
        first = "marker" + "-one"
        second = "marker" + "-two"
        location = "history:commit:file.txt"
        a = PC.scan_line(f"value={first}", location, 9, [first], "file.txt", "commit")[0]
        b = PC.scan_line(f"value={second}", location, 9, [second], "file.txt", "commit")[0]
        self.assertEqual(a.baseline_key, b.baseline_key)
        self.assertNotIn(first, a.baseline_key or "")
        self.assertNotIn(second, b.baseline_key or "")

    def test_history_only_finding_in_baseline_becomes_warning(self) -> None:
        finding = PC.Finding(
            rule_id="internal.host_ip",
            description="d",
            location="history:abc:file.go:1",
            detail="detail",
            severity="fail",
            baseline_key="known-key",
        )
        baseline = PC.Baseline(entries={"known-key"})
        classified = PC.classify_with_baseline([finding], baseline, is_history=True)
        self.assertEqual(classified[0].severity, "warn")

    def test_new_history_finding_not_in_baseline_stays_failing(self) -> None:
        finding = PC.Finding(
            rule_id="internal.host_ip",
            description="d",
            location="history:abc:file.go:1",
            detail="detail",
            severity="fail",
            baseline_key="unknown-key",
        )
        baseline = PC.Baseline(entries={"some-other-key"})
        classified = PC.classify_with_baseline([finding], baseline, is_history=True)
        self.assertEqual(classified[0].severity, "fail")


class PNGMetadataTests(unittest.TestCase):
    def _write_png_with_text_chunk(self, path: Path, keyword: bytes, text: bytes) -> None:
        import struct
        import zlib

        def chunk(ctype: bytes, data: bytes) -> bytes:
            return (
                struct.pack(">I", len(data))
                + ctype
                + data
                + struct.pack(">I", zlib.crc32(ctype + data) & 0xFFFFFFFF)
            )

        sig = b"\x89PNG\r\n\x1a\n"
        ihdr_data = struct.pack(">IIBBBBB", 1, 1, 8, 2, 0, 0, 0)
        ihdr = chunk(b"IHDR", ihdr_data)
        idat = chunk(b"IDAT", zlib.compress(b"\x00\x00\x00\x00"))
        text_chunk = chunk(b"tEXt", keyword + b"\x00" + text)
        iend = chunk(b"IEND", b"")
        path.write_bytes(sig + ihdr + text_chunk + idat + iend)

    def test_png_text_chunk_detected(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "shot.png"
            self._write_png_with_text_chunk(path, b"Comment", b"captured on host alice-laptop")
            findings = PC.scan_png_metadata(path, "tree:shot.png")
            self.assertEqual(len(findings), 1)
            self.assertEqual(findings[0].rule_id, "internal.png_metadata")

    def test_png_without_text_chunk_not_flagged(self) -> None:
        import struct
        import zlib

        def chunk(ctype: bytes, data: bytes) -> bytes:
            return (
                struct.pack(">I", len(data))
                + ctype
                + data
                + struct.pack(">I", zlib.crc32(ctype + data) & 0xFFFFFFFF)
            )

        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "clean.png"
            sig = b"\x89PNG\r\n\x1a\n"
            ihdr = chunk(b"IHDR", struct.pack(">IIBBBBB", 1, 1, 8, 2, 0, 0, 0))
            idat = chunk(b"IDAT", zlib.compress(b"\x00\x00\x00\x00"))
            iend = chunk(b"IEND", b"")
            path.write_bytes(sig + ihdr + idat + iend)
            findings = PC.scan_png_metadata(path, "tree:clean.png")
            self.assertEqual(findings, [])

    def test_non_png_file_not_flagged(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "not-a-png.png"
            path.write_bytes(b"not actually a png")
            findings = PC.scan_png_metadata(path, "tree:not-a-png.png")
            self.assertEqual(findings, [])


class TreeAndHygieneIntegrationTests(unittest.TestCase):
    """End-to-end tests against small synthetic repositories, exercising
    scan_tree/scan_history/hygiene checks together."""

    def test_tree_scan_finds_secret_in_tracked_file(self) -> None:
        repo = TempGitRepo()
        try:
            value = _fake_aws_key()
            repo.write("config.go", f'var key = "{value}"\n')
            repo.commit("add config")
            findings = PC.scan_tree(repo.root, set())
            rule_ids = {f.rule_id for f in findings}
            self.assertIn("secret.aws_access_key", rule_ids)
        finally:
            repo.cleanup()

    def test_tree_scan_clean_repo_has_no_findings(self) -> None:
        repo = TempGitRepo()
        try:
            repo.write("main.go", 'package main\n\nfunc main() {}\n')
            repo.commit("init")
            findings = PC.scan_tree(repo.root, set())
            self.assertEqual(findings, [])
        finally:
            repo.cleanup()

    def test_scanner_sources_are_clean_when_tracked(self) -> None:
        repo = TempGitRepo()
        try:
            for relpath in ("scripts/prepublish-check.py", "scripts/tests/test_prepublish_check.py"):
                repo.write(relpath, (ROOT / relpath).read_text(encoding="utf-8"))
            repo.commit("track scanner sources")
            synthetic_markers = ["synthetic" + "-operator", "marker" + "@example.invalid"]
            findings = PC.scan_tree(repo.root, synthetic_markers)
            self.assertEqual(
                [f for f in findings if f.rule_id == "internal.local_marker"], []
            )
        finally:
            repo.cleanup()

    def test_history_scan_finds_secret_only_in_deleted_file(self) -> None:
        repo = TempGitRepo()
        try:
            value = _fake_github_token()
            repo.write("leaked.env", f"TOKEN={value}\n")
            repo.commit("oops, added a token")
            (repo.root / "leaked.env").unlink()
            repo.commit("remove the token file")
            findings = PC.scan_history(repo.root, set())
            rule_ids = {f.rule_id for f in findings}
            self.assertIn("secret.github_token", rule_ids)
            # Confirm the current tree is clean even though history isn't.
            tree_findings = PC.scan_tree(repo.root, set())
            self.assertEqual(
                [f for f in tree_findings if f.rule_id == "secret.github_token"], []
            )
        finally:
            repo.cleanup()

    def test_gitignore_missing_required_entries_flagged(self) -> None:
        repo = TempGitRepo()
        try:
            repo.write(".gitignore", "*.log\n")
            repo.commit("init")
            findings = PC.check_gitignore(repo.root)
            self.assertTrue(any(f.rule_id == "hygiene.gitignore_missing_entry" for f in findings))
        finally:
            repo.cleanup()

    def test_gitignore_with_required_entries_not_flagged(self) -> None:
        repo = TempGitRepo()
        try:
            repo.write(".gitignore", "data/\ndist/\nbin/\n*.db\n")
            repo.commit("init")
            findings = PC.check_gitignore(repo.root)
            self.assertEqual(findings, [])
        finally:
            repo.cleanup()

    def test_license_missing_flagged(self) -> None:
        repo = TempGitRepo()
        try:
            findings = PC.check_license(repo.root)
            self.assertTrue(any(f.rule_id == "hygiene.license_missing" for f in findings))
        finally:
            repo.cleanup()

    def test_license_mit_with_holder_year_not_flagged(self) -> None:
        repo = TempGitRepo()
        try:
            repo.write(
                "LICENSE",
                "MIT License\n\nCopyright (c) 2026 Example Holder\n\nPermission is hereby granted...\n",
            )
            findings = PC.check_license(repo.root)
            self.assertEqual(findings, [])
        finally:
            repo.cleanup()

    def test_security_md_missing_flagged(self) -> None:
        repo = TempGitRepo()
        try:
            findings = PC.check_security_md(repo.root)
            self.assertTrue(any(f.rule_id == "hygiene.security_md_missing" for f in findings))
        finally:
            repo.cleanup()

    def test_security_md_with_private_reporting_and_current_version_not_flagged(self) -> None:
        repo = TempGitRepo()
        try:
            repo.write(
                "SECURITY.md",
                "# Security\n\nUse GitHub's private vulnerability reporting flow.\n\n"
                "## Supported versions\n\nThe supported line is v0.7.x.\n",
            )
            findings = PC.check_security_md(repo.root)
            self.assertEqual(findings, [])
        finally:
            repo.cleanup()

    def test_no_tracked_binaries_flags_elf(self) -> None:
        repo = TempGitRepo()
        try:
            (repo.root / "fake-binary").write_bytes(b"\x7fELF" + b"\x00" * 32)
            repo.commit("add binary")
            findings = PC.check_no_tracked_binaries(repo.root)
            self.assertTrue(any(f.rule_id == "hygiene.tracked_binary" for f in findings))
        finally:
            repo.cleanup()

    def test_large_blob_in_history_detected(self) -> None:
        repo = TempGitRepo()
        try:
            big = repo.write("bigfile.bin", "x" * 10)  # keep test fast; check size math directly
            big.write_bytes(b"\x00" * (PC.MAX_BLOB_BYTES + 1))
            repo.commit("add oversized file")
            findings = PC.scan_history_large_and_binary_blobs(repo.root)
            self.assertTrue(any(f.rule_id == "hygiene.large_history_blob" for f in findings))
        finally:
            repo.cleanup()

    def test_no_large_blob_when_all_small(self) -> None:
        repo = TempGitRepo()
        try:
            repo.write("small.txt", "hello\n")
            repo.commit("init")
            findings = PC.scan_history_large_and_binary_blobs(repo.root)
            self.assertEqual(findings, [])
        finally:
            repo.cleanup()


class WorkingTreeMarkerRegressionTests(unittest.TestCase):
    @unittest.skipUnless(
        os.environ.get("CP_PREPUBLISH_MARKERS"),
        "requires CP_PREPUBLISH_MARKERS supplied by the local operator",
    )
    def test_all_tracked_and_untracked_nonignored_text_is_marker_clean(self) -> None:
        findings = PC.scan_working_tree_for_markers(ROOT, PC.load_markers(ROOT))
        self.assertEqual(findings, [])


class ReportRenderingTests(unittest.TestCase):
    def test_markdown_report_never_contains_raw_secret(self) -> None:
        value = _fake_aws_key()
        finding = PC.Finding(
            rule_id="secret.aws_access_key",
            description="d",
            location="tree:x.go:1",
            detail=PC._redact(value),
        )
        report = PC.render_markdown_report([finding], [])
        self.assertNotIn(value, report)
        self.assertIn("secret.aws_access_key", report)

    def test_summary_counts_fail_and_warn_separately(self) -> None:
        tree = [PC.Finding("r", "d", "tree:x:1", "detail", severity="fail")]
        history = [
            PC.Finding("r", "d", "history:a:x:1", "detail", severity="warn"),
            PC.Finding("r", "d", "history:b:x:1", "detail", severity="fail"),
        ]
        summary = PC.render_summary(tree, history)
        self.assertIn("tree findings=1 (fail=1)", summary)
        self.assertIn("history findings=2 (fail=1, warn=1)", summary)


class EntropyTests(unittest.TestCase):
    def test_low_entropy_string_below_threshold(self) -> None:
        self.assertLess(PC._shannon_entropy("aaaaaaaaaaaaaaaaaaaa"), 4.0)

    def test_high_entropy_string_above_threshold(self) -> None:
        self.assertGreaterEqual(PC._shannon_entropy(_fake_high_entropy()), 4.0)

    def test_empty_string_zero_entropy(self) -> None:
        self.assertEqual(PC._shannon_entropy(""), 0.0)


if __name__ == "__main__":
    unittest.main()
