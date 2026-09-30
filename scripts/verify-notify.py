#!/usr/bin/env python3
"""Send one real test message per configured notify platform.

Optional, operator-run tool (SPEC-v0.6 SS4): sends a single test
notification to each of Discord/Telegram/WhatsApp whose credentials are
configured, printing the result and a secret-free diagnostic message for
any failure. Never run in CI, and never contacts a real platform unless
the operator has supplied real credentials -- a platform with no
credentials configured is silently skipped, not treated as an error.

Credentials are never accepted as command-line arguments (visible in
`ps`/shell history on a shared host). They come from either environment
variables or a `--credentials-file` (KEY=VALUE lines, no shell
interpretation) whose permissions are checked before it is read. See
`load_credentials` and `_check_credentials_file_permissions`.

All output masks tokens, webhook URLs, and phone numbers -- see `mask`.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, NoReturn

# Environment variables credentials may be read from (SPEC-v0.6 SS4's
# "개선 b"). --credentials-file, if given, takes priority over these.
ENV_DISCORD_WEBHOOK_URL = "CP_VERIFY_DISCORD_WEBHOOK_URL"
ENV_TELEGRAM_BOT_TOKEN = "CP_VERIFY_TELEGRAM_BOT_TOKEN"
ENV_TELEGRAM_CHAT_ID = "CP_VERIFY_TELEGRAM_CHAT_ID"
ENV_WHATSAPP_ACCESS_TOKEN = "CP_VERIFY_WHATSAPP_ACCESS_TOKEN"
ENV_WHATSAPP_PHONE_NUMBER_ID = "CP_VERIFY_WHATSAPP_PHONE_NUMBER_ID"
ENV_WHATSAPP_TO = "CP_VERIFY_WHATSAPP_TO"
ENV_WHATSAPP_TEMPLATE_NAME = "CP_VERIFY_WHATSAPP_TEMPLATE_NAME"
ENV_WHATSAPP_TEMPLATE_LANG = "CP_VERIFY_WHATSAPP_TEMPLATE_LANG"

_CREDENTIAL_ENV_KEYS = (
    ENV_DISCORD_WEBHOOK_URL,
    ENV_TELEGRAM_BOT_TOKEN,
    ENV_TELEGRAM_CHAT_ID,
    ENV_WHATSAPP_ACCESS_TOKEN,
    ENV_WHATSAPP_PHONE_NUMBER_ID,
    ENV_WHATSAPP_TO,
    ENV_WHATSAPP_TEMPLATE_NAME,
    ENV_WHATSAPP_TEMPLATE_LANG,
)

# The only platform names --platform accepts; "all" runs every platform
# that has credentials configured.
_VALID_PLATFORMS = ("discord", "telegram", "whatsapp", "all")

_DEFAULT_TIMEOUT_SECONDS = 20.0

# A value that looks like a credential (bot-token shape, an https
# webhook/API URL, or a long opaque bearer-looking string) is rejected
# if it appears as a command-line argument value, regardless of which
# flag it was passed to -- credentials must only ever arrive via
# environment variables or --credentials-file.
_TOKEN_LIKE_PATTERNS = (
    re.compile(r"^\d{6,}:[A-Za-z0-9_-]{20,}$"),  # Telegram bot-token shape
    re.compile(r"^https?://", re.IGNORECASE),  # any webhook/API URL
    re.compile(r"^[A-Za-z0-9_-]{40,}$"),  # long opaque bearer-looking token
)


def _looks_like_token(value: str) -> bool:
    """Report whether value has the shape of a secret credential."""
    return any(pattern.match(value) for pattern in _TOKEN_LIKE_PATTERNS)


def mask(value: str, keep_start: int = 4, keep_end: int = 2) -> str:
    """Mask value for safe display, keeping only a few edge characters.

    A value shorter than keep_start + keep_end + 1 is fully masked (its
    length alone could otherwise leak information about a short secret).
    """
    if not value:
        return ""
    if len(value) <= keep_start + keep_end:
        return "*" * len(value)
    return f"{value[:keep_start]}{'*' * (len(value) - keep_start - keep_end)}{value[-keep_end:]}"


def mask_url(url: str) -> str:
    """Mask a URL's path/query, keeping only its scheme and host.

    A Discord webhook URL or a custom API base embeds its secret token
    in the path; this never prints anything past the host.
    """
    match = re.match(r"^(https?://[^/]+)", url)
    if not match:
        return mask(url)
    return f"{match.group(1)}/…"


def mask_phone(phone: str) -> str:
    """Mask a phone number, keeping only its last two digits."""
    return mask(phone, keep_start=0, keep_end=2)


class CredentialsFilePermissionError(Exception):
    """Raised when --credentials-file has unsafe ownership/permissions."""


class UnknownArgumentError(Exception):
    """Raised when an argument value looks like a secret credential."""


@dataclass(frozen=True)
class DiscordCredentials:
    """Discord webhook credentials."""

    webhook_url: str

    def masked(self) -> dict[str, str]:
        """Return a display-safe copy of these credentials' fields."""
        return {"webhook_url": mask_url(self.webhook_url)}


