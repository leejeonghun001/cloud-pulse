#!/usr/bin/env python3
"""scripts/prepublish-check.py — public-repository final-check scanner.

Scans the current tracked tree and (optionally) the full git history
of this repository for three classes of problems a public open-source
mirror must never contain:

1. **Secrets** — API keys, tokens, webhook URLs, private-key blocks,
   JWTs, and generic high-entropy ``password=``/``token=``-shaped
   assignments. A match's actual value is never printed: only its
   first 4 characters, its length, and the first 12 hex characters of
   its sha256 digest are shown (see ``Finding.redacted``).
2. **Internal information** — absolute home directories
   (``/home/<user>``, ``/Users/<user>``, ``C:\\Users\\<name>``), this
   project's own orchestrator working-directory name (deliberately
   never written as a literal in this file — see
   ``_ORCHESTRATOR_DIR_NAME``), and host IP addresses that are not one
   of: loopback, a network/CIDR notation, or an IETF-documentation
   range (RFC 5737 IPv4, RFC 3849 IPv6).
3. **Repository hygiene** — LICENSE/SECURITY.md/CODE_OF_CONDUCT.md
   presence and content, ``.github`` templates, ``check-docs.py``
   passing, no committed blob over 5 MiB anywhere in history, no
   tracked binary executables, and a minimum set of ``.gitignore``
   entries.

Tree-mode findings are always a hard failure (exit 1). History-only
findings are checked against ``.prepublish-baseline.json`` (a file
that stores only the rule id + commit + path + a value-free sha256 key derived from the rule and source location, never
the matched text or a hash of it): a finding already in
the baseline is downgraded to a warning, a new one is still a failure.
History cannot be un-published by rewriting it (a force-push is
destructive to any existing clone and this tool does not attempt it —
see ``docs/security.md``), so the baseline is how this tool tracks
"already known, already public, consciously not scrubbed" history
findings across runs without re-litigating them every time.

Local-only allowances (a real hostname, a real email, a real IP this
operator doesn't want the scanner to flag) come from a git-ignored
``.prepublish-markers.local`` file (one marker per line) or the
``CP_PREPUBLISH_MARKERS`` environment variable (comma/newline-separated) —
never from anything committed, since committing the marker would be
the leak itself.

Usage::

    python3 scripts/prepublish-check.py [--tree-only] [--history-only]
        [--report FILE] [--baseline FILE] [--update-baseline]

Exit status is 0 if there are no failing findings (history-only
findings already present in the baseline are warnings, not failures),
non-zero otherwise.
"""

from __future__ import annotations

import argparse
import hashlib
import ipaddress
import json
import math
import re
import struct
import subprocess
import sys
import zlib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, Iterable, Iterator, NoReturn

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

# This project's own orchestrator working-directory name, built at
# runtime from parts so this scanner file never contains the literal
# string itself (it would otherwise trip its own internal-info rule
# the moment this file is scanned).
_ORCHESTRATOR_DIR_NAME = "." + "cloud-pulse-orchestrator"

MAX_BLOB_BYTES = 5 * 1024 * 1024  # 5 MiB

REQUIRED_GITIGNORE_ENTRIES = ("data/", "dist/", "bin/", "*.db")

# Extensions that, if a tracked file's content sniffs as a native
# binary executable, are hygiene failures (a script with an executable
# bit but text/shebang content is fine and common in this repo).
_BINARY_MAGIC = (
    b"\x7fELF",  # Linux ELF
    b"MZ",  # Windows PE (also DOS stub)
    b"\xca\xfe\xba\xbe",  # Mach-O fat binary
    b"\xfe\xed\xfa\xce",  # Mach-O 32-bit
    b"\xfe\xed\xfa\xcf",  # Mach-O 64-bit
    b"\xcf\xfa\xed\xfe",  # Mach-O 64-bit (reversed)
    b"\xce\xfa\xed\xfe",  # Mach-O 32-bit (reversed)
)

# Allowlisted CI/service accounts that legitimately look like a
# "personal" account name but are not this operator's own — each entry
# must carry a reason, matching the "no allowance without a reason"
# policy this tool applies uniformly to secrets and internal-info both.
# Matching is case-insensitive since Windows usernames/paths are.
_ACCOUNT_ALLOWLIST: dict[str, str] = {
    "runneradmin": "default Windows account name on GitHub-hosted windows-latest runners, not a real user",
    "runner": "default account name on GitHub-hosted Linux/macOS runners, not a real user",
    "_cloudpulse": "this project's own macOS service account name, not a personal user",
    "bob": "synthetic placeholder username used by windows-path containment test fixtures",
    "bob2": "synthetic placeholder username used by windows-path containment test fixtures",
}

# Fake-value markers: if any of these (case-insensitive) appear inside
# a matched secret-shaped string, or the line carries an inline
# "allow-secret-scan: <reason>" comment, the match is not reported as a
# real secret.
_FAKE_VALUE_MARKERS = ("test", "fake", "example", "0123456789", "dummy", "ci-")

_ALLOW_SECRET_SCAN_RE = re.compile(r"allow-secret-scan:\s*(\S.*)$")

# IETF documentation ranges (RFC 5737 IPv4, RFC 3849 IPv6) plus
# loopback — always allowed regardless of context.
_DOC_IPV4_PREFIXES = ("192.0.2.", "198.51.100.", "203.0.113.")
_DOC_IPV6_PREFIX = "2001:db8:"

