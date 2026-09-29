#!/usr/bin/env python3
"""demo_seed_v05.py — DEMO/DEV ONLY seed data generator for cloud-pulse's
v0.5.0 alerting + inventory features.

Not part of the production hub or agent. Do not run against a real,
in-use hub. This is a separate script from scripts/demo-seed.py (which
owns the v0.3/v0.4 host-fleet/bucket demo data) so the two can evolve
independently; run both against the same hub for a fully populated demo
dashboard.

Seeds, via the hub's admin REST API (CP_UI_TOKEN bearer auth):

  * A handful of notification channels (Discord/Telegram/WhatsApp/
    webhook), each pointed at a fake/placeholder endpoint — never a real
    platform.
  * A handful of alert rules attached to those channels, covering every
    models.AlertMetric.
  * One agent report per demo host carrying a models.Inventory payload
    (Docker containers with a realistic mix of states/health/ports, and
    listening ports with a mix of resolved/unresolved process names,
    plus one host reporting a permission_denied Docker status) so the
    host detail "Services & ports" card and the overview "Containers"
    column have real data to render.
  * A firing models.AlertEvent per rule (written directly into the
    alert_events table via the hub's SQLite database file, since there
    is no public "inject a fake historical event" API) so the alerts
    page's Active/History views and the navbar bell aren't empty.

Uses only the Python 3 standard library (sqlite3, urllib) — no pip
installs required.
"""

from __future__ import annotations

import argparse
import json
import sqlite3
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

DEMO_BANNER = (
    "cloud-pulse demo_seed_v05.py: DEMO/DEV DATA ONLY — do not point this "
    "at a production hub you care about."
)


@dataclass(frozen=True)
class NotifyChannelSpec:
    """A notify channel to create via POST /api/v1/alerts/channels.

    Config values are deliberately fake/placeholder — see
    docstring for the "never send real messages" policy this script
    follows. ``key`` is a short label used only in this script's own
    console output.
    """

    key: str
    name: str
    type: str  # "discord" | "telegram" | "whatsapp" | "webhook"
    config: dict[str, str]
    enabled: bool = True


@dataclass(frozen=True)
class AlertRuleSpec:
    """An alert rule to create via POST /api/v1/alerts/rules."""

    name: str
    metric: str
    operator: str
    threshold: float
    duration_sec: int
    channel_keys: tuple[str, ...]
    host_id: str = ""
    cooldown_sec: int = 3600
    notify_resolved: bool = True
    enabled: bool = True


@dataclass
class DemoContainer:
    """One fake Docker container for a demo host's inventory."""

    id: str
    name: str
    image: str
    state: str
    status: str
    health: str = ""
    compose_project: str = ""
    compose_service: str = ""
    ports: list[dict[str, Any]] = field(default_factory=list)


@dataclass
class DemoListeningPort:
    """One fake listening port for a demo host's inventory."""

    proto: str
    ip: str
    port: int
    process: str = ""
    pid: int = 0
    container_id: str = ""


@dataclass
class DemoInventoryHost:
    """A demo host's inventory payload plus the identity fields needed
    to post an AgentReport carrying it."""

    host_id: str
    hostname: str
    provider: str
    docker_status: str  # models.DockerStatus value
    containers: list[DemoContainer]
    ports: list[DemoListeningPort]
    docker_error: str = ""


# ---------------------------------------------------------------------------
# Demo data definitions
# ---------------------------------------------------------------------------

