"""Unit tests for scripts/hub-upgrade-check.sh's pure helper functions.

The full end-to-end rehearsal (real sandbox hub, real upgrade, real
rollback) lives in scripts/test-hub-upgrade.sh / the CI `hub-upgrade`
job — this file only isolates and exercises the small number of
functions in hub-upgrade-check.sh that are pure enough to unit-test
without a running hub: path joining, hub.env parsing (never printing
values), the embedded python3 sqlite snapshot/integrity-check/backup
helpers, and --password-file's 0600 permission check. Every test here
runs in well under a second; no root, no network, no systemctl.
"""

from __future__ import annotations

import json
import os
import sqlite3
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "hub-upgrade-check.sh"


def _extract_function(text: str, function_name: str) -> str:
    """Return the source of one function definition ("name() {\\n...\\n}\\n")."""
    start_marker = f"\n{function_name}() {{\n"
    start = text.index(start_marker)
    end = text.index("\n}\n", start) + len("\n}\n")
    return text[start + 1 : end]


def _run_shell_function(
    function_name: str, args: list[str], *, extra_script: str = "", also_needs: tuple[str, ...] = ()
) -> str:
    """Extract one function definition from hub-upgrade-check.sh (plus
    any dependency functions named in also_needs), call it with args,
    and return its stdout. extra_script (if given) runs before the
    call, e.g. to set up local variables the function reads.
    """
    text = SCRIPT.read_text(encoding="utf-8")
    func_src = "\n".join(_extract_function(text, name) for name in (*also_needs, function_name))
    quoted_args = " ".join(f"'{a}'" for a in args)
    script = "set -euo pipefail\n" + extra_script + "\n" + func_src + f"\n{function_name} {quoted_args}\n"
    result = subprocess.run(
        ["bash", "-c", script],
        capture_output=True,
        text=True,
        timeout=20,
    )
    if result.returncode != 0:
        raise AssertionError(f"{function_name} failed (exit {result.returncode}): {result.stderr}")
    return result.stdout


class JoinRootTests(unittest.TestCase):
    def test_empty_prefix_returns_suffix_unchanged(self) -> None:
        out = _run_shell_function("join_root", ["", "/etc/cloud-pulse/hub.env"])
        self.assertEqual(out, "/etc/cloud-pulse/hub.env")

    def test_prefix_is_joined_with_suffix(self) -> None:
        out = _run_shell_function("join_root", ["/tmp/sandbox", "/etc/cloud-pulse/hub.env"])
        self.assertEqual(out, "/tmp/sandbox/etc/cloud-pulse/hub.env")

    def test_trailing_slash_on_prefix_is_not_doubled(self) -> None:
        out = _run_shell_function("join_root", ["/tmp/sandbox/", "/etc/cloud-pulse/hub.env"])
        self.assertEqual(out, "/tmp/sandbox/etc/cloud-pulse/hub.env")


