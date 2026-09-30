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

    def test_build_single_sample_is_deterministic_given_same_rng_state(self) -> None:
        host = DEMO.DemoHost(
            host_id="test-host",
            hostname="test-host",
            provider="other",
            os="linux",
            arch="amd64",
            cpu_model="test",
            cpu_cores=2,
            egress_limit_bytes=DEMO.GIB,
            egress_level="ok",
            seed=7,
        )
        rng_a = DEMO.random.Random(host.seed)
        rng_b = DEMO.random.Random(host.seed)
        sample_a = DEMO.build_single_sample(host, ts=1_800_000_500, t_offset=500, rng=rng_a)
        sample_b = DEMO.build_single_sample(host, ts=1_800_000_500, t_offset=500, rng=rng_b)
        self.assertEqual(sample_a, sample_b)
        self.assertEqual(sample_a["ts"], 1_800_000_500)
        self.assertGreaterEqual(sample_a["cpu_percent"], 0.0)
        self.assertLessEqual(sample_a["cpu_percent"], 100.0)

    def test_run_live_keepalive_skips_down_hosts(self) -> None:
        up_hosts = [h for h in DEMO.DEMO_HOSTS if not h.down]
        down_hosts = [h for h in DEMO.DEMO_HOSTS if h.down]
        self.assertGreater(len(down_hosts), 0, "fixture expects at least one intentionally-offline demo host")

        posted_host_ids: list[str] = []

        def fake_post_report(hub_url, token, host, samples, timeout=10.0):  # noqa: ANN001
            posted_host_ids.append(host.host_id)

        # Drive exactly one loop iteration deterministically: monotonic()
        # returns an increasing fake clock that crosses the deadline only
        # AFTER the loop body (the per-host post_report calls) has run
        # once, and time.sleep is a no-op so the test doesn't actually
        # wait on wall-clock time.
        fake_clock = {"value": 1000.0}

        def fake_monotonic():
            return fake_clock["value"]

        def fake_sleep(seconds):
            fake_clock["value"] += 3600.0  # jump far past any deadline after one tick

        original_post_report = DEMO.post_report
        original_sleep = DEMO.time.sleep
        original_monotonic = DEMO.time.monotonic
        try:
            DEMO.post_report = fake_post_report
            DEMO.time.sleep = fake_sleep
            DEMO.time.monotonic = fake_monotonic
            DEMO.run_live_keepalive("http://127.0.0.1:0", "test-token", duration_seconds=5.0, interval_seconds=5.0)
        finally:
            DEMO.post_report = original_post_report
            DEMO.time.sleep = original_sleep
            DEMO.time.monotonic = original_monotonic

        for host in up_hosts:
            self.assertEqual(posted_host_ids.count(host.host_id), 1)
        for host in down_hosts:
            self.assertNotIn(host.host_id, posted_host_ids)


if __name__ == "__main__":
    unittest.main()
