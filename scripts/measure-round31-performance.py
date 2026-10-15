#!/usr/bin/env python3
"""Bounded, offline, local Round31 development performance measurement.

This is deliberately a one-off stdlib harness, not a general benchmark.
It builds only the current development sources with module/network access
disabled, runs isolated loopback services, and writes evidence only after a
complete successful run.
"""

from __future__ import annotations

import hashlib
import http.server
import ctypes
import errno
import json
import math
import os
import platform
import re
import shutil
import signal
import socket
import stat
import statistics
import subprocess
import sys
import tempfile
import threading
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
EVIDENCE_DIR = ROOT / ".verification" / "round31" / "performance"
APPROVED_TMP = Path("/tmp/opencode")
HUB_PORT = 18132
FAKE_HUB_PORT = 18134
HUB_TOKEN = "round31-synthetic-agent-token-0123456789"
UI_TOKEN = "round31-synthetic-ui-token"
HTTP_SAMPLES = 20
PROCESS_SAMPLE_INTERVAL = 0.25
AGENT_LOOP_SECONDS = 60.0
MAX_COMMAND_SECONDS = 300
MAX_STDOUT_BYTES = 1 << 20
MAX_STDERR_BYTES = 1 << 20
MAX_BUILD_LOG_BYTES = 1 << 19
MAX_HTTP_BODY_BYTES = 8 << 20
MAX_HTTP_HEADER_BYTES = 128 << 10
MAX_MEASUREMENT_JSON_BYTES = 1 << 20
MAX_CURL_STDERR_BYTES = 64 << 10
MAX_FAKE_HUB_HANDLERS = 8
FAKE_HUB_IO_TIMEOUT_S = 1.0
FAKE_HUB_JOIN_TIMEOUT_S = 2.0
GROUP_TERM_GRACE_S = 0.5
STAGE_PREFIX = ".round31-performance-stage-"
_CANCELLING = False

DOCUMENT_PATHS = [
    ("document-root", "/"),
    ("document-login", "/login"),
    ("document-settings-network", "/settings/network"),
]
ASSET_PATHS = [
    ("asset-app-css", "/assets/app.css"),
    ("asset-main-js", "/assets/js/main.js"),
    ("asset-theme-init-js", "/assets/js/theme-init.js"),
    ("asset-uplot-css", "/assets/vendor/uplot/uPlot.min.css"),
]
API_PATHS = [
    ("api-hosts", "/api/v1/hosts"),
    ("api-egress", "/api/v1/egress"),
    ("api-buckets", "/api/v1/buckets"),
    ("api-billing", "/api/v1/billing"),
    ("api-version", "/api/v1/version"),
    ("api-settings", "/api/v1/settings"),
    ("api-fleet-versions", "/api/v1/fleet/versions"),
]


def percentile(values: list[float], p: float) -> float | None:
    """Return an interpolated type-7 percentile (the common spreadsheet rule)."""
    if not values:
        return None
    if not 0 <= p <= 100:
        raise ValueError("percentile must be between 0 and 100")
    ordered = sorted(values)
    position = (len(ordered) - 1) * p / 100
    low = math.floor(position)
    high = math.ceil(position)
    if low == high:
        return ordered[low]
    fraction = position - low
    return ordered[low] + (ordered[high] - ordered[low]) * fraction


def summarize(values: list[float]) -> dict[str, float | int | None]:
    if not values:
        return {"n": 0, "mean": None, "median": None, "p95": None, "sample_max": None}
    return {
        "n": len(values),
        "mean": statistics.fmean(values),
        "median": statistics.median(values),
        "p95": percentile(values, 95),
        "sample_max": max(values),
    }


def read_proc_sample(pid: int) -> tuple[int, int, int] | None:
    """Read utime+stime ticks and VmRSS/VmHWM KiB for one live Linux PID."""
    try:
        stat_text = Path(f"/proc/{pid}/stat").read_text(encoding="ascii")
        close = stat_text.rfind(")")
        if close < 0:
            return None
        fields = stat_text[close + 2 :].split()
        # The fields after comm begin at proc stat field 3: utime/stime are
        # fields 14/15, respectively (zero-based indexes 11/12 here).
        ticks = int(fields[11]) + int(fields[12])
        status: dict[str, int] = {}
        for line in Path(f"/proc/{pid}/status").read_text(encoding="ascii").splitlines():
            key, sep, value = line.partition(":")
            if sep and key in {"VmRSS", "VmHWM"}:
                parts = value.split()
                if parts:
                    status[key] = int(parts[0])
        if "VmRSS" not in status or "VmHWM" not in status:
            return None
        return ticks, status["VmRSS"], status["VmHWM"]
    except (OSError, ValueError, IndexError):
        return None


def process_resource_sample(previous_ticks: int, previous_at: float,
                            current: tuple[int, int, int], now: float,
                            clock_ticks: int, start: float) -> dict[str, float | int]:
    ticks, rss_kib, hwm_kib = current
    elapsed = max(now - previous_at, 1e-9)
    cpu_pct = max(0, ticks - previous_ticks) / clock_ticks / elapsed * 100
    return {
        "elapsed_s": now - start,
        "interval_s": elapsed,
        "cpu_one_core_pct": cpu_pct,
        "vmrss_kib": rss_kib,
        "vmhwm_kib": hwm_kib,
    }


def resource_summary(samples: list[dict[str, float | int]], window_s: float,
                     clock_ticks: int) -> dict[str, Any]:
    return {
        "window_s": window_s,
        "sample_interval_target_s": PROCESS_SAMPLE_INTERVAL,
        "sample_count": len(samples),
        "sc_clk_tck": clock_ticks,
        "cpu_one_core_pct": summarize([float(s["cpu_one_core_pct"]) for s in samples]),
        "cpu_mean_definition": "unweighted arithmetic mean of sampled interval percentages; not total CPU over the full process window",
        "vmrss_kib": summarize([float(s["vmrss_kib"]) for s in samples]),
        "vmhwm_kib": summarize([float(s["vmhwm_kib"]) for s in samples]),
        "sample_max_is_observed_not_an_absolute_instantaneous_peak": True,
        "samples": samples,
    }


