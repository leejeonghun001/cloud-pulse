#!/usr/bin/env python3
"""Offline provenance and bounded smoke verifier for the Round 30 Go matrix.

This tool is intentionally inert until invoked. ``freeze`` pins the dirty source
tree and an explicitly supplied UTC timestamp; ``build`` produces the six-target
hub/agent matrix without toolchain or module downloads; ``verify`` checks the
artifacts and, only on Linux/aarch64, runs the native agent's local-only smoke.
It never starts a hub, contacts a server, or invokes Docker/Wine/QEMU.
"""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import pathlib
import re
import selectors
import shutil
import signal
import stat
import struct
import subprocess
import sys
import tempfile
import time
from typing import Any, Iterable


ROOT = pathlib.Path(__file__).resolve().parents[1]
DEFAULT_OUT = ROOT / ".verification/round30/platforms"
VERSION = "v0.8.0-round30-dirty"
MODULE = "github.com/leejeonghun001/cloud-pulse"
PACKAGES = {"hub": "./cmd/hub", "agent": "./cmd/agent"}
TARGETS = (
    ("linux", "amd64"),
    ("linux", "arm64"),
    ("windows", "amd64"),
    ("windows", "arm64"),
    ("darwin", "amd64"),
    ("darwin", "arm64"),
)
TARGET_SET = set(TARGETS)
SUPPORTED_HEAD = re.compile(r"^[0-9a-f]{40,64}$")
HEX_SHA256 = re.compile(r"^[0-9a-f]{64}$")
EXCLUSION_POLICY = [
    {"path": ".git/**", "reason": "Git object/database and mutable index metadata are not compiler inputs; full HEAD and safe status paths are recorded."},
    {"path": ".verification/**", "reason": "Round30 output/evidence is written here; any Go package consuming an excluded path is refused."},
    {"path": "node_modules/**", "reason": "Root-only ignored Node development tooling; tracked entries refuse, Go source/embed/local replacements under this excluded root are refused, and this precise policy is SHA256-bound into the composite source fingerprint."},
    {"path": "external Go toolchain/module/build caches", "reason": "external module inputs are bound by the offline Go module graph, go.mod/go.sum inventory and exact module identities."},
]
MAX_CHILD_OUTPUT = 1024 * 1024
MAX_GO_LIST_OUTPUT = 32 * 1024 * 1024


class VerificationError(RuntimeError):
    """A fail-closed verification error with no source contents attached."""


def fail(message: str) -> None:
    raise VerificationError(message)


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def source_fingerprint_digest(tree_hash: str, module_hash: str,
                              closure_hash: str, exclusion_hash: str) -> str:
    binding = {"tree_sha256": tree_hash,
               "go_module_inventory_sha256": module_hash,
               "go_input_closures_sha256": closure_hash,
               "exclusions_sha256": exclusion_hash}
    return sha256(json_bytes(binding, pretty=False))


