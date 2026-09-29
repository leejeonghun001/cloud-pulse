"""Unit tests for the pure demo-seed helpers (stdlib unittest only)."""

from __future__ import annotations

import importlib.util
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


DEMO = load_script("demo-seed.py", "cloud_pulse_demo_seed_test")


class DemoSeedTests(unittest.TestCase):
    """Exercise deterministic data-shaping helpers without HTTP or SQLite."""

    def test_egress_level_target_fraction(self) -> None:
        self.assertEqual(DEMO.egress_level_target_fraction("ok"), 0.45)
        self.assertEqual(DEMO.egress_level_target_fraction("warning"), 0.85)
        self.assertEqual(DEMO.egress_level_target_fraction("critical"), 0.97)
        self.assertEqual(DEMO.egress_level_target_fraction("exceeded"), 1.12)

    def test_ingress_limit_prefers_hub_override(self) -> None:
        host = DEMO.DemoHost(
            host_id="test-host",
            hostname="test-host",
            provider="other",
            os="linux",
            arch="amd64",
            cpu_model="test",
            cpu_cores=1,
            egress_limit_bytes=0,
            egress_level="ok",
            hub_ingress_override_gib=3.5,
            seed=1,
        )
        self.assertEqual(DEMO.ingress_limit_bytes_for(host), int(3.5 * DEMO.GIB))

    def test_build_samples_is_deterministic_and_timestamp_aligned(self) -> None:
        host = DEMO.DemoHost(
            host_id="test-host",
            hostname="test-host",
            provider="other",
            os="linux",
            arch="amd64",
            cpu_model="test",
            cpu_cores=2,
            egress_limit_bytes=DEMO.GIB,
            egress_level="warning",
            seed=123,
        )
        now = 1_800_000_000
        samples = DEMO.build_samples(host, now)
        self.assertEqual(samples, DEMO.build_samples(host, now))
        self.assertEqual(len(samples), DEMO.HISTORY_SECONDS // DEMO.SAMPLE_INTERVAL_SECONDS)
        self.assertEqual(samples[0]["ts"], now - DEMO.HISTORY_SECONDS)
        self.assertTrue(all(sample["ts"] % DEMO.SAMPLE_INTERVAL_SECONDS == 0 for sample in samples))


if __name__ == "__main__":
    unittest.main()
