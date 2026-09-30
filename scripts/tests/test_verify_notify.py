"""Unit tests for scripts/verify-notify.py using only unittest.

Covers: argument rejection (unknown flags, secret-looking values),
credentials-file permission checks, file-over-env priority, output
masking, and per-platform diagnostic output against fake
http.server.HTTPServer instances (Discord/Telegram/WhatsApp response
shapes) -- never a real network call.
"""

from __future__ import annotations

import importlib.util
import json
import os
import sys
import tempfile
import threading
import io
import contextlib
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
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


VERIFY = load_script("verify-notify.py", "cloud_pulse_verify_notify_test")


class FakePlatformServer:
    """A minimal threaded HTTP server that returns one scripted response.

    Used as a stand-in for Discord/Telegram/WhatsApp's real endpoints;
    the test sets `response_status`/`response_body` before each request.
    """

    def __init__(self) -> None:
        self.response_status = 200
        self.response_body = "{}"
        self.last_path: str | None = None
        self.last_body: bytes = b""

        outer = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self) -> None:  # noqa: N802 - required BaseHTTPRequestHandler API
                length = int(self.headers.get("Content-Length", "0"))
                outer.last_path = self.path
                outer.last_body = self.rfile.read(length)
                body = outer.response_body.encode("utf-8")
                self.send_response(outer.response_status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, format_string: str, *args: object) -> None:
                """Suppress per-request logging noise in test output."""

        self._server = HTTPServer(("127.0.0.1", 0), Handler)
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()

    @property
    def base_url(self) -> str:
        """Return this fake server's http://127.0.0.1:<port> base URL."""
        host, port = self._server.server_address[:2]
        return f"http://{host}:{port}"

    def close(self) -> None:
        """Stop the server and join its thread."""
        self._server.shutdown()
        self._server.server_close()
        self._thread.join(timeout=5)


class MaskingTests(unittest.TestCase):
    """mask/mask_url/mask_phone never leak the full secret value."""

    def test_mask_keeps_only_edges(self) -> None:
        masked = VERIFY.mask("123456:TEST-TOKEN-abcdefgh")
        self.assertNotIn("TEST-TOKEN", masked)
        self.assertTrue(masked.startswith("1234"))
        self.assertTrue(masked.endswith("gh"))

    def test_mask_short_value_is_fully_masked(self) -> None:
        self.assertEqual(VERIFY.mask("abc"), "***")

    def test_mask_empty_value(self) -> None:
        self.assertEqual(VERIFY.mask(""), "")

    def test_mask_url_keeps_only_scheme_and_host(self) -> None:
        masked = VERIFY.mask_url("https://discord.com/api/webhooks/123456/SUPER-SECRET-TOKEN")
        self.assertEqual(masked, "https://discord.com/…")
        self.assertNotIn("SUPER-SECRET-TOKEN", masked)

    def test_mask_phone_keeps_last_two_digits_only(self) -> None:
        masked = VERIFY.mask_phone("15551234567")
        self.assertTrue(masked.endswith("67"))
        self.assertNotIn("15551234", masked)


class ArgumentValidationTests(unittest.TestCase):
    """The argument parser rejects unknown flags and secret-looking values."""

    def test_unknown_flag_rejected(self) -> None:
        with self.assertRaises(SystemExit):
            VERIFY.parse_args(["--not-a-real-flag", "x"])

    def test_token_like_positional_value_rejected(self) -> None:
        with self.assertRaises(VERIFY.UnknownArgumentError):
            VERIFY.parse_args(["https://discord.com/api/webhooks/1/abcdefghijklmnopqrstuvwxyz"])

    def test_bot_token_shaped_value_rejected(self) -> None:
        with self.assertRaises(VERIFY.UnknownArgumentError):
            VERIFY.parse_args(["123456:AAAAAAAAAAAAAAAAAAAAAAAAAAA"])

    def test_valid_flags_accepted(self) -> None:
        args = VERIFY.parse_args(["--platform", "discord", "--timeout", "5", "--dry-run", "--cleanup"])
        self.assertEqual(args.platform, "discord")
        self.assertEqual(args.timeout, 5.0)
        self.assertTrue(args.dry_run)
        self.assertTrue(args.cleanup)

    def test_cleanup_defaults_to_false(self) -> None:
        args = VERIFY.parse_args(["--platform", "discord"])
        self.assertFalse(args.cleanup)

    def test_invalid_platform_choice_rejected(self) -> None:
        with self.assertRaises(SystemExit):
            VERIFY.parse_args(["--platform", "bogus"])

    def test_unrecognized_argument_value_never_echoed(self) -> None:
        # A short secret that does not match the token-shape heuristics must
        # still never appear in argparse's error output.
        secret = "123456:SHORT-SECRET"
        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr), self.assertRaises(SystemExit) as ctx:
            VERIFY.parse_args(["--platform", "telegram", secret])
        self.assertEqual(ctx.exception.code, 2)
        self.assertNotIn(secret, stderr.getvalue())
        self.assertIn("values not shown", stderr.getvalue())

    def test_invalid_choice_value_never_echoed(self) -> None:
        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr), self.assertRaises(SystemExit):
            VERIFY.parse_args(["--platform", "tok-SECRET-VALUE"])
        self.assertNotIn("tok-SECRET-VALUE", stderr.getvalue())


