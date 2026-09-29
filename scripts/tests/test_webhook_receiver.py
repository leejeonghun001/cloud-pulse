"""Unit tests for scripts/webhook_receiver.py using only unittest."""

from __future__ import annotations

import importlib.util
import json
import sys
import unittest
from pathlib import Path
from types import ModuleType

ROOT = Path(__file__).resolve().parents[2]


def load_script(filename: str, module_name: str) -> ModuleType:
    """Loads a hyphenated script filename as an isolated Python module."""
    spec = importlib.util.spec_from_file_location(module_name, ROOT / "scripts" / filename)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"could not load {filename}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    spec.loader.exec_module(module)
    return module


RECEIVER = load_script("webhook_receiver.py", "cloud_pulse_webhook_receiver_test")


class WebhookReceiverTests(unittest.TestCase):
    """Verify request validation and append-only logging without a socket."""

    def test_append_json_line_preserves_a_single_json_record(self) -> None:
        with self.subTest("valid JSON"):
            with self._temp_log() as path:
                RECEIVER.append_json_line(path, b'{"title":"test"}')
                self.assertEqual(path.read_text(encoding="utf-8"), '{"title":"test"}\n')
                self.assertEqual(json.loads(path.read_text(encoding="utf-8")), {"title": "test"})

    def test_append_json_line_rejects_invalid_utf8_or_json(self) -> None:
        with self._temp_log() as path:
            for raw in (b"{", b"\xff"):
                with self.subTest(raw=raw), self.assertRaises(ValueError):
                    RECEIVER.append_json_line(path, raw)
            self.assertFalse(path.exists())

    def _temp_log(self):  # type: ignore[no-untyped-def]
        """Returns a temporary-directory context yielding a log path."""
        return _TempLog()


class _TempLog:
    """Small context manager avoiding non-stdlib test dependencies."""

    def __enter__(self) -> Path:
        import tempfile

        self._tmp = tempfile.TemporaryDirectory()
        return Path(self._tmp.name) / "received.jsonl"

    def __exit__(self, exc_type, exc_value, traceback) -> None:  # type: ignore[no-untyped-def]
        self._tmp.cleanup()


if __name__ == "__main__":
    unittest.main()