@dataclass(frozen=True)
class TelegramCredentials:
    """Telegram bot credentials."""

    bot_token: str
    chat_id: str

    def masked(self) -> dict[str, str]:
        """Return a display-safe copy of these credentials' fields."""
        return {"bot_token": mask(self.bot_token), "chat_id": self.chat_id}


@dataclass(frozen=True)
class WhatsAppCredentials:
    """WhatsApp Cloud API credentials."""

    access_token: str
    phone_number_id: str
    to: str
    template_name: str = ""
    template_lang: str = "en_US"

    def masked(self) -> dict[str, str]:
        """Return a display-safe copy of these credentials' fields."""
        return {
            "access_token": mask(self.access_token),
            "phone_number_id": mask(self.phone_number_id),
            "to": mask_phone(self.to),
            "template_name": self.template_name,
            "template_lang": self.template_lang,
        }


@dataclass(frozen=True)
class VerifyResult:
    """The outcome of one platform's test send.

    message_id/chat_id and cleaned are populated only when the platform
    supports the corresponding operation (see verify_discord/
    verify_telegram's own docstrings for each platform's limitations).
    """

    platform: str
    ok: bool
    detail: str = ""
    verified: bool = False
    message_id: str = ""
    cleaned: bool | None = None


def _check_credentials_file_permissions(path: Path) -> None:
    """Reject a credentials file not owned by, and private to, the caller.

    Raises CredentialsFilePermissionError if path is not owned by the
    current effective user, or if its mode grants any permission to
    group/other (i.e. anything beyond 0600).
    """
    stat_result = path.stat()
    if stat_result.st_uid != os.geteuid():
        raise CredentialsFilePermissionError(f"{path}: not owned by the current user")
    if stat_result.st_mode & 0o077:
        raise CredentialsFilePermissionError(f"{path}: must not be readable/writable by group or other (chmod 600)")


def _parse_credentials_file(path: Path) -> dict[str, str]:
    """Parse a KEY=VALUE credentials file with no shell interpretation.

    Blank lines and lines starting with "#" are skipped. Every other
    line must contain exactly one "=" separating a non-empty key from
    its value.
    """
    _check_credentials_file_permissions(path)
    result: dict[str, str] = {}
    for line_number, raw_line in enumerate(path.read_text(encoding="utf-8").splitlines(), start=1):
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        if "=" not in line:
            raise ValueError(f"{path}:{line_number}: expected KEY=VALUE")
        key, _, value = line.partition("=")
        key = key.strip()
        if not key:
            raise ValueError(f"{path}:{line_number}: empty key")
        result[key] = value
    return result


