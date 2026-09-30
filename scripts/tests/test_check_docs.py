"""Unit tests for scripts/check-docs.py, run against small synthetic
fixture directories (never this repository's own docs) so each check
is exercised in isolation with a known-good/known-bad input.
"""

from __future__ import annotations

import importlib.util
import shutil
import sys
import tempfile
import unittest
from pathlib import Path
from types import ModuleType

ROOT = Path(__file__).resolve().parents[2]


def load_script(filename: str, module_name: str) -> ModuleType:
    spec = importlib.util.spec_from_file_location(module_name, ROOT / "scripts" / filename)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"could not load {filename}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    spec.loader.exec_module(module)
    return module


CD = load_script("check-docs.py", "cloud_pulse_check_docs_test")


class GithubSlugTests(unittest.TestCase):
    def test_simple_heading(self) -> None:
        self.assertEqual(CD.github_slug("Quick start"), "quick-start")

    def test_strips_inline_code_and_emphasis(self) -> None:
        self.assertEqual(CD.github_slug("The `CP_LISTEN` variable"), "the-cp_listen-variable")

    def test_punctuation_dropped_without_collapsing_whitespace(self) -> None:
        # Matches this repo's own README anchor:
        # "Inventory: Docker services + listening ports" ->
        # "#inventory-docker-services--listening-ports" (double hyphen).
        self.assertEqual(
            CD.github_slug("Inventory: Docker services + listening ports"),
            "inventory-docker-services--listening-ports",
        )

    def test_leading_trailing_whitespace_stripped(self) -> None:
        self.assertEqual(CD.github_slug("  Security model  "), "security-model")


class FixtureRepo:
    """Builds a tiny synthetic repository tree under a temp directory
    for check-docs.py to scan, with helpers to add markdown/source
    files without touching this repository's own docs."""

    def __init__(self) -> None:
        self.root = Path(tempfile.mkdtemp(prefix="cloud-pulse-check-docs-fixture-"))

    def write(self, relpath: str, content: str) -> Path:
        path = self.root / relpath
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
        return path

    def cleanup(self) -> None:
        shutil.rmtree(self.root, ignore_errors=True)