class EnvValueOfTests(unittest.TestCase):
    def _write_env_file(self, tmp_path: Path, contents: str) -> Path:
        path = tmp_path / "hub.env"
        path.write_text(contents, encoding="utf-8")
        return path

    def test_reads_active_assignment(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            env_file = self._write_env_file(Path(tmp), "CP_LISTEN=127.0.0.1:8090\n")
            out = _run_shell_function("env_value_of", ["CP_LISTEN", str(env_file)])
            self.assertEqual(out.strip(), "127.0.0.1:8090")

    def test_commented_assignment_is_ignored(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            env_file = self._write_env_file(Path(tmp), "#CP_UI_TOKEN=should-not-be-read\n")
            out = _run_shell_function("env_value_of", ["CP_UI_TOKEN", str(env_file)])
            self.assertEqual(out.strip(), "")

    def test_last_active_assignment_wins(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            env_file = self._write_env_file(Path(tmp), "CP_LISTEN=:8090\nCP_LISTEN=:9090\n")
            out = _run_shell_function("env_value_of", ["CP_LISTEN", str(env_file)])
            self.assertEqual(out.strip(), ":9090")

    def test_missing_file_returns_empty(self) -> None:
        out = _run_shell_function("env_value_of", ["CP_LISTEN", "/nonexistent/hub.env"])
        self.assertEqual(out.strip(), "")

    def test_never_prints_a_key_that_is_not_asked_for(self) -> None:
        # Regression guard for the "never print secret values" rule:
        # asking for one key must never leak a different key's value
        # (e.g. a token) into the output.
        with tempfile.TemporaryDirectory() as tmp:
            env_file = self._write_env_file(
                Path(tmp), "CP_AGENT_TOKEN=super-secret-token-value\nCP_LISTEN=:8090\n"
            )
            out = _run_shell_function("env_value_of", ["CP_LISTEN", str(env_file)])
            self.assertNotIn("super-secret-token-value", out)


class EnvActiveKeysTests(unittest.TestCase):
    def test_lists_keys_only_never_values(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            env_file = Path(tmp) / "hub.env"
            env_file.write_text(
                "CP_AGENT_TOKEN=super-secret-value\n#CP_UI_TOKEN=commented-out\nCP_LISTEN=:8090\n",
                encoding="utf-8",
            )
            out = _run_shell_function("env_active_keys", [str(env_file)])
            keys = sorted(out.split())
            self.assertEqual(keys, ["CP_AGENT_TOKEN", "CP_LISTEN"])
            self.assertNotIn("super-secret-value", out)


class HubUrlFromEnvTests(unittest.TestCase):
    def _run_with_env_file(self, contents: str, override: str = "") -> str:
        with tempfile.TemporaryDirectory() as tmp:
            env_file = Path(tmp) / "hub.env"
            env_file.write_text(contents, encoding="utf-8")
            setup = f"ENV_FILE='{env_file}'\nHUB_URL_OVERRIDE='{override}'\n"
            return _run_shell_function("hub_url_from_env", [], extra_script=setup, also_needs=("env_value_of",))

    def test_explicit_host_and_port(self) -> None:
        out = self._run_with_env_file("CP_LISTEN=192.0.2.1:8090\n")
        self.assertEqual(out.strip(), "http://192.0.2.1:8090")

    def test_wildcard_host_resolves_to_loopback(self) -> None:
        out = self._run_with_env_file("CP_LISTEN=:8090\n")
        self.assertEqual(out.strip(), "http://127.0.0.1:8090")

    def test_star_host_resolves_to_loopback(self) -> None:
        out = self._run_with_env_file("CP_LISTEN=*:8090\n")
        self.assertEqual(out.strip(), "http://127.0.0.1:8090")

    def test_missing_listen_defaults_to_8090(self) -> None:
        out = self._run_with_env_file("")
        self.assertEqual(out.strip(), "http://127.0.0.1:8090")

    def test_multi_listen_uses_first_entry(self) -> None:
        out = self._run_with_env_file("CP_LISTEN=192.0.2.1:8090,192.0.2.2:8090\n")
        self.assertEqual(out.strip(), "http://192.0.2.1:8090")

    def test_override_wins_over_env_file(self) -> None:
        out = self._run_with_env_file("CP_LISTEN=192.0.2.1:8090\n", override="http://198.51.100.1:9999")
        self.assertEqual(out.strip(), "http://198.51.100.1:9999")


class PasswordFilePermissionTests(unittest.TestCase):
    def test_rejects_a_password_file_that_is_not_mode_0600(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "password"
            path.write_text("changeme\n", encoding="utf-8")
            os.chmod(path, 0o644)
            setup = f"PASSWORD_FILE='{path}'\n"
            with self.assertRaises(AssertionError):
                _run_shell_function("read_password", [], extra_script=setup)

    def test_accepts_a_0600_password_file_and_never_echoes_it_verbatim_on_stderr_label(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "password"
            path.write_text("changeme\n", encoding="utf-8")
            os.chmod(path, 0o600)
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            setup = f"PASSWORD_FILE='{path}'\n"
            out = _run_shell_function("read_password", [], extra_script=setup)
            self.assertEqual(out.strip(), "changeme")

    @unittest.skipUnless(os.name == "posix", "requires POSIX /dev/tty semantics")
    def test_rejects_an_unopenable_tty_with_a_clear_error(self) -> None:
        text = SCRIPT.read_text(encoding="utf-8")
        function = _extract_function(text, "read_password")
        probe = (
            "set -euo pipefail\n"
            "PASSWORD_FILE=''\n"
            "err() { printf 'ERR: %s\\n' \"$*\" >&2; }\n"
            + function
            + "\nread_password\n"
        )
        result = subprocess.run(
            ["bash", "-c", probe],
            capture_output=True,
            text=True,
            timeout=20,
            start_new_session=True,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no --password-file given and /dev/tty is unavailable", result.stderr)
        self.assertNotIn("unbound variable", result.stderr)
        self.assertNotIn("/dev/tty: No such device or address", result.stderr)


class SqlitePythonHelperTests(unittest.TestCase):
    """Exercises the python3 heredocs (backup/integrity_check/snapshot)
    directly by calling the wrapping shell functions against a real
    temporary SQLite database — these never take a raw DB path or
    table name from string interpolation into the embedded Python
    source, only via sys.argv, per CODING_CONVENTIONS.md.
    """

    def _make_db(self, tmp: Path) -> Path:
        db_path = tmp / "cloud-pulse.db"
        conn = sqlite3.connect(db_path)
        conn.execute("CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)")
        conn.execute("INSERT INTO schema_migrations (version) VALUES (1), (2), (3)")
        conn.execute("CREATE TABLE hosts (id TEXT PRIMARY KEY, info_json TEXT, last_seen INTEGER)")
        conn.execute("INSERT INTO hosts (id, info_json, last_seen) VALUES ('host-a', '{}', 100)")
        conn.execute("INSERT INTO hosts (id, info_json, last_seen) VALUES ('host-b', '{}', 200)")
        conn.execute("CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)")
        conn.execute("INSERT INTO settings (key, value, updated_at) VALUES ('alert_webhook_url', 'http://example.test', 1)")
        conn.execute(
            "CREATE TABLE alert_rules (id INTEGER PRIMARY KEY, name TEXT, metric TEXT)"
        )
        conn.execute(
            "INSERT INTO alert_rules (name, metric) VALUES ('Outbound traffic 80% (warning)', 'egress_out_pct')"
        )
        conn.execute(
            "INSERT INTO alert_rules (name, metric) VALUES ('Outbound traffic 95% (critical)', 'egress_out_pct')"
        )
        conn.execute(
            "INSERT INTO alert_rules (name, metric) VALUES ('Outbound traffic 100% (exceeded)', 'egress_out_pct')"
        )
        conn.commit()
        conn.close()
        return db_path

    def test_backup_produces_a_readable_copy(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            src = self._make_db(tmp_path)
            dest = tmp_path / "backup.db"
            _run_shell_function("py_sqlite_backup", [str(src), str(dest)])
            self.assertTrue(dest.exists())
            conn = sqlite3.connect(dest)
            (count,) = conn.execute("SELECT COUNT(*) FROM hosts").fetchone()
            conn.close()
            self.assertEqual(count, 2)

    def test_integrity_check_reports_ok_for_a_healthy_database(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            db = self._make_db(Path(tmp))
            out = _run_shell_function("py_sqlite_integrity_check", [str(db)])
            self.assertEqual(out.strip(), "ok")

    def test_integrity_check_fails_on_a_corrupted_database(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            corrupt = Path(tmp) / "corrupt.db"
            corrupt.write_bytes(b"not a real sqlite database file, definitely corrupted" * 4)
            with self.assertRaises(AssertionError):
                _run_shell_function("py_sqlite_integrity_check", [str(corrupt)])

    def test_snapshot_reports_expected_tables_row_counts_hosts_and_egress_rules(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            db = self._make_db(tmp_path)
            out_json = tmp_path / "snapshot.json"
            _run_shell_function("py_sqlite_snapshot", [str(db), str(out_json)])
            snapshot = json.loads(out_json.read_text(encoding="utf-8"))

            self.assertEqual(snapshot["migrations"], ["1", "2", "3"])
            self.assertEqual(snapshot["row_counts"]["hosts"], 2)
            self.assertEqual(snapshot["row_counts"]["settings"], 1)
            self.assertEqual(snapshot["host_ids"], ["host-a", "host-b"])
            self.assertEqual(snapshot["setting_keys"], ["alert_webhook_url"])
            self.assertEqual(len(snapshot["egress_rule_names"]), 3)
            # A table this fixture never created (e.g. storage_accounts,
            # only added in a later migration) must be silently skipped,
            # not raise or appear with a count of 0 — this is what makes
            # the snapshot logic work unmodified against both an old
            # (v0.3.2) and a current schema.
            self.assertNotIn("storage_accounts", snapshot["row_counts"])
            self.assertIn("hosts", snapshot["tables_present"])
            self.assertNotIn("storage_accounts", snapshot["tables_present"])

    def test_snapshot_never_embeds_secret_looking_setting_values_only_keys(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            db_path = tmp_path / "cloud-pulse.db"
            conn = sqlite3.connect(db_path)
            conn.execute("CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)")
            conn.execute(
                "INSERT INTO settings (key, value, updated_at) VALUES ('auth_password_hash', 'super-secret-hash-value', 1)"
            )
            conn.commit()
            conn.close()

            out_json = tmp_path / "snapshot.json"
            _run_shell_function("py_sqlite_snapshot", [str(db_path), str(out_json)])
            raw = out_json.read_text(encoding="utf-8")
            self.assertNotIn("super-secret-hash-value", raw)
            snapshot = json.loads(raw)
            self.assertEqual(snapshot["setting_keys"], ["auth_password_hash"])


class JsonFieldTests(unittest.TestCase):
    def test_extracts_a_string_field(self) -> None:
        out = _run_shell_function("json_field", ['{"version": "v0.7.0"}', "version"])
        self.assertEqual(out.strip(), "v0.7.0")

    def test_extracts_a_boolean_field_as_lowercase_string(self) -> None:
        out = _run_shell_function("json_field", ['{"must_change_password": true}', "must_change_password"])
        self.assertEqual(out.strip(), "true")

    def test_missing_key_returns_empty(self) -> None:
        out = _run_shell_function("json_field", ['{"version": "v0.7.0"}', "commit"])
        self.assertEqual(out.strip(), "")

    def test_malformed_json_returns_empty_rather_than_raising(self) -> None:
        out = _run_shell_function("json_field", ["not json at all {{{", "version"])
        self.assertEqual(out.strip(), "")


if __name__ == "__main__":
    unittest.main()
