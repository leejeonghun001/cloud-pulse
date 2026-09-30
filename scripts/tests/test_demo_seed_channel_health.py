"""Tests for scripts/demo_seed_v05.py's ChannelHealthSeeder (SPEC-v0.8
§3-2 demo data: synthetic-test history + schedules for the Health tab).

Builds a real SQLite database via the hub binary's own migrations
(invoking `go run ./cmd/hub gen-token`-style bootstrap is overkill for
this; instead this applies the tracked migration SQL files directly,
mirroring internal/storage's own test pattern) so the seeder's SQL
runs against the real schema rather than a hand-rolled approximation.
"""

from __future__ import annotations

import importlib.util
import sqlite3
import sys
import tempfile
import time
import unittest
from pathlib import Path
from types import ModuleType

ROOT = Path(__file__).resolve().parents[2]
MIGRATIONS_DIR = ROOT / "internal" / "storage" / "migrations"


def load_script(filename: str, module_name: str) -> ModuleType:
    spec = importlib.util.spec_from_file_location(module_name, ROOT / "scripts" / filename)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"could not load {filename}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    spec.loader.exec_module(module)
    return module


SEED = load_script("demo_seed_v05.py", "cloud_pulse_demo_seed_v05_test")


def _build_migrated_db(path: Path) -> None:
    """Applies every tracked 000N_*.sql migration file directly, in
    order, to a fresh SQLite file at path — enough for this test's
    purposes (it only needs notify_channels + the SPEC-v0.8 §3-2 tables
    to exist and be insertable-into), without depending on a Go build
    or the hub binary being present."""
    conn = sqlite3.connect(path)
    try:
        conn.execute("PRAGMA foreign_keys = ON")
        for migration in sorted(MIGRATIONS_DIR.glob("*.sql")):
            conn.executescript(migration.read_text(encoding="utf-8"))
        conn.commit()
    finally:
        conn.close()


def _insert_channel(conn: sqlite3.Connection, name: str) -> int:
    now = int(time.time())
    cur = conn.execute(
        "INSERT INTO notify_channels (name, type, enabled, config, created_at, updated_at) VALUES (?, 'webhook', 1, '{}', ?, ?)",
        (name, now, now),
    )
    return cur.lastrowid


class ChannelHealthSeederTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.db_path = Path(self.tmp.name) / "test.db"
        _build_migrated_db(self.db_path)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def _channel_ids(self) -> dict[str, int]:
        conn = sqlite3.connect(self.db_path)
        try:
            healthy_id = _insert_channel(conn, "Discord #ops")
            failing_id = _insert_channel(conn, "Failing webhook (demo)")
            conn.commit()
        finally:
            conn.close()
        return {"discord-ops": healthy_id, "webhook-failing-demo": failing_id}

    def test_seeds_schedule_for_both_demo_channels(self) -> None:
        channel_ids = self._channel_ids()
        SEED.ChannelHealthSeeder(self.db_path).seed(channel_ids)

        conn = sqlite3.connect(self.db_path)
        try:
            rows = conn.execute("SELECT channel_id, enabled FROM channel_health_settings ORDER BY channel_id").fetchall()
        finally:
            conn.close()
        self.assertEqual(len(rows), 2)
        self.assertTrue(all(enabled == 1 for _, enabled in rows))

    def test_healthy_channel_gets_twelve_verified_results(self) -> None:
        channel_ids = self._channel_ids()
        SEED.ChannelHealthSeeder(self.db_path).seed(channel_ids)

        conn = sqlite3.connect(self.db_path)
        try:
            rows = conn.execute(
                "SELECT outcome FROM channel_synthetic_results WHERE channel_id = ? ORDER BY started_at",
                (channel_ids["discord-ops"],),
            ).fetchall()
        finally:
            conn.close()
        self.assertEqual(len(rows), 12)
        self.assertTrue(all(outcome == "verified" for (outcome,) in rows))

    def test_failing_channel_ends_with_a_failed_result_and_diagnosis(self) -> None:
        channel_ids = self._channel_ids()
        SEED.ChannelHealthSeeder(self.db_path).seed(channel_ids)

        conn = sqlite3.connect(self.db_path)
        try:
            rows = conn.execute(
                "SELECT outcome, diagnosis_code FROM channel_synthetic_results WHERE channel_id = ? ORDER BY started_at",
                (channel_ids["webhook-failing-demo"],),
            ).fetchall()
        finally:
            conn.close()
        self.assertEqual(len(rows), 12)
        self.assertEqual(rows[-1][0], "failed")
        self.assertTrue(rows[-1][1])  # non-empty diagnosis code
        # Not every run failed -- the strip should show a mix, not a
        # wall of red, per the seeder's own doc comment.
        self.assertTrue(any(outcome == "verified" for outcome, _ in rows))

    def test_failing_channel_gets_an_open_unnotified_incident(self) -> None:
        channel_ids = self._channel_ids()
        SEED.ChannelHealthSeeder(self.db_path).seed(channel_ids)

        conn = sqlite3.connect(self.db_path)
        try:
            row = conn.execute(
                "SELECT notified_at, recovered_at FROM channel_failure_incidents WHERE channel_id = ?",
                (channel_ids["webhook-failing-demo"],),
            ).fetchone()
        finally:
            conn.close()
        self.assertIsNotNone(row)
        notified_at, recovered_at = row
        self.assertEqual(notified_at, 0)
        self.assertEqual(recovered_at, 0)

    def test_healthy_channel_has_no_failure_incident(self) -> None:
        channel_ids = self._channel_ids()
        SEED.ChannelHealthSeeder(self.db_path).seed(channel_ids)

        conn = sqlite3.connect(self.db_path)
        try:
            row = conn.execute(
                "SELECT 1 FROM channel_failure_incidents WHERE channel_id = ?",
                (channel_ids["discord-ops"],),
            ).fetchone()
        finally:
            conn.close()
        self.assertIsNone(row)

    def test_seed_is_idempotent_on_rerun(self) -> None:
        channel_ids = self._channel_ids()
        seeder = SEED.ChannelHealthSeeder(self.db_path)
        seeder.seed(channel_ids)
        seeder.seed(channel_ids)  # must not raise or duplicate schedule rows

        conn = sqlite3.connect(self.db_path)
        try:
            count = conn.execute("SELECT count(*) FROM channel_health_settings").fetchone()[0]
        finally:
            conn.close()
        self.assertEqual(count, 2, "ON CONFLICT upsert must not create duplicate schedule rows")

    def test_missing_channel_keys_are_silently_skipped(self) -> None:
        # An empty/partial map (e.g. the real channels weren't created
        # with the expected keys) must not raise.
        SEED.ChannelHealthSeeder(self.db_path).seed({})


if __name__ == "__main__":
    unittest.main()