class CredentialsFileTests(unittest.TestCase):
    """--credentials-file permission checks and KEY=VALUE parsing."""

    def test_rejects_world_readable_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "creds"
            path.write_text(f"{VERIFY.ENV_DISCORD_WEBHOOK_URL}=https://discord.com/api/webhooks/1/tok\n", encoding="utf-8")
            path.chmod(0o644)
            with self.assertRaises(VERIFY.CredentialsFilePermissionError):
                VERIFY.load_credentials(path, {})

    def test_accepts_owner_only_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "creds"
            path.write_text(f"{VERIFY.ENV_DISCORD_WEBHOOK_URL}=https://discord.com/api/webhooks/1/tok\n", encoding="utf-8")
            path.chmod(0o600)
            values = VERIFY.load_credentials(path, {})
            self.assertEqual(values[VERIFY.ENV_DISCORD_WEBHOOK_URL], "https://discord.com/api/webhooks/1/tok")

    def test_parses_comments_and_blank_lines(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "creds"
            path.write_text(
                "\n# a comment\n" f"{VERIFY.ENV_TELEGRAM_BOT_TOKEN}=123456:TEST-TOKEN\n" f"{VERIFY.ENV_TELEGRAM_CHAT_ID}=-100123\n",
                encoding="utf-8",
            )
            path.chmod(0o600)
            values = VERIFY.load_credentials(path, {})
            self.assertEqual(values[VERIFY.ENV_TELEGRAM_BOT_TOKEN], "123456:TEST-TOKEN")
            self.assertEqual(values[VERIFY.ENV_TELEGRAM_CHAT_ID], "-100123")

    def test_malformed_line_raises(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "creds"
            path.write_text("not-a-valid-line\n", encoding="utf-8")
            path.chmod(0o600)
            with self.assertRaises(ValueError):
                VERIFY.load_credentials(path, {})

    def test_file_takes_priority_over_env(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "creds"
            path.write_text(f"{VERIFY.ENV_DISCORD_WEBHOOK_URL}=https://discord.com/api/webhooks/1/from-file\n", encoding="utf-8")
            path.chmod(0o600)
            env = {VERIFY.ENV_DISCORD_WEBHOOK_URL: "https://discord.com/api/webhooks/1/from-env"}
            values = VERIFY.load_credentials(path, env)
            self.assertEqual(values[VERIFY.ENV_DISCORD_WEBHOOK_URL], "https://discord.com/api/webhooks/1/from-file")

    def test_env_used_when_no_file_given(self) -> None:
        env = {VERIFY.ENV_TELEGRAM_BOT_TOKEN: "123456:TEST-TOKEN", VERIFY.ENV_TELEGRAM_CHAT_ID: "-100123"}
        values = VERIFY.load_credentials(None, env)
        self.assertEqual(values[VERIFY.ENV_TELEGRAM_BOT_TOKEN], "123456:TEST-TOKEN")

    def test_no_credentials_at_all_yields_no_configured_platforms(self) -> None:
        verifier = VERIFY.build_verifier({}, timeout_seconds=5)
        self.assertEqual(verifier.configured_platforms(), [])


class DiscordVerifyTests(unittest.TestCase):
    """verify_discord against a fake Discord-shaped HTTP server."""

    def setUp(self) -> None:
        self.server = FakePlatformServer()
        self.addCleanup(self.server.close)

    def test_success(self) -> None:
        self.server.response_status = 200
        self.server.response_body = "{}"
        creds = VERIFY.DiscordCredentials(webhook_url=self.server.base_url + "/api/webhooks/1/tok")
        result = VERIFY.verify_discord(creds, VERIFY.HTTPRequester(timeout_seconds=5))
        self.assertTrue(result.ok)

    def test_unknown_webhook_404(self) -> None:
        self.server.response_status = 404
        self.server.response_body = json.dumps({"code": 10015, "message": "Unknown Webhook"})
        creds = VERIFY.DiscordCredentials(webhook_url=self.server.base_url + "/api/webhooks/1/tok")
        result = VERIFY.verify_discord(creds, VERIFY.HTTPRequester(timeout_seconds=5))
        self.assertFalse(result.ok)
        self.assertIn("not found", result.detail)
        self.assertIn("Unknown Webhook", result.detail)

    def test_invalid_webhook_token_401(self) -> None:
        self.server.response_status = 401
        self.server.response_body = json.dumps({"code": 50027, "message": "Invalid Webhook Token"})
        creds = VERIFY.DiscordCredentials(webhook_url=self.server.base_url + "/api/webhooks/1/tok")
        result = VERIFY.verify_discord(creds, VERIFY.HTTPRequester(timeout_seconds=5))
        self.assertFalse(result.ok)
        self.assertIn("unauthorized", result.detail)

    def test_no_secret_in_diagnostic(self) -> None:
        self.server.response_status = 401
        self.server.response_body = json.dumps({"code": 50027, "message": "Invalid Webhook Token"})
        secret_token = "SUPER-SECRET-SHOULD-NEVER-APPEAR"
        creds = VERIFY.DiscordCredentials(webhook_url=f"{self.server.base_url}/api/webhooks/1/{secret_token}")
        result = VERIFY.verify_discord(creds, VERIFY.HTTPRequester(timeout_seconds=5))
        self.assertNotIn(secret_token, result.detail)

    def test_read_back_confirms_attachment(self) -> None:
        # A single fake server can't easily distinguish "the initial
        # ?wait=true POST" from "the follow-up GET read-back" by path
        # alone since FakePlatformServer only implements do_POST -- so
        # this test uses a server subclass that also serves GET, wired
        # directly rather than through FakePlatformServer.
        server = _DiscordReadBackFakeServer(message_id="42", attachments=[{"id": "9"}])
        self.addCleanup(server.close)
        creds = VERIFY.DiscordCredentials(webhook_url=server.base_url + "/api/webhooks/1/tok")
        result = VERIFY.verify_discord(creds, VERIFY.HTTPRequester(timeout_seconds=5))
        self.assertTrue(result.ok)
        self.assertTrue(result.verified)
        self.assertEqual(result.message_id, "42")

    def test_read_back_message_missing(self) -> None:
        server = _DiscordReadBackFakeServer(message_id="42", get_status=404, get_body='{"message":"Unknown Message"}')
        self.addCleanup(server.close)
        creds = VERIFY.DiscordCredentials(webhook_url=server.base_url + "/api/webhooks/1/tok")
        result = VERIFY.verify_discord(creds, VERIFY.HTTPRequester(timeout_seconds=5))
        # The original send still succeeded (ok=True); only the
        # read-back confirmation failed.
        self.assertTrue(result.ok)
        self.assertFalse(result.verified)
        self.assertIn("read-back failed", result.detail)

    def test_read_back_attachment_missing(self) -> None:
        server = _DiscordReadBackFakeServer(message_id="42", attachments=[])
        self.addCleanup(server.close)
        creds = VERIFY.DiscordCredentials(webhook_url=server.base_url + "/api/webhooks/1/tok")
        result = VERIFY.verify_discord(creds, VERIFY.HTTPRequester(timeout_seconds=5))
        self.assertTrue(result.ok)
        self.assertFalse(result.verified)
        self.assertIn("no attachment", result.detail)

    def test_cleanup_calls_delete(self) -> None:
        server = _DiscordReadBackFakeServer(message_id="42", attachments=[{"id": "9"}])
        self.addCleanup(server.close)
        creds = VERIFY.DiscordCredentials(webhook_url=server.base_url + "/api/webhooks/1/tok")
        result = VERIFY.verify_discord(creds, VERIFY.HTTPRequester(timeout_seconds=5), cleanup=True)
        self.assertTrue(result.ok)
        self.assertTrue(result.cleaned)
        self.assertEqual(server.delete_calls, 1)

    def test_no_cleanup_by_default(self) -> None:
        server = _DiscordReadBackFakeServer(message_id="42", attachments=[{"id": "9"}])
        self.addCleanup(server.close)
        creds = VERIFY.DiscordCredentials(webhook_url=server.base_url + "/api/webhooks/1/tok")
        result = VERIFY.verify_discord(creds, VERIFY.HTTPRequester(timeout_seconds=5))
        self.assertIsNone(result.cleaned)
        self.assertEqual(server.delete_calls, 0)


class _DiscordReadBackFakeServer:
    """A fake Discord webhook server supporting POST (send), GET
    (read-back), and DELETE (cleanup) -- used only where DiscordVerifyTests
    needs to distinguish those three calls, which FakePlatformServer (POST
    only) cannot do.
    """

    def __init__(
        self,
        message_id: str,
        attachments: list[dict[str, object]] | None = None,
        get_status: int = 200,
        get_body: str | None = None,
    ) -> None:
        self.message_id = message_id
        self.attachments = attachments if attachments is not None else []
        self.get_status = get_status
        self.get_body = get_body
        self.delete_calls = 0

        outer = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self) -> None:  # noqa: N802
                length = int(self.headers.get("Content-Length", "0"))
                self.rfile.read(length)
                body = json.dumps({"id": outer.message_id, "attachments": outer.attachments}).encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_GET(self) -> None:  # noqa: N802
                body_text = outer.get_body if outer.get_body is not None else json.dumps({"id": outer.message_id, "attachments": outer.attachments})
                body = body_text.encode("utf-8")
                self.send_response(outer.get_status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_DELETE(self) -> None:  # noqa: N802
                outer.delete_calls += 1
                self.send_response(204)
                self.send_header("Content-Length", "0")
                self.end_headers()

            def log_message(self, format_string: str, *args: object) -> None:
                """Suppress per-request logging noise in test output."""

        self._server = HTTPServer(("127.0.0.1", 0), Handler)
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()

    @property
    def base_url(self) -> str:
        """Return this fake server's http://127.0.0.1:<port> base URL."""
        host, port = self._server.server_address[:2]
        return f"http://{host}:{port}"

    def close(self) -> None:
        """Stop the server and join its thread."""
        self._server.shutdown()
        self._server.server_close()
        self._thread.join(timeout=5)


class TelegramVerifyTests(unittest.TestCase):
    """verify_telegram against a fake Telegram-Bot-API-shaped HTTP server."""

    def setUp(self) -> None:
        self.server = FakePlatformServer()
        self.addCleanup(self.server.close)

    def test_success(self) -> None:
        self.server.response_status = 200
        self.server.response_body = json.dumps({"ok": True, "result": {}})
        creds = VERIFY.TelegramCredentials(bot_token="123456:TEST-TOKEN", chat_id="-100123")
        result = VERIFY.verify_telegram(creds, VERIFY.HTTPRequester(timeout_seconds=5), self.server.base_url)
        self.assertTrue(result.ok)

    def test_chat_not_found(self) -> None:
        self.server.response_status = 400
        self.server.response_body = json.dumps({"ok": False, "error_code": 400, "description": "Bad Request: chat not found"})
        creds = VERIFY.TelegramCredentials(bot_token="123456:TEST-TOKEN", chat_id="-100123")
        result = VERIFY.verify_telegram(creds, VERIFY.HTTPRequester(timeout_seconds=5), self.server.base_url)
        self.assertFalse(result.ok)
        self.assertIn("chat not found", result.detail)

    def test_bot_blocked(self) -> None:
        self.server.response_status = 403
        self.server.response_body = json.dumps({"ok": False, "error_code": 403, "description": "Forbidden: bot was blocked by the user"})
        creds = VERIFY.TelegramCredentials(bot_token="123456:TEST-TOKEN", chat_id="-100123")
        result = VERIFY.verify_telegram(creds, VERIFY.HTTPRequester(timeout_seconds=5), self.server.base_url)
        self.assertFalse(result.ok)
        self.assertIn("blocked", result.detail)

    def test_unauthorized(self) -> None:
        self.server.response_status = 401
        self.server.response_body = json.dumps({"ok": False, "error_code": 401, "description": "Unauthorized"})
        creds = VERIFY.TelegramCredentials(bot_token="123456:TEST-TOKEN", chat_id="-100123")
        result = VERIFY.verify_telegram(creds, VERIFY.HTTPRequester(timeout_seconds=5), self.server.base_url)
        self.assertFalse(result.ok)
        self.assertIn("token invalid", result.detail)

    def test_no_secret_in_diagnostic(self) -> None:
        self.server.response_status = 401
        self.server.response_body = json.dumps({"ok": False, "error_code": 401, "description": "Unauthorized"})
        creds = VERIFY.TelegramCredentials(bot_token="123456:SUPER-SECRET-TOKEN-VALUE", chat_id="-100123")
        result = VERIFY.verify_telegram(creds, VERIFY.HTTPRequester(timeout_seconds=5), self.server.base_url)
        self.assertNotIn("SUPER-SECRET-TOKEN-VALUE", result.detail)

    def test_read_back_fields_confirm_chat_id_match(self) -> None:
        self.server.response_status = 200
        self.server.response_body = json.dumps({"ok": True, "result": {"message_id": 555, "chat": {"id": -100123}}})
        creds = VERIFY.TelegramCredentials(bot_token="123456:TEST-TOKEN", chat_id="-100123")
        result = VERIFY.verify_telegram(creds, VERIFY.HTTPRequester(timeout_seconds=5), self.server.base_url)
        self.assertTrue(result.ok)
        self.assertTrue(result.verified)
        self.assertEqual(result.message_id, "555")

    def test_chat_id_mismatch_reported_as_unverified(self) -> None:
        self.server.response_status = 200
        # Response reports a different chat id than what was configured.
        self.server.response_body = json.dumps({"ok": True, "result": {"message_id": 555, "chat": {"id": -999999}}})
        creds = VERIFY.TelegramCredentials(bot_token="123456:TEST-TOKEN", chat_id="-100123")
        result = VERIFY.verify_telegram(creds, VERIFY.HTTPRequester(timeout_seconds=5), self.server.base_url)
        self.assertTrue(result.ok)
        self.assertFalse(result.verified)
        self.assertIn("mismatch", result.detail)

    def test_cleanup_calls_delete_message(self) -> None:
        calls: list[str] = []

        self.server.response_status = 200
        self.server.response_body = json.dumps({"ok": True, "result": {"message_id": 555, "chat": {"id": -100123}}})
        creds = VERIFY.TelegramCredentials(bot_token="123456:TEST-TOKEN", chat_id="-100123")

        original_post_json = VERIFY.HTTPRequester.post_json

        def recording_post_json(self: VERIFY.HTTPRequester, url: str, payload: dict[str, object]) -> tuple[int, str]:
            calls.append(url)
            return original_post_json(self, url, payload)

        VERIFY.HTTPRequester.post_json = recording_post_json  # type: ignore[method-assign]
        try:
            result = VERIFY.verify_telegram(creds, VERIFY.HTTPRequester(timeout_seconds=5), self.server.base_url, cleanup=True)
        finally:
            VERIFY.HTTPRequester.post_json = original_post_json  # type: ignore[method-assign]

        self.assertTrue(result.ok)
        self.assertTrue(result.cleaned)
        self.assertTrue(any("deleteMessage" in url for url in calls))
        self.assertTrue(any("sendMessage" in url for url in calls))


class WhatsAppVerifyTests(unittest.TestCase):
    """verify_whatsapp against a fake Graph-API-shaped HTTP server."""

    def setUp(self) -> None:
        self.server = FakePlatformServer()
        self.addCleanup(self.server.close)

    def test_success(self) -> None:
        self.server.response_status = 200
        self.server.response_body = json.dumps({"messages": [{"id": "wamid.abc"}]})
        creds = VERIFY.WhatsAppCredentials(access_token="tok", phone_number_id="123", to="15551234567")
        result = VERIFY.verify_whatsapp(creds, VERIFY.HTTPRequester(timeout_seconds=5), self.server.base_url)
        self.assertTrue(result.ok)

    def test_token_expired_190(self) -> None:
        self.server.response_status = 401
        self.server.response_body = json.dumps({"error": {"message": "Error validating access token", "type": "OAuthException", "code": 190}})
        creds = VERIFY.WhatsAppCredentials(access_token="tok", phone_number_id="123", to="15551234567")
        result = VERIFY.verify_whatsapp(creds, VERIFY.HTTPRequester(timeout_seconds=5), self.server.base_url)
        self.assertFalse(result.ok)
        self.assertIn("expired", result.detail)

    def test_recipient_not_allowed_131030(self) -> None:
        self.server.response_status = 400
        self.server.response_body = json.dumps({"error": {"message": "Recipient not allowed", "type": "OAuthException", "code": 131030}})
        creds = VERIFY.WhatsAppCredentials(access_token="tok", phone_number_id="123", to="15551234567")
        result = VERIFY.verify_whatsapp(creds, VERIFY.HTTPRequester(timeout_seconds=5), self.server.base_url)
        self.assertFalse(result.ok)
        self.assertIn("allowed test-number", result.detail)

    def test_window_closed_131047(self) -> None:
        self.server.response_status = 400
        self.server.response_body = json.dumps({"error": {"message": "Re-engagement message", "type": "OAuthException", "code": 131047}})
        creds = VERIFY.WhatsAppCredentials(access_token="tok", phone_number_id="123", to="15551234567")
        result = VERIFY.verify_whatsapp(creds, VERIFY.HTTPRequester(timeout_seconds=5), self.server.base_url)
        self.assertFalse(result.ok)
        self.assertIn("window closed", result.detail)

    def test_no_secret_in_diagnostic(self) -> None:
        self.server.response_status = 401
        self.server.response_body = json.dumps({"error": {"message": "Error validating access token", "type": "OAuthException", "code": 190}})
        creds = VERIFY.WhatsAppCredentials(access_token="SUPER-SECRET-ACCESS-TOKEN", phone_number_id="123", to="15551234567")
        result = VERIFY.verify_whatsapp(creds, VERIFY.HTTPRequester(timeout_seconds=5), self.server.base_url)
        self.assertNotIn("SUPER-SECRET-ACCESS-TOKEN", result.detail)


class VerifierRunTests(unittest.TestCase):
    """Verifier.run's platform-selection/skip behavior."""

    def test_run_all_only_covers_configured_platforms(self) -> None:
        server = FakePlatformServer()
        self.addCleanup(server.close)
        server.response_status = 200
        server.response_body = json.dumps({"ok": True})

        verifier = VERIFY.Verifier(
            requester=VERIFY.HTTPRequester(timeout_seconds=5),
            telegram_api_base=server.base_url,
            telegram_credentials=VERIFY.TelegramCredentials(bot_token="123456:TEST-TOKEN", chat_id="-100123"),
        )
        results = verifier.run("all")
        self.assertEqual([r.platform for r in results], ["telegram"])

    def test_run_specific_platform_with_no_credentials_returns_nothing(self) -> None:
        verifier = VERIFY.Verifier(requester=VERIFY.HTTPRequester(timeout_seconds=5))
        self.assertEqual(verifier.run("discord"), [])


class MainEntryPointTests(unittest.TestCase):
    """main()'s exit codes for the top-level flows that don't need a network."""

    def test_no_credentials_configured_exits_zero(self) -> None:
        env_backup = {key: os.environ.pop(key, None) for key in VERIFY._CREDENTIAL_ENV_KEYS}
        try:
            exit_code = VERIFY.main([])
        finally:
            for key, value in env_backup.items():
                if value is not None:
                    os.environ[key] = value
        self.assertEqual(exit_code, 0)

    def test_unknown_flag_exits_nonzero(self) -> None:
        exit_code = VERIFY.main(["--bogus"])
        self.assertEqual(exit_code, 2)

    def test_secret_looking_argument_exits_nonzero(self) -> None:
        exit_code = VERIFY.main(["123456:AAAAAAAAAAAAAAAAAAAAAAAAAAA"])
        self.assertEqual(exit_code, 2)

    def test_unreadable_credentials_file_exits_nonzero(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "creds"
            path.write_text(f"{VERIFY.ENV_DISCORD_WEBHOOK_URL}=https://discord.com/api/webhooks/1/tok\n", encoding="utf-8")
            path.chmod(0o644)
            exit_code = VERIFY.main(["--credentials-file", str(path)])
            self.assertEqual(exit_code, 2)


if __name__ == "__main__":
    unittest.main()