def base_env(home: Path, tmp: Path) -> dict[str, str]:
    """Return a small environment; credentials, app settings and proxies are not inherited."""
    return {
        "PATH": os.environ.get("PATH", os.defpath),
        "HOME": str(home),
        "TMPDIR": str(tmp),
        "LANG": "C.UTF-8",
        "LC_ALL": "C.UTF-8",
        "TZ": "UTC",
    }


class OutputLimitError(RuntimeError):
    pass


class MeasurementCancelled(BaseException):
    def __init__(self, signum: int):
        self.signum = signum


class CappedReader:
    """Drain a subprocess pipe immediately while retaining at most limit bytes."""

    def __init__(self, stream: Any, limit: int):
        self.stream = stream
        self.limit = limit
        self.data = bytearray()
        self.overflow = threading.Event()
        self.error: BaseException | None = None
        self.thread = threading.Thread(target=self._drain, daemon=True)

    def start(self) -> None:
        self.thread.start()

    def _drain(self) -> None:
        try:
            while True:
                chunk = self.stream.read(65536)
                if not chunk:
                    break
                available = max(0, self.limit - len(self.data))
                if available:
                    self.data.extend(chunk[:available])
                if len(chunk) > available:
                    self.overflow.set()
        except BaseException as exc:
            self.error = exc
        finally:
            try:
                self.stream.close()
            except OSError:
                pass


def spawn_captured(command: list[str], *, cwd: Path, env: dict[str, str],
                   stdout_limit: int = MAX_STDOUT_BYTES,
                   stderr_limit: int = MAX_STDERR_BYTES,
                   pass_fds: tuple[int, ...] = ()) -> tuple[subprocess.Popen[Any], list[CappedReader]]:
    readers: list[CappedReader] = []
    process = subprocess.Popen(command, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                               start_new_session=True, pass_fds=pass_fds)
    try:
        for stream, limit in ((process.stdout, stdout_limit), (process.stderr, stderr_limit)):
            reader = CappedReader(stream, limit)
            readers.append(reader)
            reader.start()
    except BaseException:
        terminate_group(process)
        for stream in (process.stdout, process.stderr):
            try:
                stream.close()
            except OSError:
                pass
        for reader in readers:
            if reader.thread.ident is not None:
                reader.thread.join(timeout=1)
        raise
    return process, readers


def capture_overflow(readers: list[CappedReader]) -> bool:
    return any(reader.overflow.is_set() for reader in readers)


def finish_capture(process: subprocess.Popen[Any], readers: list[CappedReader]) -> None:
    """Kill surviving members, reap the leader, then bound pipe-reader joins."""
    cleanup_error: BaseException | None = None
    try:
        terminate_group(process)
    except BaseException as exc:
        cleanup_error = exc
    for reader in readers:
        if reader.thread.ident is not None:
            reader.thread.join(timeout=2)
        if reader.thread.is_alive():
            try:
                reader.stream.close()
            except OSError:
                pass
            reader.thread.join(timeout=0.25)
            cleanup_error = cleanup_error or RuntimeError("owned output reader did not stop")
        if reader.error:
            cleanup_error = cleanup_error or RuntimeError("owned output reader failed")
    if cleanup_error:
        raise cleanup_error


def run_captured(command: list[str], *, cwd: Path, env: dict[str, str], timeout: float,
                 stdout_limit: int = MAX_STDOUT_BYTES,
                 stderr_limit: int = MAX_STDERR_BYTES,
                 pass_fds: tuple[int, ...] = ()) -> subprocess.CompletedProcess[bytes]:
    """Run while draining both pipes; every exit path closes the owned process group."""
    process, readers = spawn_captured(command, cwd=cwd, env=env,
                                      stdout_limit=stdout_limit, stderr_limit=stderr_limit,
                                      pass_fds=pass_fds)
    try:
        deadline = time.monotonic() + timeout
        while process.poll() is None:
            if capture_overflow(readers):
                raise OutputLimitError(f"{Path(command[0]).name} exceeded captured output limit")
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise RuntimeError(f"command exceeded {timeout:g}s: {Path(command[0]).name}")
            try:
                process.wait(timeout=min(0.05, remaining))
            except subprocess.TimeoutExpired:
                pass
    finally:
        finish_capture(process, readers)
    if capture_overflow(readers):
        raise OutputLimitError(f"{Path(command[0]).name} exceeded captured output limit")
    for reader in readers:
        if reader.error:
            raise RuntimeError(f"failed draining {Path(command[0]).name} output") from None
    if process.returncode != 0:
        raise RuntimeError(f"command failed ({process.returncode}): {Path(command[0]).name}")
    return subprocess.CompletedProcess(command, process.returncode,
                                       bytes(readers[0].data), bytes(readers[1].data))


def run_bounded(command: list[str], *, cwd: Path, env: dict[str, str], timeout: float,
                stdout_limit: int = MAX_STDOUT_BYTES,
                stderr_limit: int = MAX_STDERR_BYTES) -> subprocess.CompletedProcess[bytes]:
    return run_captured(command, cwd=cwd, env=env, timeout=timeout,
                        stdout_limit=stdout_limit, stderr_limit=stderr_limit)