def load_credentials(
    credentials_file: Path | None,
    env: dict[str, str],
) -> dict[str, str]:
    """Resolve credential values, --credentials-file taking priority over env.

    Only known credential keys are considered; unrelated file/env entries
    are ignored rather than rejected, so a shared credentials file with
    extra unrelated keys still works.
    """
    resolved: dict[str, str] = {}
    if credentials_file is not None:
        file_values = _parse_credentials_file(credentials_file)
        for key in _CREDENTIAL_ENV_KEYS:
            if key in file_values:
                resolved[key] = file_values[key]
    for key in _CREDENTIAL_ENV_KEYS:
        if key not in resolved and key in env:
            resolved[key] = env[key]
    return resolved


def discord_credentials_from(values: dict[str, str]) -> DiscordCredentials | None:
    """Build DiscordCredentials from resolved values, or None if unset."""
    webhook_url = values.get(ENV_DISCORD_WEBHOOK_URL, "")
    if not webhook_url:
        return None
    return DiscordCredentials(webhook_url=webhook_url)


def telegram_credentials_from(values: dict[str, str]) -> TelegramCredentials | None:
    """Build TelegramCredentials from resolved values, or None if unset."""
    bot_token = values.get(ENV_TELEGRAM_BOT_TOKEN, "")
    chat_id = values.get(ENV_TELEGRAM_CHAT_ID, "")
    if not bot_token or not chat_id:
        return None
    return TelegramCredentials(bot_token=bot_token, chat_id=chat_id)


def whatsapp_credentials_from(values: dict[str, str]) -> WhatsAppCredentials | None:
    """Build WhatsAppCredentials from resolved values, or None if unset."""
    access_token = values.get(ENV_WHATSAPP_ACCESS_TOKEN, "")
    phone_number_id = values.get(ENV_WHATSAPP_PHONE_NUMBER_ID, "")
    to = values.get(ENV_WHATSAPP_TO, "")
    if not access_token or not phone_number_id or not to:
        return None
    return WhatsAppCredentials(
        access_token=access_token,
        phone_number_id=phone_number_id,
        to=to,
        template_name=values.get(ENV_WHATSAPP_TEMPLATE_NAME, ""),
        template_lang=values.get(ENV_WHATSAPP_TEMPLATE_LANG, "") or "en_US",
    )


@dataclass
class HTTPRequester:
    """Sends one HTTP request and returns (status, decoded body text).

    A thin wrapper over urllib so tests can point base URLs at a fake
    http.server instance instead of a real platform, without this
    script accepting any operator-supplied endpoint override (SPEC-v0.6
    SS4 deliberately has no such flag -- see module docstring).
    """

    timeout_seconds: float = _DEFAULT_TIMEOUT_SECONDS

    def post_json(self, url: str, payload: dict[str, object]) -> tuple[int, str]:
        """POST payload as JSON to url, returning (status, body text)."""
        data = json.dumps(payload).encode("utf-8")
        request = urllib.request.Request(url, data=data, method="POST", headers={"Content-Type": "application/json"})
        return self._send(request)

    def get(self, url: str) -> tuple[int, str]:
        """GET url, returning (status, body text)."""
        request = urllib.request.Request(url, method="GET")
        return self._send(request)

    def delete(self, url: str) -> tuple[int, str]:
        """DELETE url, returning (status, body text)."""
        request = urllib.request.Request(url, method="DELETE")
        return self._send(request)

    def _send(self, request: urllib.request.Request) -> tuple[int, str]:
        """Execute request, treating an HTTPError's body as a normal response."""
        try:
            with urllib.request.urlopen(request, timeout=self.timeout_seconds) as response:
                return response.status, response.read().decode("utf-8", errors="replace")
        except urllib.error.HTTPError as exc:
            return exc.code, exc.read().decode("utf-8", errors="replace")
        except urllib.error.URLError as exc:
            return 0, str(exc.reason)


