#!/usr/bin/env python3
"""Receive local generic-webhook test notifications into a JSONL file.

This loopback-only fixture is used by scripts/smoke.sh. It validates received
JSON before appending it, allowing the smoke test to distinguish malformed
requests from no delivery.
"""

from __future__ import annotations

import argparse
import http.server
import json
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Mapping, cast


def port_number(value: str) -> int:
    """Parse a valid TCP port for argparse."""
    try:
        port = int(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("port must be an integer") from exc
    if not 1 <= port <= 65535:
        raise argparse.ArgumentTypeError("port must be between 1 and 65535")
    return port


def append_json_line(log_path: Path, raw: bytes) -> None:
    """Validate raw UTF-8 JSON and append it as exactly one JSONL record."""
    try:
        text = raw.decode("utf-8")
        json.loads(text)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ValueError("invalid JSON request body") from exc
    with log_path.open("a", encoding="utf-8") as log_file:
        log_file.write(text)
        log_file.write("\n")


@dataclass(frozen=True)
class WebhookServerConfig:
    """Configuration shared by the webhook request handler."""

    log_path: Path


class WebhookHTTPServer(http.server.ThreadingHTTPServer):
    """HTTP server carrying the append-only JSONL destination."""

    config: WebhookServerConfig

    def __init__(self, address: tuple[str, int], config: WebhookServerConfig) -> None:
        super().__init__(address, WebhookRequestHandler)
        self.config = config


class WebhookRequestHandler(http.server.BaseHTTPRequestHandler):
    """Serve a readiness endpoint and accept JSON webhook deliveries."""

    server: WebhookHTTPServer

    def log_message(self, format_string: str, *args: object) -> None:
        """Suppress routine fixture traffic; callers assert its JSONL log."""

    def do_GET(self) -> None:  # noqa: N802 - required BaseHTTPRequestHandler API
        """Return a small readiness response for smoke-test polling."""
        if self.path == "/healthz":
            self._write_json(http.HTTPStatus.OK, {"ok": True})
            return
        self.send_error(http.HTTPStatus.NOT_FOUND, "not found")

    def do_POST(self) -> None:  # noqa: N802 - required BaseHTTPRequestHandler API
        """Validate and record a JSON webhook body."""
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            self.send_error(http.HTTPStatus.BAD_REQUEST, "invalid content length")
            return
        if length < 0:
            self.send_error(http.HTTPStatus.BAD_REQUEST, "invalid content length")
            return
        raw = self.rfile.read(length)
        try:
            append_json_line(cast(WebhookHTTPServer, self.server).config.log_path, raw)
        except ValueError:
            self.send_error(http.HTTPStatus.BAD_REQUEST, "invalid json")
            return
        except OSError:
            self.send_error(http.HTTPStatus.INTERNAL_SERVER_ERROR, "could not write webhook log")
            return
        self._write_json(http.HTTPStatus.OK, {"ok": True})

    def _write_json(self, status: http.HTTPStatus, payload: Mapping[str, object]) -> None:
        """Send a compact JSON response with a correct content length."""
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    """Parse the fixture's stable positional command-line interface."""
    parser = argparse.ArgumentParser(description="Receive local cloud-pulse webhook test deliveries.")
    parser.add_argument("port", type=port_number)
    parser.add_argument("log_file", type=Path)
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    """Run the loopback-only webhook receiver until interrupted."""
    args = parse_args(argv)
    server = WebhookHTTPServer(("127.0.0.1", args.port), WebhookServerConfig(log_path=args.log_file))
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        return 0
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