def process_group_exists(pgid: int) -> bool:
    try:
        os.killpg(pgid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True


def terminate_group(process: subprocess.Popen[Any]) -> None:
    """Stop every remaining member of this new session, even after its leader exits."""
    pgid = process.pid
    if process_group_exists(pgid):
        try:
            os.killpg(pgid, signal.SIGTERM)
        except ProcessLookupError:
            pass
    deadline = time.monotonic() + GROUP_TERM_GRACE_S
    while process_group_exists(pgid) and time.monotonic() < deadline:
        process.poll()  # Reap the leader without assuming it owns the whole group.
        time.sleep(0.02)
    if process_group_exists(pgid):
        try:
            os.killpg(pgid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        deadline = time.monotonic() + 2
        while process_group_exists(pgid) and time.monotonic() < deadline:
            process.poll()
            time.sleep(0.02)
        if process_group_exists(pgid):
            raise RuntimeError("owned process group remained after SIGKILL")
    try:
        process.wait(timeout=2)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(pgid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=1)
        except subprocess.TimeoutExpired:
            raise RuntimeError("owned process leader did not reap after SIGKILL") from None


def install_cancellation_handlers() -> None:
    def cancel(signum: int, _frame: Any) -> None:
        global _CANCELLING
        if not _CANCELLING:
            _CANCELLING = True
            raise MeasurementCancelled(signum)

    signal.signal(signal.SIGTERM, cancel)
    signal.signal(signal.SIGINT, cancel)


def write_exclusive(path: Path, data: bytes, limit: int) -> None:
    if len(data) > limit:
        raise OutputLimitError(f"refusing oversized output file: {path.name}")
    with path.open("xb") as stream:
        stream.write(data)


def append_capped(path: Path, data: bytes, limit: int) -> None:
    previous = path.stat().st_size if path.exists() else 0
    if previous + len(data) > limit:
        raise OutputLimitError(f"refusing oversized log: {path.name}")
    with path.open("ab") as stream:
        stream.write(data)


def ensure_port_free(port: int) -> None:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 0)
        try:
            sock.bind(("127.0.0.1", port))
        except OSError as exc:
            raise RuntimeError(f"required loopback port {port} is unavailable; no process was touched") from exc


def curl_request(url: str, env: dict[str, str],
                 authorization: str | None = None) -> dict[str, Any]:
    header_read, header_write = os.pipe()
    command = [
        "curl", "--silent", "--show-error", "--max-redirs", "0", "--noproxy", "*",
        "--http1.1",
        "--connect-timeout", "2", "--max-time", "5", "--dump-header", f"/dev/fd/{header_write}",
        "--output", "-", "--write-out",
        "%{stderr}%{time_starttransfer}\\t%{time_total}\\t%{http_code}\\t%{size_download}\\n",
    ]
    if authorization:
        command.extend(["--header", f"Authorization: Bearer {authorization}"])
    command.append(url)
    try:
        process, readers = spawn_captured(command, cwd=ROOT, env=env,
            stdout_limit=MAX_HTTP_BODY_BYTES, stderr_limit=MAX_CURL_STDERR_BYTES,
            pass_fds=(header_write,))
    except BaseException:
        os.close(header_read)
        os.close(header_write)
        raise
    header_stream = None
    read_open: int | None = header_read
    write_open: int | None = header_write
    try:
        fd = write_open
        write_open = None
        try:
            os.close(fd)
        except OSError:
            pass
        header_stream = os.fdopen(read_open, "rb", buffering=0)
        read_open = None
        header_capture = CappedReader(header_stream, MAX_HTTP_HEADER_BYTES)
        readers.append(header_capture)
        header_capture.start()
        header_stream = None
        deadline = time.monotonic() + 8
        while process.poll() is None:
            if capture_overflow(readers):
                raise OutputLimitError("curl response body/header/output limit exceeded")
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise RuntimeError("curl request exceeded 8s")
            try:
                process.wait(timeout=min(0.05, remaining))
            except subprocess.TimeoutExpired:
                pass
    finally:
        if write_open is not None:
            try:
                os.close(write_open)
            except OSError:
                pass
        if read_open is not None:
            try:
                os.close(read_open)
            except OSError:
                pass
        if header_stream is not None:
            try:
                header_stream.close()
            except OSError:
                pass
        finish_capture(process, readers)
    if capture_overflow(readers):
        raise OutputLimitError("curl response body/header/output limit exceeded")
    if process.returncode != 0:
        raise RuntimeError("curl request failed")
    stderr_text = bytes(readers[1].data).decode("ascii", errors="replace")
    timing_line = next((line for line in reversed(stderr_text.splitlines())
                        if line.count("\t") == 3), "")
    fields = timing_line.split("\t")
    if len(fields) != 4:
        raise RuntimeError("curl did not return its four expected timing fields")
    header_bytes = bytes(header_capture.data)
    body = bytes(readers[0].data)
    body_bytes = len(body)
    header_text = header_bytes.decode("iso-8859-1", errors="replace")
    headers: dict[str, str] = {}
    for line in header_text.splitlines()[1:]:
        name, sep, value = line.partition(":")
        if sep:
            headers[name.strip().lower()] = value.strip()
    return {
        "ttfb_ms": float(fields[0]) * 1000,
        "total_ms": float(fields[1]) * 1000,
        "http_status": int(fields[2]),
        "curl_size_download_bytes": int(float(fields[3])),
        "body_file_bytes": body_bytes,
        "content_type": headers.get("content-type", ""),
        "content_encoding": headers.get("content-encoding", "identity (header absent)"),
        "content_length_header": headers.get("content-length", "(absent)"),
        "headers_raw": header_bytes,
        "body_raw": body,
    }


def measure_route(label: str, path: str, kind: str, base_url: str, env: dict[str, str],
                  evidence_stage: Path, authorization: str | None) -> dict[str, Any]:
    samples: list[dict[str, Any]] = []
    slug = re.sub(r"[^a-z0-9]+", "-", label.lower()).strip("-")
    is_document_or_asset = kind in {"document", "embedded_asset"}
    for index in range(HTTP_SAMPLES):
        sample = curl_request(base_url + path, env, authorization)
        if sample["http_status"] != 200:
            raise RuntimeError(f"{label} sample {index + 1} returned HTTP {sample['http_status']}")
        headers = sample.pop("headers_raw")
        body = sample.pop("body_raw")
        if index == 0:
            raw_dir = evidence_stage / "raw-http"
            raw_dir.mkdir(parents=True, exist_ok=True)
            header_name = f"{slug}.first-response.headers.txt"
            write_exclusive(raw_dir / header_name, headers, MAX_HTTP_HEADER_BYTES)
            sample["raw_headers_path"] = f"raw-http/{header_name}"
            if is_document_or_asset:
                body_name = f"{slug}.first-response.body"
                write_exclusive(raw_dir / body_name, body, MAX_HTTP_BODY_BYTES)
                sample["raw_body_path"] = f"raw-http/{body_name}"
            else:
                sample["raw_body_path"] = "not retained (API response body intentionally not logged)"
        samples.append(sample)
    ttfb = [float(item["ttfb_ms"]) for item in samples]
    totals = [float(item["total_ms"]) for item in samples]
    sizes = [float(item["body_file_bytes"]) for item in samples]
    repeats = samples[1:]
    return {
        "label": label,
        "kind": kind,
        "method": "GET",
        "path": path,
        "sample_count": len(samples),
        "first_request": {
            "ttfb_ms": samples[0]["ttfb_ms"],
            "http_status": samples[0]["http_status"],
            "content_type": samples[0]["content_type"],
            "content_encoding": samples[0]["content_encoding"],
            "content_length_header": samples[0]["content_length_header"],
            "encoded_http_body_bytes": samples[0]["body_file_bytes"],
            "curl_size_download_bytes": samples[0]["curl_size_download_bytes"],
            "raw_headers_path": samples[0]["raw_headers_path"],
            "raw_body_path": samples[0]["raw_body_path"],
        },
        "all_20_requests": {
            "ttfb_ms": summarize(ttfb),
            "total_ms": summarize(totals),
            "encoded_http_body_bytes": summarize(sizes),
        },
        "repeats_2_to_20_excluding_first_request": {
            "sample_count": len(repeats),
            "ttfb_ms": summarize([float(item["ttfb_ms"]) for item in repeats]),
            "total_ms": summarize([float(item["total_ms"]) for item in repeats]),
        },
        "samples": samples,
    }


class FakeHubHandler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    request_counts: dict[str, int] = {}
    total_request_bytes = 0

    def setup(self) -> None:
        super().setup()
        self.connection.settimeout(FAKE_HUB_IO_TIMEOUT_S)

    def log_message(self, _format: str, *_args: Any) -> None:
        # Intentionally suppress BaseHTTPRequestHandler's access logs.
        return

    def record_route(self) -> None:
        route = self.path if self.path in {"/api/v1/agent/time", "/api/v1/agent/report"} else "<unexpected>"
        type(self).request_counts[route] = min(type(self).request_counts.get(route, 0) + 1, 1_000_000)

    def send_json(self, status: int, body: dict[str, Any]) -> None:
        payload = json.dumps(body, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self) -> None:
        self.record_route()
        if self.path == "/api/v1/agent/time":
            self.send_json(200, {"server_time_ms": int(time.time() * 1000)})
        else:
            self.send_json(404, {"error": "not found"})

    def do_POST(self) -> None:
        self.record_route()
        if self.path != "/api/v1/agent/report":
            self.send_json(404, {"error": "not found"})
            return
        if self.headers.get("Authorization") != f"Bearer {HUB_TOKEN}":
            self.send_json(401, {"error": "unauthorized"})
            return
        content_length = int(self.headers.get("Content-Length", "0"))
        if content_length < 0 or content_length > MAX_HTTP_BODY_BYTES:
            self.send_json(413, {"error": "payload too large"})
            return
        # Drain but never parse, retain, print, or forward the report body.
        remaining = content_length
        while remaining:
            chunk = self.rfile.read(min(65536, remaining))
            if not chunk:
                break
            remaining -= len(chunk)
            type(self).total_request_bytes += len(chunk)
        if remaining:
            self.send_json(400, {"error": "incomplete request"})
            return
        self.send_json(200, {"server_time_ms": int(time.time() * 1000), "latest_version": ""})


class BoundedFakeHubServer(http.server.ThreadingHTTPServer):
    daemon_threads = True
    block_on_close = False
    request_queue_size = MAX_FAKE_HUB_HANDLERS

    def __init__(self, address: tuple[str, int], handler: type[http.server.BaseHTTPRequestHandler]):
        self._slots = threading.BoundedSemaphore(MAX_FAKE_HUB_HANDLERS)
        self._lock = threading.Lock()
        self._connections: set[socket.socket] = set()
        self._handlers: set[threading.Thread] = set()
        super().__init__(address, handler)

    def handle_error(self, _request: Any, _client_address: Any) -> None:
        # Timeouts and closed owned sockets must not emit unbounded tracebacks.
        return

    def process_request(self, request: socket.socket, client_address: Any) -> None:
        if not self._slots.acquire(blocking=False):
            request.close()
            return
        thread = threading.Thread(target=self._process_bounded, args=(request, client_address), daemon=True)
        with self._lock:
            self._connections.add(request)
            self._handlers.add(thread)
        try:
            thread.start()
        except BaseException:
            with self._lock:
                self._connections.discard(request)
                self._handlers.discard(thread)
            self._slots.release()
            self.shutdown_request(request)
            raise

    def _process_bounded(self, request: socket.socket, client_address: Any) -> None:
        try:
            super().process_request_thread(request, client_address)
        finally:
            with self._lock:
                self._connections.discard(request)
                self._handlers.discard(threading.current_thread())
            self._slots.release()


def start_fake_hub(port: int = FAKE_HUB_PORT) -> tuple[BoundedFakeHubServer, threading.Thread]:
    FakeHubHandler.request_counts = {}
    FakeHubHandler.total_request_bytes = 0
    server = BoundedFakeHubServer(("127.0.0.1", port), FakeHubHandler)
    thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.05},
                              name="round31-fake-hub", daemon=True)
    try:
        thread.start()
        return server, thread
    except BaseException:
        if thread.is_alive():
            stop_fake_hub(server, thread)
        else:
            server.server_close()
        raise


