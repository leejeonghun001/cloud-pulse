#!/usr/bin/env python3
"""demo-seed.py — DEMO/DEV ONLY seed data generator for cloud-pulse.

Not part of the production hub or agent. Do not run against a real,
in-use hub — it posts fake AgentReports and (optionally) writes rows
directly into the hub's SQLite bucket_stats table for visual/manual
testing of the dashboard.

Two independent modes, usable together or separately:

  --hub URL --token TOKEN [--ui-token UI_TOKEN]
      POSTs AgentReport payloads for ~6 fake hosts (mixed aws/oci/other
      providers, one host reported as "down" by only posting samples
      with an old last-seen timestamp) to <URL>/api/v1/agent/report.
      Each host gets ~2 hours of 15s-interval samples with sine+noise
      curves so charts look realistic, a tx-byte curve tuned per host to
      land in a specific outbound egress level (ok/warning/critical/
      exceeded) against its egress_limit_bytes, and an rx-byte curve
      tuned per host to land in a specific inbound level against
      whatever inbound limit ends up effective for it. When --ui-token
      is also given (must match the hub's CP_UI_TOKEN), a few hosts also
      get hub-side outbound/inbound limit overrides applied via
      PUT /api/v1/hosts/{id}/limits (an admin endpoint), so the settings
      page's limits editor and "hub override" badges have real data.

      Hosts also report a mix of agent_version values (see DemoHost.agent_version
      and DEMO_HOSTS) so the dashboard's update banner/badge/panel
      (SPEC-v0.3 D-U6) has real cases to render: a current release, an
      older but self-update-capable v0.3.x build, and a legacy
      pre-v0.3.0 build (no built-in updater — installer-one-liner path).

  --db PATH
      Inserts demo rows directly into the bucket_stats table (see
      internal/storage/migrations/0001_init.sql for the exact schema)
      for 2 S3 + 2 R2 demo buckets, each with ~24h of hourly history.

Uses only the Python 3 standard library — no pip installs required.
"""

from __future__ import annotations

import argparse
import json
import math
import random
import sqlite3
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from dataclasses import dataclass, field

DEMO_BANNER = (
    "cloud-pulse demo-seed.py: DEMO/DEV DATA ONLY — do not point this at a "
    "production hub you care about."
)

# ---------------------------------------------------------------------------
# Host fleet definition
# ---------------------------------------------------------------------------

GIB = 1 << 30
TIB = 1 << 40


@dataclass
class DemoHost:
    host_id: str
    hostname: str
    provider: str  # "aws" | "oci" | "other"
    os: str
    arch: str
    cpu_model: str
    cpu_cores: int
    egress_limit_bytes: int
    egress_level: str  # "ok" | "warning" | "critical" | "exceeded"
    down: bool = False
    cpu_base: float = 20.0
    cpu_amp: float = 15.0
    mem_total: int = 8 * GIB
    disk_total: int = 100 * GIB
    seed: int = field(default_factory=lambda: random.randint(0, 1_000_000))
    # Hub-side limit overrides exercised via PUT /api/v1/hosts/{id}/limits
    # when --ui-token is given (see apply_hub_limit_overrides). None means
    # "don't touch this host's overrides" (leave whatever's already
    # stored, or unset if never seeded before).
    hub_egress_override_gib: float | None = None
    hub_ingress_override_gib: float | None = None
    # ingress_level drives how large the inbound (rx) byte curve is
    # relative to whatever inbound limit ends up effective for this
    # host (agent-reported default is always unlimited, so this only
    # visibly matters once a hub ingress override is applied above).
    ingress_level: str = "ok"
    # agent_version is reported as HostInfo.agent_version, driving the
    # dashboard's D-U6 per-host "agent vX" text and update badge/panel.
    # Mixed across the fleet on purpose (see DEMO_HOSTS below) so the
    # overview/host-detail/settings screenshots show all three cases:
    # a current build (no badge), an older v0.3.x build (self-update
    # badge/command), and a legacy pre-v0.3.0 build (installer-one-liner
    # explanation).
    agent_version: str = "v0.3.0"