NOTIFY_CHANNELS: tuple[NotifyChannelSpec, ...] = (
    NotifyChannelSpec(
        key="discord-ops",
        name="Discord #ops",
        type="discord",
        config={"webhook_url": "https://discord.com/api/webhooks/000000000000000000/demo-fake-token-not-real"},
    ),
    NotifyChannelSpec(
        key="telegram-oncall",
        name="Telegram on-call",
        type="telegram",
        config={"bot_token": "123456:TEST-TOKEN", "chat_id": "-1000000000000"},
    ),
    NotifyChannelSpec(
        key="whatsapp-oncall",
        name="WhatsApp on-call",
        type="whatsapp",
        config={
            "access_token": "demo-fake-access-token-not-real",
            "phone_number_id": "000000000000000",
            "to": "15550000000",
            "template_name": "cloud_pulse_alert",
        },
    ),
    NotifyChannelSpec(
        key="webhook-generic",
        name="Generic webhook",
        type="webhook",
        config={"webhook_url": "https://example.invalid/cloud-pulse-demo-webhook", "include_image": "true"},
    ),
)

ALERT_RULES: tuple[AlertRuleSpec, ...] = (
    AlertRuleSpec(
        name="CPU above 90% for 5 minutes",
        metric="cpu",
        operator=">",
        threshold=90,
        duration_sec=300,
        channel_keys=("discord-ops",),
    ),
    AlertRuleSpec(
        name="Memory above 90% for 10 minutes",
        metric="memory",
        operator=">",
        threshold=90,
        duration_sec=600,
        channel_keys=("discord-ops", "telegram-oncall"),
    ),
    AlertRuleSpec(
        name="Disk above 90%",
        metric="disk",
        operator=">",
        threshold=90,
        duration_sec=0,
        channel_keys=("telegram-oncall",),
    ),
    AlertRuleSpec(
        name="Load average above 4",
        metric="load1",
        operator=">",
        threshold=4,
        duration_sec=120,
        channel_keys=("webhook-generic",),
    ),
    AlertRuleSpec(
        name="Outbound traffic above 80% of limit",
        metric="egress_out_pct",
        operator=">",
        threshold=80,
        duration_sec=0,
        channel_keys=("discord-ops",),
        notify_resolved=False,
    ),
    AlertRuleSpec(
        name="Inbound traffic above 80% of limit",
        metric="egress_in_pct",
        operator=">",
        threshold=80,
        duration_sec=0,
        channel_keys=("discord-ops",),
        notify_resolved=False,
    ),
    AlertRuleSpec(
        name="Host offline for 2 minutes",
        metric="host_down",
        operator=">",
        threshold=0,
        duration_sec=120,
        channel_keys=("discord-ops", "telegram-oncall", "whatsapp-oncall"),
    ),
)

