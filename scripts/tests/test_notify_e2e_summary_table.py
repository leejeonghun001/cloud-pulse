"""Tests for scripts/notify_e2e_summary_table.py (SPEC-v0.8 §3-2's
notify-e2e.yml job-summary table)."""

from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path
from types import ModuleType

ROOT = Path(__file__).resolve().parents[2]


def load_script(filename: str, module_name: str) -> ModuleType:
    """Loads a script filename as an isolated Python module."""
    spec = importlib.util.spec_from_file_location(module_name, ROOT / "scripts" / filename)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"could not load {filename}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    spec.loader.exec_module(module)
    return module


TABLE = load_script("notify_e2e_summary_table.py", "cloud_pulse_notify_e2e_summary_table_test")


class RenderTableTests(unittest.TestCase):
    def test_empty_results_shows_placeholder_row(self) -> None:
        table = TABLE.render_table({"results": []})
        self.assertIn("_(no results in report)_", table)

    def test_verified_result_shows_yes_for_attachment_and_cleanup(self) -> None:
        table = TABLE.render_table({"results": [{"platform": "discord", "status": "verified", "attachment_verified": True, "cleaned": True}]})
        self.assertIn("| discord | verified | yes | yes |  |", table)

    def test_skipped_result_shows_not_applicable(self) -> None:
        table = TABLE.render_table({"results": [{"platform": "telegram", "status": "skipped"}]})
        self.assertIn("| telegram | skipped | n/a | n/a |  |", table)

    def test_failed_result_shows_diagnosis_code(self) -> None:
        table = TABLE.render_table(
            {"results": [{"platform": "whatsapp", "status": "failed", "diagnosis": {"code": "whatsapp_token_invalid"}}]}
        )
        self.assertIn("whatsapp_token_invalid", table)

    def test_cleaned_false_shows_no(self) -> None:
        table = TABLE.render_table({"results": [{"platform": "webhook", "status": "accepted", "cleaned": False}]})
        self.assertIn("| webhook | accepted | n/a | no |  |", table)

    def test_never_raises_on_missing_optional_fields(self) -> None:
        table = TABLE.render_table({"results": [{"platform": "discord", "status": "accepted"}]})
        self.assertIn("discord", table)


class MainEntryPointTests(unittest.TestCase):
    def test_missing_report_file_prints_placeholder_and_exits_zero(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            missing = Path(tmp) / "does-not-exist.json"
            rc = TABLE.main([str(missing)])
            self.assertEqual(rc, 0)

    def test_malformed_json_prints_placeholder_and_exits_zero(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            bad = Path(tmp) / "bad.json"
            bad.write_text("not json", encoding="utf-8")
            rc = TABLE.main([str(bad)])
            self.assertEqual(rc, 0)

    def test_no_arguments_exits_nonzero(self) -> None:
        rc = TABLE.main([])
        self.assertEqual(rc, 2)

    def test_valid_report_exits_zero(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "report.json"
            path.write_text(json.dumps({"results": [{"platform": "discord", "status": "verified"}]}), encoding="utf-8")
            rc = TABLE.main([str(path)])
            self.assertEqual(rc, 0)


if __name__ == "__main__":
    unittest.main()
