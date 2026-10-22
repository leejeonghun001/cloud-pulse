"""Focused offline tests for the Round 30 platform verifier."""

import hashlib
import importlib.util
import json
import os
import pathlib
import struct
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock


SCRIPT = pathlib.Path(__file__).resolve().parents[1] / "verify-round30-platforms.py"
SPEC = importlib.util.spec_from_file_location("verify_round30_platforms", SCRIPT)
verifier = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(verifier)


def git(root, *args):
    return subprocess.run(["git", *args], cwd=root, check=True, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE, env={"PATH": os.environ.get("PATH", "/usr/bin:/bin"),
                                                      "HOME": os.environ.get("HOME", "/tmp")})


def init_repo(root):
    root.mkdir(parents=True, exist_ok=True)
    git(root, "init", "-q")
    git(root, "config", "user.name", "Verifier Test")
    git(root, "config", "user.email", "verifier-test@example.invalid")
    (root / "go.mod").write_text("module example.invalid/fixture\n\ngo 1.22\n", encoding="utf-8")
    (root / "main.go").write_text("package main\n", encoding="utf-8")
    (root / "cmd/hub").mkdir(parents=True)
    (root / "cmd/hub/main.go").write_text("package main\n", encoding="utf-8")
    agent = root / "cmd/agent"
    (agent / "assets").mkdir(parents=True)
    (agent / "main.go").write_text(
        'package main\nimport _ "embed"\n//go:embed assets/generated.txt\nvar generated string\n',
        encoding="utf-8")
    (agent / "generated_windows.go").write_text(
        "//go:build windows\n\npackage main\nconst generatedWindows = true\n", encoding="utf-8")
    (agent / "assets/generated.txt").write_text("embedded generated fixture\n", encoding="utf-8")
    (root / ".gitignore").write_text(
        "cmd/agent/generated_windows.go\ncmd/agent/assets/generated.txt\n", encoding="utf-8")
    git(root, "add", "go.mod", "main.go", "cmd", ".gitignore")
    git(root, "commit", "-qm", "fixture")


def sha(data):
    return hashlib.sha256(data).hexdigest()


def fake_binary(target):
    goos, goarch = target
    if goos == "linux":
        machine = 62 if goarch == "amd64" else 183
        data = bytearray(64)
        data[:6] = b"\x7fELF\x02\x01"
        struct.pack_into("<H", data, 18, machine)
        return bytes(data)
    if goos == "windows":
        data = bytearray(96)
        data[:2] = b"MZ"
        struct.pack_into("<I", data, 0x3C, 64)
        data[64:68] = b"PE\0\0"
        struct.pack_into("<H", data, 68, 0x8664 if goarch == "amd64" else 0xAA64)
        struct.pack_into("<H", data, 88, 0x20B)
        return bytes(data)
    data = bytearray(32)
    data[:4] = b"\xcf\xfa\xed\xfe"
    struct.pack_into("<I", data, 4, 0x01000007 if goarch == "amd64" else 0x0100000C)
    return bytes(data)


class InventoryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="round30-inventory-")
        self.root = pathlib.Path(self.temp.name) / "repo"
        init_repo(self.root)

    def tearDown(self):
        self.temp.cleanup()

    def test_dirty_changed_and_untracked_inputs_are_fingerprinted(self):
        baseline = verifier.inventory(self.root)
        (self.root / "main.go").write_text("package main // dirty\n", encoding="utf-8")
        (self.root / "new input.txt").write_text("new\n", encoding="utf-8")
        changed = verifier.inventory(self.root)
        self.assertTrue(changed["dirty"])
        self.assertNotEqual(baseline["tree_sha256"], changed["tree_sha256"])
        self.assertIn("new input.txt", [row["path"] for row in changed["input_inventory"]])
        status_paths = {row["path"] for row in changed["status_paths"]}
        self.assertEqual(status_paths, {"main.go", "new input.txt", "cmd/agent/assets/generated.txt",
                                        "cmd/agent/generated_windows.go"})
        with self.assertRaises(verifier.VerificationError):
            verifier.verify_source_unchanged(self.root, baseline)

    def test_ordering_exact_modes_and_canonical_fingerprint(self):
        (self.root / "z-input").write_text("z", encoding="utf-8")
        (self.root / "é-input").write_text("accent", encoding="utf-8")
        (self.root / "exec-input").write_text("#!/bin/sh\n", encoding="utf-8")
        (self.root / "private-mode").write_text("mode", encoding="utf-8")
        (self.root / "exec-input").chmod(0o755)
        (self.root / "private-mode").chmod(0o600)
        result = verifier.inventory(self.root)
        names = [row["path"] for row in result["input_inventory"]]
        self.assertEqual(names, sorted(names, key=lambda item: item.encode("utf-8")))
        records = {row["path"]: row for row in result["input_inventory"]}
        self.assertEqual(records["exec-input"]["mode"], "0755")
        self.assertEqual(records["private-mode"]["mode"], "0600")
        canonical = json.dumps(result["input_inventory"], ensure_ascii=False,
                               separators=(",", ":")).encode("utf-8")
        self.assertEqual(result["tree_sha256"], sha(canonical))

    def test_deletion_symlinks_and_unsafe_names_refuse_inventory(self):
        (self.root / "main.go").unlink()
        with self.assertRaises(verifier.VerificationError):
            verifier.inventory(self.root)
        (self.root / "main.go").write_text("package main\n", encoding="utf-8")
        (self.root / "link.go").symlink_to(self.root / "main.go")
        with self.assertRaises(verifier.VerificationError):
            verifier.inventory(self.root)

    def test_only_explicit_exclusions_are_omitted(self):
        (self.root / ".verification/round30/platforms").mkdir(parents=True)
        (self.root / ".verification/round30/platforms/ignored-evidence.json").write_text("{}")
        (self.root / ".cache").mkdir()
        (self.root / ".cache/tool-cache").write_text("cache")
        (self.root / "cloud-pulse-agent-linux-arm64").write_bytes(b"generated")
        (self.root / "web/assets").mkdir(parents=True)
        (self.root / "web/assets/embedded.js").write_text("embedded", encoding="utf-8")
        result = verifier.inventory(self.root)
        names = {row["path"] for row in result["input_inventory"]}
        self.assertIn("go.mod", names)
        self.assertIn("web/assets/embedded.js", names)
        self.assertIn(".cache/tool-cache", names)
        self.assertNotIn(".verification/round30/platforms/ignored-evidence.json", names)
        self.assertIn("cloud-pulse-agent-linux-arm64", names)
        self.assertGreaterEqual(result["excluded_path_count"], 1)
        status_names = {row["path"] for row in result["status_paths"]}
        self.assertNotIn(".verification/round30/platforms/ignored-evidence.json", status_names)
        self.assertIn(".cache/tool-cache", status_names)
        self.assertIn("cloud-pulse-agent-linux-arm64", status_names)

    def test_ignored_root_node_modules_tool_symlink_is_excluded(self):
        with (self.root / ".gitignore").open("a", encoding="utf-8") as stream:
            stream.write("node_modules/\n")
        tool_dir = self.root / "node_modules/.bin"
        tool_dir.mkdir(parents=True)
        # Intentionally never inspect or follow this disposable tooling link.
        (tool_dir / "detect-libc").symlink_to("../detect-libc/bin/detect-libc.js")
        result = verifier.inventory(self.root)
        names = {row["path"] for row in result["input_inventory"]}
        status_names = {row["path"] for row in result["status_paths"]}
        self.assertNotIn("node_modules/.bin/detect-libc", names)
        self.assertNotIn("node_modules/.bin/detect-libc", status_names)
        policy = next(row for row in result["exclusions"] if row["path"] == "node_modules/**")
        self.assertIn("Root-only", policy["reason"])
        self.assertEqual(result["exclusions_sha256"],
                         sha(json.dumps(result["exclusions"], ensure_ascii=False,
                                        separators=(",", ":")).encode()))

    def test_nested_tracked_node_modules_remains_in_inventory(self):
        with (self.root / ".gitignore").open("a", encoding="utf-8") as stream:
            stream.write("node_modules/\n")
        nested = self.root / "web/node_modules/cache-like.txt"
        nested.parent.mkdir(parents=True)
        nested.write_text("tracked nested tooling-like input\n", encoding="utf-8")
        git(self.root, "add", "-f", "web/node_modules/cache-like.txt")
        result = verifier.inventory(self.root)
        self.assertIn("web/node_modules/cache-like.txt",
                      {row["path"] for row in result["input_inventory"]})

    def test_tracked_root_node_modules_content_refuses_exclusion(self):
        with (self.root / ".gitignore").open("a", encoding="utf-8") as stream:
            stream.write("node_modules/\n")
        tracked = self.root / "node_modules/tracked.txt"
        tracked.parent.mkdir()
        tracked.write_text("must not be omitted\n", encoding="utf-8")
        git(self.root, "add", "-f", "node_modules/tracked.txt")
        with self.assertRaisesRegex(verifier.VerificationError, "tracked input under the excluded root"):
            verifier.inventory(self.root)

    def test_ignored_embedded_and_generated_go_inputs_are_closed_for_all_targets(self):
        result = verifier.inventory(self.root)
        names = {row["path"] for row in result["input_inventory"]}
        self.assertIn("cmd/agent/assets/generated.txt", names)
        self.assertIn("cmd/agent/generated_windows.go", names)
        self.assertEqual(len(result["go_input_closures"]), 12)
        closure_map = {(row["target"]["goos"], row["target"]["goarch"], row["package"]): set(row["repo_inputs"])
                       for row in result["go_input_closures"]}
        for target in verifier.TARGETS:
            self.assertIn("cmd/hub/main.go", closure_map[(*target, "hub")])
            self.assertIn("cmd/agent/main.go", closure_map[(*target, "agent")])
            self.assertIn("cmd/agent/assets/generated.txt", closure_map[(*target, "agent")])
        self.assertIn("cmd/agent/generated_windows.go", closure_map[("windows", "amd64", "agent")])
        self.assertNotIn("cmd/agent/generated_windows.go", closure_map[("linux", "amd64", "agent")])

    def test_consumed_but_excluded_input_fails_closed(self):
        excluded = self.root / ".verification/source"
        excluded.mkdir(parents=True)
        (excluded / "consumed.go").write_text("package ignored\n", encoding="utf-8")
        payload = json.dumps({"Dir": str(self.root / ".verification/source"),
                              "GoFiles": ["consumed.go"]}).encode()
        with mock.patch.object(verifier, "run_bounded", return_value=(0, payload, b"")):
            with self.assertRaisesRegex(verifier.VerificationError, "excluded repository input"):
                verifier._go_input_closures(self.root, set(), [])

    def test_root_node_modules_go_embed_source_and_replacement_consumption_refuse(self):
        excluded_module = self.root / "node_modules/localdep"
        excluded_module.mkdir(parents=True)
        with self.subTest(consumed="embed"):
            payload = json.dumps({"Dir": str(self.root),
                                  "EmbedFiles": ["node_modules/embedded.txt"]}).encode()
            with mock.patch.object(verifier, "run_bounded", return_value=(0, payload, b"")):
                with self.assertRaisesRegex(verifier.VerificationError, "excluded repository input"):
                    verifier._go_input_closures(self.root, set(), [])
        with self.subTest(consumed="Go source"):
            payload = json.dumps({"Dir": str(excluded_module), "GoFiles": ["dep.go"]}).encode()
            with mock.patch.object(verifier, "run_bounded", return_value=(0, payload, b"")):
                with self.assertRaisesRegex(verifier.VerificationError, "package is under an excluded"):
                    verifier._go_input_closures(self.root, set(), [])
        replacement = json.dumps({"Path": "example.invalid/localdep", "Version": "v0.0.0",
                                  "Replace": {"Path": "./node_modules/localdep",
                                              "Dir": str(excluded_module)}}).encode()
        with self.subTest(consumed="local replacement"), \
             mock.patch.object(verifier, "run_bounded", return_value=(0, replacement, b"")):
            with self.assertRaisesRegex(verifier.VerificationError, "local Go replacement under an excluded path"):
                verifier._go_module_inventory(self.root)

    def test_consumed_input_missing_from_inventory_fails_closed(self):
        payload = json.dumps({"Dir": str(self.root / "cmd/agent"),
                              "GoFiles": ["unrecorded.go"]}).encode()
        with mock.patch.object(verifier, "run_bounded", return_value=(0, payload, b"")):
            with self.assertRaisesRegex(verifier.VerificationError, "missing from inventory"):
                verifier._go_input_closures(self.root, set(), [])

    def test_external_local_replacement_is_refused_and_workspace_is_disabled(self):
        outside = pathlib.Path(self.temp.name) / "outside"
        outside.mkdir()
        (outside / "go.mod").write_text("module example.invalid/outside\n\ngo 1.22\n", encoding="utf-8")
        with (self.root / "go.mod").open("a", encoding="utf-8") as stream:
            stream.write("\nrequire example.invalid/outside v0.0.0\nreplace example.invalid/outside => ../outside\n")
        with self.assertRaisesRegex(verifier.VerificationError, "local Go replacement outside"):
            verifier.inventory(self.root)
        self.assertEqual(verifier.build_env(go=True)["GOWORK"], "off")
        self.assertEqual(verifier.build_env(go=True)["GOENV"], "off")
        self.assertEqual(verifier.build_env(go=True)["GOFLAGS"], "")

    def test_in_repository_local_replacement_is_inventoried_and_closed(self):
        local = self.root / "internal/localdep"
        local.mkdir(parents=True)
        (local / "go.mod").write_text("module example.invalid/localdep\n\ngo 1.22\n", encoding="utf-8")
        (local / "dep.go").write_text("package localdep\nconst Value = 1\n", encoding="utf-8")
        with (self.root / "go.mod").open("a", encoding="utf-8") as stream:
            stream.write("\nrequire example.invalid/localdep v0.0.0\nreplace example.invalid/localdep => ./internal/localdep\n")
        (self.root / "cmd/hub/main.go").write_text(
            "package main\nimport _ \"example.invalid/localdep\"\n", encoding="utf-8")
        result = verifier.inventory(self.root)
        names = {row["path"] for row in result["input_inventory"]}
        closure = next(row for row in result["go_input_closures"]
                       if row["target"] == {"goos": "linux", "goarch": "amd64"} and row["package"] == "hub")
        self.assertIn("internal/localdep/go.mod", names)
        self.assertIn("internal/localdep/dep.go", names)
        self.assertIn("internal/localdep/dep.go", closure["repo_inputs"])
        self.assertTrue(any(module.get("replace", {}).get("repository_dir") == "internal/localdep"
                            for module in result["go_module_inventory"]))

    def test_parent_workspace_file_is_ignored(self):
        workspace = self.root.parent / "go.work"
        workspace.write_text("go 1.22\n\nuse ./repo\n", encoding="utf-8")
        code, stdout, stderr = verifier.run_bounded(
            ["go", "env", "GOWORK"], cwd=self.root, env=verifier.build_env(go=True), timeout=10)
        self.assertEqual(verifier.require_command_ok(code, stdout, stderr, "go env GOWORK").strip(), b"off")


