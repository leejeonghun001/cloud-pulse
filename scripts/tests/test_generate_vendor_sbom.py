import hashlib
import importlib.util
import json
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


REPOSITORY = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location(
    "generate_vendor_sbom", REPOSITORY / "scripts/generate-vendor-sbom.py"
)
sbom = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(sbom)


class VendorSbomTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for relative in sbom.ALL_FILES:
            target = self.root / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(REPOSITORY / relative, target)

    def test_repeat_generation_is_byte_identical(self):
        first = sbom.render(sbom._assemble(self.root))
        second = sbom.render(sbom._assemble(self.root))
        self.assertEqual(first, second)
        self.assertNotIn(b"timestamp", first.lower())
        self.assertNotIn(str(self.root).encode(), first)

    def test_hashes_components_and_dependency_relationships(self):
        bom = sbom._assemble(self.root)
        sbom.validate_profile(bom, self.root)
        self.assertEqual(bom["specVersion"], "1.6")
        self.assertEqual(len(bom["components"]), 13)
        by_ref = {component["bom-ref"]: component for component in bom["components"]}
        for relative in sbom.ALL_FILES:
            component = by_ref[f"file:{relative}"]
            actual = hashlib.sha256((self.root / relative).read_bytes()).hexdigest()
            self.assertEqual(component["hashes"], [{"alg": "SHA-256", "content": actual}])
            properties = {item["name"]: item["value"] for item in component["properties"]}
            self.assertEqual(properties["source:path"], relative)
        dependencies = {item["ref"]: set(item["dependsOn"]) for item in bom["dependencies"]}
        self.assertEqual(dependencies[sbom.APP_REF], {
            "library:npm:uplot@1.6.32", "library:lucide-icon-path-data",
            "library:npm:@fontsource-variable/inter@5.2.8",
        })
        self.assertEqual(dependencies["library:npm:@fontsource-variable/inter@5.2.8"],
                         {f"file:{item}" for item in sbom.INTER_FILES})
        self.assertEqual(dependencies["library:npm:uplot@1.6.32"],
                         {f"file:{item}" for item in sbom.UPLOT_FILES})
        self.assertEqual(dependencies["library:lucide-icon-path-data"],
                         {f"file:{item}" for item in sbom.LUCIDE_FILES})

    def test_lucide_version_and_revision_remain_unknown(self):
        bom = sbom._assemble(self.root)
        lucide = next(item for item in bom["components"]
                      if item["bom-ref"] == "library:lucide-icon-path-data")
        self.assertNotIn("version", lucide)
        self.assertNotIn("purl", lucide)
        properties = {item["name"]: item["value"] for item in lucide["properties"]}
        self.assertIn("unknown", properties["vendor:version-status"])
        self.assertIn("unknown", properties["vendor:upstream-commit-status"])
        uplot = next(item for item in bom["components"] if item["bom-ref"] == "library:npm:uplot@1.6.32")
        claims = {item["name"]: item["value"] for item in uplot["properties"]}
        self.assertIn("not independently verified by this inventory", claims["vendor:integrity-claim:sha512"])

    def _run_cli(self, *args):
        return subprocess.run(
            [sys.executable, str(REPOSITORY / "scripts/generate-vendor-sbom.py"),
             "--root", str(self.root), *args],
            text=True, capture_output=True, check=False,
        )

    def test_modified_input_refuses_stale_artifact(self):
        artifact = self.root / "vendor.cdx.json"
        first = self._run_cli("--output", str(artifact))
        self.assertEqual(first.returncode, 0, first.stderr)
        source = self.root / "web/assets/vendor/uplot/uPlot.esm.js"
        source.write_bytes(source.read_bytes() + b"\n// changed\n")
        check = self._run_cli("--output", str(artifact), "--check")
        self.assertNotEqual(check.returncode, 0)
        self.assertIn("stale/invalid", check.stderr)

    def test_malformed_artifact_is_refused(self):
        artifact = self.root / "malformed.json"
        artifact.write_text("{not json", encoding="utf-8")
        result = self._run_cli("--output", str(artifact), "--check")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("malformed JSON", result.stderr)

    def test_missing_required_input_is_refused(self):
        (self.root / sbom.UPLOT_FILES[0]).unlink()
        with self.assertRaises(sbom.InventoryError):
            sbom._assemble(self.root)

    def test_symlink_escaping_selected_root_is_refused(self):
        external = Path(self.temp.name).parent / (Path(self.temp.name).name + "-outside")
        external.write_text("outside", encoding="utf-8")
        self.addCleanup(lambda: external.unlink(missing_ok=True))
        source = self.root / "web/assets/vendor/uplot/uPlot.esm.js"
        source.unlink()
        source.symlink_to(external)
        with self.assertRaisesRegex(sbom.InventoryError, "symlink escapes"):
            sbom._assemble(self.root)

    def test_license_evidence_change_is_refused(self):
        readme = self.root / "web/assets/vendor/lucide/README.md"
        readme.write_text(readme.read_text(encoding="utf-8").replace("License**: ISC", "License**: MIT"),
                          encoding="utf-8")
        with self.assertRaisesRegex(sbom.InventoryError, "license"):
            sbom._assemble(self.root)


if __name__ == "__main__":
    unittest.main()
