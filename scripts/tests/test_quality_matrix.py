import contextlib
import importlib.util
import io
import sys
import unittest
from pathlib import Path
from unittest import mock


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location(
    "check_quality_matrix", ROOT / "scripts/check-quality-matrix.py"
)
checker = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(checker)


class QualityMatrixTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.valid = (ROOT / ".github/workflows/quality-matrix.yml").read_text(encoding="utf-8")

    def test_existing_workflow_matches_supported_profile(self):
        checker.validate_workflow(checker.parse_workflow(self.valid))

    def assert_refused(self, text, pattern=None):
        with self.assertRaises(checker.QualityWorkflowError) as raised:
            checker.validate_workflow(checker.parse_workflow(text))
        if pattern:
            self.assertRegex(str(raised.exception), pattern)

    def test_malformed_yaml_refused(self):
        with self.assertRaises(checker.QualityWorkflowError):
            checker.parse_workflow("name: [unclosed\n")

    def test_duplicate_mapping_key_refused(self):
        self.assert_refused(self.valid.replace("  contents: read\n", "  contents: read\n  contents: read\n", 1),
                            "duplicate")

    def test_duplicate_build_target_refused(self):
        changed = self.valid.replace(
            "          - goos: linux\n            goarch: arm64",
            "          - goos: linux\n            goarch: amd64",
            1,
        )
        self.assert_refused(changed, "duplicate targets")

    def test_write_permission_refused(self):
        self.assert_refused(self.valid.replace("  contents: read", "  contents: write", 1), "permissions")

    def test_extra_trigger_refused(self):
        self.assert_refused(self.valid.replace("  workflow_dispatch:\n", "  workflow_dispatch:\n  push:\n", 1),
                            "trigger")

    def test_missing_binary_refused(self):
        changed = self.valid.replace(
            'go build -trimpath -o "dist/cloud-pulse-agent${suffix}" ./cmd/agent',
            'go build -trimpath -o "dist/cloud-pulse-hub${suffix}" ./cmd/hub',
            1,
        )
        self.assert_refused(changed, "commands and outputs")

    def test_missing_native_command_refused(self):
        self.assert_refused(self.valid.replace("go vet ./...", "go test ./...", 1), "go vet step command")

    def test_unsupported_merge_tag_or_shape_refused(self):
        with self.assertRaises(checker.QualityWorkflowError):
            checker.parse_workflow(self.valid.replace("name: Quality Matrix", "name: !custom Quality Matrix", 1))
        with self.assertRaisesRegex(checker.QualityWorkflowError, "merge keys are unsupported"):
            checker.parse_workflow("jobs:\n  <<: {}\n")
        self.assert_refused(self.valid.replace("jobs:\n", "jobs:\n  release:\n    runs-on: ubuntu-latest\n", 1),
                            "jobs shape")

    def test_missing_yaml_parser_is_clear_nonzero_capability_error(self):
        stderr = io.StringIO()
        with mock.patch.object(checker, "yaml", None):
            with contextlib.redirect_stderr(stderr):
                result = checker.main(["--workflow", str(ROOT / ".github/workflows/quality-matrix.yml")])
        self.assertEqual(result, 2)
        self.assertIn("PyYAML is unavailable", stderr.getvalue())
        self.assertIn("no dependency was installed", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