def stop_fake_hub(server: BoundedFakeHubServer, thread: threading.Thread) -> None:
    server.shutdown()
    with server._lock:
        connections = list(server._connections)
    for connection in connections:
        try:
            connection.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        try:
            connection.close()
        except OSError:
            pass
    server.server_close()
    deadline = time.monotonic() + FAKE_HUB_JOIN_TIMEOUT_S
    thread.join(max(0, deadline - time.monotonic()))
    with server._lock:
        handlers = list(server._handlers)
    for handler in handlers:
        handler.join(max(0, deadline - time.monotonic()))
    if thread.is_alive() or any(handler.is_alive() for handler in handlers):
        raise RuntimeError("owned fake-hub accept/handler threads did not stop")


def sample_process(process: subprocess.Popen[Any], start: float, deadline: float,
                   clock_ticks: int, readers: list[CappedReader]) -> list[dict[str, float | int]]:
    first = read_proc_sample(process.pid)
    if first is None:
        return []
    previous_ticks = first[0]
    previous_at = time.monotonic()
    samples: list[dict[str, float | int]] = []
    while process.poll() is None:
        if capture_overflow(readers):
            raise OutputLimitError("profiled agent exceeded stdout/stderr capture limit")
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            break
        try:
            process.wait(timeout=min(PROCESS_SAMPLE_INTERVAL, remaining))
        except subprocess.TimeoutExpired:
            pass
        now = time.monotonic()
        current = read_proc_sample(process.pid)
        if current is None:
            break
        samples.append(process_resource_sample(previous_ticks, previous_at,
                                               current, now, clock_ticks, start))
        previous_ticks, previous_at = current[0], now
        if process.poll() is not None:
            break
    return samples