class BinaryTests(unittest.TestCase):
    def test_all_required_binary_headers_parse(self):
        for target in verifier.TARGETS:
            with self.subTest(target=target):
                result = verifier.platform_header(fake_binary(target), target)
                self.assertIn(result["format"], ("ELF64", "PE32+", "Mach-O64"))

    def test_header_and_target_mismatches_refuse(self):
        with self.assertRaises(verifier.VerificationError):
            verifier.platform_header(fake_binary(("linux", "amd64")), ("linux", "arm64"))
        with self.assertRaises(verifier.VerificationError):
            verifier.platform_header(b"not an executable", ("windows", "amd64"))
        elf_big = bytearray(fake_binary(("linux", "amd64")))
        elf_big[5] = 2
        with self.assertRaisesRegex(verifier.VerificationError, "ELF64 header"):
            verifier.platform_header(bytes(elf_big), ("linux", "amd64"))
        macho_big = bytearray(32)
        macho_big[:4] = b"\xfe\xed\xfa\xcf"
        struct.pack_into(">I", macho_big, 4, 0x01000007)
        with self.assertRaises(verifier.VerificationError):
            verifier.platform_header(bytes(macho_big), ("darwin", "amd64"))

    def test_binary_record_accepts_real_go_version_m_trimpath_setting(self):
        with tempfile.TemporaryDirectory(prefix="round30-binary-record-") as tmp:
            root = pathlib.Path(tmp)
            out = root / ".verification/round30/platforms"
            build = out / "build"
            build.mkdir(parents=True)
            target = ("linux", "amd64")
            rel = pathlib.Path(".verification/round30/platforms/build") / verifier.expected_filename("hub", target)
            (root / rel).write_bytes(fake_binary(target))
            output = (f"{rel.as_posix()}: go1.23.4\n"
                      "\tbuild -buildmode=exe\n"
                      "\tbuild -compiler=gc\n"
                      "\tbuild CGO_ENABLED=0\n"
                      "\tbuild GOARCH=amd64\n"
                      "\tbuild GOOS=linux\n"
                      "\tbuild -trimpath=true\n").encode()
            with mock.patch.object(verifier, "run_bounded", return_value=(0, output, b"")):
                record = verifier.binary_record(root, out, target, "hub", "go version go1.23.4 linux/amd64")
            self.assertEqual(record["go_version_m_settings"]["-trimpath"], "true")
            bad = output.replace(b"-trimpath=true", b"-trimpath=false")
            with mock.patch.object(verifier, "run_bounded", return_value=(0, bad, b"")):
                with self.assertRaisesRegex(verifier.VerificationError, "trimpath"):
                    verifier.binary_record(root, out, target, "hub", "go version go1.23.4 linux/amd64")

    def test_only_linux_aarch64_can_make_native_claim(self):
        self.assertEqual(verifier.runtime_class(("linux", "arm64"), True),
                         "NATIVE_LINUX_AARCH64_SMOKE")
        for target in verifier.TARGETS:
            if target != ("linux", "arm64"):
                self.assertEqual(verifier.runtime_class(target, True), "STATIC_ONLY/NOT_EXECUTED")
        self.assertEqual(verifier.runtime_class(("linux", "arm64"), False),
                         "STATIC_ONLY/NOT_EXECUTED")


class FinalManifestTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="round30-reports-")
        self.root = pathlib.Path(self.temp.name) / "repo"
        init_repo(self.root)
        self.out = self.root / ".verification/round30/platforms"
        build_dir = self.out / "build"
        report_dir = self.out / "reports"
        build_dir.mkdir(parents=True)
        report_dir.mkdir()
        inventory = verifier.inventory(self.root)
        self.frozen = {"head": inventory["head"], "dirty": inventory["dirty"],
                       "status_paths": inventory["status_paths"],
                       "fingerprint_sha256": inventory["fingerprint_sha256"],
                       "version": verifier.VERSION, "frozen_utc": "2026-10-07T12:00:00Z",
                       "source": inventory}
        source = verifier._record_source(self.frozen)
        native_host = os.name == "posix" and sys.platform.startswith("linux") and (
            os.uname().machine.lower() in ("aarch64", "arm64"))
        target_items = []
        for target in verifier.TARGETS:
            binaries = {}
            for name in ("hub", "agent"):
                rel = pathlib.Path(".verification/round30/platforms/build") / verifier.expected_filename(name, target)
                data = fake_binary(target)
                (self.root / rel).write_bytes(data)
                binaries[name] = {"path": rel.as_posix(), "sha256": sha(data),
                                  "bytes": len(data),
                                  "header": verifier.platform_header(data, target)}
            classification = verifier.runtime_class(target, native_host)
            report = {"schema": "round30-platform-target-report-v1", "status": "passed",
                      "target": {"goos": target[0], "goarch": target[1]},
                      "verification_class": classification,
                      "source": source, "binaries": binaries,
                      "native_claim": classification == "NATIVE_LINUX_AARCH64_SMOKE",
                      "native_smoke": self._native_fixture(target, binaries, source) if classification.startswith("NATIVE") else {
                          "status": "not-executed",
                          "reason": "foreign/static-only target or host is not Linux/aarch64", "claims": []}}
            report_name = f"{target[0]}-{target[1]}.json"
            report_path = report_dir / report_name
            verifier.write_json(report_path, report)
            target_items.append({"goos": target[0], "goarch": target[1],
                                  "verification_class": classification,
                                  "report_path": f"reports/{report_name}",
                                  "report_sha256": verifier.sha_file(report_path),
                                  "binaries": {name: {"path": binaries[name]["path"],
                                                      "sha256": binaries[name]["sha256"]}
                                               for name in ("hub", "agent")}})
        self.manifest = {"schema": "round30-platform-matrix-v1", "status": "FINALIZED",
                         "verified": True, "finalized": True,
                         "finalized_utc": "2026-10-07T12:00:00Z", "version": verifier.VERSION,
                         "source": source,
                         "linker_metadata": {"version": verifier.VERSION,
                                             "commit": self.frozen["head"],
                                             "date": self.frozen["frozen_utc"],
                                             "ldflags": verifier.fixed_ldflags(self.frozen["head"], self.frozen["frozen_utc"])},
                         "environment": {"platform": sys.platform,
                                          "machine": os.uname().machine if hasattr(os, "uname") else "unknown"},
                         "targets": target_items}

    def _native_fixture(self, target, binaries, source):
        version = f"{verifier.VERSION} ({self.frozen['head']}, {self.frozen['frozen_utc']})"
        expected_paths = {name: binaries[name]["path"] for name in ("hub", "agent")}
        executions = {}
        for command, name in (("agent_version", "agent"), ("agent_once", "agent"), ("hub_version", "hub")):
            argv = [str(self.root / expected_paths[name]), "-once" if command == "agent_once" else "-version"]
            output = b'{"ok":true}\n' if command == "agent_once" else (version + "\n").encode()
            execution = {"argv": argv, "exit_code": 0,
                         "binary_sha256_before": binaries[name]["sha256"],
                         "binary_sha256_after": binaries[name]["sha256"],
                         "binary_hash_unchanged": True, "timeout_seconds": 30,
                         "output_limit_bytes": verifier.MAX_CHILD_OUTPUT,
                         "stdout_bytes": len(output), "stdout_sha256": sha(output),
                         "stderr_bytes": 0, "stderr_sha256": sha(b"")}
            if command == "agent_once":
                execution["sample_observation"] = {"valid_json_object": True, "bytes": len(output),
                    "top_level_keys": ["ok"], "values_recorded": False,
                    "hub_configuration_used": False, "send_performed": False}
            else:
                execution["observed_version"] = version
            executions[command] = execution
        machine = os.uname().machine if hasattr(os, "uname") else "unknown"
        return {"status": "passed", "native_platform": "linux/aarch64",
                "native_host": {"platform": sys.platform, "machine": machine},
                "target": {"goos": target[0], "goarch": target[1]},
                "source_fingerprint_sha256": source["fingerprint_sha256"],
                "exact_artifact_paths": expected_paths,
                "synthetic_environment": {"allowlisted_variables": ["PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "TZ"],
                    "inherited_CP_variables": False, "inherited_proxy_variables": False,
                    "application_credentials_or_endpoints": False},
                "executions": executions}

    def tearDown(self):
        self.temp.cleanup()

    def test_reports_bind_both_binary_hashes_and_pass(self):
        result = verifier.validate_final_manifest(self.out, self.manifest)
        self.assertEqual(len(result["targets"]), 6)

    def test_report_hash_tamper_refuses(self):
        report = self.out / self.manifest["targets"][0]["report_path"]
        report.write_bytes(report.read_bytes() + b"tamper")
        with self.assertRaisesRegex(verifier.VerificationError, "report hash mismatch"):
            verifier.validate_final_manifest(self.out, self.manifest)

    def test_binary_hash_tamper_refuses(self):
        row = self.manifest["targets"][0]
        report = json.loads((self.out / row["report_path"]).read_text())
        path = self.root / report["binaries"]["hub"]["path"]
        path.write_bytes(path.read_bytes() + b"tamper")
        with self.assertRaisesRegex(verifier.VerificationError, "binary hash mismatch"):
            verifier.validate_final_manifest(self.out, self.manifest)

    def test_target_set_mismatch_and_false_static_native_claim_refuse(self):
        bad_target = json.loads(json.dumps(self.manifest))
        bad_target["targets"][0]["goarch"] = "mips"
        with self.assertRaisesRegex(verifier.VerificationError, "canonical|unexpected target"):
            verifier.validate_final_manifest(self.out, bad_target)
        row = next(row for row in self.manifest["targets"] if row["goos"] == "windows")
        report_path = self.out / row["report_path"]
        report = json.loads(report_path.read_text())
        report["native_claim"] = True
        verifier.write_json(report_path, report)
        row["report_sha256"] = verifier.sha_file(report_path)
        with self.assertRaisesRegex(verifier.VerificationError, "native claim/class"):
            # Native evidence on a foreign/static target is not a claim this
            # contract accepts, regardless of the report's own hash.
            verifier.validate_final_manifest(self.out, self.manifest)

    def test_terminal_finalization_path_class_and_native_records_are_required(self):
        for field, invalid in (("status", "passed"), ("verified", False), ("finalized", False)):
            bad = json.loads(json.dumps(self.manifest))
            bad[field] = invalid
            with self.subTest(field=field), self.assertRaises(verifier.VerificationError):
                verifier.validate_final_manifest(self.out, bad)
        bad_path = json.loads(json.dumps(self.manifest))
        bad_path["targets"][0]["report_path"] = "reports/other.json"
        with self.assertRaisesRegex(verifier.VerificationError, "report path"):
            verifier.validate_final_manifest(self.out, bad_path)
        bad_class = json.loads(json.dumps(self.manifest))
        bad_class["targets"][0]["verification_class"] = "NATIVE_LINUX_AARCH64_SMOKE"
        with self.assertRaisesRegex(verifier.VerificationError, "verification class"):
            verifier.validate_final_manifest(self.out, bad_class)

    def test_status_only_native_record_refuses(self):
        row = next(row for row in self.manifest["targets"]
                   if row["verification_class"] == "STATIC_ONLY/NOT_EXECUTED")
        report_path = self.out / row["report_path"]
        report = json.loads(report_path.read_text())
        report["native_smoke"] = {"status": "passed"}
        verifier.write_json(report_path, report)
        row["report_sha256"] = verifier.sha_file(report_path)
        with self.assertRaisesRegex(verifier.VerificationError, "native execution evidence"):
            verifier.validate_final_manifest(self.out, self.manifest)

    def test_complete_native_execution_records_are_host_and_hash_bound(self):
        row = next(row for row in self.manifest["targets"]
                   if (row["goos"], row["goarch"]) == ("linux", "arm64"))
        report_path = self.out / row["report_path"]
        report = json.loads(report_path.read_text())
        binaries = report["binaries"]
        with mock.patch.object(verifier.sys, "platform", "linux"), \
             mock.patch.object(verifier.os, "uname", return_value=type("Uname", (), {"machine": "aarch64"})()):
            row["verification_class"] = "NATIVE_LINUX_AARCH64_SMOKE"
            report["verification_class"] = "NATIVE_LINUX_AARCH64_SMOKE"
            report["native_claim"] = True
            report["native_smoke"] = self._native_fixture(("linux", "arm64"), binaries, report["source"])
            self.manifest["environment"]["platform"] = "linux"
            self.manifest["environment"]["machine"] = "aarch64"
            verifier.write_json(report_path, report)
            row["report_sha256"] = verifier.sha_file(report_path)
            verifier.validate_final_manifest(self.out, self.manifest)

            report["native_smoke"]["executions"]["agent_version"]["binary_sha256_after"] = "0" * 64
            verifier.write_json(report_path, report)
            row["report_sha256"] = verifier.sha_file(report_path)
            with self.assertRaisesRegex(verifier.VerificationError, "native command result/hash/argv"):
                verifier.validate_final_manifest(self.out, self.manifest)

    def test_tampered_source_inventory_refuses(self):
        bad = json.loads(json.dumps(self.manifest))
        bad["source"]["input_inventory"][0]["sha256"] = "c" * 64
        with self.assertRaisesRegex(verifier.VerificationError, "inventory hash"):
            verifier.validate_final_manifest(self.out, bad)

    def test_late_candidate_validation_failure_does_not_publish_terminal_manifest(self):
        with tempfile.TemporaryDirectory(prefix="round30-late-failure-") as tmp:
            root = pathlib.Path(tmp) / "repo"
            init_repo(root)
            out = root / ".verification/round30/platforms"
            (out / "build").mkdir(parents=True)
            inventory = verifier.inventory(root)
            timestamp = "2026-10-07T12:00:00Z"
            frozen = {"schema": "round30-platform-source-v1", "status": "preliminary-source-freeze",
                      "version": verifier.VERSION, "frozen_utc": timestamp, "head": inventory["head"],
                      "dirty": inventory["dirty"], "status_paths": inventory["status_paths"],
                "fingerprint_sha256": inventory["fingerprint_sha256"], "source": inventory,
                      "linker_metadata": {"version": verifier.VERSION, "commit": inventory["head"],
                          "date": timestamp, "ldflags": verifier.fixed_ldflags(inventory["head"], timestamp)}}
            verifier.write_json(out / "source-manifest.json", frozen)
            go_version = "go version go1.23.4 linux/amd64"
            targets = []
            commands = []
            for target in verifier.TARGETS:
                artifacts = {}
                for name, package in verifier.PACKAGES.items():
                    rel = (pathlib.Path(".verification/round30/platforms/build") /
                           verifier.expected_filename(name, target)).as_posix()
                    data = fake_binary(target)
                    (root / rel).write_bytes(data)
                    artifacts[name] = {"name": name, "path": rel, "bytes": len(data), "sha256": sha(data),
                                       "header": verifier.platform_header(data, target),
                                       "go_version_m": "fixture go version -m"}
                    command = ["go", "build", "-mod=readonly", "-trimpath", "-ldflags", frozen["linker_metadata"]["ldflags"],
                               "-o", rel, package]
                    commands.append({"target": {"goos": target[0], "goarch": target[1]},
                        "binary": name, "package": package, "argv": command, "exit_code": 0,
                        "environment": {"CGO_ENABLED": "0", "GOOS": target[0], "GOARCH": target[1],
                            "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off",
                            "GOENV": "off", "GOWORK": "off", "GOFLAGS": "", "trimpath": True}})
                targets.append({"goos": target[0], "goarch": target[1], "artifacts": artifacts})
            build_result = {"schema": "round30-platform-build-v1", "source_head": inventory["head"],
                "source_fingerprint_sha256": inventory["fingerprint_sha256"],
                "source_inventory_sha256": inventory["tree_sha256"],
                "go_input_closures_sha256": inventory["go_input_closures_sha256"],
                "go_module_inventory_sha256": inventory["go_module_inventory_sha256"],
                "version": verifier.VERSION, "frozen_utc": timestamp, "module_mode": "readonly",
                "linker_metadata": frozen["linker_metadata"], "toolchain_policy": {
                    "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOENV": "off",
                    "GOWORK": "off", "GOFLAGS": "", "CGO_ENABLED": "0"},
                "targets": targets, "go_version": go_version, "commands": commands}
            verifier.write_json(out / "build-results.json", build_result)
            with mock.patch.object(verifier, "verify_source_unchanged", return_value=inventory), \
                 mock.patch.object(verifier, "binary_record", side_effect=lambda _root, _out, target, name, _go, expected=None:
                    next(row["artifacts"][name] for row in build_result["targets"]
                         if (row["goos"], row["goarch"]) == target)), \
                 mock.patch.object(verifier, "native_smoke", return_value={"status": "passed"}), \
                 mock.patch.object(verifier, "validate_final_manifest",
                                   side_effect=verifier.VerificationError("late validation refusal")):
                with self.assertRaisesRegex(verifier.VerificationError, "late validation"):
                    verifier.verify(root, out)
            self.assertFalse((out / "matrix-manifest.json").exists())
            self.assertFalse((out / "reports").exists())


class RefusalTests(unittest.TestCase):
    def test_timestamp_and_frozen_inventory_refusal(self):
        self.assertTrue(verifier.timestamp_is_valid("2026-10-07T12:00:00Z"))
        self.assertFalse(verifier.timestamp_is_valid("2026-10-07T12:00:00+00:00"))
        self.assertFalse(verifier.timestamp_is_valid("2026-02-30T12:00:00Z"))
        with tempfile.TemporaryDirectory(prefix="round30-freeze-") as tmp:
            root = pathlib.Path(tmp) / "repo"
            init_repo(root)
            frozen = verifier.inventory(root)
            path = root / "main.go"
            path.write_text("package main // changed\n", encoding="utf-8")
            with self.assertRaisesRegex(verifier.VerificationError, "source"):
                verifier.verify_source_unchanged(root, frozen)


class OutputPathTests(unittest.TestCase):
    def test_existing_terminal_manifest_is_not_overwritten(self):
        with tempfile.TemporaryDirectory(prefix="round30-terminal-output-") as tmp:
            root = pathlib.Path(tmp) / "repo"
            init_repo(root)
            out = root / ".verification/round30/platforms"
            out.mkdir(parents=True)
            manifest = out / "matrix-manifest.json"
            manifest.write_text('{"status":"FINALIZED"}\n', encoding="utf-8")
            original = manifest.read_bytes()
            with mock.patch.object(verifier, "run_bounded", side_effect=AssertionError("command launched")):
                with self.assertRaisesRegex(verifier.VerificationError, "existing terminal matrix manifest"):
                    verifier.verify(root, out)
            self.assertEqual(manifest.read_bytes(), original)

    def test_symlink_output_children_refuse_before_build_launch(self):
        with tempfile.TemporaryDirectory(prefix="round30-output-path-") as tmp:
            root = pathlib.Path(tmp) / "repo"
            init_repo(root)
            out = root / ".verification/round30/platforms"
            out.mkdir(parents=True)
            outside = pathlib.Path(tmp) / "outside"
            outside.mkdir()
            (out / "build").symlink_to(outside, target_is_directory=True)
            with mock.patch.object(verifier, "run_bounded", side_effect=AssertionError("command launched")):
                with self.assertRaisesRegex(verifier.VerificationError, "symlink evidence output child"):
                    verifier.build(root, out)

    def test_target_symlink_file_refuses_before_build_launch(self):
        with tempfile.TemporaryDirectory(prefix="round30-output-file-") as tmp:
            root = pathlib.Path(tmp) / "repo"
            init_repo(root)
            out = root / ".verification/round30/platforms"
            build = out / "build"
            build.mkdir(parents=True)
            outside = pathlib.Path(tmp) / "outside-binary"
            outside.write_bytes(b"not a binary")
            name = verifier.expected_filename("hub", ("linux", "amd64"))
            (build / name).symlink_to(outside)
            with mock.patch.object(verifier, "run_bounded", side_effect=AssertionError("command launched")):
                with self.assertRaisesRegex(verifier.VerificationError, "symlink evidence output child"):
                    verifier.build(root, out)


class ChildProcessTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="round30-child-")
        self.root = pathlib.Path(self.temp.name)
        self.env = verifier.build_env(go=True)

    def tearDown(self):
        self.temp.cleanup()

    @staticmethod
    def assert_pid_stopped(test, pid):
        for _ in range(80):
            try:
                os.kill(pid, 0)
            except ProcessLookupError:
                return
            stat_path = pathlib.Path(f"/proc/{pid}/stat")
            try:
                if stat_path.exists() and stat_path.read_text().split()[2] == "Z":
                    return
            except (FileNotFoundError, ProcessLookupError):
                # The process exited between the exists() check and the
                # read: /proc reports that as ESRCH on some kernels.
                return
            time.sleep(0.025)
        test.fail(f"owned child {pid} was not terminated")

    def test_early_parent_exit_kills_pipe_inheriting_descendant(self):
        child = "import time; time.sleep(30)"
        parent = ("import subprocess,sys; p=subprocess.Popen([sys.executable,'-c'," + repr(child) + "]); "
                  "print(p.pid, flush=True)")
        code, stdout, _ = verifier.run_bounded([sys.executable, "-c", parent], cwd=self.root,
                                                env=self.env, timeout=5)
        self.assertEqual(code, 0)
        self.assert_pid_stopped(self, int(stdout.strip()))

    def test_timeout_kills_process_group(self):
        pidfile = self.root / "child.pid"
        child = "import time; time.sleep(30)"
        parent = ("import subprocess,sys,time,pathlib; p=subprocess.Popen([sys.executable,'-c'," + repr(child) + "]); "
                  "pathlib.Path(sys.argv[1]).write_text(str(p.pid)); time.sleep(30)")
        with self.assertRaisesRegex(verifier.VerificationError, "time limit"):
            verifier.run_bounded([sys.executable, "-c", parent, str(pidfile)], cwd=self.root,
                                 env=self.env, timeout=0.4)
        self.assert_pid_stopped(self, int(pidfile.read_text()))

    def test_oversized_output_is_stopped_while_reading(self):
        script = "import os; os.write(1, b'x' * " + str(verifier.MAX_CHILD_OUTPUT + 4096) + ")"
        with self.assertRaisesRegex(verifier.VerificationError, "output exceeded"):
            verifier.run_bounded([sys.executable, "-c", script], cwd=self.root,
                                 env=self.env, timeout=5)

    def test_cancellation_terminates_owned_process_group(self):
        original_popen = subprocess.Popen
        spawned = {}

        def capture_popen(*args, **kwargs):
            process = original_popen(*args, **kwargs)
            spawned["pid"] = process.pid
            return process

        with mock.patch.object(verifier.subprocess, "Popen", side_effect=capture_popen), \
             mock.patch.object(verifier.selectors.DefaultSelector, "select", side_effect=KeyboardInterrupt):
            with self.assertRaises(KeyboardInterrupt):
                verifier.run_bounded([sys.executable, "-c", "import time; time.sleep(30)"],
                                     cwd=self.root, env=self.env, timeout=10)
        with self.assertRaises(ProcessLookupError):
            os.killpg(spawned["pid"], 0)

    def test_failed_hard_limit_installation_fails_closed(self):
        with mock.patch.object(verifier, "_limited_child", side_effect=OSError("rlimit unavailable")):
            with self.assertRaisesRegex(verifier.VerificationError, "could not be started with required limits"):
                verifier.run_bounded([sys.executable, "-c", "pass"], cwd=self.root,
                                     env=self.env, timeout=5, limit_child=True)


if __name__ == "__main__":
    unittest.main()