def json_bytes(value: Any, *, pretty: bool = True) -> bytes:
    if pretty:
        return (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode("utf-8")
    return json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def write_json(path: pathlib.Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(json_bytes(value))
    path.chmod(0o600)


def read_json(path: pathlib.Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError):
        fail(f"cannot read valid JSON evidence: {path.name}")


def sha_file(path: pathlib.Path) -> str:
    h = hashlib.sha256()
    try:
        with path.open("rb") as stream:
            while True:
                chunk = stream.read(1024 * 1024)
                if not chunk:
                    break
                h.update(chunk)
    except OSError:
        fail(f"cannot read required file: {path.name}")
    return h.hexdigest()


def _run_git(root: pathlib.Path, args: list[str]) -> bytes:
    try:
        result = subprocess.run(
            ["git", *args], cwd=root, stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            timeout=15, check=False, env=build_env(),
        )
    except (OSError, subprocess.TimeoutExpired):
        fail("git query failed or exceeded its time limit")
    if result.returncode:
        fail("git query failed")
    return result.stdout


def _decode_path(raw: bytes) -> str:
    try:
        name = raw.decode("utf-8", "strict")
    except UnicodeDecodeError:
        fail("non-UTF-8 worktree path refused")
    path = pathlib.PurePosixPath(name)
    if (not name or name.startswith("/") or "\\" in name or
            any(part in ("", ".", "..") for part in name.split("/")) or
            any(ord(char) < 32 or ord(char) == 127 for char in name)):
        fail("unsafe worktree path refused")
    if path.is_absolute():
        fail("absolute worktree path refused")
    return name


def _status_paths(root: pathlib.Path) -> list[dict[str, str]]:
    raw = _run_git(root, ["status", "--porcelain=v1", "-z", "--untracked-files=all"])
    fields = raw.split(b"\0")
    if fields and fields[-1] == b"":
        fields.pop()
    result: list[dict[str, str]] = []
    index = 0
    while index < len(fields):
        item = fields[index]
        index += 1
        if len(item) < 4 or item[2:3] != b" ":
            fail("unparseable git status record")
        try:
            status = item[:2].decode("ascii", "strict")
        except UnicodeDecodeError:
            fail("unparseable git status code")
        result.append({"status": status, "path": _decode_path(item[3:])})
        if "R" in status or "C" in status:
            if index >= len(fields):
                fail("incomplete git rename status record")
            result.append({"status": "source", "path": _decode_path(fields[index])})
            index += 1
    result.sort(key=lambda item: item["path"].encode("utf-8"))
    return result


def _is_root_node_modules(name: str) -> bool:
    return name == "node_modules" or name.startswith("node_modules/")


def _excluded(name: str) -> bool:
    parts = name.split("/")
    if name == ".git" or name.startswith(".git/"):
        return True
    if name == ".verification" or name.startswith(".verification/"):
        return True
    # Deliberately root-only. Nested node_modules and other cache-like names
    # remain inventoried unless they are actual compiler inputs under this
    # exact excluded root.
    return _is_root_node_modules(name)


def _safe_input_path(root: pathlib.Path, name: str) -> pathlib.Path:
    current = root
    for part in name.split("/"):
        current = current / part
        try:
            info = current.lstat()
        except OSError:
            fail(f"missing inventoried input: {name}")
        if stat.S_ISLNK(info.st_mode):
            fail(f"symlink input refused: {name}")
    try:
        if not current.resolve(strict=True).is_relative_to(root.resolve(strict=True)):
            fail(f"input escapes repository root: {name}")
    except OSError:
        fail(f"unresolvable input path: {name}")
    return current


def _json_objects(blob: bytes, description: str) -> list[dict[str, Any]]:
    try:
        text = blob.decode("utf-8", "strict")
        decoder = json.JSONDecoder()
        values: list[dict[str, Any]] = []
        index = 0
        while index < len(text):
            while index < len(text) and text[index].isspace():
                index += 1
            if index == len(text):
                break
            value, index = decoder.raw_decode(text, index)
            if not isinstance(value, dict):
                fail(f"{description} returned malformed JSON records")
            values.append(value)
        return values
    except (UnicodeDecodeError, json.JSONDecodeError):
        fail(f"{description} returned malformed JSON")


def _go_module_inventory(root: pathlib.Path) -> list[dict[str, Any]]:
    code, stdout, stderr = run_bounded(
        ["go", "list", "-mod=readonly", "-m", "-json", "all"], cwd=root,
        env=build_env(go=True), timeout=60, output_limit=MAX_GO_LIST_OUTPUT,
    )
    modules = _json_objects(require_command_ok(code, stdout, stderr, "go module inventory"),
                            "go module inventory")
    if not modules:
        fail("Go module inventory was empty")
    root_real = root.resolve(strict=True)
    identities = []
    for module in modules:
        path = module.get("Path")
        if not isinstance(path, str) or not path:
            fail("Go module inventory omitted a module path")
        record: dict[str, Any] = {"path": path, "main": bool(module.get("Main"))}
        for source_key, dest_key in (("Version", "version"), ("Sum", "sum"),
                                     ("GoModSum", "go_mod_sum")):
            value = module.get(source_key)
            if value:
                record[dest_key] = value
        replacement = module.get("Replace")
        if isinstance(replacement, dict):
            rep: dict[str, Any] = {"path": replacement.get("Path", "")}
            if replacement.get("Version"):
                rep["version"] = replacement["Version"]
            if replacement.get("Sum"):
                rep["sum"] = replacement["Sum"]
            if replacement.get("GoModSum"):
                rep["go_mod_sum"] = replacement["GoModSum"]
            if not replacement.get("Version"):
                directory = replacement.get("Dir")
                if not isinstance(directory, str) or not directory:
                    fail("local Go replacement has no resolved directory")
                replacement_dir = pathlib.Path(directory).resolve(strict=True)
                if not replacement_dir.is_relative_to(root_real):
                    fail("local Go replacement outside the inventoried repository refused")
                replacement_rel = replacement_dir.relative_to(root_real).as_posix()
                if _excluded(replacement_rel):
                    fail("local Go replacement under an excluded path refused")
                rep["repository_dir"] = replacement_rel
            record["replace"] = rep
        identities.append(record)
    identities.sort(key=lambda item: (item["path"], item.get("version", "")))
    return identities


def _go_input_closures(root: pathlib.Path, repo_names: set[str],
                       module_inventory: list[dict[str, Any]]) -> list[dict[str, Any]]:
    closures: list[dict[str, Any]] = []
    root_real = root.resolve(strict=True)
    module_keys = set()
    for module in module_inventory:
        module_keys.add((module.get("path"), module.get("version", "")))
        replacement = module.get("replace")
        if isinstance(replacement, dict) and replacement.get("version"):
            module_keys.add((replacement.get("path"), replacement["version"]))
    source_fields = ("GoFiles", "EmbedFiles", "SFiles", "SysoFiles")
    for goos, goarch in TARGETS:
        for package_name, package_arg in PACKAGES.items():
            env = build_env(go=True)
            env.update({"GOOS": goos, "GOARCH": goarch})
            code, stdout, stderr = run_bounded(
                ["go", "list", "-mod=readonly", "-deps", "-json", package_arg], cwd=root,
                env=env, timeout=120, output_limit=MAX_GO_LIST_OUTPUT,
            )
            packages = _json_objects(require_command_ok(
                code, stdout, stderr, f"go package closure {goos}/{goarch} {package_name}"),
                f"go package closure {goos}/{goarch} {package_name}")
            repo_inputs: set[str] = set()
            for package in packages:
                if package.get("Incomplete") or package.get("Error"):
                    fail(f"incomplete Go package closure for {goos}/{goarch} {package_name}")
                directory = package.get("Dir")
                if not isinstance(directory, str) or not directory:
                    fail("Go package closure omitted a package directory")
                package_dir = pathlib.Path(directory).resolve(strict=True)
                if not package_dir.is_relative_to(root_real):
                    module = package.get("Module")
                    if package.get("Standard"):
                        continue
                    if not isinstance(module, dict) or not module.get("Path"):
                        fail("external Go package lacks a module identity")
                    if (module.get("Path"), module.get("Version", "")) not in module_keys:
                        fail("external Go package module identity is absent from offline module inventory")
                    continue
                package_relative = package_dir.relative_to(root_real).as_posix()
                if _excluded(package_relative):
                    fail("Go package is under an excluded repository input")
                for field in source_fields:
                    values = package.get(field, [])
                    if not isinstance(values, list):
                        fail("Go package source list is malformed")
                    for filename in values:
                        if not isinstance(filename, str) or not filename:
                            fail("Go package source path is malformed")
                        path = (package_dir / filename).resolve(strict=False)
                        if not path.is_relative_to(root_real):
                            fail("Go package source escapes the repository")
                        relative = path.relative_to(root_real).as_posix()
                        if _excluded(relative):
                            fail(f"Go package consumes an excluded repository input: {relative}")
                        repo_inputs.add(relative)
            missing = repo_inputs - repo_names
            if missing:
                fail("Go package consumes a repository input missing from inventory")
            closures.append({
                "target": {"goos": goos, "goarch": goarch},
                "package": package_name,
                "command": ["go", "list", "-mod=readonly", "-deps", "-json", package_arg],
                "environment": {"GOOS": goos, "GOARCH": goarch, "CGO_ENABLED": "0",
                                "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off",
                                "GOENV": "off", "GOWORK": "off", "GOFLAGS": ""},
                "repo_inputs": sorted(repo_inputs, key=lambda value: value.encode("utf-8")),
                "module_identities": module_inventory,
            })
    return closures


def inventory(root: pathlib.Path) -> dict[str, Any]:
    """Inventory every repository file except explicit metadata/output exclusions."""
    root = root.resolve(strict=True)
    head = _run_git(root, ["rev-parse", "HEAD"]).decode("ascii", "strict").strip()
    if not SUPPORTED_HEAD.fullmatch(head):
        fail("HEAD is not a full hexadecimal commit id")
    git_root = _run_git(root, ["rev-parse", "--show-toplevel"]).decode("utf-8", "strict").strip()
    if pathlib.Path(git_root).resolve(strict=True) != root:
        fail("repository root mismatch")
    tracked_raw = _run_git(root, ["ls-files", "--cached", "-z"])
    tracked_names = {_decode_path(raw) for raw in tracked_raw.split(b"\0") if raw}
    if any(_is_root_node_modules(name) for name in tracked_names):
        fail("tracked input under the excluded root node_modules tree refused")
    names: set[str] = set()
    excluded_count = 0
    for current, dirs, files in os.walk(root, topdown=True, followlinks=False):
        current_path = pathlib.Path(current)
        kept_dirs = []
        for directory in dirs:
            child = current_path / directory
            relative = child.relative_to(root).as_posix()
            if _excluded(relative):
                excluded_count += 1
                continue
            info = child.lstat()
            if stat.S_ISLNK(info.st_mode) or not stat.S_ISDIR(info.st_mode):
                fail(f"unsafe repository directory refused: {relative}")
            kept_dirs.append(directory)
        dirs[:] = kept_dirs
        for filename in files:
            child = current_path / filename
            relative = _decode_path(child.relative_to(root).as_posix().encode("utf-8", "strict"))
            if _excluded(relative):
                excluded_count += 1
                continue
            names.add(relative)
    for tracked in tracked_names:
        if not _excluded(tracked) and tracked not in names:
            fail(f"missing inventoried input: {tracked}")
    records: list[dict[str, str]] = []
    for name in sorted(names, key=lambda value: value.encode("utf-8")):
        path = _safe_input_path(root, name)
        try:
            info = path.lstat()
        except OSError:
            fail(f"missing inventoried input: {name}")
        if not stat.S_ISREG(info.st_mode):
            fail(f"non-regular build input refused: {name}")
        if info.st_mode & (stat.S_ISUID | stat.S_ISGID | stat.S_ISVTX):
            fail(f"special permission bits refused on input: {name}")
        records.append({
            "path": name,
            "mode": format(stat.S_IMODE(info.st_mode), "04o"),
            "sha256": sha_file(path),
        })
    record_blob = json_bytes(records, pretty=False)
    # The explicit evidence/output exclusion keeps this verifier's own files
    # out of the dirty-path record; ignored inputs elsewhere remain included.
    status_paths = [row for row in _status_paths(root) if not _excluded(row["path"])]
    known_status = {row["path"] for row in status_paths}
    for name in names - tracked_names - known_status:
        status_paths.append({"status": "??", "path": name})
    status_paths.sort(key=lambda item: item["path"].encode("utf-8"))
    module_inventory = _go_module_inventory(root)
    closures = _go_input_closures(root, names, module_inventory)
    return {
        "head": head,
        "dirty": bool(status_paths),
        "status_paths": status_paths,
        "tree_sha256": sha256(record_blob),
        "input_inventory": records,
        "selection": "all on-disk repository files, including ignored/untracked files, plus all tracked paths; only .git/**, .verification/**, and the exact root node_modules/** tooling tree are excluded; tracked root node_modules paths and consumed excluded Go inputs refuse.",
        "exclusions": EXCLUSION_POLICY,
        "excluded_path_count": excluded_count,
        "go_module_inventory": module_inventory,
        "go_input_closures": closures,
        "go_module_inventory_sha256": sha256(json_bytes(module_inventory, pretty=False)),
        "go_input_closures_sha256": sha256(json_bytes(closures, pretty=False)),
        "exclusions_sha256": sha256(json_bytes(EXCLUSION_POLICY, pretty=False)),
        "fingerprint_sha256": source_fingerprint_digest(
            sha256(record_blob), sha256(json_bytes(module_inventory, pretty=False)),
            sha256(json_bytes(closures, pretty=False)), sha256(json_bytes(EXCLUSION_POLICY, pretty=False))),
        "fingerprint_definition": "SHA256 of compact UTF-8 JSON array of ordered {path,mode,sha256} records; keys in that order; sorted by UTF-8 path bytes; ensure_ascii=False; separators comma/colon; no final newline.",
    }


def timestamp_is_valid(value: str) -> bool:
    if not re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z", value):
        return False
    try:
        parsed = dt.datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ")
    except ValueError:
        return False
    return parsed.strftime("%Y-%m-%dT%H:%M:%SZ") == value


def fixed_ldflags(head: str, timestamp: str) -> str:
    return (f"-s -w -X {MODULE}/internal/version.Version={VERSION} "
            f"-X {MODULE}/internal/version.Commit={head} "
            f"-X {MODULE}/internal/version.Date={timestamp}")


def source_fingerprint(source: dict[str, Any]) -> str:
    return str(source["fingerprint_sha256"])


def verify_source_unchanged(root: pathlib.Path, frozen: dict[str, Any]) -> dict[str, Any]:
    current = inventory(root)
    for key in ("head", "dirty", "status_paths", "tree_sha256", "input_inventory",
                "exclusions", "exclusions_sha256", "go_module_inventory",
                "go_module_inventory_sha256", "go_input_closures",
                "go_input_closures_sha256"):
        if current.get(key) != frozen.get(key):
            fail(f"source {key} changed since freeze")
    return current


def platform_header(data: bytes, target: tuple[str, str]) -> dict[str, Any]:
    goos, goarch = target
    expected = {
        ("linux", "amd64"): ("ELF64", 62),
        ("linux", "arm64"): ("ELF64", 183),
        ("windows", "amd64"): ("PE32+", 0x8664),
        ("windows", "arm64"): ("PE32+", 0xAA64),
        ("darwin", "amd64"): ("Mach-O64", 0x01000007),
        ("darwin", "arm64"): ("Mach-O64", 0x0100000C),
    }
    if target not in expected:
        fail("unsupported target refused")
    expected_format, expected_machine = expected[target]
    try:
        if data.startswith(b"\x7fELF"):
            if len(data) < 64 or data[4] != 2 or data[5] != 1:
                fail("ELF artifact is not a complete ELF64 header")
            machine = struct.unpack_from("<H", data, 18)[0]
            header = {"format": "ELF64", "machine": machine, "class": 2,
                      "endianness": "little"}
        elif data.startswith(b"MZ"):
            if len(data) < 64:
                fail("truncated PE DOS header")
            pe_offset = struct.unpack_from("<I", data, 0x3C)[0]
            if pe_offset + 26 > len(data) or data[pe_offset:pe_offset + 4] != b"PE\0\0":
                fail("invalid PE signature or truncated PE header")
            machine = struct.unpack_from("<H", data, pe_offset + 4)[0]
            optional_magic = struct.unpack_from("<H", data, pe_offset + 24)[0]
            if optional_magic != 0x20B:
                fail("PE artifact is not PE32+")
            header = {"format": "PE32+", "machine": machine, "optional_magic": optional_magic}
        elif len(data) >= 32 and data[:4] == b"\xcf\xfa\xed\xfe":
            endian = "<"
            magic = struct.unpack_from(endian + "I", data, 0)[0]
            if magic != 0xFEEDFACF:
                fail("Mach-O artifact is not 64-bit")
            machine = struct.unpack_from(endian + "I", data, 4)[0]
            header = {"format": "Mach-O64", "machine": machine,
                      "endianness": "little" if endian == "<" else "big"}
        else:
            fail("unknown executable format")
    except (IndexError, struct.error):
        fail("truncated executable header")
    if header["format"] != expected_format or header["machine"] != expected_machine:
        fail(f"binary header does not match target {goos}/{goarch}")
    return header


def expected_filename(name: str, target: tuple[str, str]) -> str:
    goos, goarch = target
    suffix = ".exe" if goos == "windows" else ""
    return f"cloud-pulse-{name}-{goos}-{goarch}{suffix}"


def build_env(*, go: bool = False, scratch: pathlib.Path | None = None) -> dict[str, str]:
    """Use a small inherited allowlist and explicitly neutralize Go config."""
    env = {key: os.environ[key] for key in ("PATH", "HOME", "TMPDIR", "TEMP", "TMP", "SYSTEMROOT", "WINDIR", "LANG", "LC_ALL", "TZ") if key in os.environ}
    env.setdefault("PATH", "/usr/bin:/bin")
    if scratch is not None:
        temp_dir = scratch / "tmp"
        temp_dir.mkdir(parents=True, exist_ok=True)
        env["TMPDIR"] = str(temp_dir)
        env["TEMP"] = str(temp_dir)
        env["TMP"] = str(temp_dir)
    if go:
        env.update({
            "GOTOOLCHAIN": "local",
            "GOPROXY": "off",
            "GOSUMDB": "off",
            "CGO_ENABLED": "0",
            "GOENV": "off",
            "GOWORK": "off",
            "GOFLAGS": "",
        })
    return env


def runtime_env(scratch: pathlib.Path) -> dict[str, str]:
    home = scratch / "home"
    home.mkdir(parents=True, exist_ok=True)
    tmp = scratch / "tmp"
    tmp.mkdir(parents=True, exist_ok=True)
    # No inherited application credentials, endpoint settings, proxies or CP_*.
    return {"PATH": "/usr/bin:/bin", "HOME": str(home), "TMPDIR": str(tmp),
            "LANG": "C", "LC_ALL": "C", "TZ": "UTC"}


def _limited_child() -> None:
    import resource
    resource.setrlimit(resource.RLIMIT_CPU, (25, 25))
    resource.setrlimit(resource.RLIMIT_FSIZE, (1024 * 1024, 1024 * 1024))
    resource.setrlimit(resource.RLIMIT_NOFILE, (64, 64))


def run_bounded(
    args: list[str], *, cwd: pathlib.Path, env: dict[str, str], timeout: int,
    capture: bool = True, limit_child: bool = False, scratch: pathlib.Path | None = None,
    output_limit: int = MAX_CHILD_OUTPUT,
) -> tuple[int, bytes, bytes]:
    """Execute in an owned group, enforcing time/output bounds while draining pipes."""
    if timeout <= 0 or output_limit <= 0:
        fail("invalid child process bounds")
    if os.name != "posix":
        fail("process-group containment is unavailable on this host")
    if limit_child and os.name == "posix":
        try:
            import resource  # noqa: F401
        except ImportError:
            fail("hard child resource limits are unavailable")
    if scratch is not None:
        scratch.mkdir(parents=True, exist_ok=True)
    try:
        process = subprocess.Popen(
            args, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            start_new_session=True,
            preexec_fn=_limited_child if limit_child else None,
        )
    except (OSError, ValueError, subprocess.SubprocessError):
        fail("required executable could not be started with required limits")

    selector: selectors.BaseSelector | None = None
    buffers: dict[Any, bytearray] = {}
    counts: dict[Any, int] = {}
    code: int | None = None
    failure: str | None = None

    def kill_group() -> bool:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            return True
        except OSError:
            return False
        return True

    def terminate_and_reap() -> bool:
        terminated = kill_group()
        try:
            process.wait(timeout=5)
            return terminated
        except subprocess.TimeoutExpired:
            terminated = kill_group() and terminated
        try:
            process.wait(timeout=5)
            return terminated
        except subprocess.TimeoutExpired:
            return False

    try:
        if process.stdout is None or process.stderr is None:
            fail("child output pipes were not created")
        selector = selectors.DefaultSelector()
        buffers = {process.stdout: bytearray(), process.stderr: bytearray()}
        counts = {process.stdout: 0, process.stderr: 0}
        for stream in buffers:
            os.set_blocking(stream.fileno(), False)
            selector.register(stream, selectors.EVENT_READ)
        deadline = time.monotonic() + timeout
        while selector.get_map() or code is None:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                failure = "command exceeded its time limit and was terminated"
                break
            events = selector.select(min(0.1, remaining)) if selector.get_map() else []
            if not selector.get_map():
                time.sleep(min(0.05, remaining))
            for key, _mask in events:
                stream = key.fileobj
                try:
                    chunk = os.read(stream.fileno(), 65536)
                except BlockingIOError:
                    continue
                if not chunk:
                    selector.unregister(stream)
                    stream.close()
                    continue
                counts[stream] += len(chunk)
                if counts[stream] > output_limit:
                    failure = "command output exceeded its bounded evidence limit"
                    break
                if capture:
                    buffers[stream].extend(chunk)
            if failure:
                break
            if code is None:
                code = process.poll()
                if code is not None:
                    # Kill any descendants that inherited either pipe, even if
                    # their parent exited successfully before them.
                    kill_group()
        # Also terminate group members when both streams closed before the
        # direct child was observed exiting.
        cleanup_ok = terminate_and_reap()
        code = process.returncode
        # Finish draining only after group termination; any remaining data is
        # still subject to the same hard output cap.
        drain_deadline = time.monotonic() + 2
        while selector.get_map() and time.monotonic() < drain_deadline:
            for key, _mask in selector.select(0.05):
                stream = key.fileobj
                try:
                    chunk = os.read(stream.fileno(), 65536)
                except BlockingIOError:
                    continue
                if not chunk:
                    selector.unregister(stream)
                    stream.close()
                    continue
                counts[stream] += len(chunk)
                if counts[stream] > output_limit:
                    failure = "command output exceeded its bounded evidence limit"
                    kill_group()
                    break
                if capture:
                    buffers[stream].extend(chunk)
        if selector.get_map():
            failure = failure or "command descendants retained output pipes after termination"
            terminate_and_reap()
            for key in list(selector.get_map().values()):
                selector.unregister(key.fileobj)
                key.fileobj.close()
        if failure:
            fail(failure)
        if not cleanup_ok:
            fail("owned process group could not be terminated/reaped")
        assert code is not None
        return code, bytes(buffers[process.stdout]), bytes(buffers[process.stderr])
    except BaseException as original:
        if not terminate_and_reap():
            raise VerificationError("owned process group could not be terminated/reaped") from original
        raise
    finally:
        if selector is not None:
            selector.close()
        for stream in (process.stdout, process.stderr):
            if stream is None:
                continue
            if not stream.closed:
                stream.close()


def require_command_ok(code: int, _stdout: bytes, _stderr: bytes, description: str) -> bytes:
    if code != 0:
        fail(f"{description} failed (exit {code}); raw output was not retained")
    return _stdout


def load_frozen(out: pathlib.Path) -> dict[str, Any]:
    source = read_json(out / "source-manifest.json")
    if not isinstance(source, dict):
        fail("source freeze is malformed")
    if source.get("schema") != "round30-platform-source-v1":
        fail("unsupported or missing source freeze")
    if source.get("version") != VERSION or not timestamp_is_valid(str(source.get("frozen_utc", ""))):
        fail("source freeze metadata is invalid")
    if not SUPPORTED_HEAD.fullmatch(str(source.get("head", ""))):
        fail("source freeze does not pin a full HEAD")
    expected_metadata = {
        "version": VERSION,
        "commit": source["head"],
        "date": source["frozen_utc"],
        "ldflags": fixed_ldflags(source["head"], source["frozen_utc"]),
    }
    if source.get("linker_metadata") != expected_metadata:
        fail("source freeze linker flags do not match pinned metadata")
    inventory_data = source.get("source")
    if not isinstance(inventory_data, dict):
        fail("source freeze inventory missing")
    computed = sha256(json_bytes(inventory_data.get("input_inventory"), pretty=False))
    if computed != inventory_data.get("tree_sha256"):
        fail("source freeze inventory fingerprint mismatch")
    if (inventory_data.get("exclusions") != EXCLUSION_POLICY or
            inventory_data.get("exclusions_sha256") != sha256(json_bytes(EXCLUSION_POLICY, pretty=False))):
        fail("source freeze exclusion inventory mismatch")
    if inventory_data.get("go_module_inventory_sha256") != sha256(
            json_bytes(inventory_data.get("go_module_inventory"), pretty=False)):
        fail("source freeze module inventory fingerprint mismatch")
    if inventory_data.get("go_input_closures_sha256") != sha256(
            json_bytes(inventory_data.get("go_input_closures"), pretty=False)):
        fail("source freeze Go input closure fingerprint mismatch")
    combined = source_fingerprint_digest(
        str(inventory_data["tree_sha256"]), str(inventory_data["go_module_inventory_sha256"]),
        str(inventory_data["go_input_closures_sha256"]), str(inventory_data["exclusions_sha256"]))
    if combined != inventory_data.get("fingerprint_sha256") or source.get("fingerprint_sha256") != combined:
        fail("source freeze composite fingerprint mismatch")
    closures = inventory_data.get("go_input_closures")
    expected_closures = {(goos, goarch, name) for goos, goarch in TARGETS
                         for name in PACKAGES}
    if not isinstance(closures, list) or any(not isinstance(row, dict) or not isinstance(row.get("target"), dict)
                                               for row in closures):
        fail("source freeze Go input closure is malformed")
    if {(row["target"].get("goos"), row["target"].get("goarch"), row.get("package"))
            for row in closures} != expected_closures:
        fail("source freeze Go input closure target/package set mismatch")
    for key in ("head", "dirty", "status_paths"):
        if source.get(key) != inventory_data.get(key):
            fail(f"source freeze {key} binding mismatch")
    if bool(source.get("dirty")) != bool(source.get("status_paths")):
        fail("source freeze dirty status/path mismatch")
    if source.get("dirty") is not True:
        fail("dirty release version requires a dirty source freeze")
    return source


def freeze(root: pathlib.Path, out: pathlib.Path, timestamp: str) -> dict[str, Any]:
    root, out = _check_paths(root, out)
    if not timestamp_is_valid(timestamp):
        fail("timestamp must be exact UTC YYYY-MM-DDTHH:MM:SSZ")
    _ensure_output_dir(root, out, out)
    source_path = out / "source-manifest.json"
    if source_path.exists() or source_path.is_symlink():
        fail("refusing to overwrite an existing source freeze")
    before = inventory(root)
    if not before["dirty"]:
        fail("dirty version requested but source worktree has no dirty status paths")
    frozen = {
        "schema": "round30-platform-source-v1",
        "status": "preliminary-source-freeze",
        "version": VERSION,
        "frozen_utc": timestamp,
        "head": before["head"],
        "dirty": before["dirty"],
        "status_paths": before["status_paths"],
        "fingerprint_sha256": before["fingerprint_sha256"],
        "source": before,
        "linker_metadata": {
            "version": VERSION,
            "commit": before["head"],
            "date": timestamp,
            "ldflags": fixed_ldflags(before["head"], timestamp),
        },
        "build_policy": {
            "targets": [f"{goos}/{goarch}" for goos, goarch in TARGETS],
            "binaries_per_target": ["hub", "agent"],
            "CGO_ENABLED": "0", "GOTOOLCHAIN": "local", "GOPROXY": "off",
            "GOSUMDB": "off", "GOENV": "off", "trimpath": True,
            "GOWORK": "off", "GOFLAGS": "", "module_mode": "readonly",
            "linker_flags": "-s -w plus the exact frozen version/commit/date -X assignments",
            "release_script_semantics": "scripts/build-release.sh package paths and naming, CGO-disabled trimpath release builds and linker metadata; invoked directly per target to pin full HEAD and one fixed timestamp without editing the release script.",
            "network": "module and toolchain downloads disabled; GOPROXY=off, GOSUMDB=off, GOTOOLCHAIN=local",
            "inherited_environment": "PATH/HOME and standard locale variables only; TMP/TEMP/TMPDIR redirected into evidence scratch; GOFLAGS explicitly empty, GOWORK off, GOARM/CP_* and proxy variables omitted",
        },
    }
    after = inventory(root)
    for key in ("head", "dirty", "status_paths", "tree_sha256", "input_inventory",
                "exclusions", "exclusions_sha256", "go_module_inventory",
                "go_module_inventory_sha256", "go_input_closures",
                "go_input_closures_sha256"):
        if after[key] != before[key]:
            fail(f"source changed while freezing: {key}")
    write_json(source_path, frozen)
    return frozen


def _tool_version(root: pathlib.Path, scratch: pathlib.Path) -> str:
    go_path = shutil.which("go", path=build_env(go=True, scratch=scratch).get("PATH"))
    if go_path is None:
        fail("Go toolchain unavailable")
    code, stdout, stderr = run_bounded(
        ["go", "version"], cwd=root, env=build_env(go=True, scratch=scratch), timeout=15, scratch=scratch,
    )
    output = require_command_ok(code, stdout, stderr, "go version")
    try:
        text = output.decode("utf-8", "strict").strip()
    except UnicodeDecodeError:
        fail("Go version output is not UTF-8")
    if not text.startswith("go version go"):
        fail("unrecognized Go version output")
    return text


def binary_record(root: pathlib.Path, out: pathlib.Path, target: tuple[str, str], name: str,
                  go_version: str, expected_go_version_m: str | None = None) -> dict[str, Any]:
    filename = expected_filename(name, target)
    relpath = pathlib.Path(".verification/round30/platforms/build") / filename
    path = root / relpath
    if not path.is_file() or path.is_symlink():
        fail(f"missing or unsafe matrix binary: {filename}")
    data = path.read_bytes()
    header = platform_header(data, target)
    try:
        rel = relpath.as_posix()
        code, stdout, stderr = run_bounded(
            ["go", "version", "-m", rel], cwd=root, env=build_env(go=True, scratch=out), timeout=30,
            scratch=out, limit_child=True,
        )
    except VerificationError:
        raise
    output = require_command_ok(code, stdout, stderr, f"go version -m {filename}")
    try:
        go_version_m = output.decode("utf-8", "strict")
    except UnicodeDecodeError:
        fail(f"go version -m output is not UTF-8: {filename}")
    if expected_go_version_m is not None and go_version_m != expected_go_version_m:
        fail(f"go version -m changed since build: {filename}")
    lines = go_version_m.splitlines()
    toolchain_match = re.fullmatch(re.escape(rel) + r":\s+(go\S+)", lines[0]) if lines else None
    if not toolchain_match:
        fail(f"go version -m output does not identify {filename}")
    if toolchain_match.group(1) not in go_version.split():
        fail(f"artifact Go toolchain does not match recorded toolchain: {filename}")
    settings = {}
    for line in lines:
        match = re.fullmatch(r"\s*build\s+(\S+)=(.*)", line)
        if match:
            settings[match.group(1)] = match.group(2)
    if settings.get("GOOS") != target[0] or settings.get("GOARCH") != target[1] or settings.get("CGO_ENABLED") != "0":
        fail(f"Go build metadata target/settings mismatch: {filename}")
    if settings.get("-trimpath") != "true":
        fail(f"Go build metadata does not prove trimpath: {filename}")
    return {
        "name": name,
        "path": rel,
        "bytes": len(data),
        "sha256": sha256(data),
        "header": header,
        "go_version_m": go_version_m,
        "go_version_m_settings": settings,
        "toolchain_version": go_version,
    }


def build(root: pathlib.Path, out: pathlib.Path) -> dict[str, Any]:
    root, out = _check_paths(root, out)
    frozen = load_frozen(out)
    verify_source_unchanged(root, frozen["source"])
    build_result_path = out / "build-results.json"
    if build_result_path.exists() or build_result_path.is_symlink():
        fail("refusing to overwrite existing build results")
    build_dir = out / "build"
    _ensure_output_dir(root, out, build_dir)
    _ensure_output_dir(root, out, out / "tmp")
    tool_version = _tool_version(root, out)
    result_targets = []
    command_records = []
    for target in TARGETS:
        goos, goarch = target
        artifacts: dict[str, Any] = {}
        for name, package in PACKAGES.items():
            filename = expected_filename(name, target)
            output_path = pathlib.Path(".verification/round30/platforms/build") / filename
            dest = root / output_path
            if dest.exists() or dest.is_symlink():
                fail(f"refusing to overwrite existing binary: {filename}")
            ldflags = frozen["linker_metadata"]["ldflags"]
            command = ["go", "build", "-mod=readonly", "-trimpath", "-ldflags", ldflags,
                       "-o", output_path.as_posix(), package]
            env = build_env(go=True, scratch=out)
            env["GOOS"] = goos
            env["GOARCH"] = goarch
            # GOARM and inherited toolchain/proxy settings are absent; GOFLAGS
            # is explicitly cleared and module mode is pinned in argv.
            code, stdout, stderr = run_bounded(command, cwd=root, env=env, timeout=600,
                                               capture=False, scratch=out)
            if code != 0:
                fail(f"build failed for {goos}/{goarch} {name} (exit {code}); raw output was not retained")
            record = binary_record(root, out, target, name, tool_version)
            artifacts[name] = record
            command_records.append({
                "target": {"goos": goos, "goarch": goarch},
                "binary": name,
                "package": package,
                "argv": command,
                "exit_code": code,
                "environment": {
                    "CGO_ENABLED": "0", "GOOS": goos, "GOARCH": goarch,
                    "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOENV": "off",
                    "GOWORK": "off", "GOFLAGS": "",
                    "trimpath": True,
                },
            })
        result_targets.append({"goos": goos, "goarch": goarch, "artifacts": artifacts})
    verify_source_unchanged(root, frozen["source"])
    result = {
        "schema": "round30-platform-build-v1",
        "status": "preliminary-build-results",
        "source_head": frozen["head"],
        "source_fingerprint_sha256": frozen["fingerprint_sha256"],
        "source_inventory_sha256": frozen["source"]["tree_sha256"],
        "go_input_closures_sha256": frozen["source"]["go_input_closures_sha256"],
        "go_module_inventory_sha256": frozen["source"]["go_module_inventory_sha256"],
        "frozen_utc": frozen["frozen_utc"],
        "version": VERSION,
        "module_mode": "readonly",
        "linker_metadata": frozen["linker_metadata"],
        "go_version": tool_version,
        "go_executable": shutil.which("go", path=build_env(go=True).get("PATH")),
        "toolchain_policy": {"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOENV": "off", "GOWORK": "off", "GOFLAGS": "", "CGO_ENABLED": "0"},
        "targets": result_targets,
        "commands": command_records,
    }
    write_json(build_result_path, result)
    return result


def runner_availability() -> dict[str, bool]:
    tools = ("docker", "wine", "qemu-aarch64", "qemu-aarch64-static", "qemu-x86_64", "qemu-x86_64-static")
    return {name: shutil.which(name) is not None for name in tools}


def runtime_class(target: tuple[str, str], native_linux_arm64: bool) -> str:
    if target == ("linux", "arm64") and native_linux_arm64:
        return "NATIVE_LINUX_AARCH64_SMOKE"
    return "STATIC_ONLY/NOT_EXECUTED"


def _hash_guarded_execution(binary: pathlib.Path, args: list[str], *, cwd: pathlib.Path,
                            env: dict[str, str], scratch: pathlib.Path,
                            expected_sha: str) -> tuple[dict[str, Any], bytes]:
    before = sha_file(binary)
    if before != expected_sha:
        fail(f"binary hash changed before execution: {binary.name}")
    try:
        code, stdout, stderr = run_bounded(args, cwd=cwd, env=env, timeout=30,
                                           capture=True, limit_child=True, scratch=scratch)
    except VerificationError:
        after = sha_file(binary)
        if after != before:
            fail(f"binary hash changed during execution: {binary.name}")
        raise
    after = sha_file(binary)
    if after != before:
        fail(f"binary hash changed during execution: {binary.name}")
    execution = {
        "argv": args,
        "exit_code": code,
        "binary_sha256_before": before,
        "binary_sha256_after": after,
        "binary_hash_unchanged": True,
        "timeout_seconds": 30,
        "output_limit_bytes": MAX_CHILD_OUTPUT,
        "stdout_bytes": len(stdout),
        "stdout_sha256": sha256(stdout),
        "stderr_bytes": len(stderr),
        "stderr_sha256": sha256(stderr),
    }
    if code != 0:
        fail(f"native binary command failed (exit {code}); raw output was not retained")
    return execution, stdout


def native_smoke(root: pathlib.Path, out: pathlib.Path, target_row: dict[str, Any],
                 scratch: pathlib.Path, expected: dict[str, str]) -> dict[str, Any]:
    env = runtime_env(scratch)
    artifacts = target_row["artifacts"]
    executions: dict[str, Any] = {}
    agent = root / artifacts["agent"]["path"]
    expected_agent_sha = artifacts["agent"]["sha256"]
    agent_version, version_bytes = _hash_guarded_execution(
        agent, [str(agent), "-version"], cwd=root, env=env, scratch=scratch,
        expected_sha=expected_agent_sha,
    )
    try:
        printed_version = version_bytes.decode("utf-8", "strict").strip()
    except UnicodeDecodeError:
        fail("native agent -version output is not UTF-8")
    if printed_version != expected["version"]:
        fail("native agent version/commit/date does not match frozen linker metadata")
    agent_version["observed_version"] = printed_version
    executions["agent_version"] = agent_version

    agent_once, sample = _hash_guarded_execution(
        agent, [str(agent), "-once"], cwd=root, env=env, scratch=scratch,
        expected_sha=expected_agent_sha,
    )
    try:
        parsed = json.loads(sample.decode("utf-8", "strict"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        fail("native agent -once did not emit one valid JSON sample")
    if not isinstance(parsed, dict):
        fail("native agent -once JSON sample was not an object")
    # Do not retain host/sample values: only report shape and byte count.
    agent_once["sample_observation"] = {
        "valid_json_object": True,
        "bytes": len(sample),
        "top_level_keys": sorted(str(key) for key in parsed.keys()),
        "values_recorded": False,
        "hub_configuration_used": False,
        "send_performed": False,
    }
    executions["agent_once"] = agent_once

    hub = root / artifacts["hub"]["path"]
    hub_version, hub_bytes = _hash_guarded_execution(
        hub, [str(hub), "-version"], cwd=root, env=env, scratch=scratch,
        expected_sha=artifacts["hub"]["sha256"],
    )
    try:
        printed_hub_version = hub_bytes.decode("utf-8", "strict").strip()
    except UnicodeDecodeError:
        fail("native hub -version output is not UTF-8")
    if printed_hub_version != expected["version"]:
        fail("native hub version/commit/date does not match frozen linker metadata")
    hub_version["observed_version"] = printed_hub_version
    executions["hub_version"] = hub_version
    return {
        "status": "passed",
        "native_platform": "linux/aarch64",
        "native_host": {"platform": sys.platform,
                        "machine": os.uname().machine if hasattr(os, "uname") else "unknown"},
        "target": {"goos": "linux", "goarch": "arm64"},
        "source_fingerprint_sha256": expected["source_fingerprint_sha256"],
        "exact_artifact_paths": {name: record["path"] for name, record in artifacts.items()},
        "synthetic_environment": {
            "allowlisted_variables": ["PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "TZ"],
            "inherited_CP_variables": False,
            "inherited_proxy_variables": False,
            "application_credentials_or_endpoints": False,
        },
        "agent_once_scope": "two local samples; no hub config, hub process, network send, or production endpoint",
        "executions": executions,
    }


def _record_source(frozen: dict[str, Any]) -> dict[str, Any]:
    return {
        "head": frozen["head"], "dirty": frozen["dirty"],
        "status_paths": frozen["status_paths"],
        "fingerprint_sha256": frozen["fingerprint_sha256"],
        "frozen_utc": frozen["frozen_utc"], "version": frozen["version"],
        "input_inventory": frozen["source"]["input_inventory"],
        "tree_sha256": frozen["source"]["tree_sha256"],
        "selection": frozen["source"]["selection"],
        "exclusions": frozen["source"]["exclusions"],
        "exclusions_sha256": frozen["source"]["exclusions_sha256"],
        "excluded_path_count": frozen["source"]["excluded_path_count"],
        "go_module_inventory": frozen["source"]["go_module_inventory"],
        "go_module_inventory_sha256": frozen["source"]["go_module_inventory_sha256"],
        "go_input_closures": frozen["source"]["go_input_closures"],
        "go_input_closures_sha256": frozen["source"]["go_input_closures_sha256"],
    }


def _publish_json_no_replace(path: pathlib.Path, value: Any) -> None:
    """Atomically publish a fully validated JSON file without clobbering."""
    try:
        parent_info = path.parent.lstat()
        if stat.S_ISLNK(parent_info.st_mode) or not stat.S_ISDIR(parent_info.st_mode):
            fail("terminal manifest parent is unsafe")
        if path.exists() or path.is_symlink():
            fail("refusing to overwrite an existing terminal matrix manifest")
        fd, temporary_name = tempfile.mkstemp(prefix=".matrix-manifest-", dir=path.parent)
    except VerificationError:
        raise
    except OSError:
        fail("cannot stage terminal matrix manifest")
    temporary = pathlib.Path(temporary_name)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(json_bytes(value))
            stream.flush()
            os.fsync(stream.fileno())
        try:
            os.link(temporary, path)
        except FileExistsError:
            fail("refusing to overwrite an existing terminal matrix manifest")
        except OSError:
            fail("cannot atomically publish terminal matrix manifest")
    finally:
        try:
            temporary.unlink(missing_ok=True)
        except OSError:
            pass


def _validate_source_record(source: Any) -> dict[str, Any]:
    if not isinstance(source, dict):
        fail("final matrix source inventory is missing")
    records = source.get("input_inventory")
    if not isinstance(records, list) or not records:
        fail("final matrix source input inventory is incomplete")
    names = []
    for record in records:
        if not isinstance(record, dict) or set(record) != {"path", "mode", "sha256"}:
            fail("final matrix source inventory record is malformed")
        name = record["path"]
        if not isinstance(name, str) or _excluded(name) or not HEX_SHA256.fullmatch(str(record["sha256"])):
            fail("final matrix source inventory path/hash is invalid")
        if not re.fullmatch(r"0[0-7]{3}", str(record["mode"])):
            fail("final matrix source inventory mode is invalid")
        names.append(name)
    if names != sorted(set(names), key=lambda value: value.encode("utf-8")):
        fail("final matrix source inventory ordering is invalid")
    tree_hash = sha256(json_bytes(records, pretty=False))
    if source.get("tree_sha256") != tree_hash:
        fail("final matrix source inventory hash mismatch")
    if source.get("exclusions") != EXCLUSION_POLICY or source.get("exclusions_sha256") != sha256(
            json_bytes(EXCLUSION_POLICY, pretty=False)):
        fail("final matrix source exclusions are incomplete or altered")
    modules = source.get("go_module_inventory")
    if not isinstance(modules, list) or not modules or source.get("go_module_inventory_sha256") != sha256(
            json_bytes(modules, pretty=False)):
        fail("final matrix Go module inventory hash mismatch")
    for module in modules:
        if not isinstance(module, dict) or not isinstance(module.get("path"), str) or not module.get("path"):
            fail("final matrix Go module identity is malformed")
    closures = source.get("go_input_closures")
    expected_closures = {(goos, goarch, name) for goos, goarch in TARGETS for name in PACKAGES}
    if not isinstance(closures, list) or source.get("go_input_closures_sha256") != sha256(
            json_bytes(closures, pretty=False)):
        fail("final matrix Go source closure hash mismatch")
    combined = source_fingerprint_digest(tree_hash, source["go_module_inventory_sha256"],
                                         source["go_input_closures_sha256"],
                                         source["exclusions_sha256"])
    if source.get("fingerprint_sha256") != combined:
        fail("final matrix composite source fingerprint mismatch")
    seen = set()
    inventory_names = set(names)
    for closure in closures:
        if not isinstance(closure, dict) or not isinstance(closure.get("target"), dict):
            fail("final matrix Go source closure is malformed")
        target = closure.get("target", {})
        key = (target.get("goos"), target.get("goarch"), closure.get("package"))
        if key not in expected_closures or key in seen:
            fail("final matrix Go source closure target/package mismatch")
        seen.add(key)
        expected_closure_command = ["go", "list", "-mod=readonly", "-deps", "-json",
                                    PACKAGES[key[2]]]
        expected_closure_env = {"GOOS": key[0], "GOARCH": key[1], "CGO_ENABLED": "0",
                                "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off",
                                "GOENV": "off", "GOWORK": "off", "GOFLAGS": ""}
        if closure.get("command") != expected_closure_command or closure.get("environment") != expected_closure_env:
            fail("final matrix Go closure command/environment policy mismatch")
        inputs = closure.get("repo_inputs")
        if (not isinstance(inputs, list) or any(not isinstance(value, str) for value in inputs) or
                inputs != sorted(set(inputs), key=lambda value: value.encode("utf-8"))):
            fail("final matrix Go source closure inputs are incomplete")
        if not set(inputs).issubset(inventory_names):
            fail("final matrix Go source closure references an unrecorded input")
        if closure.get("module_identities") != modules:
            fail("final matrix Go source closure module identity mismatch")
    if seen != expected_closures:
        fail("final matrix Go source closure target/package set is incomplete")
    if not SUPPORTED_HEAD.fullmatch(str(source.get("head", ""))) or source.get("dirty") is not True:
        fail("final matrix source metadata is invalid")
    status_paths = source.get("status_paths")
    if not isinstance(status_paths, list) or not status_paths:
        fail("final matrix dirty source paths are incomplete")
    for row in status_paths:
        if not isinstance(row, dict) or not isinstance(row.get("path"), str) or _excluded(row["path"]):
            fail("final matrix source status path is malformed")
    if not timestamp_is_valid(str(source.get("frozen_utc", ""))) or source.get("version") != VERSION:
        fail("final matrix source freeze metadata is invalid")
    return source


def _validate_native_record(native: Any, *, root: pathlib.Path, target: tuple[str, str],
                            binaries: dict[str, Any], fingerprint: str,
                            expected_version: str, host_platform: str,
                            host_machine: str) -> None:
    if not isinstance(native, dict) or native.get("status") != "passed":
        fail("native execution evidence is incomplete")
    if target != ("linux", "arm64") or host_platform != sys.platform or not host_platform.startswith("linux") or host_machine.lower() not in ("aarch64", "arm64"):
        fail("native execution host evidence is not the actual Linux/aarch64 host")
    actual_machine = os.uname().machine if hasattr(os, "uname") else "unknown"
    if host_machine != actual_machine:
        fail("native execution machine evidence does not match this host")
    if (native.get("native_platform") != "linux/aarch64" or
            native.get("native_host") != {"platform": host_platform, "machine": host_machine} or
            native.get("target") != {"goos": target[0], "goarch": target[1]} or
            native.get("source_fingerprint_sha256") != fingerprint):
        fail("native execution target/fingerprint binding mismatch")
    expected_paths = {name: f".verification/round30/platforms/build/{expected_filename(name, target)}"
                      for name in ("hub", "agent")}
    if native.get("exact_artifact_paths") != expected_paths:
        fail("native execution artifact paths are not canonical")
    expected_environment = {
        "allowlisted_variables": ["PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "TZ"],
        "inherited_CP_variables": False, "inherited_proxy_variables": False,
        "application_credentials_or_endpoints": False,
    }
    if native.get("synthetic_environment") != expected_environment:
        fail("native execution environment is not credential-scrubbed")
    executions = native.get("executions")
    if not isinstance(executions, dict) or set(executions) != {"agent_version", "agent_once", "hub_version"}:
        fail("native command execution records are incomplete")
    version_paths = {"agent_version": "agent", "agent_once": "agent", "hub_version": "hub"}
    for record_name, binary_name in version_paths.items():
        record = executions.get(record_name)
        binary = binaries[binary_name]
        expected_argv = [str(root / expected_paths[binary_name]),
                         "-once" if record_name == "agent_once" else "-version"]
        if (not isinstance(record, dict) or record.get("argv") != expected_argv or
                record.get("exit_code") != 0 or
                record.get("binary_sha256_before") != binary.get("sha256") or
                record.get("binary_sha256_after") != binary.get("sha256") or
                record.get("binary_hash_unchanged") is not True or
                record.get("timeout_seconds") != 30 or
                record.get("output_limit_bytes") != MAX_CHILD_OUTPUT):
            fail("native command result/hash/argv evidence is invalid")
        for prefix in ("stdout", "stderr"):
            count = record.get(f"{prefix}_bytes")
            digest = record.get(f"{prefix}_sha256")
            if not isinstance(count, int) or count < 0 or count > MAX_CHILD_OUTPUT or not HEX_SHA256.fullmatch(str(digest)):
                fail("native command log hash/bound evidence is invalid")
        if record_name in ("agent_version", "hub_version"):
            if record.get("observed_version") != expected_version or record.get("stdout_bytes", 0) <= 0:
                fail("native version command result is invalid")
        else:
            observation = record.get("sample_observation")
            if (not isinstance(observation, dict) or observation.get("valid_json_object") is not True or
                    observation.get("values_recorded") is not False or
                    observation.get("hub_configuration_used") is not False or
                    observation.get("send_performed") is not False or
                    observation.get("bytes") != record.get("stdout_bytes") or observation.get("bytes", 0) <= 0):
                fail("native -once result evidence is invalid")


def validate_final_manifest(out: pathlib.Path, manifest: dict[str, Any] | None = None,
                            *, check_artifacts: bool = True,
                            report_root: pathlib.Path | None = None) -> dict[str, Any]:
    """Validate a complete terminal manifest before it can be published."""
    if manifest is None:
        manifest = read_json(out / "matrix-manifest.json")
    if not isinstance(manifest, dict):
        fail("final matrix manifest is malformed")
    if (manifest.get("schema") != "round30-platform-matrix-v1" or
            manifest.get("status") != "FINALIZED" or manifest.get("verified") is not True or
            manifest.get("finalized") is not True or
            not timestamp_is_valid(str(manifest.get("finalized_utc", "")))):
        fail("matrix manifest is not terminally finalized")
    if manifest.get("version") != VERSION:
        fail("matrix manifest version mismatch")
    root = out.resolve(strict=True).parents[2]
    source = _validate_source_record(manifest.get("source"))
    if manifest.get("linker_metadata") != {
            "version": VERSION, "commit": source["head"],
            "date": source["frozen_utc"],
            "ldflags": fixed_ldflags(source["head"], source["frozen_utc"])}:
        fail("matrix linker metadata does not bind the frozen source")
    environment = manifest.get("environment")
    if not isinstance(environment, dict):
        fail("matrix host environment evidence is missing")
    actual_machine = os.uname().machine if hasattr(os, "uname") else "unknown"
    if environment.get("platform") != sys.platform or environment.get("machine") != actual_machine:
        fail("matrix host environment does not match the actual host")
    native_linux_arm64 = sys.platform.startswith("linux") and actual_machine.lower() in ("aarch64", "arm64")
    targets = manifest.get("targets")
    if not isinstance(targets, list) or len(targets) != len(TARGETS):
        fail("matrix manifest target count mismatch")
    if [(item.get("goos"), item.get("goarch")) if isinstance(item, dict) else None
            for item in targets] != list(TARGETS):
        fail("matrix target order/set is not canonical")
    seen: set[tuple[str, str]] = set()
    report_base = report_root if report_root is not None else out
    expected_version = f"{VERSION} ({source['head']}, {source['frozen_utc']})"
    for item in targets:
        if not isinstance(item, dict):
            fail("matrix manifest target record is malformed")
        target = (item.get("goos"), item.get("goarch"))
        if target not in TARGET_SET or target in seen:
            fail("matrix manifest has missing, duplicate, or unexpected target")
        seen.add(target)
        expected_class = runtime_class(target, native_linux_arm64)
        if item.get("verification_class") != expected_class:
            fail("matrix target verification class mismatch")
        expected_report = f"reports/{target[0]}-{target[1]}.json"
        if item.get("report_path") != expected_report:
            fail("matrix report path is not canonical")
        report_path = _safe_input_path(report_base, expected_report)
        if not report_path.is_file() or report_path.is_symlink():
            fail("final per-target report missing or unsafe")
        report_data = report_path.read_bytes()
        if sha256(report_data) != item.get("report_sha256"):
            fail("final per-target report hash mismatch")
        report = read_json(report_path)
        if not isinstance(report, dict):
            fail("per-target report is malformed")
        if (report.get("schema") != "round30-platform-target-report-v1" or
                report.get("status") != "passed"):
            fail("invalid per-target report status/schema")
        if report.get("target") != {"goos": target[0], "goarch": target[1]}:
            fail("per-target report target mismatch")
        if report.get("verification_class") != expected_class:
            fail("per-target report verification class mismatch")
        if report.get("source") != source:
            fail("per-target report source inventory/exclusion binding mismatch")
        binaries = report.get("binaries")
        matrix_binaries = item.get("binaries")
        if not isinstance(binaries, dict) or set(binaries) != {"hub", "agent"} or not isinstance(matrix_binaries, dict) or set(matrix_binaries) != {"hub", "agent"}:
            fail("per-target report does not bind hub and agent binaries")
        for name in ("hub", "agent"):
            binary = binaries[name]
            expected_path = f".verification/round30/platforms/build/{expected_filename(name, target)}"
            if (not isinstance(binary, dict) or binary.get("path") != expected_path or
                    not HEX_SHA256.fullmatch(str(binary.get("sha256"))) or
                    matrix_binaries[name] != {"path": expected_path, "sha256": binary.get("sha256")}):
                fail("per-target report binary path/hash binding mismatch")
            if check_artifacts:
                binary_path = _safe_input_path(root, expected_path)
                if not binary_path.is_file() or binary_path.is_symlink() or sha_file(binary_path) != binary["sha256"]:
                    fail("matrix binary hash mismatch")
                expected_header = platform_header(binary_path.read_bytes(), target)
                if binary.get("header") != expected_header or binary.get("bytes") != binary_path.stat().st_size:
                    fail("matrix binary header/report mismatch")
        if report.get("native_claim") is not (expected_class == "NATIVE_LINUX_AARCH64_SMOKE"):
            fail("per-target native claim/class mismatch")
        native = report.get("native_smoke")
        if expected_class == "STATIC_ONLY/NOT_EXECUTED":
            if native != {"status": "not-executed",
                          "reason": "foreign/static-only target or host is not Linux/aarch64",
                          "claims": []}:
                fail("foreign/static-only target contains native execution evidence")
        else:
            _validate_native_record(native, root=root, target=target, binaries=binaries,
                                    fingerprint=source["fingerprint_sha256"],
                                    expected_version=expected_version,
                                    host_platform=environment["platform"],
                                    host_machine=environment["machine"])
    if seen != TARGET_SET:
        fail("matrix manifest target set mismatch")
    return manifest


def verify(root: pathlib.Path, out: pathlib.Path) -> dict[str, Any]:
    root, out = _check_paths(root, out)
    manifest_path = out / "matrix-manifest.json"
    if manifest_path.exists() or manifest_path.is_symlink():
        fail("refusing to overwrite an existing terminal matrix manifest")
    reports_dir = out / "reports"
    if reports_dir.exists() or reports_dir.is_symlink():
        fail("refusing to overwrite existing final reports")
    _ensure_output_dir(root, out, out / "tmp")
    frozen = load_frozen(out)
    verify_source_unchanged(root, frozen["source"])
    build_result = read_json(out / "build-results.json")
    if not isinstance(build_result, dict):
        fail("build results are malformed")
    if build_result.get("schema") != "round30-platform-build-v1":
        fail("build results missing or invalid")
    if (build_result.get("source_head") != frozen["head"] or
            build_result.get("source_fingerprint_sha256") != frozen["fingerprint_sha256"] or
            build_result.get("source_inventory_sha256") != frozen["source"]["tree_sha256"] or
            build_result.get("go_input_closures_sha256") != frozen["source"]["go_input_closures_sha256"] or
            build_result.get("go_module_inventory_sha256") != frozen["source"]["go_module_inventory_sha256"]):
        fail("build results are not bound to frozen source")
    if build_result.get("version") != VERSION or build_result.get("frozen_utc") != frozen["frozen_utc"]:
        fail("build result metadata mismatch")
    if build_result.get("module_mode") != "readonly":
        fail("build results do not prove readonly module mode")
    expected_policy = {"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOENV": "off", "GOWORK": "off", "GOFLAGS": "", "CGO_ENABLED": "0"}
    if build_result.get("linker_metadata") != frozen["linker_metadata"] or build_result.get("toolchain_policy") != expected_policy:
        fail("build flags/toolchain policy do not match source freeze")
    rows = build_result.get("targets")
    if not isinstance(rows, list) or len(rows) != len(TARGETS) or any(not isinstance(row, dict) for row in rows):
        fail("build result target count mismatch")
    by_target: dict[tuple[str, str], dict[str, Any]] = {}
    for row in rows:
        target = (row.get("goos"), row.get("goarch"))
        if target not in TARGET_SET or target in by_target:
            fail("build results have unexpected or duplicate targets")
        by_target[target] = row
    if set(by_target) != TARGET_SET:
        fail("build result target set mismatch")
    go_version = build_result.get("go_version")
    if not isinstance(go_version, str) or not go_version.startswith("go version go"):
        fail("build result toolchain version missing")
    command_rows = build_result.get("commands")
    if not isinstance(command_rows, list) or len(command_rows) != 12 or any(not isinstance(row, dict) for row in command_rows):
        fail("build command count mismatch")
    command_index = {}
    for record in command_rows:
        if not isinstance(record, dict) or not isinstance(record.get("target"), dict):
            fail("build command provenance is malformed")
        key = (record.get("target", {}).get("goos"), record.get("target", {}).get("goarch"), record.get("binary"))
        if key in command_index:
            fail("duplicate build command provenance")
        command_index[key] = record
    for goos, goarch in TARGETS:
        for name, package in PACKAGES.items():
            expected_output = (pathlib.Path(".verification/round30/platforms/build") /
                               expected_filename(name, (goos, goarch))).as_posix()
            expected_command = ["go", "build", "-mod=readonly", "-trimpath", "-ldflags",
                                frozen["linker_metadata"]["ldflags"], "-o", expected_output, package]
            expected_env = {"CGO_ENABLED": "0", "GOOS": goos, "GOARCH": goarch,
                            "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off",
                            "GOENV": "off", "GOWORK": "off", "GOFLAGS": "", "trimpath": True}
            record = command_index.get((goos, goarch, name))
            if not record or record.get("argv") != expected_command or record.get("exit_code") != 0 or record.get("environment") != expected_env or record.get("package") != package:
                fail(f"build command/flags provenance mismatch for {goos}/{goarch} {name}")
    expected_version = f"{VERSION} ({frozen['head']}, {frozen['frozen_utc']})"
    host_linux_arm64 = sys.platform.startswith("linux") and (os.uname().machine.lower() in ("aarch64", "arm64"))
    availability = runner_availability()
    manifest_targets = []
    published_reports = False
    with tempfile.TemporaryDirectory(prefix="round30-verify-", dir=out / "tmp") as tmp:
        staging = pathlib.Path(tmp)
        staging_reports = staging / "reports"
        staging_reports.mkdir()
        scratch = staging / "runtime"
        scratch.mkdir()
        for target in TARGETS:
            row = by_target[target]
            artifacts_in = row.get("artifacts")
            if not isinstance(artifacts_in, dict) or set(artifacts_in) != {"hub", "agent"}:
                fail(f"missing hub or agent artifact for {target[0]}/{target[1]}")
            artifacts: dict[str, Any] = {}
            build_commands = [
                command for command in build_result.get("commands", [])
                if command.get("target") == {"goos": target[0], "goarch": target[1]}
            ]
            if len(build_commands) != 2 or {command.get("binary") for command in build_commands} != {"hub", "agent"}:
                fail(f"build command provenance incomplete for {target[0]}/{target[1]}")
            for name in ("hub", "agent"):
                stored = artifacts_in[name]
                expected_artifact_path = (pathlib.Path(".verification/round30/platforms/build") /
                                          expected_filename(name, target)).as_posix()
                if not isinstance(stored, dict) or stored.get("name") != name or stored.get("path") != expected_artifact_path:
                    fail(f"artifact filename/target mismatch for {target[0]}/{target[1]} {name}")
                fresh = binary_record(root, out, target, name, go_version, stored.get("go_version_m"))
                if fresh["sha256"] != stored.get("sha256") or fresh["header"] != stored.get("header"):
                    fail(f"binary hash/header differs from build result: {stored.get('name')}")
                artifacts[name] = fresh
            classification = runtime_class(target, host_linux_arm64)
            native = None
            if classification == "NATIVE_LINUX_AARCH64_SMOKE":
                native = native_smoke(root, out, {"artifacts": artifacts}, scratch,
                                      {"version": expected_version,
                                       "source_fingerprint_sha256": frozen["fingerprint_sha256"]})
            report = {
                "schema": "round30-platform-target-report-v1",
                "status": "passed",
                "target": {"goos": target[0], "goarch": target[1]},
                "verification_class": classification,
                "native_claim": classification == "NATIVE_LINUX_AARCH64_SMOKE",
                "source": _record_source(frozen),
                "binaries": artifacts,
                "build_provenance": {
                    "linker_metadata": frozen["linker_metadata"],
                    "commands": build_commands,
                    "environment": build_result.get("toolchain_policy"),
                },
                "static_checks": {
                    "both_binaries_present": True,
                    "binary_hashes_checked": True,
                    "format_machine_headers_checked": True,
                    "go_version_m_exact_recorded_and_rechecked": True,
                    "target_and_cgo_settings_checked": True,
                    "trimpath_setting_checked": True,
                },
                "toolchain": {"go_version": go_version,
                              "go_executable": build_result.get("go_executable"),
                              "build_environment": build_result.get("toolchain_policy"),
                              "runner_availability": availability},
                "native_smoke": native if native is not None else {
                    "status": "not-executed",
                    "reason": "foreign/static-only target or host is not Linux/aarch64",
                    "claims": [],
                },
            }
            report_path = staging_reports / f"{target[0]}-{target[1]}.json"
            write_json(report_path, report)
            report_sha = sha_file(report_path)
            manifest_targets.append({
                "goos": target[0], "goarch": target[1],
                "verification_class": classification,
                "report_path": f"reports/{report_path.name}",
                "report_sha256": report_sha,
                "binaries": {name: {"path": artifacts[name]["path"], "sha256": artifacts[name]["sha256"]}
                             for name in ("hub", "agent")},
            })
        verify_source_unchanged(root, frozen["source"])
        now = dt.datetime.now(dt.timezone.utc).replace(microsecond=0)
        matrix = {
            "schema": "round30-platform-matrix-v1",
            "status": "FINALIZED",
            "verified": True,
            "finalized": True,
            "finalized_utc": now.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "version": VERSION,
            "source": _record_source(frozen),
            "linker_metadata": frozen["linker_metadata"],
            "targets": manifest_targets,
            "environment": {
                "platform": sys.platform,
                "machine": os.uname().machine if hasattr(os, "uname") else "unknown",
                "runner_availability": availability,
                "foreign_runners_invoked": False,
                "downloads_performed": False,
                "live_hub_or_server_access": False,
                "production_endpoints_or_credentials": False,
            },
            "source_stability": "Exact ordered source inventory, full HEAD, dirty bit, Go source closure/module graph and status paths matched freeze before/after verification.",
            "report_hash_note": "Each report SHA256 is exported in this terminal matrix manifest, not embedded in its report.",
        }
        validate_final_manifest(out, matrix, report_root=staging)
        if reports_dir.exists() or reports_dir.is_symlink() or manifest_path.exists() or manifest_path.is_symlink():
            fail("refusing to overwrite a concurrently created terminal output")
        os.rename(staging_reports, reports_dir)
        published_reports = True
    try:
        _check_paths(root, out)
        validate_final_manifest(out, matrix)
        verify_source_unchanged(root, frozen["source"])
        _publish_json_no_replace(manifest_path, matrix)
    except BaseException:
        if published_reports and not manifest_path.exists() and reports_dir.exists() and not reports_dir.is_symlink():
            try:
                shutil.rmtree(reports_dir)
            except OSError:
                pass
        raise
    return matrix


def _check_paths(root: pathlib.Path, out: pathlib.Path) -> tuple[pathlib.Path, pathlib.Path]:
    root = root.resolve(strict=True)
    allowed = root / ".verification/round30/platforms"
    try:
        out_resolved = out.resolve(strict=False)
        if out_resolved != allowed.resolve(strict=False):
            fail("evidence output must be exactly .verification/round30/platforms")
    except OSError:
        fail("unsafe evidence output path")
    cursor = root
    relative = allowed.relative_to(root)
    for part in relative.parts:
        cursor = cursor / part
        try:
            info = cursor.lstat()
        except FileNotFoundError:
            continue
        except OSError:
            fail("unsafe evidence output component")
        if stat.S_ISLNK(info.st_mode) or not stat.S_ISDIR(info.st_mode):
            fail("evidence output ancestors must be repository-contained directories")
        if not cursor.resolve(strict=True).is_relative_to(root):
            fail("evidence output component escapes repository root")
    if allowed.exists():
        for current, dirs, files in os.walk(allowed, topdown=True, followlinks=False):
            base = pathlib.Path(current)
            for name in dirs + files:
                child = base / name
                info = child.lstat()
                if stat.S_ISLNK(info.st_mode):
                    fail("symlink evidence output child refused")
                if name in dirs and not stat.S_ISDIR(info.st_mode):
                    fail("unsafe evidence output directory")
                if name in files and not stat.S_ISREG(info.st_mode):
                    fail("unsafe evidence output file")
                if not child.resolve(strict=True).is_relative_to(root):
                    fail("evidence output child escapes repository root")
    return root, allowed


def _ensure_output_dir(root: pathlib.Path, out: pathlib.Path, path: pathlib.Path) -> None:
    root, out = _check_paths(root, out)
    try:
        relative_out = out.relative_to(root)
        relative_path = path.relative_to(root)
    except ValueError:
        fail("output directory is outside the evidence root")
    if relative_path.parts[:len(relative_out.parts)] != relative_out.parts:
        fail("output directory is outside the evidence root")
    cursor = root
    for part in relative_path.parts:
        cursor = cursor / part
        try:
            info = cursor.lstat()
        except FileNotFoundError:
            try:
                cursor.mkdir()
            except OSError:
                fail("cannot create safe evidence output directory")
            info = cursor.lstat()
        except OSError:
            fail("cannot inspect evidence output directory")
        if stat.S_ISLNK(info.st_mode) or not stat.S_ISDIR(info.st_mode):
            fail("unsafe evidence output directory refused")
        if not cursor.resolve(strict=True).is_relative_to(root):
            fail("evidence output directory escapes repository root")


def main(argv: Iterable[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("freeze", "build", "verify", "all"))
    parser.add_argument("--root", type=pathlib.Path, default=ROOT)
    parser.add_argument("--out", type=pathlib.Path, default=DEFAULT_OUT)
    parser.add_argument("--timestamp", help="required for freeze/all; exact UTC YYYY-MM-DDTHH:MM:SSZ")
    args = parser.parse_args(argv)
    try:
        root, out = _check_paths(args.root, args.out)
        if args.action in ("freeze", "all"):
            if args.timestamp is None:
                fail("--timestamp is required for freeze/all")
            freeze(root, out, args.timestamp)
            if args.action == "freeze":
                print(f"Frozen source fingerprint: {read_json(out / 'source-manifest.json')['fingerprint_sha256']}")
                return 0
            build(root, out)
            manifest = verify(root, out)
        elif args.action == "build":
            build(root, out)
            print("Built preliminary six-target hub/agent matrix.")
            return 0
        else:
            manifest = verify(root, out)
        print(f"Round30 matrix passed: {len(manifest['targets'])} targets; source={manifest['source']['fingerprint_sha256']}")
        return 0
    except VerificationError as exc:
        print(f"REFUSED: {exc}", file=sys.stderr)
        return 2
    except (KeyError, TypeError, ValueError, OSError, IndexError, AttributeError):
        print("REFUSED: malformed or inaccessible verification evidence", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