def run_profiled_command(command: list[str], env: dict[str, str], cwd: Path,
                         timeout: float, clock_ticks: int) -> tuple[dict[str, Any], bytes]:
    start = time.monotonic()
    process, readers = spawn_captured(command, cwd=cwd, env=env)
    try:
        samples = sample_process(process, start, start + timeout, clock_ticks, readers)
        if process.poll() is None:
            raise RuntimeError(f"agent {Path(command[0]).name} exceeded {timeout:g}s")
    finally:
        finish_capture(process, readers)
    if capture_overflow(readers):
        raise OutputLimitError(f"agent {Path(command[0]).name} exceeded captured output limit")
    if process.returncode != 0:
        raise RuntimeError(f"agent {Path(command[0]).name} command exited {process.returncode}")
    stdout = bytes(readers[0].data)
    elapsed = time.monotonic() - start
    return {
        "elapsed_s": elapsed,
        "exit_code": process.returncode,
        "stdout_bytes": len(stdout),
        "resource_observations": resource_summary(samples, elapsed, clock_ticks),
    }, stdout


def run_agent_loop(agent_bin: Path, env: dict[str, str], clock_ticks: int) -> dict[str, Any]:
    process = subprocess.Popen([str(agent_bin)], cwd=ROOT, env=env, stdin=subprocess.DEVNULL,
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                               start_new_session=True)
    try:
        completed = False
        start = time.monotonic()
        deadline = start + AGENT_LOOP_SECONDS
        samples = sample_process(process, start, deadline, clock_ticks, [])
        if process.poll() is not None:
            raise RuntimeError(f"normal agent loop exited early ({process.returncode})")
        elapsed = time.monotonic() - start
        if elapsed < AGENT_LOOP_SECONDS - 0.05:
            raise RuntimeError("normal agent loop sampling window ended early")
        terminate_group(process)
        completed = True
        return {
            "configured_interval": "15s",
            "sampling_window_s": elapsed,
            "exit_after_owned_sigterm": process.returncode,
            "resource_observations": resource_summary(samples, elapsed, clock_ticks),
        }
    finally:
        if not completed:
            terminate_group(process)


def read_temperature_c() -> float | None:
    for path in sorted(Path("/sys/class/thermal").glob("thermal_zone*/temp")):
        try:
            value = float(path.read_text(encoding="ascii").strip())
            return value / 1000 if value > 1000 else value
        except (OSError, ValueError):
            continue
    return None


def host_context() -> dict[str, Any]:
    try:
        load = list(os.getloadavg())
    except (AttributeError, OSError):
        load = None
    return {
        "os": platform.system(),
        "architecture": platform.machine(),
        "host_class": "generic Linux arm64 development host / single-board-computer class",
        "logical_cpus": os.cpu_count(),
        "load_average_1m_5m_15m": load,
        "thermal_zone0_or_first_available_celsius": read_temperature_c(),
    }