# ---------------------------------------------------------------------------
# Secret detection rules
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class SecretRule:
    """A single secret-shaped pattern to search for.

    ``pattern`` must contain at most one capturing group; if present,
    the captured group is what gets hashed/redacted (e.g. to exclude a
    fixed prefix like ``AKIA`` from the "looks like a real credential"
    entropy check without losing it from the reported match text).
    """

    rule_id: str
    description: str
    pattern: re.Pattern[str]
    min_entropy: float = 0.0


def _shannon_entropy(s: str) -> float:
    if not s:
        return 0.0
    counts: dict[str, int] = {}
    for ch in s:
        counts[ch] = counts.get(ch, 0) + 1
    length = len(s)
    entropy = 0.0
    for count in counts.values():
        p = count / length
        entropy -= p * math.log2(p)
    return entropy


SECRET_RULES: list[SecretRule] = [
    SecretRule(
        "secret.aws_access_key",
        "AWS access key ID",
        re.compile(r"\b((?:AKIA|ASIA)[A-Z0-9]{16})\b"),
    ),
    SecretRule(
        "secret.github_token",
        "GitHub personal/app/OAuth/refresh token",
        re.compile(r"\b((?:ghp|gho|ghs|ghu|ghr)_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{20,})\b"),
    ),
    SecretRule(
        "secret.slack_token",
        "Slack token",
        re.compile(r"\b(xox[abprs]-[A-Za-z0-9-]{10,})\b"),
    ),
    SecretRule(
        "secret.discord_webhook",
        "Discord webhook URL",
        re.compile(
            r"(https://discord(?:app)?\.com/api/webhooks/\d{17,20}/[A-Za-z0-9_-]{60,})"
        ),
    ),
    SecretRule(
        "secret.telegram_bot_token",
        "Telegram bot token",
        re.compile(r"\b(\d{8,10}:[A-Za-z0-9_-]{35})\b"),
    ),
    SecretRule(
        "secret.google_oauth_client_secret",
        "Google OAuth client secret",
        re.compile(r"\b(GOCSPX-[A-Za-z0-9_-]{20,})\b"),
    ),
    SecretRule(
        "secret.dropbox_token",
        "Dropbox long-lived token",
        re.compile(r"\b(sl\.[A-Za-z0-9_-]{60,})\b"),
    ),
    SecretRule(
        "secret.private_key_block",
        "PEM private key block",
        re.compile(r"(-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----)"),
    ),
    SecretRule(
        "secret.jwt",
        "JSON Web Token",
        re.compile(r"\b(eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,})\b"),
    ),
    SecretRule(
        "secret.generic_high_entropy_assignment",
        "generic password/secret/token/api_key assignment with high-entropy value",
        re.compile(
            r"(?i)\b(?:password|secret|token|api_key)\b\s*[:=]\s*['\"]?([A-Za-z0-9+/_.-]{20,})['\"]?"
        ),
        min_entropy=4.0,
    ),
]


def _is_fake_value(value: str, line: str) -> bool:
    lowered = value.lower()
    if any(marker in lowered for marker in _FAKE_VALUE_MARKERS):
        return True
    return bool(_ALLOW_SECRET_SCAN_RE.search(line))


def _allow_reason(line: str) -> str | None:
    m = _ALLOW_SECRET_SCAN_RE.search(line)
    if m:
        return m.group(1).strip()
    return None


# ---------------------------------------------------------------------------
# Internal-info detection rules
# ---------------------------------------------------------------------------

_HOME_PATH_RES = [
    re.compile(r"/home/([A-Za-z0-9_.-]+)(?:/|\b)"),
    re.compile(r"/Users/([A-Za-z0-9_.-]+)(?:/|\b)"),
    re.compile(r"C:\\\\Users\\\\([A-Za-z0-9_.-]+)(?:\\\\|\b)"),
    re.compile(r"C:\\Users\\([A-Za-z0-9_.-]+)(?:\\|\b)"),
]

_IPV4_RE = re.compile(r"\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})(?:/(\d{1,2}))?\b")
# A conventional full IPv6-address regex (handles :: compression in
# any position, including bare "::"), so it neither misses real
# addresses using IPv6 shorthand notation nor false-positives on short
# positives on short hex/time-like tokens (git hashes, "00:20:16"
# clock strings) the way a looser "2+ colon-separated groups" pattern
# does.
_IPV6_RE = re.compile(
    r"(?<![0-9A-Fa-f:])("
    r"(?:[0-9A-Fa-f]{1,4}:){7}[0-9A-Fa-f]{1,4}"
    r"|(?:[0-9A-Fa-f]{1,4}:){1,7}:"
    r"|(?:[0-9A-Fa-f]{1,4}:){1,6}:[0-9A-Fa-f]{1,4}"
    r"|(?:[0-9A-Fa-f]{1,4}:){1,5}(?::[0-9A-Fa-f]{1,4}){1,2}"
    r"|(?:[0-9A-Fa-f]{1,4}:){1,4}(?::[0-9A-Fa-f]{1,4}){1,3}"
    r"|(?:[0-9A-Fa-f]{1,4}:){1,3}(?::[0-9A-Fa-f]{1,4}){1,4}"
    r"|(?:[0-9A-Fa-f]{1,4}:){1,2}(?::[0-9A-Fa-f]{1,4}){1,5}"
    r"|[0-9A-Fa-f]{1,4}:(?:(?::[0-9A-Fa-f]{1,4}){1,6})"
    r"|:(?:(?::[0-9A-Fa-f]{1,4}){1,7}|:)"
    r")(?:/(\d{1,3}))?(?![0-9A-Fa-f:])"
)