def verify_discord(credentials: DiscordCredentials, requester: HTTPRequester, cleanup: bool = False) -> VerifyResult:
    """Send a test message to a Discord webhook, with optional read-back.

    Uses ?wait=true so Discord returns the created message body (with
    its id) instead of 204 No Content -- see
    https://discord.com/developers/docs/resources/webhook#execute-webhook.
    On a successful send, performs a read-back GET against
    .../messages/{id} to confirm the message still exists, then (if
    cleanup is set) deletes it via DELETE .../messages/{id}.
    """
    wait_url = credentials.webhook_url + ("&wait=true" if "?" in credentials.webhook_url else "?wait=true")
    status, body = requester.post_json(wait_url, {"content": "cloud-pulse verify-notify.py test message"})
    if not (200 <= status < 300):
        return VerifyResult(platform="discord", ok=False, detail=_diagnose_discord(status, body))

    message_id = _extract_json_field(body, ("id",))
    if not message_id:
        return VerifyResult(platform="discord", ok=True, detail="delivered (no message id in response to read back)")

    verified, verify_detail = _discord_read_back(credentials, requester, message_id)
    cleaned: bool | None = None
    if cleanup:
        cleaned = _discord_delete(credentials, requester, message_id)

    detail = "delivered" if not verify_detail else f"delivered; {verify_detail}"
    return VerifyResult(platform="discord", ok=True, detail=detail, verified=verified, message_id=message_id, cleaned=cleaned)


def _discord_read_back(credentials: DiscordCredentials, requester: HTTPRequester, message_id: str) -> tuple[bool, str]:
    """GET the message back and confirm it still carries an attachment.

    Returns (verified, detail) -- verified is False (never raises) if the
    message is missing or has no attachment; the caller still reports
    the original send as ok=True since Discord already accepted it.
    """
    base = credentials.webhook_url.split("?", 1)[0].rstrip("/")
    status, body = requester.get(f"{base}/messages/{message_id}")
    if not (200 <= status < 300):
        return False, f"read-back failed: {_diagnose_discord(status, body)}"
    try:
        parsed = json.loads(body)
    except json.JSONDecodeError:
        return False, "read-back failed: could not parse response"
    attachments = parsed.get("attachments") if isinstance(parsed, dict) else None
    if not attachments:
        return False, "read-back succeeded but no attachment found"
    return True, "read-back confirmed message and attachment"


def _discord_delete(credentials: DiscordCredentials, requester: HTTPRequester, message_id: str) -> bool:
    """DELETE the message, returning whether cleanup succeeded."""
    base = credentials.webhook_url.split("?", 1)[0].rstrip("/")
    status, _ = requester.delete(f"{base}/messages/{message_id}")
    return 200 <= status < 300


def verify_telegram(credentials: TelegramCredentials, requester: HTTPRequester, api_base: str, cleanup: bool = False) -> VerifyResult:
    """Send a plain test message via the Telegram Bot API's sendMessage.

    Telegram bots have no API to read a message back after sending it,
    so "verified" here means only that the send response's own
    result.message_id and result.chat.id fields are present and the
    chat id matches what was configured -- the ceiling SPEC-v0.7 SS2
    documents for this platform. With cleanup=True, deletes the sent
    message via deleteMessage (subject to Telegram's 48-hour window,
    https://core.telegram.org/bots/api#deletemessage).
    """
    url = f"{api_base}/bot{credentials.bot_token}/sendMessage"
    status, body = requester.post_json(url, {"chat_id": credentials.chat_id, "text": "cloud-pulse verify-notify.py test message"})
    parsed_ok, description = _parse_telegram_response(body)
    if not (200 <= status < 300 and parsed_ok):
        return VerifyResult(platform="telegram", ok=False, detail=_diagnose_telegram(status, description))

    message_id, chat_id, verified = _telegram_result_fields(body, credentials.chat_id)
    cleaned: bool | None = None
    if cleanup and message_id:
        cleaned = _telegram_delete(credentials, requester, api_base, message_id)

    detail = "delivered" if verified else "delivered; response fields incomplete or chat id mismatch"
    return VerifyResult(platform="telegram", ok=True, detail=detail, verified=verified, message_id=message_id, cleaned=cleaned)