def wait_for_hub(hub: subprocess.Popen[Any], curl_env: dict[str, str]) -> None:
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if hub.poll() is not None:
            raise RuntimeError(f"owned development hub exited during startup ({hub.returncode})")
        try:
            response = curl_request(f"http://127.0.0.1:{HUB_PORT}/healthz", curl_env)
            if response["http_status"] == 200:
                return
        except (RuntimeError, OSError, ValueError):
            pass
        time.sleep(0.25)
    raise RuntimeError("owned development hub did not become ready within 10s")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def build_and_measure(stage: Path, scratch: Path) -> dict[str, Any]:
    started_utc = datetime.now(timezone.utc).isoformat()
    host_start = host_context()
    home = scratch / "home"
    home.mkdir()
    build_env = base_env(home, scratch)
    build_env.update({
        "GOENV": "off",
        "GOWORK": "off",
        "GOPROXY": "off",
        "GOSUMDB": "off",
        "GOTOOLCHAIN": "local",
        "CGO_ENABLED": "0",
        "GOCACHE": str(scratch / "go-cache"),
    })
    cache_probe_env = base_env(Path(os.path.expanduser("~")).resolve(), scratch)
    cache_probe_env.update({
        "GOENV": "off",
        "GOWORK": "off",
        "GOPROXY": "off",
        "GOSUMDB": "off",
        "GOTOOLCHAIN": "local",
    })
    cache_probe = run_bounded(["go", "env", "GOMODCACHE"], cwd=ROOT,
                              env=cache_probe_env, timeout=15)
    module_cache = Path(cache_probe.stdout.decode().strip()).resolve()
    if not module_cache.is_dir():
        raise RuntimeError("configured local Go module cache is unavailable")
    build_env["GOMODCACHE"] = str(module_cache)

    hub_bin = scratch / "cloud-pulse-hub-dev"
    agent_bin = scratch / "cloud-pulse-agent-dev"
    build_log = scratch / "build.log"
    write_exclusive(build_log, b"", MAX_BUILD_LOG_BYTES)
    build_commands = [
        ["go", "build", "-mod=readonly", "-buildvcs=false", "-trimpath", "-o", str(hub_bin), "./cmd/hub"],
        ["go", "build", "-mod=readonly", "-buildvcs=false", "-trimpath", "-o", str(agent_bin), "./cmd/agent"],
    ]
    for command in build_commands:
        completed = run_bounded(command, cwd=ROOT, env=build_env, timeout=MAX_COMMAND_SECONDS)
        append_capped(build_log, ("$ " + " ".join(command) + "\n").encode() +
                      completed.stdout + completed.stderr, MAX_BUILD_LOG_BYTES)
    version_env = base_env(home, scratch)
    hub_version = run_bounded([str(hub_bin), "-version"], cwd=ROOT,
                              env=version_env, timeout=10).stdout.decode().strip()
    agent_version = run_bounded([str(agent_bin), "-version"], cwd=ROOT,
                                env=version_env, timeout=10).stdout.decode().strip()

    hub_data = scratch / "hub-data"
    hub_data.mkdir()
    hub_env = base_env(home, scratch)
    hub_env.update({
        "CP_AGENT_TOKEN": HUB_TOKEN,
        "CP_UI_TOKEN": UI_TOKEN,
        "CP_ALLOWED_CIDRS": "127.0.0.1/32",
        "CP_UPDATE_CHECK": "false",
        "CP_BILLING": "off",
        "CP_LISTEN": f"127.0.0.1:{HUB_PORT}",
        "CP_DATA_DIR": str(hub_data),
        "CP_LOG_LEVEL": "error",
        "CP_LOG_FORMAT": "json",
        "CP_CLOUD_INTERVAL": "24h",
        "CP_STORAGE_INTERVAL": "24h",
    })
    curl_env = base_env(home, scratch)
    hub = subprocess.Popen([str(hub_bin), "-listen", f"127.0.0.1:{HUB_PORT}",
                            "-data-dir", str(hub_data)], cwd=ROOT, env=hub_env,
                           stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                           stderr=subprocess.DEVNULL, start_new_session=True)
    try:
        wait_for_hub(hub, curl_env)
        measured: list[dict[str, Any]] = []
        for label, path in DOCUMENT_PATHS:
            measured.append(measure_route(label, path, "document",
                f"http://127.0.0.1:{HUB_PORT}", curl_env, stage, None))
        for label, path in ASSET_PATHS:
            measured.append(measure_route(label, path, "embedded_asset",
                f"http://127.0.0.1:{HUB_PORT}", curl_env, stage, None))
        for label, path in API_PATHS:
            measured.append(measure_route(label, path, "api",
                f"http://127.0.0.1:{HUB_PORT}", curl_env, stage, UI_TOKEN))
    finally:
        terminate_group(hub)

    fake_hub, fake_hub_thread = start_fake_hub()
    try:
        agent_env = base_env(home, scratch)
        agent_env.update({
        "CP_HUB_URL": f"http://127.0.0.1:{FAKE_HUB_PORT}",
        "CP_AGENT_TOKEN": HUB_TOKEN,
        "CP_HOST_ID": "round31-perf-synthetic",
        "CP_INTERVAL": "15s",
        "CP_PROVIDER": "other",
        "CP_TIME_SYNC": "local",
        "CP_CLOUD_METADATA": "off",
        "CP_DOCKER": "off",
        "CP_REMOTE_UPDATE": "off",
        "CP_SEND_JITTER": "0",
        "CP_LOG_LEVEL": "error",
        "CP_LOG_FORMAT": "json",
        })
        clock_ticks = os.sysconf("SC_CLK_TCK")
        agent_results: dict[str, Any] = {}
        version_start = time.monotonic()
        version_completed = run_bounded([str(agent_bin), "-version"], cwd=ROOT,
                                        env=agent_env, timeout=10)
        version_elapsed = time.monotonic() - version_start
        version_stdout = version_completed.stdout
        agent_results["version"] = {
            "elapsed_s": version_elapsed,
            "exit_code": version_completed.returncode,
            "version_output": version_stdout.decode("utf-8", errors="replace").strip(),
            "resource_observations": "not sampled; short version-only CLI invocation",
        }
        once_result, once_stdout = run_profiled_command(
            [str(agent_bin), "-once"], agent_env, ROOT, 20, clock_ticks)
        try:
            once_json = json.loads(once_stdout)
            once_keys = sorted(once_json) if isinstance(once_json, dict) else []
        except (json.JSONDecodeError, UnicodeDecodeError):
            once_keys = []
            raise RuntimeError("agent -once did not emit valid JSON")
        agent_results["once_local_collection"] = {
            **once_result,
            "json_top_level_keys_only": once_keys,
            "stdout_payload_retained": False,
            "note": "local two-sample collection; stdout values intentionally not stored",
        }
        agent_results["normal_15s_loop"] = run_agent_loop(agent_bin, agent_env, clock_ticks)
    finally:
        stop_fake_hub(fake_hub, fake_hub_thread)

    request_counts = dict(FakeHubHandler.request_counts)
    if request_counts.get("/api/v1/agent/report", 0) < 3:
        raise RuntimeError("normal agent loop did not produce expected local report requests")
    if set(request_counts) != {"/api/v1/agent/report"}:
        raise RuntimeError("agent reached an unexpected fake-hub route; local time sync was expected")
    agent_results["normal_15s_loop"]["fake_hub_observation"] = {
        "listen_address": f"127.0.0.1:{FAKE_HUB_PORT}",
        "request_counts_by_path": request_counts,
        "report_body_bytes_drained_without_retention_or_logging": FakeHubHandler.total_request_bytes,
        "redirects": "none returned or followed",
        "forwarding": "none; fake hub never forwards",
        "payload_logging": "disabled; bodies drained into no-retention sink",
    }

    try:
        git_head = run_bounded(["git", "rev-parse", "HEAD"], cwd=ROOT,
                               env=base_env(home, scratch), timeout=10).stdout.decode().strip()
    except RuntimeError:
        git_head = "unavailable"

    measurement = {
        "schema": "round31-performance-measurement-v1",
        "started_utc": started_utc,
        "finished_utc": datetime.now(timezone.utc).isoformat(),
        "measurement_window_note": "timestamp recorded after collection; request and process windows are recorded individually",
        "source": {
            "measurement_harness_sha256": sha256_file(Path(__file__).resolve()),
            "git_head_reference_only": git_head,
            "source_state": "current development working tree; source was not modified by this harness",
            "development_build": True,
            "release_or_deployment_artifacts_used": False,
            "go_module_mode": "-mod=readonly; GOPROXY=off; GOSUMDB=off; GOENV=off; GOWORK=off; GOTOOLCHAIN=local; CGO_ENABLED=0",
            "module_cache_policy": "reuse existing local module cache with downloads disabled; cache is not filesystem-read-only",
            "module_cache_path": str(module_cache),
            "hub_version": hub_version,
            "agent_version": agent_version,
            "hub_binary_sha256": sha256_file(hub_bin),
            "agent_binary_sha256": sha256_file(agent_bin),
            "hub_build_command": "go build -mod=readonly -buildvcs=false -trimpath -o <scratch>/cloud-pulse-hub-dev ./cmd/hub",
            "agent_build_command": "go build -mod=readonly -buildvcs=false -trimpath -o <scratch>/cloud-pulse-agent-dev ./cmd/agent",
        },
        "tool_versions": {
            "go": run_bounded(["go", "version"], cwd=ROOT, env=build_env,
                               timeout=10).stdout.decode().strip(),
            "python": platform.python_version(),
            "curl": run_bounded(["curl", "--version"], cwd=ROOT, env=curl_env,
                                timeout=10).stdout.decode().splitlines()[0],
        },
        "host_start": host_start,
        "http": {
            "hub_address": f"127.0.0.1:{HUB_PORT}",
            "samples_per_path": HTTP_SAMPLES,
            "curl_writeout": ["time_starttransfer", "time_total", "http_code", "size_download"],
            "fresh_curl_process_and_tcp_connection_per_sample": True,
            "redirects": "disabled (--max-redirs 0); any non-200 fails",
            "proxy_environment": "not inherited; curl also uses --noproxy '*'",
            "authorization": "synthetic static dashboard token for dashboard APIs; token value omitted",
            "server_config": {
                "CP_UPDATE_CHECK": "false",
                "CP_BILLING": "off",
                "CP_ALLOWED_CIDRS": "127.0.0.1/32",
                "CP_AGENT_TOKEN": "synthetic token, value omitted",
                "CP_UI_TOKEN": "synthetic token, value omitted",
                "data_dir": "fresh temporary directory; removed after run",
                "cloud_credentials": "none inherited or configured",
            },
            "routes": measured,
        },
        "agent": {
            "config": {
                "CP_HUB_URL": f"http://127.0.0.1:{FAKE_HUB_PORT}",
                "CP_TIME_SYNC": "local",
                "CP_CLOUD_METADATA": "off",
                "CP_DOCKER": "off",
                "CP_REMOTE_UPDATE": "off",
                "CP_INTERVAL": "15s",
                "CP_PROVIDER": "other",
                "token": "synthetic value omitted",
            },
            "cases": agent_results,
        },
        "host_finish": host_context(),
        "limitations": [
            "Local loopback development measurements only; not representative of remote network latency, public fleet behavior, production capacity, or an SLO.",
            "The hub process is ready and health-checked before path sampling. First request per path is separated from the next 19 repeats; this is not a process-cold start.",
            "Every curl invocation uses a fresh process and TCP connection. Browser cache, operating-system cold page cache, browser rendering, SPA hash-route navigation/render time, and user-perceived load were not measured.",
            "HTTP body bytes are curl's received entity-body bytes (Content-Encoding is not auto-decoded); they exclude response headers, HTTP/1 framing, TCP/IP, and link overhead, so are not wire bytes.",
            "Percentiles use linear interpolation at (n-1)*p; with 20 samples p95 is not a stable tail-latency estimate. Sample maximum is not a guaranteed worst case.",
            "Agent CPU is a /proc/PID/stat utime+stime delta divided by SC_CLK_TCK and elapsed time, expressed as percent of one logical core. RSS/HWM are VmRSS/VmHWM from /proc/PID/status sampled nominally every 250 ms; observed sample maxima do not claim an absolute instantaneous peak.",
            "The agent once path reports only sample count/JSON key names; host metrics are not retained. Loop reports are drained by an owned loopback-only fake hub without parsing, forwarding, redirects, or payload logging.",
            "Native browser UI/render evidence is intentionally unmeasured (UX hold); no UI files, screenshots, release bins, installed agents, deployment candidates, live services, production databases, paid providers, or external network services were used.",
            "Load and thermal observations describe this host at measurement start/finish and can be affected by unrelated system activity.",
        ],
    }

    write_exclusive(stage / "build.log", build_log.read_bytes(), MAX_BUILD_LOG_BYTES)
    write_exclusive(stage / "measurement.json",
        (json.dumps(measurement, indent=2, sort_keys=True) + "\n").encode(),
        MAX_MEASUREMENT_JSON_BYTES)
    write_exclusive(stage / "repeat-command.txt",
        b"python3 scripts/measure-round31-performance.py\n", 1024)
    return measurement


