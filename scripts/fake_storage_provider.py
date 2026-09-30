#!/usr/bin/env python3
"""Fake combined Google Drive + Dropbox OAuth/API server for scripts/smoke.sh.

Never contacts real Google/Dropbox services. Serves the exact endpoint
paths internal/storageusage/googledrive and internal/storageusage/dropbox
call, redirected here via CP_STORAGE_ALLOW_CUSTOM_ENDPOINTS +
CP_STORAGE_FAKE_BASE_URL (see DECISIONS_LOG.md D-081):

  Google (device flow):
    POST /device/code           -> device_code/user_code/verification_url
    POST /token                 -> access_token/refresh_token (grant) or
                                    an error body (authorization_pending,
                                    once per account, to exercise the
                                    poller's RFC 8628 retry loop once
                                    before granting)
    GET  /drive/v3/about        -> storageQuota + user

  Dropbox (PKCE):
    POST /oauth2/token           -> access_token/refresh_token
    POST /2/users/get_space_usage    -> used/allocation
    POST /2/users/get_current_account -> email/name.display_name

State is entirely in-memory and keyed by nothing beyond "has /token been
polled once yet" (a single boolean) — sufficient for one smoke-test
account per provider, which is all scripts/smoke.sh ever creates.
"""

from __future__ import annotations

import argparse
import http.server
import json
import sys
import threading
from typing import cast


def port_number(value: str) -> int:
    """Parse a valid TCP port for argparse."""
    try:
        port = int(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("port must be an integer") from exc
    if not 1 <= port <= 65535:
        raise argparse.ArgumentTypeError("port must be between 1 and 65535")
    return port


class FakeStorageHTTPServer(http.server.ThreadingHTTPServer):
    """HTTP server tracking whether the Google device-flow poll has fired once."""

    google_poll_count: int
    google_poll_lock: threading.Lock

    def __init__(self, address: tuple[str, int]) -> None:
        super().__init__(address, FakeStorageRequestHandler)
        self.google_poll_count = 0
        self.google_poll_lock = threading.Lock()


class FakeStorageRequestHandler(http.server.BaseHTTPRequestHandler):
    """Serve the fixed set of Google/Dropbox endpoint paths smoke.sh needs."""

    server: FakeStorageHTTPServer

    def log_message(self, format_string: str, *args: object) -> None:
        """Suppress routine fixture traffic; smoke.sh asserts on hub-side behavior instead."""

    def do_GET(self) -> None:  # noqa: N802 - required BaseHTTPRequestHandler API
        """Return a small readiness response, or serve Google's GET-based about.get."""
        if self.path == "/healthz":
            self._write_json(http.HTTPStatus.OK, {"ok": True})
            return
        # Drive's about.get is a GET with a "fields" query parameter
        # (see googledrive.Client.FetchAbout) — path.split keeps this
        # match independent of query-string contents.
        if self.path.split("?", 1)[0] == "/drive/v3/about":
            _handle_google_about(self)
            return
        self.send_error(http.HTTPStatus.NOT_FOUND, "not found")

    def do_POST(self) -> None:  # noqa: N802 - required BaseHTTPRequestHandler API
        """Dispatch a fixed OAuth/API path to its fake response body."""
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            length = 0
        if length > 0:
            self.rfile.read(length)  # drain the body; none of these fakes need to parse it

        handler = _ROUTES.get(self.path)
        if handler is None:
            self.send_error(http.HTTPStatus.NOT_FOUND, f"unhandled fake storage path {self.path!r}")
            return
        handler(self)

    def _write_json(self, status: http.HTTPStatus, payload: object) -> None:
        """Send a compact JSON response with a correct content length."""
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def _handle_google_device_code(handler: FakeStorageRequestHandler) -> None:
    """POST /device/code: hand out a fixed device/user code pair."""
    handler._write_json(
        http.HTTPStatus.OK,
        {
            "device_code": "smoke-fake-device-code",
            "user_code": "SMOK-EFAK",
            "verification_url": "https://www.google.com/device",
            "expires_in": 1800,
            "interval": 1,
        },
    )


def _handle_google_token(handler: FakeStorageRequestHandler) -> None:
    """POST /token: authorization_pending once, then grant (RFC 8628-style)."""
    with handler.server.google_poll_lock:
        handler.server.google_poll_count += 1
        first_poll = handler.server.google_poll_count == 1
    if first_poll:
        handler._write_json(
            http.HTTPStatus.BAD_REQUEST,
            {"error": "authorization_pending"},
        )
        return
    handler._write_json(
        http.HTTPStatus.OK,
        {
            "access_token": "smoke-fake-google-access-token",
            "refresh_token": "smoke-fake-google-refresh-token",
            "expires_in": 3600,
        },
    )


def _handle_google_about(handler: FakeStorageRequestHandler) -> None:
    """GET /drive/v3/about: a fixed quota well past a 90% alert threshold."""
    handler._write_json(
        http.HTTPStatus.OK,
        {
            "storageQuota": {
                "limit": "1000000000",
                "usage": "950000000",
                "usageInDrive": "940000000",
                "usageInDriveTrash": "1000000",
            },
            "user": {
                "emailAddress": "smoke-fake-google@example.test",
                "displayName": "Smoke Fake Google Account",
            },
        },
    )


def _handle_dropbox_token(handler: FakeStorageRequestHandler) -> None:
    """POST /oauth2/token: grant immediately (Dropbox's PKCE exchange is one-shot)."""
    handler._write_json(
        http.HTTPStatus.OK,
        {
            "access_token": "smoke-fake-dropbox-access-token",
            "refresh_token": "smoke-fake-dropbox-refresh-token",
            "expires_in": 14400,
        },
    )


def _handle_dropbox_space_usage(handler: FakeStorageRequestHandler) -> None:
    """POST /2/users/get_space_usage: a fixed quota well past a 90% alert threshold."""
    handler._write_json(
        http.HTTPStatus.OK,
        {
            "used": 1900000000,
            "allocation": {".tag": "individual", "allocated": 2000000000},
        },
    )


def _handle_dropbox_current_account(handler: FakeStorageRequestHandler) -> None:
    """POST /2/users/get_current_account: a fixed display name/email."""
    handler._write_json(
        http.HTTPStatus.OK,
        {
            "email": "smoke-fake-dropbox@example.test",
            "name": {"display_name": "Smoke Fake Dropbox Account"},
        },
    )


_ROUTES = {
    "/device/code": _handle_google_device_code,
    "/token": _handle_google_token,
    "/oauth2/token": _handle_dropbox_token,
    "/2/users/get_space_usage": _handle_dropbox_space_usage,
    "/2/users/get_current_account": _handle_dropbox_current_account,
}


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    """Parse the fixture's stable positional command-line interface."""
    parser = argparse.ArgumentParser(description="Fake Google Drive + Dropbox OAuth/API server for scripts/smoke.sh.")
    parser.add_argument("port", type=port_number)
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    """Run the loopback-only fake storage-provider server until interrupted."""
    args = parse_args(argv)
    server = cast(FakeStorageHTTPServer, FakeStorageHTTPServer(("127.0.0.1", args.port)))
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        return 0
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