# ---------------------------------------------------------------------------
# Generic address classification
# ---------------------------------------------------------------------------


def _classify_ipv4(ip: str, has_cidr: bool, prefix_len: int | None) -> str | None:
    """Classifies a non-documentation IPv4 host address for public disclosure.

    Private, CGNAT, and other non-global literals are normal examples in this
    network-monitoring codebase. A local marker handles any operator-specific
    value in those ranges; this generic rule only reports globally routable
    host addresses.
    """
    if ip.startswith("127.") or any(ip.startswith(prefix) for prefix in _DOC_IPV4_PREFIXES):
        return None
    if has_cidr and prefix_len is not None and prefix_len < 32:
        return None
    try:
        address = ipaddress.IPv4Address(ip)
    except ipaddress.AddressValueError:
        return None
    if address.is_multicast or address.is_reserved:
        return None
    return "public IPv4 address" if address.is_global else None


def _classify_ipv6(ip: str, has_cidr: bool, prefix_len: int | None) -> str | None:
    """Classifies a non-documentation IPv6 host address for public disclosure."""
    lowered = ip.lower()
    if lowered in ("::1", "::") or lowered.startswith(_DOC_IPV6_PREFIX):
        return None
    if has_cidr and prefix_len is not None and prefix_len < 128:
        return None
    try:
        address = ipaddress.IPv6Address(ip)
    except ipaddress.AddressValueError:
        return None
    return (
        "public IPv6 address"
        if address.is_global and address in ipaddress.IPv6Network("2000::/3")
        else None
    )


# ---------------------------------------------------------------------------
# Finding model
# ---------------------------------------------------------------------------


@dataclass
class Finding:
    rule_id: str
    description: str
    location: str  # "tree:<path>:<line>" or "history:<commit>:<path>"
    detail: str  # secret-free description, may mention classification
    severity: str = "fail"  # "fail" | "warn"
    baseline_key: str | None = None  # only set for findings hashed for baseline


def _redact(value: str) -> str:
    digest = hashlib.sha256(value.encode("utf-8", errors="surrogateescape")).hexdigest()[:12]
    prefix = value[:4]
    return f"prefix={prefix!r} len={len(value)} sha256_12={digest}"


def _baseline_hash(rule_id: str, commit: str, path: str, line_no: int) -> str:
    """Returns the value-free baseline key for a finding's source location.

    The key intentionally excludes the matched text and every derivative of
    it. In particular, hashes of small domains such as IP addresses are not a
    safe substitute for the value because they can be enumerated.
    """
    payload = "\0".join((rule_id, commit, path, str(line_no)))
    return hashlib.sha256(payload.encode("utf-8", errors="surrogateescape")).hexdigest()


def _finding_baseline_key(
    rule_id: str, commit: str, path_hint: str, location_prefix: str, line_no: int
) -> str:
    """Builds a baseline key while keeping the public finding model simple."""
    return _baseline_hash(rule_id, commit, path_hint or location_prefix, line_no)


# ---------------------------------------------------------------------------
# Markers (local-only allowlist)
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class Marker:
    """A local-only personal-data marker with a stable, redacted index."""

    index: int
    value: str
    pattern: re.Pattern[str]


_IPV4_PREFIX_MARKER_RE = re.compile(r"^(?:\d{1,3}\.){3}$")
_IPV4_MARKER_RE = re.compile(r"^(?:\d{1,3}\.){3}\d{1,3}$")
_HOSTNAME_MARKER_RE = re.compile(
    r"^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)*$"
)


def _marker_pattern(value: str) -> re.Pattern[str]:
    """Compiles the required exact/prefix/boundary marker matching modes."""
    escaped = re.escape(value)
    if _IPV4_PREFIX_MARKER_RE.fullmatch(value):
        return re.compile(rf"(?<![0-9.]){escaped}\d{{1,3}}(?![0-9.])")
    if _IPV4_MARKER_RE.fullmatch(value):
        return re.compile(rf"(?<![0-9.]){escaped}(?![0-9.])")
    try:
        ipaddress.IPv6Address(value)
    except ipaddress.AddressValueError:
        pass
    else:
        return re.compile(rf"(?<![0-9A-Fa-f:]){escaped}(?![0-9A-Fa-f:])", re.IGNORECASE)
    if "@" in value:
        return re.compile(rf"(?<![A-Za-z0-9._%+-]){escaped}(?![A-Za-z0-9._%+-])", re.IGNORECASE)
    if _HOSTNAME_MARKER_RE.fullmatch(value):
        return re.compile(rf"(?<![A-Za-z0-9.-]){escaped}(?![A-Za-z0-9.-])", re.IGNORECASE)
    return re.compile(escaped)


def load_markers(repo_root: Path, env: dict[str, str] | None = None) -> list[Marker]:
    """Loads ordered local markers from a file and comma/newline-separated env.

    ``.prepublish-markers.local`` is one marker per non-comment line. File
    markers precede environment markers; duplicates retain their first index,
    so output can identify a marker without disclosing its value.
    """
    import os

    environ = env if env is not None else dict(os.environ)
    values: list[str] = []
    markers_file = repo_root / ".prepublish-markers.local"
    if markers_file.is_file():
        for line in markers_file.read_text(encoding="utf-8", errors="replace").splitlines():
            value = line.strip()
            if value and not value.startswith("#"):
                values.append(value)
    values.extend(
        value.strip()
        for value in re.split(r"[,\n]", environ.get("CP_PREPUBLISH_MARKERS", ""))
        if value.strip()
    )
    unique_values = list(dict.fromkeys(values))
    return [Marker(index, value, _marker_pattern(value)) for index, value in enumerate(unique_values, 1)]