class CheckLinksAndAnchorsTests(unittest.TestCase):
    def setUp(self) -> None:
        self.fixture = FixtureRepo()

    def tearDown(self) -> None:
        self.fixture.cleanup()

    def test_valid_relative_link_passes(self) -> None:
        self.fixture.write("docs/other.md", "# Other\n\nContent.\n")
        self.fixture.write("README.md", "See [other](docs/other.md).\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        CD.check_links_and_anchors(self.fixture.root, md_files, result)
        self.assertTrue(result.ok, result.issues)

    def test_broken_relative_link_fails(self) -> None:
        self.fixture.write("README.md", "See [missing](docs/missing.md).\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        CD.check_links_and_anchors(self.fixture.root, md_files, result)
        self.assertFalse(result.ok)
        self.assertIn("broken relative link", result.issues[0].message)

    def test_valid_same_file_anchor_passes(self) -> None:
        self.fixture.write("README.md", "# Quick start\n\nSee [above](#quick-start).\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        CD.check_links_and_anchors(self.fixture.root, md_files, result)
        self.assertTrue(result.ok, result.issues)

    def test_invalid_same_file_anchor_fails(self) -> None:
        self.fixture.write("README.md", "# Quick start\n\nSee [above](#does-not-exist).\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        CD.check_links_and_anchors(self.fixture.root, md_files, result)
        self.assertFalse(result.ok)
        self.assertIn("anchor not found in this file", result.issues[0].message)

    def test_cross_file_anchor_validated(self) -> None:
        self.fixture.write("docs/other.md", "# Section one\n\nContent.\n")
        self.fixture.write("README.md", "See [it](docs/other.md#section-one) and [bad](docs/other.md#nope).\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        CD.check_links_and_anchors(self.fixture.root, md_files, result)
        self.assertEqual(len(result.issues), 1)
        self.assertIn("#nope", result.issues[0].message)

    def test_link_escaping_repo_root_flagged(self) -> None:
        self.fixture.write("README.md", "See [escape](../../../etc/passwd).\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        CD.check_links_and_anchors(self.fixture.root, md_files, result)
        self.assertFalse(result.ok)
        self.assertIn("escapes repository root", result.issues[0].message)

    def test_http_links_ignored(self) -> None:
        self.fixture.write("README.md", "See [ext](https://example.com/nope.md).\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        CD.check_links_and_anchors(self.fixture.root, md_files, result)
        self.assertTrue(result.ok, result.issues)

    def test_links_inside_code_fences_ignored(self) -> None:
        self.fixture.write("README.md", "```text\nSee [missing](docs/missing.md).\n```\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        CD.check_links_and_anchors(self.fixture.root, md_files, result)
        self.assertTrue(result.ok, result.issues)

    def test_duplicate_headings_get_numeric_suffix(self) -> None:
        self.fixture.write(
            "README.md",
            "# Overview\n\nfirst\n\n# Overview\n\nsecond\n\nSee [first](#overview) and [second](#overview-1).\n",
        )
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        CD.check_links_and_anchors(self.fixture.root, md_files, result)
        self.assertTrue(result.ok, result.issues)


class CheckFenceLanguagesTests(unittest.TestCase):
    def setUp(self) -> None:
        self.fixture = FixtureRepo()

    def tearDown(self) -> None:
        self.fixture.cleanup()

    def test_tagged_fence_passes(self) -> None:
        self.fixture.write("README.md", "```bash\necho hi\n```\n")
        result = CD.CheckResult()
        CD.check_fence_languages([self.fixture.root / "README.md"], result)
        self.assertTrue(result.ok, result.issues)

    def test_untagged_fence_fails(self) -> None:
        self.fixture.write("README.md", "```\nsome text\n```\n")
        result = CD.CheckResult()
        CD.check_fence_languages([self.fixture.root / "README.md"], result)
        self.assertFalse(result.ok)
        self.assertIn("no language tag", result.issues[0].message)


class CheckReadmeLengthTests(unittest.TestCase):
    def setUp(self) -> None:
        self.fixture = FixtureRepo()

    def tearDown(self) -> None:
        self.fixture.cleanup()

    def test_under_cap_passes(self) -> None:
        self.fixture.write("README.md", "\n".join(f"line {i}" for i in range(10)) + "\n")
        result = CD.CheckResult()
        CD.check_readme_length(self.fixture.root, 350, result)
        self.assertTrue(result.ok, result.issues)

    def test_over_cap_fails(self) -> None:
        self.fixture.write("README.md", "\n".join(f"line {i}" for i in range(400)) + "\n")
        result = CD.CheckResult()
        CD.check_readme_length(self.fixture.root, 350, result)
        self.assertFalse(result.ok)
        self.assertIn("exceeding", result.issues[0].message)

    def test_custom_cap_respected(self) -> None:
        self.fixture.write("README.md", "\n".join(f"line {i}" for i in range(20)) + "\n")
        result = CD.CheckResult()
        CD.check_readme_length(self.fixture.root, 10, result)
        self.assertFalse(result.ok)


class CheckEnvAndFlagsTests(unittest.TestCase):
    def setUp(self) -> None:
        self.fixture = FixtureRepo()

    def tearDown(self) -> None:
        self.fixture.cleanup()

    def test_documented_env_var_found_in_source_passes(self) -> None:
        self.fixture.write("internal/config/hub.go", 'const key = "CP_LISTEN"\n')
        self.fixture.write("README.md", "Set `CP_LISTEN` to configure the listen address.\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        CD.check_env_and_flags(self.fixture.root, md_files, result, enabled=True)
        self.assertTrue(result.ok, result.issues)

    def test_documented_env_var_missing_from_source_fails(self) -> None:
        self.fixture.write("internal/config/hub.go", 'const key = "CP_LISTEN"\n')
        self.fixture.write("README.md", "Set `CP_TOTALLY_MADE_UP` to do something.\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        CD.check_env_and_flags(self.fixture.root, md_files, result, enabled=True)
        self.assertFalse(result.ok)
        self.assertIn("CP_TOTALLY_MADE_UP", result.issues[0].message)

    def test_env_var_from_shell_script_recognized(self) -> None:
        self.fixture.write("scripts/install-hub.sh", 'echo "${CP_INSTALL_ROOT:-}"\n')
        self.fixture.write("README.md", "Set `CP_INSTALL_ROOT` for sandbox installs.\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        CD.check_env_and_flags(self.fixture.root, md_files, result, enabled=True)
        self.assertTrue(result.ok, result.issues)

    def test_hook_file_without_extension_recognized(self) -> None:
        self.fixture.write(".githooks/pre-commit", 'if [ "$CP_CHECK_ALL" = "1" ]; then :; fi\n')
        self.fixture.write("README.md", "Set `CP_CHECK_ALL=1` to check everything.\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        CD.check_env_and_flags(self.fixture.root, md_files, result, enabled=True)
        self.assertTrue(result.ok, result.issues)

    def test_disabled_flag_skips_check(self) -> None:
        self.fixture.write("README.md", "Set `CP_TOTALLY_MADE_UP` to do something.\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        CD.check_env_and_flags(self.fixture.root, md_files, result, enabled=False)
        self.assertTrue(result.ok, result.issues)

    def test_documented_flag_found_via_flagset_passes(self) -> None:
        self.fixture.write(
            "cmd/agent/update.go",
            'noRestartFlag := fs.Bool("no-restart", false, "skip restart")\n',
        )
        self.fixture.write("README.md", "Pass `--no-restart` to skip the restart step.\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        CD.check_env_and_flags(self.fixture.root, md_files, result, enabled=True)
        self.assertTrue(result.ok, result.issues)

    def test_undefined_flag_in_backticks_fails(self) -> None:
        self.fixture.write(
            "cmd/agent/update.go",
            'noRestartFlag := fs.Bool("no-restart", false, "skip restart")\n',
        )
        self.fixture.write("README.md", "Pass `--totally-made-up` to do something.\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        CD.check_env_and_flags(self.fixture.root, md_files, result, enabled=True)
        self.assertFalse(result.ok)
        self.assertIn("--totally-made-up", result.issues[0].message)

    def test_flag_allowlist_entries_never_flagged(self) -> None:
        self.fixture.write("cmd/agent/update.go", 'x := fs.Bool("no-restart", false, "")\n')
        self.fixture.write("README.md", "See `--help` and `--dry-run` for details.\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        CD.check_env_and_flags(self.fixture.root, md_files, result, enabled=True)
        self.assertTrue(result.ok, result.issues)


class FindMarkdownFilesTests(unittest.TestCase):
    def setUp(self) -> None:
        self.fixture = FixtureRepo()

    def tearDown(self) -> None:
        self.fixture.cleanup()

    def test_skips_vendor_and_node_modules(self) -> None:
        self.fixture.write("README.md", "# Root\n")
        self.fixture.write("web/assets/vendor/uplot/README.md", "# Vendor\n")
        self.fixture.write("node_modules/pkg/README.md", "# Node\n")
        self.fixture.write("vendor/pkg/README.md", "# Vendor2\n")
        found = CD.find_markdown_files(self.fixture.root)
        names = {str(p.relative_to(self.fixture.root)) for p in found}
        self.assertEqual(names, {"README.md"})


class MainEntryPointTests(unittest.TestCase):
    def setUp(self) -> None:
        self.fixture = FixtureRepo()

    def tearDown(self) -> None:
        self.fixture.cleanup()

    def test_clean_repo_exits_zero(self) -> None:
        self.fixture.write("README.md", "# Root\n\n```text\nfine\n```\n")
        code = CD.main(["--repo-root", str(self.fixture.root), "--skip-length-cap"])
        self.assertEqual(code, 0)

    def test_broken_link_exits_nonzero(self) -> None:
        self.fixture.write("README.md", "[bad](missing.md)\n")
        code = CD.main(["--repo-root", str(self.fixture.root), "--skip-length-cap"])
        self.assertEqual(code, 1)

    def test_no_markdown_files_exits_nonzero(self) -> None:
        (self.fixture.root / "placeholder.txt").write_text("x", encoding="utf-8")
        code = CD.main(["--repo-root", str(self.fixture.root), "--skip-length-cap"])
        self.assertEqual(code, 1)

    def test_length_cap_enforced_by_default(self) -> None:
        self.fixture.write("README.md", "\n".join(f"line {i}" for i in range(400)) + "\n")
        code = CD.main(["--repo-root", str(self.fixture.root), "--max-readme-lines", "350"])
        self.assertEqual(code, 1)

    def test_skip_length_cap_flag_bypasses_cap(self) -> None:
        self.fixture.write("README.md", "\n".join(f"line {i}" for i in range(400)) + "\n")
        code = CD.main(["--repo-root", str(self.fixture.root), "--skip-length-cap"])
        self.assertEqual(code, 0)


if __name__ == "__main__":
    unittest.main()