DEMO_HOSTS = [
    DemoHost(
        host_id="demo-aws-web-01",
        hostname="demo-aws-web-01",
        provider="aws",
        os="linux",
        arch="amd64",
        cpu_model="AWS Graviton3 (demo)",
        cpu_cores=4,
        egress_limit_bytes=100 * GIB,
        egress_level="ok",
        cpu_base=18,
        cpu_amp=12,
        mem_total=8 * GIB,
        disk_total=80 * GIB,
        # Hub override tightens the agent's 100 GiB AWS default down to
        # 50 GiB outbound, and adds a 20 GiB inbound limit — demonstrates
        # both directions' hub-override badge and bar at once.
        hub_egress_override_gib=50,
        hub_ingress_override_gib=20,
        ingress_level="warning",
        # Current release: no update badge/panel.
        agent_version="v0.3.1",
    ),
    DemoHost(
        host_id="demo-aws-db-01",
        hostname="demo-aws-db-01",
        provider="aws",
        os="linux",
        arch="amd64",
        cpu_model="Intel Xeon Platinum (demo)",
        cpu_cores=8,
        egress_limit_bytes=100 * GIB,
        egress_level="warning",
        cpu_base=35,
        cpu_amp=20,
        mem_total=32 * GIB,
        disk_total=500 * GIB,
        ingress_level="ok",
        # Older v0.3.x build (has the built-in updater, self_update=true):
        # shows the amber badge + "sudo cloud-pulse-agent update" panel.
        agent_version="v0.3.0",
    ),
    DemoHost(
        host_id="demo-oci-app-01",
        hostname="demo-oci-app-01",
        provider="oci",
        os="linux",
        arch="arm64",
        cpu_model="Ampere Altra (demo)",
        cpu_cores=4,
        egress_limit_bytes=10 * TIB,
        egress_level="critical",
        cpu_base=45,
        cpu_amp=25,
        mem_total=16 * GIB,
        disk_total=200 * GIB,
        # Explicitly-unlimited hub override (0 GiB = unlimited), overriding
        # OCI's 10 TiB provider default — exercises the "0 means
        # unlimited" override path end to end.
        hub_egress_override_gib=0,
        ingress_level="ok",
        # Legacy pre-v0.3.0 build (predates the built-in updater,
        # self_update=false): shows the badge + installer-one-liner
        # command + the legacy explanation panel. This is the host used
        # for the "host detail of a legacy agent" screenshot.
        agent_version="v0.2.0",
    ),
    DemoHost(
        host_id="demo-other-nas-01",
        hostname="demo-other-nas-01",
        provider="other",
        os="linux",
        arch="arm64",
        cpu_model="Raspberry Pi 5 (demo)",
        cpu_cores=4,
        egress_limit_bytes=0,
        egress_level="ok",
        cpu_base=10,
        cpu_amp=8,
        mem_total=4 * GIB,
        disk_total=2 * TIB,
        ingress_level="ok",
    ),
    DemoHost(
        host_id="demo-aws-cache-01",
        hostname="demo-aws-cache-01",
        provider="aws",
        os="linux",
        arch="amd64",
        cpu_model="AWS Graviton3 (demo)",
        cpu_cores=2,
        egress_limit_bytes=100 * GIB,
        egress_level="exceeded",
        cpu_base=60,
        cpu_amp=15,
        mem_total=8 * GIB,
        disk_total=40 * GIB,
        # Inbound-only hub override at a level that lands "exceeded" too,
        # so both directions show red on the same host card.
        hub_ingress_override_gib=5,
        ingress_level="exceeded",
    ),
    DemoHost(
        host_id="demo-oci-offline-01",
        hostname="demo-oci-offline-01",
        provider="oci",
        os="linux",
        arch="amd64",
        cpu_model="AMD EPYC (demo)",
        cpu_cores=4,
        egress_limit_bytes=10 * TIB,
        egress_level="ok",
        down=True,
        cpu_base=25,
        cpu_amp=10,
        mem_total=8 * GIB,
        disk_total=100 * GIB,
        ingress_level="ok",
    ),
]