def _coerce_markers(markers: Iterable[Marker | str]) -> list[Marker]:
    """Accepts strings in direct unit callers while normalizing marker indexes."""
    result: list[Marker] = []
    for index, marker in enumerate(markers, 1):
        result.append(marker if isinstance(marker, Marker) else Marker(index, marker, _marker_pattern(marker)))
    return result


def _has_marker_match(line: str, markers: Iterable[Marker | str]) -> bool:
    """Returns whether a local marker occurs, without exposing its value."""
    return any(marker.pattern.search(line) for marker in _coerce_markers(markers))


def _marker_findings(
    line: str, location_prefix: str, lineno: int, markers: Iterable[Marker | str], path_hint: str, commit: str
) -> Iterator[Finding]:
    """Reports local-marker hits without ever putting the marker in output."""
    for marker in _coerce_markers(markers):
        match = marker.pattern.search(line)
        if match:
            yield Finding(
                rule_id="internal.local_marker",
                description="local-only personal-data marker",
                location=f"{location_prefix}:{lineno}",
                detail=f"marker #{marker.index} matched <redacted len={len(match.group(0))}>",
                baseline_key=_finding_baseline_key(
                    "internal.local_marker", commit, path_hint, location_prefix, lineno
                ),
            )


# ---------------------------------------------------------------------------
# Line-level scanning (shared by tree + history scanners)
# ---------------------------------------------------------------------------


def scan_line_for_secrets(
    line: str,
    location_prefix: str,
    lineno: int,
    markers: Iterable[Marker | str],
    path_hint: str = "",
    commit: str = "",
) -> Iterator[Finding]:
    if _has_marker_match(line, markers):
        return
    for rule in SECRET_RULES:
        for m in rule.pattern.finditer(line):
            value = m.group(1) if m.groups() else m.group(0)
            if _is_fake_value(value, line):
                continue
            if rule.min_entropy and _shannon_entropy(value) < rule.min_entropy:
                continue
            yield Finding(
                rule_id=rule.rule_id,
                description=rule.description,
                location=f"{location_prefix}:{lineno}",
                detail=_redact(value),
                baseline_key=_finding_baseline_key(rule.rule_id, commit, path_hint, location_prefix, lineno),
            )


def scan_line_for_internal_info(
    line: str, location_prefix: str, lineno: int, markers: Iterable[Marker | str], path_hint: str, commit: str = ""
) -> Iterator[Finding]:
    if _has_marker_match(line, markers):
        return
    allow_reason = _allow_reason(line)

    for regex in _HOME_PATH_RES:
        for m in regex.finditer(line):
            user = m.group(1)
            if user.lower() in _ACCOUNT_ALLOWLIST:
                continue
            if allow_reason:
                continue
            yield Finding(
                rule_id="internal.home_path",
                description="absolute home directory path",
                location=f"{location_prefix}:{lineno}",
                detail=f"user={user!r}",
                baseline_key=_finding_baseline_key("internal.home_path", commit, path_hint, location_prefix, lineno),
            )

    if _ORCHESTRATOR_DIR_NAME in line:
        if not allow_reason:
            yield Finding(
                rule_id="internal.orchestrator_dir",
                description="orchestrator working-directory name",
                location=f"{location_prefix}:{lineno}",
                detail="matched orchestrator directory name literal",
                baseline_key=_finding_baseline_key(
                    "internal.orchestrator_dir", commit, path_hint, location_prefix, lineno),
            )

    for m in _IPV4_RE.finditer(line):
        ip = m.group(1)
        has_cidr = m.group(2) is not None
        prefix_len = int(m.group(2)) if has_cidr else None
        classification = _classify_ipv4(ip, has_cidr, prefix_len)
        if classification is None:
            continue
        if allow_reason:
            continue
        yield Finding(
            rule_id="internal.host_ip",
            description="host IP address",
            location=f"{location_prefix}:{lineno}",
            detail=f"ip_class={classification}",
            baseline_key=_finding_baseline_key("internal.host_ip", commit, path_hint, location_prefix, lineno),
        )

    for m in _IPV6_RE.finditer(line):
        candidate = m.group(1)
        if candidate.count(":") < 2:
            continue
        has_cidr = m.group(2) is not None
        prefix_len = int(m.group(2)) if has_cidr else None
        classification = _classify_ipv6(candidate, has_cidr, prefix_len)
        if classification is None:
            continue
        if allow_reason:
            continue
        yield Finding(
            rule_id="internal.host_ip",
            description="host IP address",
            location=f"{location_prefix}:{lineno}",
            detail=f"ip_class={classification}",
            baseline_key=_finding_baseline_key("internal.host_ip", commit, path_hint, location_prefix, lineno),
        )