def _telegram_result_fields(body: str, expected_chat_id: str) -> tuple[str, str, bool]:
    """Extract result.message_id/result.chat.id and report whether both
    are present and the chat id matches expected_chat_id."""
    try:
        parsed = json.loads(body)
    except json.JSONDecodeError:
        return "", "", False
    result = parsed.get("result") if isinstance(parsed, dict) else None
    if not isinstance(result, dict):
        return "", "", False
    message_id = str(result.get("message_id", "")) if result.get("message_id") is not None else ""
    chat = result.get("chat")
    chat_id = str(chat.get("id", "")) if isinstance(chat, dict) and chat.get("id") is not None else ""
    verified = bool(message_id) and bool(chat_id) and chat_id == str(expected_chat_id)
    return message_id, chat_id, verified


def _telegram_delete(credentials: TelegramCredentials, requester: HTTPRequester, api_base: str, message_id: str) -> bool:
    """Call deleteMessage, returning whether cleanup succeeded."""
    url = f"{api_base}/bot{credentials.bot_token}/deleteMessage"
    status, body = requester.post_json(url, {"chat_id": credentials.chat_id, "message_id": int(message_id)})
    ok, _ = _parse_telegram_response(body)
    return 200 <= status < 300 and ok


def verify_whatsapp(credentials: WhatsAppCredentials, requester: HTTPRequester, api_base: str) -> VerifyResult:
    """Send a plain text test message via the WhatsApp Cloud API.

    Uses the plain-text message path (no template) since verify-notify.py
    has no chart image to attach -- this only proves credentials/
    permissions, not the 24-hour-window template path (the manual
    checklist in the Settings UI covers that distinction).
    """
    url = f"{api_base}/v21.0/{credentials.phone_number_id}/messages"
    payload = {
        "messaging_product": "whatsapp",
        "to": credentials.to,
        "type": "text",
        "text": {"body": "cloud-pulse verify-notify.py test message"},
    }
    status, body = requester.post_json(url, payload)
    if 200 <= status < 300:
        return VerifyResult(platform="whatsapp", ok=True, detail="delivered")
    return VerifyResult(platform="whatsapp", ok=False, detail=_diagnose_whatsapp(status, body))


def _diagnose_discord(status: int, body: str) -> str:
    """Build a secret-free diagnostic string for a failed Discord send."""
    message = _extract_json_field(body, ("message",)) or "unknown error"
    if status == 404:
        return f"webhook not found (deleted or invalid): {message}"
    if status in (401, 403):
        return f"webhook unauthorized (token invalid/revoked): {message}"
    return f"HTTP {status}: {message}"


def _parse_telegram_response(body: str) -> tuple[bool, str]:
    """Parse Telegram's {"ok": bool, "description": str} response shape."""
    try:
        parsed = json.loads(body)
    except json.JSONDecodeError:
        return False, body[:200]
    if not isinstance(parsed, dict):
        return False, body[:200]
    return bool(parsed.get("ok")), str(parsed.get("description", ""))


def _diagnose_telegram(status: int, description: str) -> str:
    """Build a secret-free diagnostic string for a failed Telegram send."""
    lowered = description.lower()
    if status == 401 or "unauthorized" in lowered:
        return f"bot token invalid/revoked: {description}"
    if "chat not found" in lowered:
        return f"chat not found: {description}"
    if "blocked" in lowered:
        return f"bot was blocked by the recipient: {description}"
    if "not enough rights" in lowered or "member" in lowered:
        return f"bot lacks permission in this chat: {description}"
    return f"HTTP {status}: {description}" if description else f"HTTP {status}"


def _diagnose_whatsapp(status: int, body: str) -> str:
    """Build a secret-free diagnostic string for a failed WhatsApp send."""
    message = _extract_json_field(body, ("error", "message")) or "unknown error"
    code = _extract_json_field(body, ("error", "code"))
    if code == "190" or status == 401:
        return f"access token expired/invalid: {message}"
    if code in ("10", "200") or status == 403:
        return f"permission denied: {message}"
    if code == "131030":
        return f"recipient not in the allowed test-number list: {message}"
    if code == "131047":
        return f"24-hour customer-service window closed: {message}"
    if code in ("132000", "132001"):
        return f"template problem: {message}"
    return f"HTTP {status}: {message}"


