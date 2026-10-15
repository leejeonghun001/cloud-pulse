#!/usr/bin/env python3
"""Generate/check a deterministic, source-bound CycloneDX 1.6 vendor inventory.

This deliberately implements a narrow project-profile validator, not the full
CycloneDX schema. It only describes the browser vendor files listed below.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path, PurePosixPath
from typing import Any


DEFAULT_ROOT = Path(__file__).resolve().parents[1]
DEFAULT_OUTPUT = Path("docs/sbom/vendor.cdx.json")
APP_REF = "application:cloud-pulse"

UPLOT_FILES = (
    "web/assets/vendor/uplot/LICENSE",
    "web/assets/vendor/uplot/README.md",
    "web/assets/vendor/uplot/uPlot.esm.js",
    "web/assets/vendor/uplot/uPlot.min.css",
)
LUCIDE_FILES = (
    "web/assets/vendor/lucide/LICENSE",
    "web/assets/vendor/lucide/README.md",
    "web/assets/js/ui/icons.js",
)
INTER_FILES = (
    "web/assets/vendor/inter/LICENSE",
    "web/assets/vendor/inter/README.md",
    "web/assets/vendor/inter/inter-latin-wght-normal.woff2",
)
ALL_FILES = UPLOT_FILES + LUCIDE_FILES + INTER_FILES


class InventoryError(ValueError):
    """An input or artifact does not satisfy the local inventory profile."""


def _safe_input(root: Path, relative: str) -> Path:
    rel = PurePosixPath(relative)
    if rel.is_absolute() or ".." in rel.parts or not rel.parts:
        raise InventoryError(f"unsafe inventory path: {relative}")
    root_resolved = root.resolve(strict=True)
    path = root_resolved.joinpath(*rel.parts)
    try:
        resolved = path.resolve(strict=True)
    except OSError as exc:
        raise InventoryError(f"required input missing or unreadable: {relative}: {exc}") from exc
    if not resolved.is_relative_to(root_resolved):
        raise InventoryError(f"input symlink escapes selected root: {relative}")
    if not resolved.is_file():
        raise InventoryError(f"required input is not a file: {relative}")
    return resolved


def _source_bytes(root: Path, relative: str) -> bytes:
    try:
        return _safe_input(root, relative).read_bytes()
    except OSError as exc:
        raise InventoryError(f"required input missing or unreadable: {relative}: {exc}") from exc


def _source_text(root: Path, relative: str) -> str:
    try:
        return _source_bytes(root, relative).decode("utf-8")
    except UnicodeDecodeError as exc:
        raise InventoryError(f"evidence file is not UTF-8: {relative}") from exc


def _require(pattern: str, text: str, description: str, *, flags: int = 0) -> re.Match[str]:
    match = re.search(pattern, text, flags)
    if match is None:
        raise InventoryError(f"source evidence does not support {description}")
    return match


def _properties(items: dict[str, str]) -> list[dict[str, str]]:
    return [{"name": name, "value": items[name]} for name in sorted(items)]


def _verify_evidence(root: Path) -> tuple[dict[str, str], dict[str, str]]:
    uplot_readme_path = "web/assets/vendor/uplot/README.md"
    uplot_license_path = "web/assets/vendor/uplot/LICENSE"
    uplot_readme = _source_text(root, uplot_readme_path)
    uplot_license = _source_text(root, uplot_license_path)
    _require(r"(?im)^\s*-\s*\*\*Version\*\*:\s*1\.6\.32\s*$", uplot_readme,
             "uPlot version 1.6.32")
    _require(r"(?im)^\s*-\s*\*\*License\*\*:\s*MIT\b", uplot_readme,
             "uPlot MIT license")
    _require(r"(?i)^The MIT License\s*\(MIT\)", uplot_license,
             "uPlot LICENSE file MIT identity", flags=re.MULTILINE)
    tarball = _require(
        r"(?im)^\s*-\s*\*\*Source\*\*:\s*(https://registry\.npmjs\.org/uplot/-/uplot-1\.6\.32\.tgz)\s*$",
        uplot_readme, "uPlot registry tarball URL").group(1)
    registry = _require(
        r"(?im)^\s*-\s*\*\*Upstream registry metadata\*\*:\s*(https://registry\.npmjs\.org/uplot/1\.6\.32)\s*$",
        uplot_readme, "uPlot registry metadata URL").group(1)
    sha1 = _require(r"(?im)^\s*-\s*sha1 \(shasum\):\s*`?([0-9a-f]{40})`?\s*$",
                    uplot_readme, "historical uPlot sha1 claim").group(1)
    sha512 = _require(r"(?im)^\s*-\s*sha512 \(integrity\):\s*`?(sha512-[A-Za-z0-9+/]+={0,2})`?\s*$",
                     uplot_readme, "historical uPlot sha512 integrity claim").group(1)
    _require(r"(?i)Verified against the npm registry.*before vendoring", uplot_readme,
             "uPlot README's historical integrity-claim context", flags=re.DOTALL)

    lucide_readme_path = "web/assets/vendor/lucide/README.md"
    lucide_license_path = "web/assets/vendor/lucide/LICENSE"
    lucide_readme = _source_text(root, lucide_readme_path)
    lucide_license = _source_text(root, lucide_license_path)
    _require(r"(?i)^# Lucide icons \(vendored path data\)", lucide_readme,
             "Lucide vendored path-data identity", flags=re.MULTILINE)
    _require(r"(?im)^\s*-\s*\*\*License\*\*:\s*ISC\b", lucide_readme,
             "Lucide ISC license")
    _require(r"(?i)^ISC License\s*$", lucide_license,
             "Lucide LICENSE file ISC identity", flags=re.MULTILINE)
    _require(r"(?i)main branch", lucide_readme, "Lucide unpinned main-branch provenance")
    _require(r"(?i)hand\s*-?transcribed", lucide_readme, "Lucide hand-transcribed path data")
    if re.search(r"(?im)^\s*-\s*\*\*Version\*\*\s*:", lucide_readme):
        raise InventoryError("Lucide README now states a version; update the inventory evidence policy")
    if re.search(r"(?i)\b[0-9a-f]{40}\b", lucide_readme):
        raise InventoryError("Lucide README now states a commit; update the inventory evidence policy")

    uplot_properties = {
        "vendor:source-url": tarball,
        "vendor:registry-metadata-url": registry,
        "vendor:source-evidence": uplot_readme_path,
        "vendor:license-evidence": f"{uplot_readme_path}; {uplot_license_path}",
        "vendor:integrity-claim:sha1": (
            f"{sha1} (historical README claim made before vendoring; archive not independently verified by this inventory)"
        ),
        "vendor:integrity-claim:sha512": (
            f"{sha512} (historical README claim made before vendoring; archive not independently verified by this inventory)"
        ),
    }
    lucide_properties = {
        "vendor:upstream-url": "https://github.com/lucide-icons/lucide",
        "vendor:upstream-branch": "main (README claim; not a pinned revision)",
        "vendor:version-status": "unknown: no version is recorded in the vendoring README",
        "vendor:upstream-commit-status": "unknown: no commit is recorded in the vendoring README",
        "vendor:source-evidence": lucide_readme_path,
        "vendor:license-evidence": f"{lucide_readme_path}; {lucide_license_path}",
        "vendor:vendored-content": (
            "Raw SVG child-element data hand-transcribed into icons.js, mixed with a local SVG helper"
        ),
    }
    return uplot_properties, lucide_properties


def _verify_inter_evidence(root: Path) -> dict[str, str]:
    readme_path = "web/assets/vendor/inter/README.md"
    license_path = "web/assets/vendor/inter/LICENSE"
    readme = _source_text(root, readme_path)
    license_text = _source_text(root, license_path)
    _require(r"(?im)^\s*-\s*\*\*Version\*\*:\s*5\.2\.8\s*$", readme, "Inter version 5.2.8")
    _require(r"(?im)^\s*-\s*\*\*License\*\*:\s*OFL-1\.1\b", readme, "Inter OFL-1.1 license")
    _require(r"SIL Open Font License, Version 1\.1", license_text, "Inter LICENSE file OFL identity")
    tarball = _require(
        r"(?im)^\s*-\s*\*\*Source\*\*:\s*(https://registry\.npmjs\.org/@fontsource-variable/inter/-/inter-5\.2\.8\.tgz)\s*$",
        readme, "Inter registry tarball URL").group(1)
    sha512 = _require(r"(?im)^\s*-\s*sha512 \(integrity\):\s*`?(sha512-[A-Za-z0-9+/]+={0,2})`?\s*$",
                      readme, "Inter sha512 integrity claim").group(1)
    return {
        "vendor:upstream-url": "https://github.com/rsms/inter",
        "vendor:registry-tarball": tarball,
        "vendor:registry-integrity-claim": sha512,
        "vendor:source-evidence": readme_path,
        "vendor:license-evidence": f"{readme_path}; {license_path}",
    }


def _file_component(root: Path, relative: str, properties: dict[str, str] | None = None) -> dict[str, Any]:
    data = _source_bytes(root, relative)
    file_properties = {"source:path": relative}
    if properties:
        file_properties.update(properties)
    return {
        "type": "file",
        "bom-ref": f"file:{relative}",
        "name": PurePosixPath(relative).name,
        "hashes": [{"alg": "SHA-256", "content": hashlib.sha256(data).hexdigest()}],
        "properties": _properties(file_properties),
    }


def _assemble(root: Path) -> dict[str, Any]:
    root = root.resolve(strict=True)
    if not root.is_dir():
        raise InventoryError(f"selected root is not a directory: {root}")
    uplot_properties, lucide_properties = _verify_evidence(root)

    uplot_component: dict[str, Any] = {
        "type": "library",
        "bom-ref": "library:npm:uplot@1.6.32",
        "name": "uPlot",
        "version": "1.6.32",
        "purl": "pkg:npm/uplot@1.6.32",
        "licenses": [{"license": {"id": "MIT"}}],
        "properties": _properties(uplot_properties),
    }
    lucide_component: dict[str, Any] = {
        "type": "library",
        "bom-ref": "library:lucide-icon-path-data",
        "name": "Lucide icon path data",
        "licenses": [{"license": {"id": "ISC"}}],
        "properties": _properties(lucide_properties),
    }
    inter_component: dict[str, Any] = {
        "type": "library",
        "bom-ref": "library:npm:@fontsource-variable/inter@5.2.8",
        "name": "Inter (variable, Latin subset)",
        "version": "5.2.8",
        "purl": "pkg:npm/%40fontsource-variable/inter@5.2.8",
        "licenses": [{"license": {"id": "OFL-1.1"}}],
        "properties": _properties(_verify_inter_evidence(root)),
    }
    components: list[dict[str, Any]] = [uplot_component, lucide_component, inter_component]
    components.extend(_file_component(root, item) for item in INTER_FILES)
    components.extend(_file_component(root, item) for item in UPLOT_FILES)
    components.extend(
        _file_component(
            root,
            item,
            {"inventory:relationship-scope": (
                "Contains hand-transcribed Lucide SVG child-element data plus local helper; hash covers whole file"
            )} if item == "web/assets/js/ui/icons.js" else None,
        )
        for item in LUCIDE_FILES
    )
    components.sort(key=lambda item: item["bom-ref"])

    root_component = {
        "type": "application",
        "bom-ref": APP_REF,
        "name": "cloud-pulse",
        "properties": _properties({
            "inventory:scope": "Source-bound browser vendor inventory only",
            "inventory:excluded": (
                "Authoritative npm dependency graph, transitive dependencies, and build/test dependencies"
            ),
            "security:assessment": "No vulnerability analysis performed",
        }),
    }
    dependencies = [
        {"ref": APP_REF, "dependsOn": [
            uplot_component["bom-ref"], lucide_component["bom-ref"], inter_component["bom-ref"],
        ]},
        {"ref": inter_component["bom-ref"], "dependsOn": [f"file:{item}" for item in INTER_FILES]},
        {"ref": uplot_component["bom-ref"], "dependsOn": [f"file:{item}" for item in UPLOT_FILES]},
        {"ref": lucide_component["bom-ref"], "dependsOn": [f"file:{item}" for item in LUCIDE_FILES]},
    ]
    dependencies.sort(key=lambda item: item["ref"])
    for dependency in dependencies:
        dependency["dependsOn"].sort()
    bom = {
        "bomFormat": "CycloneDX",
        "specVersion": "1.6",
        "version": 1,
        "metadata": {"component": root_component},
        "components": components,
        "dependencies": dependencies,
    }
    _validate_structure(bom)
    return bom


def _validate_structure(bom: Any) -> None:
    if not isinstance(bom, dict) or set(bom) != {
        "bomFormat", "specVersion", "version", "metadata", "components", "dependencies"
    }:
        raise InventoryError("artifact does not match the supported CycloneDX 1.6 top-level profile")
    if (bom["bomFormat"], bom["specVersion"], bom["version"]) != ("CycloneDX", "1.6", 1):
        raise InventoryError("artifact has an unexpected CycloneDX identity/version")
    metadata = bom["metadata"]
    if not isinstance(metadata, dict) or set(metadata) != {"component"}:
        raise InventoryError("artifact metadata does not match the supported profile")
    all_components = [metadata["component"], *bom["components"]] if isinstance(bom["components"], list) else []
    if not all_components or not isinstance(bom["dependencies"], list):
        raise InventoryError("artifact components/dependencies must be arrays with an application root")
    refs: set[str] = set()
    for component in all_components:
        if not isinstance(component, dict) or not isinstance(component.get("bom-ref"), str):
            raise InventoryError("component is missing a bom-ref")
        if component["bom-ref"] in refs:
            raise InventoryError(f"duplicate component identity: {component['bom-ref']}")
        refs.add(component["bom-ref"])
    dependency_refs: set[str] = set()
    for dependency in bom["dependencies"]:
        if not isinstance(dependency, dict) or set(dependency) != {"ref", "dependsOn"}:
            raise InventoryError("dependency entry does not match the supported profile")
        if dependency["ref"] not in refs or dependency["ref"] in dependency_refs:
            raise InventoryError(f"dependency has an unknown or duplicate ref: {dependency['ref']}")
        dependency_refs.add(dependency["ref"])
        if not isinstance(dependency["dependsOn"], list) or any(ref not in refs for ref in dependency["dependsOn"]):
            raise InventoryError(f"dependency {dependency['ref']} has an unknown child reference")


def validate_profile(document: Any, root: Path) -> None:
    """Validate only this inventory's declared structural/source-bound profile."""
    _validate_structure(document)
    expected = _assemble(root)
    if document != expected:
        raise InventoryError(
            "artifact identity, component relationships, evidence facts, or source hashes are stale/invalid"
        )