def scan_line(
    line: str, location_prefix: str, lineno: int, markers: Iterable[Marker | str], path_hint: str = "", commit: str = ""
) -> list[Finding]:
    normalized_markers = _coerce_markers(markers)
    marker_findings = list(
        _marker_findings(line, location_prefix, lineno, normalized_markers, path_hint, commit)
    )
    if marker_findings:
        return marker_findings
    findings: list[Finding] = []
    findings.extend(
        scan_line_for_secrets(line, location_prefix, lineno, normalized_markers, path_hint, commit)
    )
    findings.extend(
        scan_line_for_internal_info(line, location_prefix, lineno, normalized_markers, path_hint, commit)
    )
    return findings


# ---------------------------------------------------------------------------
# Tree scanning
# ---------------------------------------------------------------------------

_TEXT_LIKELY_SUFFIXES = {
    ".go",
    ".py",
    ".sh",
    ".ps1",
    ".js",
    ".mjs",
    ".ts",
    ".md",
    ".yml",
    ".yaml",
    ".json",
    ".toml",
    ".txt",
    ".html",
    ".css",
    ".sql",
    ".env",
    ".cfg",
    ".ini",
    "",
}


def _git(repo_root: Path, *args: str) -> str:
    result = subprocess.run(
        ["git", *args],
        cwd=repo_root,
        capture_output=True,
        text=True,
        check=True,
    )
    return result.stdout


def list_tracked_files(repo_root: Path) -> list[str]:
    out = _git(repo_root, "ls-files")
    return [line for line in out.splitlines() if line]


def list_working_tree_files(repo_root: Path) -> list[str]:
    """Lists tracked plus untracked, non-ignored files for marker regression tests."""
    out = _git(repo_root, "ls-files", "-co", "--exclude-standard")
    return [line for line in out.splitlines() if line]


def scan_working_tree_for_markers(
    repo_root: Path, markers: Iterable[Marker | str]
) -> list[Finding]:
    """Scans every text file visible to Git for local markers, excluding PNGs."""
    findings: list[Finding] = []
    normalized_markers = _coerce_markers(markers)
    for relpath in list_working_tree_files(repo_root):
        if relpath.endswith(".png"):
            continue
        full = repo_root / relpath
        if not full.is_file():
            continue
        try:
            data = full.read_bytes()
        except OSError:
            continue
        if b"\0" in data[:4096]:
            continue
        try:
            text = data.decode("utf-8")
        except UnicodeDecodeError:
            continue
        location_prefix = f"worktree:{relpath}"
        for lineno, line in enumerate(text.splitlines(), start=1):
            findings.extend(
                _marker_findings(line, location_prefix, lineno, normalized_markers, relpath, "")
            )
    return findings


def scan_tree(repo_root: Path, markers: Iterable[Marker | str]) -> list[Finding]:
    """Scans every file Git would publish: tracked plus untracked, non-ignored.

    Including untracked files means a local run catches a problem *before*
    the file is committed (in CI the two sets are identical).
    """
    findings: list[Finding] = []
    for relpath in list_working_tree_files(repo_root):
        full = repo_root / relpath
        if not full.is_file():
            continue
        suffix = full.suffix
        if relpath.endswith(".png"):
            findings.extend(scan_png_metadata(full, f"tree:{relpath}"))
            continue
        if suffix not in _TEXT_LIKELY_SUFFIXES and suffix != "":
            continue
        try:
            data = full.read_bytes()
        except OSError:
            continue
        if b"\0" in data[:4096]:
            continue  # binary, not a text scan target
        try:
            text = data.decode("utf-8")
        except UnicodeDecodeError:
            continue
        location_prefix = f"tree:{relpath}"
        for lineno, line in enumerate(text.splitlines(), start=1):
            findings.extend(scan_line(line, location_prefix, lineno, markers, relpath))
    return findings


# ---------------------------------------------------------------------------
# History scanning
# ---------------------------------------------------------------------------

_DIFF_HEADER_RE = re.compile(r"^\+\+\+ b/(.+)$")
_COMMIT_HEADER_RE = re.compile(r"^commit ([0-9a-f]{40})")
_ADDED_LINE_RE = re.compile(r"^\+(?!\+\+)(.*)$")


def iter_history_added_lines(repo_root: Path) -> Iterator[tuple[str, str, int, str]]:
    """Streams added lines with commit, path, and their real new-file line number."""
    proc = subprocess.Popen(
        ["git", "log", "-p", "--all", "--no-color", "--unified=0"],
        cwd=repo_root,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        text=True,
        errors="surrogateescape",
        bufsize=1,
    )
    assert proc.stdout is not None
    current_commit = ""
    current_path = ""
    new_line_no: int | None = None
    hunk_re = re.compile(r"^@@ -[^ ]+ \+(\d+)(?:,\d+)? @@")
    try:
        for raw_line in proc.stdout:
            line = raw_line.rstrip("\n")
            commit_match = _COMMIT_HEADER_RE.match(line)
            if commit_match:
                current_commit = commit_match.group(1)
                continue
            diff_match = _DIFF_HEADER_RE.match(line)
            if diff_match:
                current_path = diff_match.group(1)
                new_line_no = None
                continue
            hunk_match = hunk_re.match(line)
            if hunk_match:
                new_line_no = int(hunk_match.group(1))
                continue
            if not current_path or new_line_no is None:
                continue
            added_match = _ADDED_LINE_RE.match(line)
            if added_match:
                yield current_commit, current_path, new_line_no, added_match.group(1)
                new_line_no += 1
            elif line.startswith(" "):
                new_line_no += 1
    finally:
        proc.stdout.close()
        proc.wait()


