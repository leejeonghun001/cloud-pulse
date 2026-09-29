#!/usr/bin/env python3
"""Serve local release assets for smoke and self-update tests.

The server deliberately binds only loopback and exposes a minimal subset of
GitHub Releases. It is a test fixture, never a production release service.
"""

from __future__ import annotations

import argparse
import http.server
import sys
import urllib.parse
from dataclasses import dataclass
from pathlib import Path
from typing import cast


@dataclass(frozen=True)
class ReleaseServerConfig:
    """Configuration shared by the fake release request handler."""

    assets_dir: Path
    latest_tag: str


def port_number(value: str) -> int:
    """Parse a valid TCP port for argparse."""
    try:
        port = int(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("port must be an integer") from exc
    if not 1 <= port <= 65535:
        raise argparse.ArgumentTypeError("port must be between 1 and 65535")
    return port


def resolve_asset_path(assets_dir: Path, tag: str, asset: str) -> Path | None:
    """Return an existing flat asset below assets_dir, rejecting traversal."""
    if not tag or not asset or any(separator in tag or separator in asset for separator in ("/", "\\")):
        return None
    root = assets_dir.resolve()
    candidate = (root / tag / asset).resolve()
    try:
        candidate.relative_to(root)
    except ValueError:
        return None
    if not candidate.is_file():
        return None
    return candidate


class ReleaseHTTPServer(http.server.ThreadingHTTPServer):
    """HTTP server carrying immutable request-handler configuration."""

    config: ReleaseServerConfig

    def __init__(self, address: tuple[str, int], config: ReleaseServerConfig) -> None:
        super().__init__(address, ReleaseRequestHandler)
        self.config = config


class ReleaseRequestHandler(http.server.BaseHTTPRequestHandler):
    """Implement fake latest/tag/download endpoints for local test assets."""

    server: ReleaseHTTPServer

    def log_message(self, format_string: str, *args: object) -> None:
        """Suppress routine fixture traffic; callers assert HTTP results."""

    def do_GET(self) -> None:  # noqa: N802 - required BaseHTTPRequestHandler API
        """Serve a release redirect, tag page, or verified local asset."""
        path = urllib.parse.unquote(urllib.parse.urlsplit(self.path).path)
        config = cast(ReleaseHTTPServer, self.server).config
        if path == "/releases/latest":
            self.send_response(http.HTTPStatus.FOUND)
            self.send_header("Location", f"/releases/tag/{config.latest_tag}")
            self.end_headers()
            return
        if path.startswith("/releases/tag/"):
            tag = path.removeprefix("/releases/tag/")
            self._send_bytes(http.HTTPStatus.OK, "text/plain", f"tag: {tag}\n".encode())
            return
        if path.startswith("/releases/download/"):
            rest = path.removeprefix("/releases/download/")
            parts = rest.split("/", 1)
            if len(parts) != 2:
                self.send_error(http.HTTPStatus.NOT_FOUND, "not found")
                return
            asset_path = resolve_asset_path(config.assets_dir, parts[0], parts[1])
            if asset_path is None:
                self.send_error(http.HTTPStatus.NOT_FOUND, "not found")
                return
            try:
                self._send_bytes(http.HTTPStatus.OK, "application/octet-stream", asset_path.read_bytes())
            except OSError:
                self.send_error(http.HTTPStatus.INTERNAL_SERVER_ERROR, "could not read asset")
            return
        self.send_error(http.HTTPStatus.NOT_FOUND, "not found")

    def _send_bytes(self, status: http.HTTPStatus, content_type: str, body: bytes) -> None:
        """Write one complete fixed-length response body."""
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    """Parse the test fixture's stable positional command-line interface."""
    parser = argparse.ArgumentParser(description="Serve local cloud-pulse release test assets.")
    parser.add_argument("port", type=port_number)
    parser.add_argument("assets_dir", type=Path)
    parser.add_argument("latest_tag")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    """Run the loopback-only fake release server until interrupted."""
    args = parse_args(argv)
    server = ReleaseHTTPServer(
        ("127.0.0.1", args.port),
        ReleaseServerConfig(assets_dir=args.assets_dir, latest_tag=args.latest_tag),
    )
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        return 0
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