INVENTORY_HOSTS: tuple[DemoInventoryHost, ...] = (
    DemoInventoryHost(
        host_id="demo-aws-web-01",
        hostname="demo-aws-web-01",
        provider="aws",
        docker_status="ok",
        containers=[
            DemoContainer(
                id="a1b2c3d4e5f6",
                name="web",
                image="nginx:1.27-alpine",
                state="running",
                status="Up 3 days",
                health="healthy",
                compose_project="myapp",
                compose_service="web",
                ports=[{"ip": "0.0.0.0", "private_port": 80, "public_port": 8080, "type": "tcp"}],
            ),
            DemoContainer(
                id="b2c3d4e5f6a1",
                name="api",
                image="myapp/api:v2.3.1",
                state="running",
                status="Up 3 days",
                health="healthy",
                compose_project="myapp",
                compose_service="api",
                ports=[{"ip": "0.0.0.0", "private_port": 8000, "public_port": 8000, "type": "tcp"}],
            ),
            DemoContainer(
                id="c3d4e5f6a1b2",
                name="worker",
                image="myapp/worker:v2.3.1",
                state="running",
                status="Up 3 days",
                health="starting",
                compose_project="myapp",
                compose_service="worker",
                ports=[],
            ),
            DemoContainer(
                id="d4e5f6a1b2c3",
                name="migration-job",
                image="myapp/api:v2.3.1",
                state="exited",
                status="Exited (0) 2 days ago",
                ports=[],
            ),
        ],
        ports=[
            DemoListeningPort(proto="tcp", ip="0.0.0.0", port=22, process="sshd", pid=812),
            DemoListeningPort(proto="tcp", ip="0.0.0.0", port=8080, process="docker-proxy", pid=2211, container_id="a1b2c3d4e5f6"),
            DemoListeningPort(proto="tcp", ip="0.0.0.0", port=8000, process="docker-proxy", pid=2244, container_id="b2c3d4e5f6a1"),
            DemoListeningPort(proto="tcp", ip="127.0.0.1", port=9090, process="node_exporter", pid=915),
            DemoListeningPort(proto="udp", ip="0.0.0.0", port=68, process=""),
        ],
    ),
    DemoInventoryHost(
        host_id="demo-oci-db-01",
        hostname="demo-oci-db-01",
        provider="oci",
        docker_status="ok",
        containers=[
            DemoContainer(
                id="e5f6a1b2c3d4",
                name="postgres",
                image="postgres:16",
                state="running",
                status="Up 12 days",
                health="healthy",
                ports=[{"private_port": 5432, "type": "tcp"}],
            ),
            DemoContainer(
                id="f6a1b2c3d4e5",
                name="redis",
                image="redis:7-alpine",
                state="running",
                status="Up 12 days",
                ports=[{"private_port": 6379, "type": "tcp"}],
            ),
            DemoContainer(
                id="a1b2c3d4e5f7",
                name="backup-cron",
                image="myapp/backup:latest",
                state="paused",
                status="Paused",
                ports=[],
            ),
        ],
        ports=[
            DemoListeningPort(proto="tcp", ip="0.0.0.0", port=22, process="sshd", pid=701),
            DemoListeningPort(proto="tcp", ip="127.0.0.1", port=5432, process="docker-proxy", pid=1502, container_id="e5f6a1b2c3d4"),
            DemoListeningPort(proto="tcp", ip="127.0.0.1", port=6379, process="docker-proxy", pid=1533, container_id="f6a1b2c3d4e5"),
        ],
    ),
    DemoInventoryHost(
        host_id="demo-other-edge-01",
        hostname="demo-other-edge-01",
        provider="other",
        docker_status="permission_denied",
        docker_error="dial unix /var/run/docker.sock: connect: permission denied",
        containers=[],
        ports=[
            DemoListeningPort(proto="tcp", ip="0.0.0.0", port=22, process="sshd", pid=401),
            DemoListeningPort(proto="tcp", ip="0.0.0.0", port=443, process=""),
            DemoListeningPort(proto="tcp", ip="0.0.0.0", port=51820, process="wireguard", pid=88),
        ],
    ),
)


class HubClient:
    """Thin urllib-based JSON client for the hub's admin REST API.

    Authenticates every request with a bearer token (either a signed-in
    session token or CP_UI_TOKEN — both are accepted by the hub's admin
    routes identically, see README's "Sign-in and accounts" section).
    Stdlib-only by design, matching scripts/demo-seed.py's convention.
    """

    def __init__(self, base_url: str, token: str, timeout: float = 10.0) -> None:
        self._base_url = base_url.rstrip("/")
        self._token = token
        self._timeout = timeout

    def request(self, method: str, path: str, body: dict[str, Any] | None = None) -> Any:
        """Sends one JSON request and returns the parsed response body
        (or None for a 204/empty body). Raises SystemExit with a
        descriptive message on any HTTP or transport error."""
        data = json.dumps(body).encode("utf-8") if body is not None else None
        req = urllib.request.Request(
            url=self._base_url + path,
            data=data,
            method=method,
            headers={
                "Content-Type": "application/json",
                "Authorization": f"Bearer {self._token}",
            },
        )
        try:
            with urllib.request.urlopen(req, timeout=self._timeout) as resp:
                raw = resp.read()
                return json.loads(raw) if raw else None
        except urllib.error.HTTPError as e:
            detail = e.read().decode("utf-8", errors="replace")
            raise SystemExit(f"error: {method} {path} failed: HTTP {e.code}: {detail}")
        except urllib.error.URLError as e:
            raise SystemExit(f"error: {method} {path} failed: {e}")

    def get(self, path: str) -> Any:
        return self.request("GET", path)

    def post(self, path: str, body: dict[str, Any] | None = None) -> Any:
        return self.request("POST", path, body)


