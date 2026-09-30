"""Regression checks for CI-only requirements that cannot run locally."""

from __future__ import annotations

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

    def test_macos_install_asserts_dedicated_user_uid_range(self) -> None:
        self.assert_macos_uid_range_check(CI_WORKFLOW.read_text(encoding="utf-8"))

    def test_macos_uid_check_regression_is_detected(self) -> None:
        workflow = CI_WORKFLOW.read_text(encoding="utf-8")
        mutated = workflow.replace("sudo dscl . -read /Users/_cloudpulse UniqueID", "dscl")
        with self.assertRaises(AssertionError):
            self.assert_macos_uid_range_check(mutated)


if __name__ == "__main__":
    unittest.main()
