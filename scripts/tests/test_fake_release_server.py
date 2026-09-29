"""Unit tests for scripts/fake_release_server.py using only unittest."""

from __future__ import annotations

import importlib.util
import sys
import tempfile
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


SERVER = load_script("fake_release_server.py", "cloud_pulse_fake_release_server_test")


class FakeReleaseServerTests(unittest.TestCase):
    """Verify only expected release assets resolve below the configured root."""

    def test_resolve_asset_path_accepts_normal_asset(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            expected = root / "v0.5.0" / "checksums.txt"
            expected.parent.mkdir()
            expected.write_text("test", encoding="utf-8")
            self.assertEqual(SERVER.resolve_asset_path(root, "v0.5.0", "checksums.txt"), expected)

    def test_resolve_asset_path_rejects_traversal(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.assertIsNone(SERVER.resolve_asset_path(root, "../outside", "asset"))
            self.assertIsNone(SERVER.resolve_asset_path(root, "v0.5.0", "../outside"))

    def test_port_number_bounds(self) -> None:
        self.assertEqual(SERVER.port_number("18090"), 18090)
        for value in ("0", "65536", "not-a-port"):
            with self.subTest(value=value), self.assertRaises(Exception):
                SERVER.port_number(value)


if __name__ == "__main__":
    unittest.main()