SAMPLE_INTERVAL_SECONDS = 15
HISTORY_SECONDS = 2 * 3600  # 2 hours of history per host
MAX_SAMPLES_PER_REQUEST = 100  # matches hub's documented reporter batching


def egress_level_target_fraction(level: str) -> float:
    """Returns a fraction of the egress limit that lands squarely in the
    requested level's range: ok<80%, warning>=80%, critical>=95%,
    exceeded>=100%."""
    return {
        "ok": 0.45,
        "warning": 0.85,
        "critical": 0.97,
        "exceeded": 1.12,
    }[level]


def ingress_limit_bytes_for(host: "DemoHost") -> int:
    """Returns the illustrative inbound limit used to size this host's
    rx-byte curve: the hub ingress override in bytes if one is
    configured, else an arbitrary baseline (inbound is unlimited by
    default agent-side, so there's no provider default to target)."""
    if host.hub_ingress_override_gib is not None and host.hub_ingress_override_gib > 0:
        return int(host.hub_ingress_override_gib * GIB)
    return 20 * GIB


def build_samples(host: DemoHost, now: int) -> list[dict]:
    """Builds ~HISTORY_SECONDS worth of 15s samples with sine+noise curves
    so dashboard charts look like a real, breathing workload rather than
    flat lines."""
    rng = random.Random(host.seed)
    samples = []

    n = HISTORY_SECONDS // SAMPLE_INTERVAL_SECONDS
    start_ts = now - HISTORY_SECONDS

    # Precompute a target total tx_bytes for the *current* UTC month so
    # the hub's egress_monthly accumulator lands at the requested level.
    # Only the samples within "now"'s calendar month count toward that
    # accumulator; since demo history is only 2h, effectively all of it
    # does unless run right at a month boundary.
    if host.egress_limit_bytes > 0:
        target_tx_total = int(host.egress_limit_bytes * egress_level_target_fraction(host.egress_level))
    else:
        # Unlimited: pick an arbitrary illustrative total.
        target_tx_total = 5 * GIB
    tx_per_sample = max(target_tx_total // max(n, 1), 1)

    rx_limit = ingress_limit_bytes_for(host)
    target_rx_total = int(rx_limit * egress_level_target_fraction(host.ingress_level))
    rx_per_sample = max(target_rx_total // max(n, 1), 1)

    prev_net_total_rx = 0
    prev_net_total_tx = 0

    for i in range(n):
        ts = start_ts + i * SAMPLE_INTERVAL_SECONDS
        # If this host is "down", only report samples up to 5 minutes
        # ago at the latest so the hub's offline-after window (60s
        # default) marks it down.
        if host.down and ts > now - 5 * 60:
            break

        t = i * SAMPLE_INTERVAL_SECONDS
        phase = t / 900.0  # slow ~15min period wave
        cpu = host.cpu_base + host.cpu_amp * (0.5 + 0.5 * math.sin(phase)) + rng.uniform(-3, 3)
        cpu = max(0.5, min(99.0, cpu))

        mem_used_pct = 40 + 20 * (0.5 + 0.5 * math.sin(phase + 1.0)) + rng.uniform(-2, 2)
        mem_used_pct = max(5.0, min(95.0, mem_used_pct))
        mem_used = int(host.mem_total * mem_used_pct / 100)
        mem_available = host.mem_total - mem_used

        disk_used_pct = 30 + 10 * (0.5 + 0.5 * math.sin(phase * 0.3)) + rng.uniform(-1, 1)
        disk_used_pct = max(1.0, min(97.0, disk_used_pct))
        disk_used = int(host.disk_total * disk_used_pct / 100)

        net_rx_bps = max(0.0, rx_per_sample / SAMPLE_INTERVAL_SECONDS + rng.uniform(-5000, 5000))
        net_tx_bps = max(0.0, tx_per_sample / SAMPLE_INTERVAL_SECONDS + rng.uniform(-5000, 5000))

        disk_read_bps = max(0.0, 50_000 + 40_000 * math.sin(phase * 0.7) + rng.uniform(-5000, 5000))
        disk_write_bps = max(0.0, 30_000 + 25_000 * math.sin(phase * 0.5 + 1) + rng.uniform(-3000, 3000))

        load1 = max(0.01, cpu / 100 * host.cpu_cores + rng.uniform(-0.2, 0.2))

        net_rx_bytes = int(net_rx_bps * SAMPLE_INTERVAL_SECONDS)
        net_tx_bytes = int(tx_per_sample)

        sample = {
            "ts": ts,
            "cpu_percent": round(cpu, 2),
            "load1": round(load1, 2),
            "load5": round(load1 * 0.9, 2),
            "load15": round(load1 * 0.8, 2),
            "mem_total": host.mem_total,
            "mem_available": mem_available,
            "mem_used": mem_used,
            "mem_used_percent": round(mem_used_pct, 2),
            "mem_cached": int(host.mem_total * 0.1),
            "swap_total": 0,
            "swap_used": 0,
            "disk_total": host.disk_total,
            "disk_used": disk_used,
            "disk_used_percent": round(disk_used_pct, 2),
            "disks": [
                {
                    "mountpoint": "/",
                    "device": "/dev/root",
                    "fstype": "ext4",
                    "total_bytes": host.disk_total,
                    "used_bytes": disk_used,
                    "used_percent": round(disk_used_pct, 2),
                }
            ],
            "disk_read_bps": round(disk_read_bps, 2),
            "disk_write_bps": round(disk_write_bps, 2),
            "net_rx_bps": round(net_rx_bps, 2),
            "net_tx_bps": round(net_tx_bps, 2),
            "net_rx_bytes": net_rx_bytes,
            "net_tx_bytes": net_tx_bytes,
            "uptime_seconds": HISTORY_SECONDS + t + 86400,
        }
        samples.append(sample)
        prev_net_total_rx += net_rx_bytes
        prev_net_total_tx += net_tx_bytes

    return samples


def build_single_sample(host: DemoHost, ts: int, t_offset: int, rng: random.Random) -> dict:
    """Builds exactly one realistic sample for `host` at timestamp `ts`,
    using the same sine+noise shaping as build_samples but for a single
    point in time (t_offset is the elapsed seconds used to phase the
    sine waves, so consecutive keepalive calls continue the same curve
    rather than restarting it each time). Factored out of build_samples
    so --live keepalive mode (run_live_keepalive below) can post one
    fresh, realistic sample per host per tick without regenerating (and
    re-posting) a full 2h history every tick."""
    phase = t_offset / 900.0
    cpu = host.cpu_base + host.cpu_amp * (0.5 + 0.5 * math.sin(phase)) + rng.uniform(-3, 3)
    cpu = max(0.5, min(99.0, cpu))

    mem_used_pct = 40 + 20 * (0.5 + 0.5 * math.sin(phase + 1.0)) + rng.uniform(-2, 2)
    mem_used_pct = max(5.0, min(95.0, mem_used_pct))
    mem_used = int(host.mem_total * mem_used_pct / 100)
    mem_available = host.mem_total - mem_used

    disk_used_pct = 30 + 10 * (0.5 + 0.5 * math.sin(phase * 0.3)) + rng.uniform(-1, 1)
    disk_used_pct = max(1.0, min(97.0, disk_used_pct))
    disk_used = int(host.disk_total * disk_used_pct / 100)

    net_rx_bps = max(0.0, 20_000 + rng.uniform(-5000, 5000))
    net_tx_bps = max(0.0, 15_000 + rng.uniform(-5000, 5000))
    disk_read_bps = max(0.0, 50_000 + 40_000 * math.sin(phase * 0.7) + rng.uniform(-5000, 5000))
    disk_write_bps = max(0.0, 30_000 + 25_000 * math.sin(phase * 0.5 + 1) + rng.uniform(-3000, 3000))
    load1 = max(0.01, cpu / 100 * host.cpu_cores + rng.uniform(-0.2, 0.2))

    return {
        "ts": ts,
        "cpu_percent": round(cpu, 2),
        "load1": round(load1, 2),
        "load5": round(load1 * 0.9, 2),
        "load15": round(load1 * 0.8, 2),
        "mem_total": host.mem_total,
        "mem_available": mem_available,
        "mem_used": mem_used,
        "mem_used_percent": round(mem_used_pct, 2),
        "mem_cached": int(host.mem_total * 0.1),
        "swap_total": 0,
        "swap_used": 0,
        "disk_total": host.disk_total,
        "disk_used": disk_used,
        "disk_used_percent": round(disk_used_pct, 2),
        "disks": [
            {
                "mountpoint": "/",
                "device": "/dev/root",
                "fstype": "ext4",
                "total_bytes": host.disk_total,
                "used_bytes": disk_used,
                "used_percent": round(disk_used_pct, 2),
            }
        ],
        "disk_read_bps": round(disk_read_bps, 2),
        "disk_write_bps": round(disk_write_bps, 2),
        "net_rx_bps": round(net_rx_bps, 2),
        "net_tx_bps": round(net_tx_bps, 2),
        "net_rx_bytes": int(net_rx_bps * SAMPLE_INTERVAL_SECONDS),
        "net_tx_bytes": int(net_tx_bps * SAMPLE_INTERVAL_SECONDS),
        "uptime_seconds": HISTORY_SECONDS + t_offset + 86400,
    }


def post_report(hub_url: str, token: str, host: DemoHost, samples: list[dict], timeout: float = 10.0) -> None:
    report_host = {
        "id": host.host_id,
        "hostname": host.hostname,
        "os": host.os,
        "platform": "ubuntu" if host.os == "linux" else host.os,
        "platform_version": "24.04",
        "kernel_version": "6.8.0-demo",
        "arch": host.arch,
        "cpu_model": host.cpu_model,
        "cpu_cores": host.cpu_cores,
        "boot_time": int(time.time()) - HISTORY_SECONDS - 86400,
        "provider": host.provider,
        "egress_limit_bytes": host.egress_limit_bytes,
        "agent_version": host.agent_version,
    }

    for i in range(0, len(samples), MAX_SAMPLES_PER_REQUEST):
        batch = samples[i : i + MAX_SAMPLES_PER_REQUEST]
        body = json.dumps({"host": report_host, "samples": batch}).encode("utf-8")
        req = urllib.request.Request(
            url=hub_url.rstrip("/") + "/api/v1/agent/report",
            data=body,
            method="POST",
            headers={
                "Content-Type": "application/json",
                "Authorization": f"Bearer {token}",
            },
        )
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                resp.read()
        except urllib.error.HTTPError as e:
            detail = e.read().decode("utf-8", errors="replace")
            raise SystemExit(f"error: POST report for {host.host_id} failed: HTTP {e.code}: {detail}")
        except urllib.error.URLError as e:
            raise SystemExit(f"error: POST report for {host.host_id} failed: {e}")


def apply_hub_limit_overrides(hub_url: str, ui_token: str, host: DemoHost, timeout: float = 10.0) -> str | None:
    """Applies host's hub_egress_override_gib/hub_ingress_override_gib via
    PUT /api/v1/hosts/{id}/limits (an admin endpoint gated behind
    CP_UI_TOKEN — see SPEC-v0.2's 'Settings/token endpoints are ADMIN
    endpoints' decision). Returns a short status string for logging, or
    None if this host has no overrides configured (both fields None) and
    the call was skipped.

    GiB values are converted to bytes (1 GiB = 2^30, matching
    CP_EGRESS_LIMIT_GB / the settings UI's units); 0 GiB is sent as the
    integer 0 (explicitly unlimited), not omitted."""
    if host.hub_egress_override_gib is None and host.hub_ingress_override_gib is None:
        return None

    body: dict[str, int | None] = {
        "egress_limit_bytes": (
            int(host.hub_egress_override_gib * GIB) if host.hub_egress_override_gib is not None else None
        ),
        "ingress_limit_bytes": (
            int(host.hub_ingress_override_gib * GIB) if host.hub_ingress_override_gib is not None else None
        ),
    }
    req = urllib.request.Request(
        url=hub_url.rstrip("/") + f"/api/v1/hosts/{urllib.parse.quote(host.host_id, safe='')}/limits",
        data=json.dumps(body).encode("utf-8"),
        method="PUT",
        headers={
            "Content-Type": "application/json",
            "Authorization": f"Bearer {ui_token}",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            resp.read()
    except urllib.error.HTTPError as e:
        detail = e.read().decode("utf-8", errors="replace")
        raise SystemExit(f"error: PUT limits for {host.host_id} failed: HTTP {e.code}: {detail}")
    except urllib.error.URLError as e:
        raise SystemExit(f"error: PUT limits for {host.host_id} failed: {e}")

    parts = []
    if host.hub_egress_override_gib is not None:
        parts.append(
            "egress=" + ("unlimited" if host.hub_egress_override_gib == 0 else f"{host.hub_egress_override_gib}GiB")
        )
    if host.hub_ingress_override_gib is not None:
        parts.append(
            "ingress=" + ("unlimited" if host.hub_ingress_override_gib == 0 else f"{host.hub_ingress_override_gib}GiB")
        )
    return ", ".join(parts)


def seed_hub(hub_url: str, token: str, db_path: Path | None = None, ui_token: str | None = None) -> None:
    now = int(time.time())
    print(f"==> seeding hub {hub_url} with {len(DEMO_HOSTS)} demo hosts")
    down_host_ids: list[str] = []
    for host in DEMO_HOSTS:
        samples = build_samples(host, now)
        post_report(hub_url, token, host, samples)
        state = "down (stale samples)" if host.down else f"up, egress={host.egress_level}"
        print(f"    {host.host_id:24s} {host.provider:6s} {len(samples):4d} samples  [{state}]")
        if host.down:
            down_host_ids.append(host.host_id)

        if ui_token:
            override_summary = apply_hub_limit_overrides(hub_url, ui_token, host)
            if override_summary:
                print(f"        hub limit override applied: {override_summary}")
    print("==> hub seeding complete")

    if not ui_token and any(
        h.hub_egress_override_gib is not None or h.hub_ingress_override_gib is not None for h in DEMO_HOSTS
    ):
        print(
            "    note: pass --ui-token (matching the hub's CP_UI_TOKEN) to also "
            "exercise PUT /api/v1/hosts/{id}/limits and seed hub-side outbound/"
            "inbound overrides for a few demo hosts."
        )

    if down_host_ids:
        if db_path:
            backdate_last_seen(db_path, down_host_ids, now)
        else:
            print(
                "    note: pass --db alongside --hub to backdate last_seen for "
                f"down hosts ({', '.join(down_host_ids)}) — the hub's ingest "
                "endpoint always stamps last_seen with wall-clock receive "
                "time, so a freshly seeded 'down' host will show as 'up' "
                "until CP_OFFLINE_AFTER (default 60s) elapses."
            )


def run_live_keepalive(hub_url: str, token: str, duration_seconds: float, interval_seconds: float = 5.0) -> None:
    """Keeps every non-`down` demo host reporting as "up" for
    `duration_seconds`, by posting one fresh, single-timestamp sample per
    up host every `interval_seconds` (default 5s — comfortably under the
    hub's CP_OFFLINE_AFTER default of 60s). Hosts with `down=True` in
    DEMO_HOSTS (see the fleet definition above) are deliberately never
    posted to here, so they stay/become offline for realism — matching
    the same intentional-offline set used by seed_hub's down_host_ids.

    Intended to run alongside (immediately after) a `seed_hub` call and
    for the duration of a screenshot capture run: start this in the
    background, run scripts/capture-screenshots.mjs, then let this
    function's deadline expire (or stop the process) once captures are
    done. Uses only stdlib (urllib), matching the rest of this script.
    """
    up_hosts = [h for h in DEMO_HOSTS if not h.down]
    if not up_hosts:
        print("==> run_live_keepalive: no non-down demo hosts to keep alive; nothing to do")
        return

    print(
        f"==> live keepalive: posting fresh samples for {len(up_hosts)} up host(s) "
        f"every {interval_seconds:.0f}s for {duration_seconds:.0f}s "
        f"({len(DEMO_HOSTS) - len(up_hosts)} host(s) intentionally left offline)"
    )
    rng_by_host = {h.host_id: random.Random(h.seed) for h in up_hosts}
    deadline = time.monotonic() + duration_seconds
    tick = 0
    while time.monotonic() < deadline:
        now = int(time.time())
        for host in up_hosts:
            sample = build_single_sample(host, now, HISTORY_SECONDS + tick * int(interval_seconds), rng_by_host[host.host_id])
            post_report(hub_url, token, host, [sample])
        tick += 1
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            break
        time.sleep(min(interval_seconds, remaining))
    print(f"==> live keepalive: complete ({tick} tick(s) posted)")


def backdate_last_seen(db_path: Path, host_ids: list[str], now: int) -> None:
    """Directly sets hosts.last_seen far in the past for host_ids so they
    render as offline immediately after seeding, without waiting out
    CP_OFFLINE_AFTER. Dev/demo-only direct DB write (mirrors the existing
    bucket_stats seeding pattern in this script); the hub ingest API has
    no field for overriding last_seen, by design (it must reflect actual
    receive time in production)."""
    stale_ts = now - 3600  # 1h ago: well past any reasonable CP_OFFLINE_AFTER
    conn = sqlite3.connect(db_path)
    try:
        cur = conn.cursor()
        cur.executemany(
            "UPDATE hosts SET last_seen = ? WHERE id = ?",
            [(stale_ts, hid) for hid in host_ids],
        )
        conn.commit()
        print(f"    backdated last_seen for {len(host_ids)} down host(s) in {db_path}")
    finally:
        conn.close()


# ---------------------------------------------------------------------------
# Bucket stats seeding (direct SQLite insert)
# ---------------------------------------------------------------------------

S3_DEMO_BUCKETS = [
    {"bucket": "demo-s3-assets", "region": "us-east-1"},
    {"bucket": "demo-s3-backups", "region": "us-west-2"},
]
R2_DEMO_BUCKETS = [
    {"bucket": "demo-r2-media", "region": ""},
    {"bucket": "demo-r2-logs", "region": ""},
]

BUCKET_HISTORY_HOURS = 24


def seed_bucket_history(provider: str, bucket_cfg: dict, now: int, rng: random.Random) -> list[dict]:
    rows = []
    base_size = rng.uniform(5, 50) * GIB
    base_objects = rng.randint(10_000, 500_000)

    for h in range(BUCKET_HISTORY_HOURS, 0, -1):
        ts = now - h * 3600
        growth = 1.0 + (BUCKET_HISTORY_HOURS - h) * 0.01
        size_bytes = int(base_size * growth + rng.uniform(-0.02, 0.02) * base_size)
        object_count = int(base_objects * growth)

        day_fraction = (BUCKET_HISTORY_HOURS - h) / BUCKET_HISTORY_HOURS
        class_a = int(rng.uniform(500, 5000) * (1 + day_fraction))
        class_b = int(rng.uniform(2000, 20000) * (1 + day_fraction))
        requests_window = int(rng.uniform(50, 400) + 100 * (0.5 + 0.5 * math.sin(h / 3.0)))

        rows.append(
            {
                "provider": provider,
                "bucket": bucket_cfg["bucket"],
                "ts": ts,
                "region": bucket_cfg.get("region", ""),
                "size_bytes": max(size_bytes, 0),
                "object_count": max(object_count, 0),
                "class_a_ops_mtd": class_a,
                "class_b_ops_mtd": class_b,
                "requests_window": requests_window,
                "window_seconds": 900,
                "egress_bytes_mtd": int(rng.uniform(1, 20) * GIB) if provider == "s3" else 0,
                "request_metrics_available": 1,
                "error": "",
            }
        )
    return rows


def seed_db(db_path: Path) -> None:
    print(f"==> seeding bucket_stats demo rows into {db_path}")
    conn = sqlite3.connect(db_path)
    try:
        cur = conn.cursor()
        cur.execute(
            "SELECT name FROM sqlite_master WHERE type='table' AND name='bucket_stats'"
        )
        if cur.fetchone() is None:
            raise SystemExit(
                f"error: {db_path} has no bucket_stats table — run the hub once first so migrations apply"
            )

        now = int(time.time())
        rng = random.Random(42)
        total_rows = 0

        for bucket_cfg in S3_DEMO_BUCKETS:
            rows = seed_bucket_history("s3", bucket_cfg, now, rng)
            insert_bucket_rows(cur, rows)
            total_rows += len(rows)
            print(f"    s3/{bucket_cfg['bucket']:20s} {len(rows)} history rows")

        for bucket_cfg in R2_DEMO_BUCKETS:
            rows = seed_bucket_history("r2", bucket_cfg, now, rng)
            insert_bucket_rows(cur, rows)
            total_rows += len(rows)
            print(f"    r2/{bucket_cfg['bucket']:20s} {len(rows)} history rows")

        conn.commit()
        print(f"==> db seeding complete ({total_rows} rows)")
    finally:
        conn.close()


def insert_bucket_rows(cur: sqlite3.Cursor, rows: list[dict]) -> None:
    cur.executemany(
        """
        INSERT OR REPLACE INTO bucket_stats (
            provider, bucket, ts, region, size_bytes, object_count,
            class_a_ops_mtd, class_b_ops_mtd, requests_window, window_seconds,
            egress_bytes_mtd, request_metrics_available, error
        ) VALUES (
            :provider, :bucket, :ts, :region, :size_bytes, :object_count,
            :class_a_ops_mtd, :class_b_ops_mtd, :requests_window, :window_seconds,
            :egress_bytes_mtd, :request_metrics_available, :error
        )
        """,
        rows,
    )


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(
        description="cloud-pulse demo/dev seed data generator (NOT for production hubs).",
    )
    parser.add_argument("--hub", help="Hub base URL, e.g. http://127.0.0.1:8090")
    parser.add_argument("--token", help="CP_AGENT_TOKEN for the target hub")
    parser.add_argument("--db", type=Path, help="Path to the hub's SQLite database file")
    parser.add_argument(
        "--ui-token",
        help=(
            "CP_UI_TOKEN for the target hub. When given, also seeds hub-side "
            "outbound/inbound limit overrides for a few demo hosts via "
            "PUT /api/v1/hosts/{id}/limits (an admin endpoint), so the "
            "settings page's limits editor and the 'hub override' badges "
            "have real data to show."
        ),
    )
    parser.add_argument(
        "--live",
        metavar="SECONDS",
        type=float,
        default=None,
        help=(
            "After the initial seed (and optional --db backdating), block for "
            "SECONDS posting a fresh sample for every non-down demo host every "
            "5s (see run_live_keepalive) so the fleet stays reporting 'up' for "
            "the duration — e.g. for the length of a screenshot capture run. "
            "Hosts with down=True in DEMO_HOSTS are still left offline for "
            "realism. Requires --hub and --token."
        ),
    )
    args = parser.parse_args(argv)

    print(DEMO_BANNER)

    if not args.hub and not args.db:
        parser.error("at least one of --hub (with --token) or --db is required")

    if args.ui_token and not args.hub:
        parser.error("--ui-token requires --hub")

    if args.live is not None and not args.hub:
        parser.error("--live requires --hub (with --token)")

    if args.hub:
        if not args.token:
            parser.error("--token is required when --hub is given")
        seed_hub(args.hub, args.token, args.db, args.ui_token)

    if args.db:
        seed_db(args.db)

    if args.live is not None:
        run_live_keepalive(args.hub, args.token, args.live)

    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