def _lstat_if_present(path: Path) -> os.stat_result | None:
    try:
        return path.lstat()
    except FileNotFoundError:
        return None


def guard_evidence_paths(root: Path = ROOT) -> tuple[Path, Path]:
    """Reject symlinks/non-directories in evidence ancestors before any command launch."""
    root = root.resolve()
    current = root
    for component in (".verification", "round31", "performance-corrections"):
        current = current / component
        info = _lstat_if_present(current)
        if info is not None and (stat.S_ISLNK(info.st_mode) or not stat.S_ISDIR(info.st_mode)):
            raise RuntimeError(f"unsafe evidence ancestor: {current}")
    destination = root / ".verification" / "round31" / "performance"
    if _lstat_if_present(destination) is not None:
        raise FileExistsError(f"evidence destination exists; refusing to overwrite: {destination}")
    return destination, root / ".verification" / "round31" / "performance-corrections"


def ensure_stage_parent(root: Path = ROOT) -> Path:
    current = root.resolve()
    for component in (".verification", "round31", "performance-corrections"):
        current = current / component
        try:
            current.mkdir()
        except FileExistsError:
            pass
        info = current.lstat()
        if stat.S_ISLNK(info.st_mode) or not stat.S_ISDIR(info.st_mode):
            raise RuntimeError(f"unsafe evidence ancestor: {current}")
    return current


