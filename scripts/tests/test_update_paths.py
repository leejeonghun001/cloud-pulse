"""Guard installer literals against the Go remote-update path contract."""

from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[2]


class UpdatePathContractTest(unittest.TestCase):
    def test_macos_and_windows_installer_paths_match_contract(self):
        shell = (ROOT / "scripts" / "install-agent.sh").read_text(encoding="utf-8")
        powershell = (ROOT / "scripts" / "install-agent.ps1").read_text(encoding="utf-8")

        self.assertIn("/Library/Application Support/cloud-pulse-agent", shell)
        self.assertIn("/Library/Application Support/cloud-pulse-agent-update", shell)
        self.assertIn("${UPDATE_STATE_DIR}/update-request.json", shell)
        self.assertIn("Join-Path $programDataRoot 'cloud-pulse-agent'", powershell)
        self.assertIn("Join-Path $programDataRoot 'cloud-pulse-agent-update'", powershell)


def _go_contract():
    """Parse internal/updatepaths' linux/darwin literals (single Go source of truth)."""
    import re
    src = "".join(p.read_text(encoding="utf-8") for p in (ROOT / "internal" / "updatepaths").glob("*.go")
                  if not p.name.endswith("_test.go"))
    out = {}
    for goos in ("linux", "darwin"):
        m = re.search(r'case\s+"%s":.*?RequestDir:\s+"([^"]+)".*?ResultDir:\s+"([^"]+)"' % goos, src, re.S)
        if m is None:
            raise AssertionError("could not parse %s paths from internal/updatepaths" % goos)
        out[goos] = {"request_dir": m.group(1), "result_dir": m.group(2)}
    return out


def _eval_shell_paths(function_name):
    """Run one setup_paths_* function from install-agent.sh (root="") and return its variables."""
    import re
    import subprocess
    shell = (ROOT / "scripts" / "install-agent.sh").read_text(encoding="utf-8")
    m = re.search(r"^%s\(\) \{\n.*?^\}\n" % re.escape(function_name), shell, re.S | re.M)
    if m is None:
        raise AssertionError("function %s not found in install-agent.sh" % function_name)
    script = (
        "set -eu\nOPT_PREFIX=/usr/local\nCP_LAUNCHD_LABEL=com.cloudpulse.agent\n"
        "CP_LAUNCHD_UPDATE_LABEL=com.cloudpulse.agent-update\n"
        + m.group(0)
        + '%s ""\nprintf "%%s\\n%%s\\n%%s\\n" "$UPDATE_STATE_DIR" "$UPDATE_REQUEST_FILE" "$UPDATE_RESULT_DIR"\n' % function_name
    )
    res = subprocess.run(["bash", "-c", script], capture_output=True, text=True, check=True, timeout=20)
    state_dir, request_file, result_dir = res.stdout.splitlines()
    return state_dir, request_file, result_dir


class PerPlatformInstallerPathsTest(unittest.TestCase):
    """Exact (not substring) comparison of each platform block with the Go contract."""

    def test_darwin_block_matches_go_contract_exactly(self):
        want = _go_contract()["darwin"]
        state_dir, request_file, result_dir = _eval_shell_paths("setup_paths_darwin")
        self.assertEqual(state_dir, want["request_dir"])
        self.assertEqual(request_file, want["request_dir"] + "/update-request.json")
        self.assertEqual(result_dir, want["result_dir"])


if __name__ == "__main__":
    unittest.main()