def scan_history(repo_root: Path, markers: Iterable[Marker | str]) -> list[Finding]:
    findings: list[Finding] = []
    for commit, path, lineno, line in iter_history_added_lines(repo_root):
        location_prefix = f"history:{commit[:12]}:{path}"
        findings.extend(scan_line(line, location_prefix, lineno, markers, path, commit))
    findings.extend(scan_history_large_and_binary_blobs(repo_root))
    return findings


def scan_history_large_and_binary_blobs(repo_root: Path) -> list[Finding]:
    """Finds any blob (including ones only reachable via a deleted
    file/branch) over MAX_BLOB_BYTES anywhere in history, via
    `git rev-list --all --objects` + `git cat-file --batch-check`."""
    findings: list[Finding] = []
    rev_list = subprocess.run(
        ["git", "rev-list", "--all", "--objects"],
        cwd=repo_root,
        capture_output=True,
        text=True,
        check=True,
    )
    batch_check = subprocess.run(
        ["git", "cat-file", "--batch-check=%(objecttype) %(objectname) %(objectsize) %(rest)"],
        cwd=repo_root,
        input=rev_list.stdout,
        capture_output=True,
        text=True,
        check=True,
    )
    for line in batch_check.stdout.splitlines():
        parts = line.split(" ", 3)
        if len(parts) < 3 or parts[0] != "blob":
            continue
        try:
            size = int(parts[2])
        except ValueError:
            continue
        rest = parts[3] if len(parts) > 3 else ""
        if size > MAX_BLOB_BYTES:
            findings.append(
                Finding(
                    rule_id="hygiene.large_history_blob",
                    description="blob over 5 MiB present in git history",
                    location=f"history:blob:{parts[1][:12]}",
                    detail=f"path={rest!r} size={size}",
                    baseline_key=_baseline_hash("hygiene.large_history_blob", "", rest, 0),
                )
            )
    return findings


# ---------------------------------------------------------------------------
# PNG metadata scanning
# ---------------------------------------------------------------------------

_PNG_TEXT_CHUNKS = (b"tEXt", b"iTXt", b"zTXt", b"eXIf")


def scan_png_metadata(path: Path, location_prefix: str) -> list[Finding]:
    findings: list[Finding] = []
    try:
        data = path.read_bytes()
    except OSError:
        return findings
    if data[:8] != b"\x89PNG\r\n\x1a\n":
        return findings
    i = 8
    while i + 8 <= len(data):
        try:
            length = struct.unpack(">I", data[i : i + 4])[0]
        except struct.error:
            break
        ctype = data[i + 4 : i + 8]
        chunk_data = data[i + 8 : i + 8 + length]
        if ctype in _PNG_TEXT_CHUNKS:
            preview = _decode_png_text_chunk(ctype, chunk_data)
            findings.append(
                Finding(
                    rule_id="internal.png_metadata",
                    description=f"PNG {ctype.decode('ascii')} metadata chunk present",
                    location=location_prefix,
                    detail=f"chunk={ctype.decode('ascii')} preview={preview[:80]!r}",
                    baseline_key=_baseline_hash("internal.png_metadata", "", str(path), 0),
                )
            )
        i += 8 + length + 4  # length + type + data + crc
    return findings


def _decode_png_text_chunk(ctype: bytes, chunk_data: bytes) -> str:
    try:
        if ctype == b"tEXt":
            return chunk_data.split(b"\x00", 1)[-1].decode("latin1", errors="replace")
        if ctype == b"zTXt":
            parts = chunk_data.split(b"\x00", 1)
            if len(parts) == 2:
                try:
                    return zlib.decompress(parts[1][1:]).decode("latin1", errors="replace")
                except zlib.error:
                    return "<zTXt: could not decompress>"
        if ctype == b"iTXt":
            return chunk_data.decode("latin1", errors="replace")
        if ctype == b"eXIf":
            return "<EXIF binary block>"
    except Exception:  # pragma: no cover - defensive, metadata is untrusted input
        return "<could not decode>"
    return "<empty>"


# ---------------------------------------------------------------------------
# Repository hygiene checks
# ---------------------------------------------------------------------------


def check_license(repo_root: Path) -> list[Finding]:
    findings: list[Finding] = []
    path = repo_root / "LICENSE"
    if not path.is_file():
        findings.append(
            Finding("hygiene.license_missing", "LICENSE file missing", "tree:LICENSE", "no LICENSE file found")
        )
        return findings
    text = path.read_text(encoding="utf-8", errors="replace")
    if "MIT License" not in text:
        findings.append(
            Finding("hygiene.license_not_mit", "LICENSE is not MIT", "tree:LICENSE", "expected 'MIT License' header")
        )
    if not re.search(r"Copyright \(c\) \d{4}", text):
        findings.append(
            Finding(
                "hygiene.license_missing_holder_year",
                "LICENSE missing copyright holder/year",
                "tree:LICENSE",
                "expected a 'Copyright (c) YYYY <holder>' line",
            )
        )
    return findings