def render(document: dict[str, Any]) -> bytes:
    return (json.dumps(document, ensure_ascii=False, indent=2, sort_keys=True) + "\n").encode("utf-8")


def _output_path(value: str, root: Path) -> Path:
    path = Path(value)
    return path if path.is_absolute() else root / path


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=DEFAULT_ROOT,
                        help="selected source root (default: repository root)")
    parser.add_argument("--output", default=str(DEFAULT_OUTPUT),
                        help="artifact path (default: docs/sbom/vendor.cdx.json; relative to --root)")
    parser.add_argument("--check", action="store_true",
                        help="validate that --output exists and exactly matches current sources")
    args = parser.parse_args(argv)
    try:
        root = args.root.resolve(strict=True)
        output = _output_path(args.output, root)
        document = _assemble(root)
        expected_bytes = render(document)
        source_paths = {_safe_input(root, item) for item in ALL_FILES}
        if output.resolve(strict=False) in source_paths:
            raise InventoryError("output path must not overwrite an inventoried source")
        if args.check:
            try:
                actual_bytes = output.read_bytes()
            except OSError as exc:
                raise InventoryError(f"cannot read SBOM artifact {output}: {exc}") from exc
            try:
                artifact = json.loads(actual_bytes)
            except (UnicodeDecodeError, json.JSONDecodeError) as exc:
                raise InventoryError(f"SBOM artifact is malformed JSON: {exc}") from exc
            validate_profile(artifact, root)
            if actual_bytes != expected_bytes:
                raise InventoryError("SBOM artifact is valid JSON but is stale or not deterministic canonical output")
            print(f"OK: {output}")
        else:
            output.parent.mkdir(parents=True, exist_ok=True)
            output.write_bytes(expected_bytes)
            print(f"Wrote {output}")
    except (InventoryError, OSError) as exc:
        print(f"vendor SBOM: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