def cleanup_owned_stage(stage: Path, correction_dir: Path) -> None:
    if stage.parent != correction_dir or not stage.name.startswith(STAGE_PREFIX):
        raise RuntimeError("refusing cleanup outside this invocation's unique staging directory")
    info = _lstat_if_present(stage)
    if info is None:
        return
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISDIR(info.st_mode):
        raise RuntimeError("owned evidence staging path was replaced; leaving it untouched")
    shutil.rmtree(stage)


def _rename_noreplace(source_fd: int, source_name: str, destination_fd: int,
                      destination_name: str) -> None:
    libc = ctypes.CDLL(None, use_errno=True)
    renameat2 = getattr(libc, "renameat2", None)
    if renameat2 is None:
        raise RuntimeError("atomic RENAME_NOREPLACE is unavailable; refusing evidence publication")
    renameat2.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int,
                          ctypes.c_char_p, ctypes.c_uint]
    renameat2.restype = ctypes.c_int
    result = renameat2(source_fd, os.fsencode(source_name), destination_fd,
                       os.fsencode(destination_name), 1)  # RENAME_NOREPLACE
    if result != 0:
        error = ctypes.get_errno()
        if error == errno.EEXIST:
            raise FileExistsError(error, os.strerror(error), destination_name)
        raise OSError(error, os.strerror(error), destination_name)


def publish_no_replace(stage: Path, destination: Path, correction_dir: Path) -> None:
    if stage.parent != correction_dir or not stage.name.startswith(STAGE_PREFIX):
        raise RuntimeError("refusing to publish an unowned staging path")
    root = destination.parents[2]
    checked_destination, checked_corrections = guard_evidence_paths(root)
    if checked_destination != destination or checked_corrections != correction_dir:
        raise RuntimeError("evidence path changed during measurement")
    parent_fd = os.open(destination.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    source_fd = os.open(correction_dir, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        parent_info = os.fstat(parent_fd)
        source_info = os.fstat(source_fd)
        if (destination.parent.lstat().st_ino, destination.parent.lstat().st_dev) != (parent_info.st_ino, parent_info.st_dev):
            raise RuntimeError("evidence destination parent changed before publication")
        if (correction_dir.lstat().st_ino, correction_dir.lstat().st_dev) != (source_info.st_ino, source_info.st_dev):
            raise RuntimeError("evidence staging parent changed before publication")
        stage_info = stage.lstat()
        if stat.S_ISLNK(stage_info.st_mode) or not stat.S_ISDIR(stage_info.st_mode):
            raise RuntimeError("owned staging path is not a directory")
        _rename_noreplace(source_fd, stage.name, parent_fd, destination.name)
    finally:
        os.close(source_fd)
        os.close(parent_fd)


def main() -> int:
    install_cancellation_handlers()
    if sys.platform != "linux" or not Path("/proc/self/stat").is_file():
        raise RuntimeError("this harness requires Linux /proc process accounting")
    if not APPROVED_TMP.is_dir():
        raise RuntimeError("approved scratch directory /tmp/opencode is unavailable")
    destination, correction_dir = guard_evidence_paths()
    ensure_port_free(HUB_PORT)
    ensure_port_free(FAKE_HUB_PORT)
    correction_dir = ensure_stage_parent()
    stage: Path | None = None
    published = False
    try:
        stage = Path(tempfile.mkdtemp(prefix=STAGE_PREFIX, dir=correction_dir))
        with tempfile.TemporaryDirectory(prefix="round31-performance-", dir=APPROVED_TMP) as temp_name:
            scratch = Path(temp_name)
            measurement = build_and_measure(stage, scratch)
        publish_no_replace(stage, destination, correction_dir)
        published = True
    finally:
        if stage is not None and not published:
            cleanup_owned_stage(stage, correction_dir)

    print(f"PASS: source-built dev measurement completed; evidence: {destination.relative_to(ROOT)}")
    print(f"Hub SHA-256: {measurement['source']['hub_binary_sha256']}")
    print(f"Agent SHA-256: {measurement['source']['agent_binary_sha256']}")
    print(f"HTTP paths: {len(measurement['http']['routes'])}, {HTTP_SAMPLES} samples each")
    loop = measurement["agent"]["cases"]["normal_15s_loop"]
    print(f"Agent normal loop: {loop['sampling_window_s']:.2f}s, {loop['resource_observations']['sample_count']} resource samples, "
          f"{loop['fake_hub_observation']['request_counts_by_path'].get('/api/v1/agent/report', 0)} local reports")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except MeasurementCancelled as exc:
        print("STOP: cancelled; owned process groups, connections, and temporary files were cleaned up", file=sys.stderr)
        raise SystemExit(128 + exc.signum)
    except KeyboardInterrupt:
        print("STOP: interrupted; owned process groups and temporary files were cleaned up", file=sys.stderr)
        raise SystemExit(130)
    except Exception as exc:
        # Errors are intentionally generic; never print environment values,
        # authentication headers, report payloads, or host metric payloads.
        print(f"STOP: {type(exc).__name__}: {exc}", file=sys.stderr)
        raise SystemExit(1)