class AlertingSeeder:
    """Creates (or reuses, if already present by name) the demo notify
    channels and alert rules via the hub's admin API.

    Idempotent by name: re-running this script against the same hub
    updates existing channels/rules with the same name in place instead
    of creating duplicates, so it's safe to run repeatedly while
    iterating on the dashboard.
    """

    def __init__(self, client: HubClient) -> None:
        self._client = client
        self._channel_ids_by_key: dict[str, int] = {}

    def seed_channels(self) -> dict[str, int]:
        """Creates/updates every NOTIFY_CHANNELS entry, returning a
        {key: channel_id} map for seed_rules() to resolve channel_keys
        against."""
        existing = {c["name"]: c["id"] for c in self._client.get("/api/v1/alerts/channels")}
        for spec in NOTIFY_CHANNELS:
            payload = {"name": spec.name, "type": spec.type, "enabled": spec.enabled, "config": spec.config}
            if spec.name in existing:
                channel_id = existing[spec.name]
                self._client.request("PUT", f"/api/v1/alerts/channels/{channel_id}", payload)
                print(f"    channel  {spec.name:24s} [{spec.type:9s}] updated (id={channel_id})")
            else:
                created = self._client.post("/api/v1/alerts/channels", payload)
                channel_id = created["id"]
                print(f"    channel  {spec.name:24s} [{spec.type:9s}] created (id={channel_id})")
            self._channel_ids_by_key[spec.key] = channel_id
        return self._channel_ids_by_key

    def seed_rules(self) -> list[dict[str, Any]]:
        """Creates/updates every ALERT_RULES entry. Must be called after
        seed_channels(). Returns the list of created/updated
        models.AlertRule JSON objects."""
        if not self._channel_ids_by_key:
            raise RuntimeError("seed_channels() must run before seed_rules()")

        existing = {r["name"]: r["id"] for r in self._client.get("/api/v1/alerts/rules")}
        results: list[dict[str, Any]] = []
        for spec in ALERT_RULES:
            channel_ids = [self._channel_ids_by_key[k] for k in spec.channel_keys]
            payload = {
                "name": spec.name,
                "enabled": spec.enabled,
                "metric": spec.metric,
                "host_id": spec.host_id,
                "operator": spec.operator,
                "threshold": spec.threshold,
                "duration_sec": spec.duration_sec,
                "cooldown_sec": spec.cooldown_sec,
                "notify_resolved": spec.notify_resolved,
                "channel_ids": channel_ids,
            }
            if spec.name in existing:
                rule_id = existing[spec.name]
                rule = self._client.request("PUT", f"/api/v1/alerts/rules/{rule_id}", payload)
                print(f"    rule     {spec.name:38s} updated (id={rule_id})")
            else:
                rule = self._client.post("/api/v1/alerts/rules", payload)
                print(f"    rule     {spec.name:38s} created (id={rule['id']})")
            results.append(rule)
        return results