def _extract_json_field(body: str, path: tuple[str, ...]) -> str:
    """Best-effort extraction of a nested JSON field, "" if unavailable."""
    try:
        node: object = json.loads(body)
    except json.JSONDecodeError:
        return ""
    for key in path:
        if not isinstance(node, dict) or key not in node:
            return ""
        node = node[key]
    return str(node) if node is not None else ""


def _validate_no_secret_arguments(argv: list[str]) -> None:
    """Reject any raw argv value that looks like a secret credential."""
    for value in argv:
        if value.startswith("-"):
            continue
        if _looks_like_token(value):
            raise UnknownArgumentError(
                "credentials must not be passed as command-line arguments; "
                "use environment variables or --credentials-file instead"
            )


class _RedactingArgumentParser(argparse.ArgumentParser):
    """ArgumentParser whose error messages never echo argument values.

    argparse's default "unrecognized arguments: <values>" message would
    print whatever the user typed, which may be a credential that did not
    match the token-shape heuristics. Only the usage line and a fixed
    explanation are printed instead.
    """

    def error(self, message: str) -> NoReturn:
        """Print a value-free error and exit with status 2."""
        if "unrecognized arguments" in message:
            message = (
                "unrecognized arguments (values not shown); credentials must be "
                "provided via environment variables or --credentials-file"
            )
        elif "invalid choice" in message:
            message = "invalid choice for --platform (see --help)"
        self.print_usage(sys.stderr)
        self.exit(2, f"{self.prog}: error: {message}\n")


def build_arg_parser() -> argparse.ArgumentParser:
    """Build the strict argument parser: only 4 flags, nothing else."""
    parser = _RedactingArgumentParser(
        prog="verify-notify.py",
        description="Send a real test notification to configured Discord/Telegram/WhatsApp channels.",
        allow_abbrev=False,
    )
    parser.add_argument(
        "--platform",
        choices=_VALID_PLATFORMS,
        default="all",
        help="Platform to test (default: all configured platforms).",
    )
    parser.add_argument(
        "--credentials-file",
        type=Path,
        default=None,
        help="Path to a KEY=VALUE credentials file (mode 0600, owned by the current user).",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=_DEFAULT_TIMEOUT_SECONDS,
        help=f"Per-request timeout in seconds (default: {_DEFAULT_TIMEOUT_SECONDS}).",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Resolve and print which platforms would be tested, without sending anything.",
    )
    parser.add_argument(
        "--cleanup",
        action="store_true",
        help="Delete the sent test message after verifying it (Discord/Telegram only; ignored for WhatsApp, which has no delete API).",
    )
    return parser


def parse_args(argv: list[str]) -> argparse.Namespace:
    """Parse argv strictly, rejecting unknown flags and secret-looking values."""
    _validate_no_secret_arguments(argv)
    parser = build_arg_parser()
    return parser.parse_args(argv)