def check_security_md(repo_root: Path) -> list[Finding]:
    findings: list[Finding] = []
    path = repo_root / "SECURITY.md"
    if not path.is_file():
        findings.append(
            Finding("hygiene.security_md_missing", "SECURITY.md missing", "tree:SECURITY.md", "file not found")
        )
        return findings
    text = path.read_text(encoding="utf-8", errors="replace")
    lowered = text.lower()
    if "private vulnerability reporting" not in lowered and "security advisories" not in lowered:
        findings.append(
            Finding(
                "hygiene.security_md_no_private_reporting",
                "SECURITY.md missing a private reporting path",
                "tree:SECURITY.md",
                "expected a mention of GitHub's private vulnerability reporting flow",
            )
        )
    if "supported version" not in lowered:
        findings.append(
            Finding(
                "hygiene.security_md_no_supported_versions",
                "SECURITY.md missing a supported-versions section",
                "tree:SECURITY.md",
                "expected a 'Supported versions' section",
            )
        )
    latest_line_re = re.compile(r"v0\.(\d+)\.x")
    versions = [int(m.group(1)) for m in latest_line_re.finditer(text)]
    if not versions or max(versions) < 7:
        findings.append(
            Finding(
                "hygiene.security_md_stale_supported_version",
                "SECURITY.md's supported-versions line looks stale",
                "tree:SECURITY.md",
                f"newest v0.x.x mentioned: {max(versions) if versions else 'none'}, want at least v0.7.x",
            )
        )
    return findings


def check_code_of_conduct(repo_root: Path) -> list[Finding]:
    findings: list[Finding] = []
    path = repo_root / "CODE_OF_CONDUCT.md"
    if not path.is_file():
        findings.append(
            Finding(
                "hygiene.code_of_conduct_missing",
                "CODE_OF_CONDUCT.md missing",
                "tree:CODE_OF_CONDUCT.md",
                "file not found",
            )
        )
        return findings
    text = path.read_text(encoding="utf-8", errors="replace").lower()
    if "security.md" not in text and "report" not in text:
        findings.append(
            Finding(
                "hygiene.code_of_conduct_no_contact",
                "CODE_OF_CONDUCT.md missing an enforcement/contact path",
                "tree:CODE_OF_CONDUCT.md",
                "expected a reference to how to report a conduct concern",
            )
        )
    return findings


def check_github_templates(repo_root: Path) -> list[Finding]:
    findings: list[Finding] = []
    required = [
        ".github/PULL_REQUEST_TEMPLATE.md",
        ".github/ISSUE_TEMPLATE",
    ]
    for rel in required:
        if not (repo_root / rel).exists():
            findings.append(
                Finding(
                    "hygiene.github_template_missing",
                    "missing .github template",
                    f"tree:{rel}",
                    f"{rel} not found",
                )
            )
    return findings


def check_gitignore(repo_root: Path) -> list[Finding]:
    findings: list[Finding] = []
    path = repo_root / ".gitignore"
    if not path.is_file():
        findings.append(
            Finding("hygiene.gitignore_missing", ".gitignore missing", "tree:.gitignore", "file not found")
        )
        return findings
    text = path.read_text(encoding="utf-8", errors="replace")
    lines = {line.strip() for line in text.splitlines()}
    for entry in REQUIRED_GITIGNORE_ENTRIES:
        if entry not in lines:
            findings.append(
                Finding(
                    "hygiene.gitignore_missing_entry",
                    "missing required .gitignore entry",
                    "tree:.gitignore",
                    f"missing entry {entry!r}",
                )
            )
    return findings


def check_no_tracked_binaries(repo_root: Path) -> list[Finding]:
    findings: list[Finding] = []
    for relpath in list_tracked_files(repo_root):
        full = repo_root / relpath
        if not full.is_file():
            continue
        try:
            with full.open("rb") as fh:
                head = fh.read(4)
        except OSError:
            continue
        if any(head.startswith(magic) for magic in _BINARY_MAGIC):
            findings.append(
                Finding(
                    "hygiene.tracked_binary",
                    "tracked file sniffs as a native binary executable",
                    f"tree:{relpath}",
                    "matched a known executable magic header",
                )
            )
    return findings