class InventorySeeder:
    """Posts an AgentReport carrying a models.Inventory payload for each
    INVENTORY_HOSTS entry, via the agent-token-authenticated ingest
    endpoint (not the admin API)."""

    def __init__(self, hub_url: str, agent_token: str, timeout: float = 10.0) -> None:
        self._hub_url = hub_url.rstrip("/")
        self._agent_token = agent_token
        self._timeout = timeout

    def seed(self) -> None:
        now = int(time.time())
        for host in INVENTORY_HOSTS:
            report = self._build_report(host, now)
            self._post_report(report)
            containers_running = sum(1 for c in host.containers if c.state == "running")
            print(
                f"    inventory {host.host_id:24s} docker={host.docker_status:17s} "
                f"containers_running={containers_running} ports={len(host.ports)}"
            )

    @staticmethod
    def _build_report(host: DemoInventoryHost, now: int) -> dict[str, Any]:
        inventory = {
            "collected_at": now,
            "ports": [
                {
                    "proto": p.proto,
                    "ip": p.ip,
                    "port": p.port,
                    "pid": p.pid,
                    "process": p.process,
                    **({"container_id": p.container_id} if p.container_id else {}),
                }
                for p in host.ports
            ],
            "docker": {
                "status": host.docker_status,
                **({"error": host.docker_error} if host.docker_error else {}),
                **({"version": "1.45"} if host.docker_status == "ok" else {}),
                "containers": [
                    {
                        "id": c.id,
                        "name": c.name,
                        "image": c.image,
                        "state": c.state,
                        "status": c.status,
                        **({"health": c.health} if c.health else {}),
                        "created_at": now - 3 * 86400,
                        **({"compose_project": c.compose_project} if c.compose_project else {}),
                        **({"compose_service": c.compose_service} if c.compose_service else {}),
                        "ports": c.ports,
                    }
                    for c in host.containers
                ],
            },
        }
        sample = {
            "ts": now,
            "cpu_percent": 22.5,
            "load1": 0.8,
            "load5": 0.9,
            "load15": 0.95,
            "mem_total": 8 << 30,
            "mem_available": 5 << 30,
            "mem_used": 3 << 30,
            "mem_used_percent": 37.5,
            "mem_cached": 1 << 30,
            "swap_total": 0,
            "swap_used": 0,
            "disk_total": 100 << 30,
            "disk_used": 40 << 30,
            "disk_used_percent": 40.0,
            "disk_read_bps": 1_000_000,
            "disk_write_bps": 500_000,
            "net_rx_bps": 2_000_000,
            "net_tx_bps": 800_000,
            "net_rx_bytes": 30_000_000,
            "net_tx_bytes": 12_000_000,
            "uptime_seconds": 3 * 86400,
        }
        return {
            "host": {
                "id": host.host_id,
                "hostname": host.hostname,
                "os": "linux",
                "platform": "ubuntu",
                "platform_version": "24.04",
                "kernel_version": "6.8.0-demo",
                "arch": "amd64",
                "cpu_model": "Demo CPU",
                "cpu_cores": 4,
                "boot_time": now - 3 * 86400,
                "provider": host.provider,
                "egress_limit_bytes": 0,
                "agent_version": "v0.5.0",
            },
            "samples": [sample],
            "inventory": inventory,
        }

    def _post_report(self, report: dict[str, Any]) -> None:
        req = urllib.request.Request(
            url=self._hub_url + "/api/v1/agent/report",
            data=json.dumps(report).encode("utf-8"),
            method="POST",
            headers={"Content-Type": "application/json", "Authorization": f"Bearer {self._agent_token}"},
        )
        try:
            with urllib.request.urlopen(req, timeout=self._timeout) as resp:
                resp.read()
        except urllib.error.HTTPError as e:
            detail = e.read().decode("utf-8", errors="replace")
            raise SystemExit(f"error: POST report for {report['host']['id']} failed: HTTP {e.code}: {detail}")
        except urllib.error.URLError as e:
            raise SystemExit(f"error: POST report for {report['host']['id']} failed: {e}")