@dataclass
class Verifier:
    """Resolves credentials and runs each platform's test send."""

    requester: HTTPRequester
    telegram_api_base: str = "https://api.telegram.org"
    whatsapp_api_base: str = "https://graph.facebook.com"
    discord_credentials: DiscordCredentials | None = None
    telegram_credentials: TelegramCredentials | None = None
    whatsapp_credentials: WhatsAppCredentials | None = None
    cleanup: bool = False
    _senders: dict[str, Callable[[], VerifyResult]] = field(init=False, repr=False)

    def __post_init__(self) -> None:
        """Wire each platform name to its bound send function."""
        self._senders = {
            "discord": self._send_discord,
            "telegram": self._send_telegram,
            "whatsapp": self._send_whatsapp,
        }

    def _send_discord(self) -> VerifyResult:
        """Send Discord's test message using this verifier's credentials."""
        if self.discord_credentials is None:
            return VerifyResult(platform="discord", ok=False, detail="no credentials configured")
        return verify_discord(self.discord_credentials, self.requester, cleanup=self.cleanup)

    def _send_telegram(self) -> VerifyResult:
        """Send Telegram's test message using this verifier's credentials."""
        if self.telegram_credentials is None:
            return VerifyResult(platform="telegram", ok=False, detail="no credentials configured")
        return verify_telegram(self.telegram_credentials, self.requester, self.telegram_api_base, cleanup=self.cleanup)

    def _send_whatsapp(self) -> VerifyResult:
        """Send WhatsApp's test message using this verifier's credentials."""
        if self.whatsapp_credentials is None:
            return VerifyResult(platform="whatsapp", ok=False, detail="no credentials configured")
        return verify_whatsapp(self.whatsapp_credentials, self.requester, self.whatsapp_api_base)

    def configured_platforms(self) -> list[str]:
        """List platform names that have credentials configured, in order."""
        platforms = []
        if self.discord_credentials is not None:
            platforms.append("discord")
        if self.telegram_credentials is not None:
            platforms.append("telegram")
        if self.whatsapp_credentials is not None:
            platforms.append("whatsapp")
        return platforms

    def run(self, platform: str) -> list[VerifyResult]:
        """Run the requested platform(s), skipping any with no credentials."""
        targets = self.configured_platforms() if platform == "all" else [platform]
        results = []
        for name in targets:
            sender = self._senders.get(name)
            if sender is None or name not in self.configured_platforms():
                continue
            results.append(sender())
        return results


def build_verifier(values: dict[str, str], timeout_seconds: float, cleanup: bool = False) -> Verifier:
    """Build a Verifier from resolved credential values."""
    return Verifier(
        requester=HTTPRequester(timeout_seconds=timeout_seconds),
        discord_credentials=discord_credentials_from(values),
        telegram_credentials=telegram_credentials_from(values),
        whatsapp_credentials=whatsapp_credentials_from(values),
        cleanup=cleanup,
    )


def _print_masked_credentials(verifier: Verifier) -> None:
    """Print which platforms are configured, with masked credential values."""
    if verifier.discord_credentials is not None:
        print(f"discord: configured ({verifier.discord_credentials.masked()})")
    if verifier.telegram_credentials is not None:
        print(f"telegram: configured ({verifier.telegram_credentials.masked()})")
    if verifier.whatsapp_credentials is not None:
        print(f"whatsapp: configured ({verifier.whatsapp_credentials.masked()})")


def main(argv: list[str] | None = None) -> int:
    """Resolve credentials, send each configured platform's test, and report."""
    raw_argv = list(sys.argv[1:] if argv is None else argv)
    try:
        args = parse_args(raw_argv)
    except UnknownArgumentError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    except SystemExit as exc:
        # argparse itself calls sys.exit(2) (via ArgumentParser.error) on
        # an unknown flag or invalid choice; argparse already printed
        # usage/error text to stderr, so just propagate its exit code.
        return exc.code if isinstance(exc.code, int) else 2

    try:
        values = load_credentials(args.credentials_file, dict(os.environ))
    except (CredentialsFilePermissionError, ValueError, OSError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    verifier = build_verifier(values, timeout_seconds=args.timeout, cleanup=args.cleanup)
    configured = verifier.configured_platforms()
    if not configured:
        print("no platform credentials configured; nothing to do (set env vars or --credentials-file)")
        return 0

    if args.dry_run:
        _print_masked_credentials(verifier)
        return 0

    if args.platform != "all" and args.platform not in configured:
        print(f"{args.platform}: no credentials configured, skipping")
        return 0

    results = verifier.run(args.platform)
    overall_ok = True
    for result in results:
        if result.ok:
            suffix = " [verified]" if result.verified else ""
            if result.cleaned is not None:
                suffix += " [cleaned up]" if result.cleaned else " [cleanup failed]"
            print(f"{result.platform}: OK ({result.detail}){suffix}")
        else:
            overall_ok = False
            print(f"{result.platform}: FAILED - {result.detail}")
    return 0 if overall_ok else 1


if __name__ == "__main__":
    sys.exit(main())