def check_docs_script(repo_root: Path) -> list[Finding]:
    findings: list[Finding] = []
    script = repo_root / "scripts" / "check-docs.py"
    if not script.is_file():
        return findings  # not this stage's problem if missing entirely
    result = subprocess.run(
        [sys.executable, str(script)],
        cwd=repo_root,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        findings.append(
            Finding(
                "hygiene.check_docs_failed",
                "scripts/check-docs.py failed",
                "tree:scripts/check-docs.py",
                f"exit={result.returncode} stderr_tail={result.stderr[-300:]!r}",
            )
        )
    return findings


def run_hygiene_checks(repo_root: Path, skip_check_docs: bool = False) -> list[Finding]:
    findings: list[Finding] = []
    findings.extend(check_license(repo_root))
    findings.extend(check_security_md(repo_root))
    findings.extend(check_code_of_conduct(repo_root))
    findings.extend(check_github_templates(repo_root))
    findings.extend(check_gitignore(repo_root))
    findings.extend(check_no_tracked_binaries(repo_root))
    if not skip_check_docs:
        findings.extend(check_docs_script(repo_root))
    return findings


# ---------------------------------------------------------------------------
# Baseline handling
# ---------------------------------------------------------------------------


@dataclass
class Baseline:
    entries: set[str] = field(default_factory=set)

    @classmethod
    def load(cls, path: Path) -> "Baseline":
        if not path.is_file():
            return cls()
        data = json.loads(path.read_text(encoding="utf-8"))
        entries = {e["baseline_key"] for e in data.get("findings", []) if "baseline_key" in e}
        return cls(entries=entries)

    def save(self, path: Path, findings: list[Finding]) -> None:
        seen: dict[str, dict[str, str]] = {}
        for f in findings:
            if f.baseline_key is None:
                continue
            seen[f.baseline_key] = {
                "baseline_key": f.baseline_key,
                "rule_id": f.rule_id,
                "location": f.location,
            }
        payload = {"version": 1, "findings": sorted(seen.values(), key=lambda e: e["baseline_key"])}
        path.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")

    def contains(self, finding: Finding) -> bool:
        return finding.baseline_key is not None and finding.baseline_key in self.entries


def classify_with_baseline(
    findings: list[Finding], baseline: Baseline, is_history: bool
) -> list[Finding]:
    classified: list[Finding] = []
    for f in findings:
        if is_history and baseline.contains(f):
            f.severity = "warn"
        classified.append(f)
    return classified


# ---------------------------------------------------------------------------
# Reporting
# ---------------------------------------------------------------------------


def render_markdown_report(
    tree_findings: list[Finding], history_findings: list[Finding]
) -> str:
    lines: list[str] = ["# prepublish-check report", ""]

    def _section(title: str, findings: list[Finding]) -> None:
        lines.append(f"## {title}")
        lines.append("")
        if not findings:
            lines.append("No findings.")
            lines.append("")
            return
        fails = [f for f in findings if f.severity == "fail"]
        warns = [f for f in findings if f.severity == "warn"]
        lines.append(f"{len(fails)} failing, {len(warns)} warning (baseline).")
        lines.append("")
        lines.append("| Severity | Rule | Location | Detail |")
        lines.append("|---|---|---|---|")
        for f in findings:
            lines.append(f"| {f.severity} | {f.rule_id} | `{f.location}` | {f.detail} |")
        lines.append("")

    _section("Tree findings", tree_findings)
    _section("History findings", history_findings)
    return "\n".join(lines)


def render_summary(tree_findings: list[Finding], history_findings: list[Finding]) -> str:
    tree_fail = sum(1 for f in tree_findings if f.severity == "fail")
    hist_fail = sum(1 for f in history_findings if f.severity == "fail")
    hist_warn = sum(1 for f in history_findings if f.severity == "warn")
    return (
        f"prepublish-check: tree findings={len(tree_findings)} (fail={tree_fail}); "
        f"history findings={len(history_findings)} (fail={hist_fail}, warn={hist_warn})"
    )


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def find_repo_root(start: Path) -> Path:
    out = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        cwd=start,
        capture_output=True,
        text=True,
        check=True,
    )
    return Path(out.stdout.strip())


def build_arg_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description=__doc__,
        epilog=(
            "Local markers: .prepublish-markers.local accepts one non-comment marker per line; "
            "CP_PREPUBLISH_MARKERS accepts comma- or newline-separated markers. Markers support "
            "exact IPv4/IPv6 addresses, IPv4 prefixes ending in a dot, hostname word-boundary "
            "matches, emails, and arbitrary strings. Marker values are never printed."
        ),
    )
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--tree-only", action="store_true", help="scan only the current tracked tree")
    mode.add_argument("--history-only", action="store_true", help="scan only git history")
    parser.add_argument("--report", type=Path, default=None, help="write a markdown report to FILE")
    parser.add_argument(
        "--baseline",
        type=Path,
        default=None,
        help="path to the baseline JSON file (default: <repo>/.prepublish-baseline.json)",
    )
    parser.add_argument(
        "--update-baseline",
        action="store_true",
        help="write the current history findings into the baseline file and exit 0",
    )
    parser.add_argument(
        "--skip-check-docs",
        action="store_true",
        help="skip invoking scripts/check-docs.py as part of hygiene checks",
    )
    parser.add_argument("--repo-root", type=Path, default=None, help="repository root (default: autodetect)")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_arg_parser().parse_args(argv)
    repo_root = args.repo_root or find_repo_root(Path.cwd())
    baseline_path = args.baseline or (repo_root / ".prepublish-baseline.json")
    markers = load_markers(repo_root)

    tree_findings: list[Finding] = []
    history_findings: list[Finding] = []

    if not args.history_only:
        tree_findings = scan_tree(repo_root, markers)
        tree_findings.extend(run_hygiene_checks(repo_root, skip_check_docs=args.skip_check_docs))

    if not args.tree_only:
        history_findings = scan_history(repo_root, markers)

    baseline = Baseline.load(baseline_path)

    if args.update_baseline:
        if args.tree_only:
            print("prepublish-check: --update-baseline is not meaningful with --tree-only", file=sys.stderr)
            return 2
        baseline.save(baseline_path, history_findings)
        try:
            baseline_display = str(baseline_path.relative_to(repo_root))
        except ValueError:
            baseline_display = "external baseline file"
        print(f"prepublish-check: wrote {baseline_display} with {len(history_findings)} history finding(s)")
        return 0

    history_findings = classify_with_baseline(history_findings, baseline, is_history=True)

    if args.report:
        args.report.write_text(render_markdown_report(tree_findings, history_findings), encoding="utf-8")

    print(render_summary(tree_findings, history_findings))
    for f in tree_findings:
        print(f"  FAIL  tree    {f.rule_id:45s} {f.location}  {f.detail}")
    for f in history_findings:
        tag = "FAIL" if f.severity == "fail" else "warn"
        print(f"  {tag}  history {f.rule_id:45s} {f.location}  {f.detail}")

    any_tree_fail = any(f.severity == "fail" for f in tree_findings)
    any_history_fail = any(f.severity == "fail" for f in history_findings)
    if any_tree_fail or any_history_fail:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