class FiringEventSeeder:
    """Writes a firing models.AlertEvent directly into the hub's SQLite
    database for each seeded rule, so the alerts page's Active/History
    views and the navbar bell have data without waiting for the
    30-second evaluation scheduler to naturally cross a threshold.

    There is intentionally no public hub API to inject a synthetic
    historical event — this is test/demo-only, hence direct DB access
    (matching scripts/demo-seed.py's existing --db mode for
    bucket_stats). Requires the hub to be stopped or SQLite's WAL mode
    to tolerate concurrent access (the hub's storage layer already
    enables WAL, so this is safe to run against a live hub too).
    """

    def __init__(self, db_path: Path) -> None:
        self._db_path = db_path

    def seed(self, rules: list[dict[str, Any]], hosts: list[dict[str, str]]) -> int:
        """Inserts one firing alert_events row per rule (scoped to the
        rule's host_id if set, otherwise the first demo host), each with
        a synthetic delivery record per channel. Returns the number of
        rows inserted."""
        conn = sqlite3.connect(self._db_path)
        inserted = 0
        try:
            cur = conn.cursor()
            cur.execute("SELECT name FROM sqlite_master WHERE type='table' AND name='alert_events'")
            if cur.fetchone() is None:
                raise SystemExit(
                    f"error: {self._db_path} has no alert_events table — "
                    "run the hub once first so migrations apply"
                )

            now = int(time.time())
            for i, rule in enumerate(rules):
                host_id = rule["host_id"] or (hosts[i % len(hosts)]["id"] if hosts else "")
                hostname = next((h["hostname"] for h in hosts if h["id"] == host_id), host_id)
                deliveries = [
                    {
                        "channel_id": cid,
                        "channel_name": f"channel-{cid}",
                        "ok": True,
                        "at": now - 60,
                    }
                    for cid in rule["channel_ids"]
                ]
                started_at = now - 300
                cur.execute(
                    """
                    INSERT INTO alert_events (
                        rule_id, rule_name, host_id, hostname, metric, state,
                        value, threshold, started_at, notified_at, resolved_at, deliveries
                    ) VALUES (?, ?, ?, ?, ?, 'firing', ?, ?, ?, ?, 0, ?)
                    """,
                    (
                        rule["id"],
                        rule["name"],
                        host_id,
                        hostname,
                        rule["metric"],
                        _demo_value_for_metric(rule["metric"], rule["threshold"]),
                        rule["threshold"],
                        started_at,
                        started_at + 30,
                        json.dumps(deliveries),
                    ),
                )
                inserted += 1
            conn.commit()
        finally:
            conn.close()
        return inserted


def _demo_value_for_metric(metric: str, threshold: float) -> float:
    """Returns a plausible "currently breaching" sample value for a
    metric, slightly above its rule's threshold (except host_down,
    which has no meaningful value — reported as 1.0 by convention)."""
    if metric == "host_down":
        return 1.0
    return round(threshold + max(1.0, threshold * 0.05), 1)


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(
        description="cloud-pulse v0.5.0 demo/dev seed data: alert channels, rules, firing events, and inventory (NOT for production hubs).",
    )
    parser.add_argument("--hub", required=True, help="Hub base URL, e.g. http://127.0.0.1:18090")
    parser.add_argument("--ui-token", required=True, help="CP_UI_TOKEN (or a session token) for the target hub's admin API")
    parser.add_argument("--agent-token", required=True, help="CP_AGENT_TOKEN for the target hub, used to post inventory-bearing agent reports")
    parser.add_argument(
        "--db",
        type=Path,
        help="Path to the hub's SQLite database file; when given, also seeds firing alert_events rows directly (see FiringEventSeeder)",
    )
    args = parser.parse_args(argv)

    print(DEMO_BANNER)

    client = HubClient(args.hub, args.ui_token)

    print("==> seeding notify channels")
    alerting = AlertingSeeder(client)
    alerting.seed_channels()

    print("==> seeding alert rules")
    rules = alerting.seed_rules()

    print("==> seeding inventory (Docker containers + listening ports)")
    inventory = InventorySeeder(args.hub, args.agent_token)
    inventory.seed()

    if args.db:
        print("==> seeding firing alert events")
        hosts = [{"id": h.host_id, "hostname": h.hostname} for h in INVENTORY_HOSTS]
        count = FiringEventSeeder(args.db).seed(rules, hosts)
        print(f"    inserted {count} firing alert_events rows")

    print("==> demo_seed_v05 complete")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
