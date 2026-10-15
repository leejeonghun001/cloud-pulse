"""Unit tests for scripts/check-docs.py, run against small synthetic
fixture directories (never this repository's own docs) so each check
is exercised in isolation with a known-good/known-bad input.
"""

from __future__ import annotations

import importlib.util
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from types import ModuleType
from unittest import mock

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
        self.allowlist = mock.patch.dict(CD.ENV_VAR_ALLOWLIST, {}, clear=True)
        self.allowlist.start()

    def tearDown(self) -> None:
        self.allowlist.stop()
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

    def test_custom_go_argument_case_flag_is_implementation(self) -> None:
        self.fixture.write("cmd/hub/notifyverify.go", '''package main
func parse(args []string) {
    for _, a := range args {
        switch {
        case a == "--status-webhook-wait":
            consume(a)
        }
    }
}
''')
        self.fixture.write("README.md", "Use `--status-webhook-wait` for status delivery.\n")
        result = CD.CheckResult()
        CD.check_env_and_flags(self.fixture.root, CD.find_markdown_files(self.fixture.root), result, enabled=True)
        self.assertTrue(result.ok, result.issues)

    def test_custom_go_or_help_cases_are_recognized(self) -> None:
        self.assertEqual(CD.collect_custom_go_case_flags('switch { case (a == "-h") || a == "--help": return }'), {"-h", "--help"})
        self.assertEqual(CD.collect_custom_go_case_flags('switch { case "--reverse" == argument: return }'), {"--reverse"})

    def test_custom_go_case_comments_are_not_implementation(self) -> None:
        self.fixture.write("cmd/hub/notifyverify.go", '''package main
// case a == "--comment-only":
/* case a == "--block-comment-only": */
''')
        self.fixture.write("README.md", "Do not claim `--comment-only` or `--block-comment-only`.\n")
        result = CD.CheckResult()
        CD.check_env_and_flags(self.fixture.root, CD.find_markdown_files(self.fixture.root), result, enabled=True)
        self.assertEqual(len(result.issues), 2)

    def test_custom_go_help_and_prose_strings_are_not_implementation(self) -> None:
        self.fixture.write("cmd/hub/notifyverify.go", '''package main
var help = `Help says --help-text-only. Fake source: case a == "--raw-string-only":`
var quoted = "case a == \\"--quoted-string-only\\":"
func helpOnly() { println("Use --prose-only") }
''')
        self.fixture.write("README.md", "`--help-text-only` `--raw-string-only` `--quoted-string-only` `--prose-only`\n")
        result = CD.CheckResult()
        CD.check_env_and_flags(self.fixture.root, CD.find_markdown_files(self.fixture.root), result, enabled=True)
        self.assertEqual(len(result.issues), 4)

    def test_custom_go_arbitrary_flag_strings_or_noncase_comparisons_are_not_implementation(self) -> None:
        text = 'var value = "--arbitrary"; if a == "--not-a-case" { return }; switch { case strings.Contains(a, "--not-equality"): return }'
        self.assertEqual(CD.collect_custom_go_case_flags(text), set())

    def test_flag_allowlist_entries_never_flagged(self) -> None:
        self.fixture.write("cmd/agent/update.go", 'x := fs.Bool("no-restart", false, "")\n')
        self.fixture.write("README.md", "See `--help` and `--dry-run` for details.\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        CD.check_env_and_flags(self.fixture.root, md_files, result, enabled=True)
        self.assertTrue(result.ok, result.issues)

    def test_env_allowlist_entry_never_flagged(self) -> None:
        """A CP_* name a SPEC documents before its code lands is not an
        error, provided it is explicitly allowlisted (see
        ENV_VAR_ALLOWLIST). Patches the module-level dict so this test
        does not depend on which SPECs happen to be in flight."""
        self.fixture.write("internal/config/agent.go", 'v := l("CP_KNOWN_EXISTING")\n')
        self.fixture.write("docs/specs/x.md", "**Status: DRAFT**\nReads `CP_SPEC_ONLY` from the env.\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        with mock.patch.dict(CD.ENV_VAR_ALLOWLIST, {"CP_SPEC_ONLY": "docs/specs/x.md"}):
            CD.check_env_and_flags(self.fixture.root, md_files, result, enabled=True)
        self.assertTrue(result.ok, result.issues)

    def test_draft_exception_does_not_apply_to_another_document(self) -> None:
        self.fixture.write("docs/specs/x.md", "**Status: DRAFT**\nReads `CP_SPEC_ONLY`.\n")
        other = self.fixture.write("README.md", "Set `CP_SPEC_ONLY`.\n")
        result = CD.CheckResult()
        with mock.patch.dict(CD.ENV_VAR_ALLOWLIST, {"CP_SPEC_ONLY": "docs/specs/x.md"}):
            CD.check_env_and_flags(self.fixture.root, CD.find_markdown_files(self.fixture.root), result, enabled=True)
        self.assertEqual(len(result.issues), 1)
        self.assertEqual(result.issues[0].path, other)

    def test_allowlist_declaration_is_not_implementation_evidence(self) -> None:
        self.fixture.write("scripts/check-docs.py", 'ENV_VAR_ALLOWLIST = {"CP_SPEC_ONLY": "docs/specs/x.md"}\n')
        self.fixture.write("docs/specs/x.md", "**Status: DRAFT**\nReads `CP_SPEC_ONLY`.\n")
        self.assertNotIn("CP_SPEC_ONLY", CD.collect_known_env_vars(self.fixture.root))
        result = CD.CheckResult()
        with mock.patch.dict(CD.ENV_VAR_ALLOWLIST, {"CP_SPEC_ONLY": "docs/specs/x.md"}):
            CD.check_env_and_flags(self.fixture.root, CD.find_markdown_files(self.fixture.root), result, enabled=True)
        self.assertTrue(result.ok, result.issues)

    def test_unit_test_fixture_strings_are_not_implementation(self) -> None:
        self.fixture.write("scripts/tests/test_fixture.py", 'value = "CP_TEST_ONLY"\n')
        self.fixture.write("internal/config/hub_test.go", 'key := "CP_TEST_ONLY"\n')
        self.fixture.write("README.md", "Set `CP_TEST_ONLY`.\n")
        result = CD.CheckResult()
        CD.check_env_and_flags(self.fixture.root, CD.find_markdown_files(self.fixture.root), result, enabled=True)
        self.assertFalse(result.ok)
        self.assertIn("CP_TEST_ONLY", result.issues[0].message)

    def test_implemented_variable_requires_removing_stale_exception(self) -> None:
        self.fixture.write("internal/config/hub.go", 'value := os.Getenv("CP_SPEC_ONLY")\n')
        self.fixture.write("docs/specs/x.md", "**Status: DRAFT**\nReads `CP_SPEC_ONLY`.\n")
        result = CD.CheckResult()
        with mock.patch.dict(CD.ENV_VAR_ALLOWLIST, {"CP_SPEC_ONLY": "docs/specs/x.md"}):
            CD.check_env_and_flags(self.fixture.root, CD.find_markdown_files(self.fixture.root), result, enabled=True)
        self.assertEqual(len(result.issues), 1)
        self.assertIn("stale env allowlist entry", result.issues[0].message)

    def test_stale_exception_for_deleted_draft_fails(self) -> None:
        result = CD.CheckResult()
        with mock.patch.dict(CD.ENV_VAR_ALLOWLIST, {"CP_SPEC_ONLY": "docs/specs/missing.md"}):
            CD.check_env_and_flags(self.fixture.root, [], result, enabled=True)
        self.assertFalse(result.ok)
        self.assertIn("mapped draft does not exist", result.issues[0].message)

    def test_stale_exception_for_nondraft_or_removed_variable_fails(self) -> None:
        for content in ["**Status: ACCEPTED**\nReads `CP_SPEC_ONLY`.\n", "**Status: DRAFT**\nNo planned variable.\n"]:
            with self.subTest(content=content):
                self.fixture.write("docs/specs/x.md", content)
                result = CD.CheckResult()
                with mock.patch.dict(CD.ENV_VAR_ALLOWLIST, {"CP_SPEC_ONLY": "docs/specs/x.md"}):
                    CD.check_env_and_flags(self.fixture.root, CD.find_markdown_files(self.fixture.root), result, enabled=True)
                self.assertTrue(any("stale env allowlist entry" in issue.message for issue in result.issues))

    def test_exception_cannot_map_to_product_docs(self) -> None:
        self.fixture.write("docs/configuration.md", "**Status: DRAFT**\nReads `CP_SPEC_ONLY`.\n")
        result = CD.CheckResult()
        with mock.patch.dict(CD.ENV_VAR_ALLOWLIST, {"CP_SPEC_ONLY": "docs/configuration.md"}):
            CD.check_env_and_flags(self.fixture.root, CD.find_markdown_files(self.fixture.root), result, enabled=True)
        self.assertTrue(any("must map to a docs/specs draft" in issue.message for issue in result.issues))

    def test_unknown_variable_and_flag_fail_even_without_any_source(self) -> None:
        self.fixture.write("README.md", "Set `CP_UNKNOWN` and pass `--unknown-flag`.\n")
        result = CD.CheckResult()
        CD.check_env_and_flags(self.fixture.root, CD.find_markdown_files(self.fixture.root), result, enabled=True)
        self.assertEqual(len(result.issues), 2)

    def test_unallowlisted_env_var_still_fails(self) -> None:
        """The allowlist must not swallow undocumented/unimplemented CP_*
        names generally — only the exact entries listed in it."""
        self.fixture.write("internal/config/agent.go", 'v := l("CP_KNOWN_EXISTING")\n')
        self.fixture.write("docs/specs/x.md", "Reads `CP_SPEC_ONLY` from the env.\n")
        result = CD.CheckResult()
        md_files = CD.find_markdown_files(self.fixture.root)
        with mock.patch.dict(CD.ENV_VAR_ALLOWLIST, {}, clear=True):
            CD.check_env_and_flags(self.fixture.root, md_files, result, enabled=True)
        self.assertFalse(result.ok)
        self.assertIn("CP_SPEC_ONLY", result.issues[0].message)


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

    def test_no_git_fixture_skips_verification_cache_and_generated_source(self) -> None:
        self.fixture.write("README.md", "# Root\n")
        self.fixture.write(".verification/cache/vendor/README.md", "# Cache\n")
        self.fixture.write(".verification/generated/config.go", 'key := "CP_GENERATED_ONLY"\n')
        self.fixture.write("internal/config/hub.go", 'key := "CP_REAL_SOURCE"\n')
        self.assertEqual(CD.find_markdown_files(self.fixture.root), [self.fixture.root / "README.md"])
        self.assertNotIn("CP_GENERATED_ONLY", CD.collect_known_env_vars(self.fixture.root))
        self.assertIn("CP_REAL_SOURCE", CD.collect_known_env_vars(self.fixture.root))

    def test_gitignored_docs_source_and_flags_are_not_evidence(self) -> None:
        subprocess.run(["git", "init", "-q", str(self.fixture.root)], check=True)
        self.fixture.write(".gitignore", "ignored/\n")
        self.fixture.write("README.md", "# Root\n")
        self.fixture.write("ignored/README.md", "# Ignore\n")
        self.fixture.write("ignored/source.go", 'key := "CP_IGNORED_ONLY"\n')
        self.fixture.write("cmd/ignored/fake.go", 'flag.Bool("ignored-flag", false, "")\n')
        self.fixture.write(".gitignore", "ignored/\ncmd/ignored/\n")
        self.assertEqual(CD.find_markdown_files(self.fixture.root), [self.fixture.root / "README.md"])
        self.assertNotIn("CP_IGNORED_ONLY", CD.collect_known_env_vars(self.fixture.root))
        self.assertNotIn("--ignored-flag", CD.collect_known_flags(self.fixture.root))

    def test_untracked_nonignored_source_docs_are_still_checked(self) -> None:
        subprocess.run(["git", "init", "-q", str(self.fixture.root)], check=True)
        doc = self.fixture.write("docs/new.md", "[missing](missing.md)\n")
        result = CD.CheckResult()
        CD.check_links_and_anchors(self.fixture.root, CD.find_markdown_files(self.fixture.root), result)
        self.assertEqual(result.issues[0].path, doc)

    def test_explicit_fixture_does_not_scan_parent_checkout(self) -> None:
        # TMPDIR is under the real ignored Round28 checkout. No .git exists in
        # this fixture, so discovery must stay confined to this explicit root.
        doc = self.fixture.write("README.md", "# Fixture only\n")
        self.assertEqual(CD.find_markdown_files(self.fixture.root), [doc])


class MainEntryPointTests(unittest.TestCase):
    def setUp(self) -> None:
        self.fixture = FixtureRepo()
        self.allowlist = mock.patch.dict(CD.ENV_VAR_ALLOWLIST, {}, clear=True)
        self.allowlist.start()

    def tearDown(self) -> None:
        self.allowlist.stop()
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

    def test_check_flags_option_is_optional_but_env_checks_are_always_on(self) -> None:
        self.fixture.write("cmd/hub/main.go", 'flag.Bool("real-flag", false, "")\n')
        self.fixture.write("README.md", "Pass `--unknown-flag`.\n")
        self.assertEqual(CD.main(["--repo-root", str(self.fixture.root)]), 0)
        self.assertEqual(CD.main(["--repo-root", str(self.fixture.root), "--check-flags"]), 1)
        self.fixture.write("README.md", "Set `CP_UNKNOWN`.\n")
        self.assertEqual(CD.main(["--repo-root", str(self.fixture.root)]), 1)


if __name__ == "__main__":
    unittest.main()
