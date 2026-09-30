"""Regression checks for CI-only requirements that cannot run locally."""

from __future__ import annotations

import json
import subprocess
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
CI_WORKFLOW = ROOT / ".github" / "workflows" / "ci.yml"


class AgentE2EWorkflowTests(unittest.TestCase):
    def assert_macos_uid_range_check(self, workflow: str) -> None:
        install_marker = '- name: "[darwin] install via install-agent.sh (real launchd, sudo)"'
        report_marker = '- name: "[darwin] wait for agent report to arrive at the hub"'
        install_start = workflow.index(install_marker)
        report_start = workflow.index(report_marker, install_start)
        install_step = workflow[install_start:report_start]
        self.assertIn("sudo dscl . -read /Users/_cloudpulse UniqueID", install_step)
        self.assertIn("''|*[!0-9]*)", install_step)
        self.assertIn('[ "$uid" -lt 200 ] || [ "$uid" -gt 400 ]', install_step)
        self.assertIn('sudo dscl . -read /Groups/_cloudpulse PrimaryGroupID', install_step)
        self.assertIn('sudo dscl . -read /Users/_cloudpulse PrimaryGroupID', install_step)
        self.assertIn("stat -f '%Sg' /usr/local/etc/cloud-pulse/agent.env", install_step)

    def test_agent_e2e_api_json_selectors_match_handler_envelopes(self) -> None:
        workflow = CI_WORKFLOW.read_text(encoding="utf-8")
        hosts = {"hosts": [{"host": {"id": "host-ci"}, "status": "up"}], "generated_at": 1}
        batch = {"batch_id": "batch-ci", "target": "v0.7.0-ci.2", "jobs": []}
        jobs = {"jobs": [{"id": 1, "host_id": "host-ci", "state": "succeeded", "reason": "", "error": ""}]}
        selectors = (
            ('print(len(json.load(sys.stdin)["hosts"]))', hosts, "1"),
            ('d=json.load(sys.stdin); print(d["hosts"][0]["host"]["id"])', hosts, "host-ci"),
            ('print(json.load(sys.stdin)["batch_id"])', batch, "batch-ci"),
            ('d=json.load(sys.stdin); jobs=d["jobs"]; print(json.dumps({k: jobs[0].get(k, "") for k in ("state", "reason", "error")}) if jobs else "{}")', jobs, '{"state": "succeeded", "reason": "", "error": ""}'),
        )
        for selector, response, expected in selectors:
            self.assertIn(selector, workflow)
            completed = subprocess.run(
                ["python3", "-c", "import json,sys; " + selector],
                input=json.dumps(response), text=True, capture_output=True, check=True,
            )
            self.assertEqual(completed.stdout.strip(), expected)
        self.assertIn("if ($hosts.hosts.Count -ge 1) { exit 0 }", workflow)

    def test_macos_install_asserts_dedicated_user_uid_range(self) -> None:
        self.assert_macos_uid_range_check(CI_WORKFLOW.read_text(encoding="utf-8"))

    def test_macos_uid_check_regression_is_detected(self) -> None:
        workflow = CI_WORKFLOW.read_text(encoding="utf-8")
        mutated = workflow.replace("sudo dscl . -read /Users/_cloudpulse UniqueID", "dscl")
        with self.assertRaises(AssertionError):
            self.assert_macos_uid_range_check(mutated)

    def test_agent_e2e_uses_ordered_capability_eligible_prereleases(self) -> None:
        workflow = CI_WORKFLOW.read_text(encoding="utf-8")
        initial = "v0.7.0-ci.1"
        target = "v0.7.0-ci.2"

        def at_least_including_prerelease(candidate: str, gate: str) -> bool:
            candidate_core = tuple(int(part) for part in candidate[1:].split("-", 1)[0].split("."))
            gate_core = tuple(int(part) for part in gate[1:].split("-", 1)[0].split("."))
            return candidate_core >= gate_core

        self.assertTrue(at_least_including_prerelease(initial, "v0.7.0"))
        self.assertTrue(at_least_including_prerelease(target, "v0.7.0"))
        self.assertFalse(at_least_including_prerelease("v0.6.9", "v0.7.0"))
        self.assertIn(initial, workflow)
        self.assertIn(target, workflow)
        windows_start = workflow.index('- name: "[windows] install via install-agent.ps1')
        windows_end = workflow.index('- name: "[windows] wait for agent report', windows_start)
        windows_install = workflow[windows_start:windows_end]
        self.assertIn("-Version " + initial, windows_install)
        self.assertNotIn("-Version " + target, windows_install)
        self.assertIn(target, workflow)


if __name__ == "__main__":
    unittest.main()
