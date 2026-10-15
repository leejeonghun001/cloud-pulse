#!/usr/bin/env python3
"""Fail-closed static profile check for the repository quality-matrix workflow.

This checks a supported subset of the workflow's structure. It is not
actionlint, full GitHub expression validation, or a workflow runner.
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path
from typing import Any

try:
    import yaml
except ImportError:  # pragma: no cover - exercised by the explicit missing-parser test
    yaml = None


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_WORKFLOW = ROOT / ".github/workflows/quality-matrix.yml"
EXPECTED_TARGETS = {
    ("linux", "amd64"), ("linux", "arm64"),
    ("windows", "amd64"), ("windows", "arm64"),
    ("darwin", "amd64"), ("darwin", "arm64"),
}
EXPECTED_RUNNERS = {"ubuntu-latest", "macos-latest", "windows-latest"}


class QualityWorkflowError(ValueError):
    """The workflow is malformed or outside the supported static profile."""


def _need_yaml() -> Any:
    if yaml is None:
        raise QualityWorkflowError(
            "PyYAML is unavailable; quality-matrix static validation cannot run (no dependency was installed)"
        )
    return yaml


def _strict_loader(yaml_module: Any) -> type:
    class StrictBaseLoader(yaml_module.BaseLoader):
        def construct_mapping(self, node: Any, deep: bool = False) -> dict[str, Any]:
            mapping: dict[str, Any] = {}
            for key_node, value_node in node.value:
                key = self.construct_object(key_node, deep=deep)
                if not isinstance(key, str):
                    raise yaml_module.constructor.ConstructorError(
                        "while constructing a workflow mapping", node.start_mark,
                        "mapping keys must be strings", key_node.start_mark,
                    )
                if key == "<<":
                    raise yaml_module.constructor.ConstructorError(
                        "while constructing a workflow mapping", node.start_mark,
                        "YAML merge keys are unsupported", key_node.start_mark,
                    )
                if key in mapping:
                    raise yaml_module.constructor.ConstructorError(
                        "while constructing a workflow mapping", node.start_mark,
                        f"duplicate mapping key {key!r}", key_node.start_mark,
                    )
                mapping[key] = self.construct_object(value_node, deep=deep)
            return mapping

    StrictBaseLoader.add_constructor(
        yaml_module.resolver.BaseResolver.DEFAULT_MAPPING_TAG,
        StrictBaseLoader.construct_mapping,
    )
    return StrictBaseLoader


def parse_workflow(text: str) -> dict[str, Any]:
    yaml_module = _need_yaml()
    try:
        for event in yaml_module.parse(text, Loader=yaml_module.BaseLoader):
            if getattr(event, "tag", None) is not None:
                raise QualityWorkflowError("explicit YAML tags are unsupported")
        data = yaml_module.load(text, Loader=_strict_loader(yaml_module))
    except QualityWorkflowError:
        raise
    except yaml_module.YAMLError as exc:
        raise QualityWorkflowError(f"malformed or unsupported YAML: {exc}") from exc
    if not isinstance(data, dict):
        raise QualityWorkflowError("workflow document must be a mapping")
    return data


def _mapping(value: Any, label: str, keys: set[str] | None = None) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise QualityWorkflowError(f"{label} must be a mapping")
    if keys is not None and set(value) != keys:
        missing = sorted(keys - set(value))
        extra = sorted(set(value) - keys)
        raise QualityWorkflowError(f"{label} shape is unsupported (missing={missing}, extra={extra})")
    return value


def _exact(value: Any, expected: Any, label: str) -> None:
    if value != expected:
        raise QualityWorkflowError(f"{label} does not match the supported quality-matrix profile")


def _action(step: Any, label: str, action: str, with_values: dict[str, str] | None = None) -> None:
    keys = {"name", "uses"} | ({"with"} if with_values is not None else set())
    item = _mapping(step, label, keys)
    expected_name = {"checkout": "Checkout", "setup-go": "Set up Go", "setup-node": "Set up Node.js"}[action]
    _exact(item["name"], expected_name, f"{label} name")
    if not isinstance(item["uses"], str) or not re.fullmatch(rf"actions/{re.escape(action)}@v[0-9]+", item["uses"]):
        raise QualityWorkflowError(f"{label} must use the known official actions/{action} action")
    if with_values is not None:
        actual = _mapping(item["with"], f"{label}.with", set(with_values))
        _exact(actual, with_values, f"{label}.with")


def _run_step(step: Any, label: str, name: str, command: str) -> None:
    item = _mapping(step, label, {"name", "run"})
    _exact(item["name"], name, f"{label} name")
    if not isinstance(item["run"], str):
        raise QualityWorkflowError(f"{label} command must be a string")
    _exact(item["run"].strip(), command, f"{label} command")


def _job_steps(job: Any, label: str, expected_keys: set[str], count: int) -> tuple[dict[str, Any], list[Any]]:
    item = _mapping(job, label, expected_keys)
    steps = item.get("steps")
    if not isinstance(steps, list) or len(steps) != count:
        raise QualityWorkflowError(f"{label}.steps must contain exactly {count} supported steps")
    return item, steps


def validate_workflow(workflow: dict[str, Any]) -> None:
    root = _mapping(workflow, "workflow", {"name", "on", "permissions", "env", "jobs"})
    _exact(root["name"], "Quality Matrix", "workflow name")
    _exact(root["on"], {"workflow_dispatch": ""}, "workflow trigger")
    _exact(root["permissions"], {"contents": "read"}, "workflow permissions")
    _exact(root["env"], {"CGO_ENABLED": "0"}, "workflow environment")
    jobs = _mapping(root["jobs"], "jobs", {"cross-build", "go-native", "node-tests"})

    build, build_steps = _job_steps(
        jobs["cross-build"], "cross-build job",
        {"name", "runs-on", "timeout-minutes", "strategy", "steps"}, 3,
    )
    _exact(build["name"], "Cross-build ${{ matrix.goos }}/${{ matrix.goarch }}", "cross-build name")
    _exact(build["runs-on"], "ubuntu-latest", "cross-build runner")
    _exact(build["timeout-minutes"], "15", "cross-build timeout")
    strategy = _mapping(build["strategy"], "cross-build strategy", {"fail-fast", "matrix"})
    _exact(strategy["fail-fast"], "false", "cross-build fail-fast")
    matrix = _mapping(strategy["matrix"], "cross-build matrix", {"include"})
    include = matrix["include"]
    if not isinstance(include, list):
        raise QualityWorkflowError("cross-build matrix.include must be a list")
    targets: list[tuple[str, str]] = []
    for index, target in enumerate(include):
        row = _mapping(target, f"matrix.include[{index}]", {"goos", "goarch"})
        if not isinstance(row["goos"], str) or not isinstance(row["goarch"], str):
            raise QualityWorkflowError(f"matrix.include[{index}] target values must be strings")
        targets.append((row["goos"], row["goarch"]))
    if len(targets) != len(set(targets)):
        raise QualityWorkflowError("cross-build matrix contains duplicate targets")
    if set(targets) != EXPECTED_TARGETS or len(targets) != len(EXPECTED_TARGETS):
        raise QualityWorkflowError("cross-build matrix must contain exactly the six supported OS/architecture targets")

    _action(build_steps[0], "cross-build checkout", "checkout")
    _action(build_steps[1], "cross-build Go setup", "setup-go", {"go-version-file": "go.mod", "cache": "true"})
    binary = _mapping(build_steps[2], "cross-build binary step", {"name", "env", "run"})
    _exact(binary["name"], "Build hub and agent binaries", "cross-build binary step name")
    _exact(binary["env"], {"GOOS": "${{ matrix.goos }}", "GOARCH": "${{ matrix.goarch }}"},
           "cross-build target environment")
    if not isinstance(binary["run"], str):
        raise QualityWorkflowError("cross-build commands must be a string")
    expected_build = '\n'.join((
        "mkdir -p dist",
        'suffix=""',
        'if [ "$GOOS" = windows ]; then suffix=".exe"; fi',
        'go build -trimpath -o "dist/cloud-pulse-hub${suffix}" ./cmd/hub',
        'go build -trimpath -o "dist/cloud-pulse-agent${suffix}" ./cmd/agent',
    ))
    _exact(binary["run"].strip(), expected_build, "cross-build commands and outputs")

    native, native_steps = _job_steps(
        jobs["go-native"], "go-native job",
        {"name", "runs-on", "timeout-minutes", "strategy", "steps"}, 4,
    )
    _exact(native["name"], "Go vet and tests (${{ matrix.os }})", "native job name")
    _exact(native["runs-on"], "${{ matrix.os }}", "native runner expression")
    _exact(native["timeout-minutes"], "20", "native timeout")
    native_strategy = _mapping(native["strategy"], "native strategy", {"fail-fast", "matrix"})
    _exact(native_strategy["fail-fast"], "false", "native fail-fast")
    native_matrix = _mapping(native_strategy["matrix"], "native matrix", {"os"})["os"]
    if not isinstance(native_matrix, list) or any(not isinstance(item, str) for item in native_matrix):
        raise QualityWorkflowError("native runner matrix must be a list of strings")
    if len(native_matrix) != len(set(native_matrix)):
        raise QualityWorkflowError("native runner matrix must not contain duplicates")
    if set(native_matrix) != EXPECTED_RUNNERS or len(native_matrix) != len(EXPECTED_RUNNERS):
        raise QualityWorkflowError("native runner matrix must be exactly Ubuntu, macOS, and Windows")
    _action(native_steps[0], "native checkout", "checkout")
    _action(native_steps[1], "native Go setup", "setup-go", {"go-version-file": "go.mod", "cache": "true"})
    _run_step(native_steps[2], "go vet step", "Run go vet", "go vet ./...")
    _run_step(native_steps[3], "go test step", "Run all Go tests", "go test -count=1 ./...")

    node, node_steps = _job_steps(
        jobs["node-tests"], "node-tests job",
        {"name", "runs-on", "timeout-minutes", "steps"}, 3,
    )
    _exact(node["name"], "Node tests", "node job name")
    _exact(node["runs-on"], "ubuntu-latest", "node runner")
    _exact(node["timeout-minutes"], "10", "node timeout")
    _action(node_steps[0], "node checkout", "checkout")
    _action(node_steps[1], "node setup", "setup-node", {"node-version": "22"})
    _run_step(node_steps[2], "npm test step", "Run npm test", "npm test")


def check_path(path: Path) -> None:
    try:
        text = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise QualityWorkflowError(f"cannot read workflow {path}: {exc}") from exc
    except UnicodeError as exc:
        raise QualityWorkflowError(f"workflow is not valid UTF-8: {exc}") from exc
    workflow = parse_workflow(text)
    validate_workflow(workflow)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workflow", type=Path, default=DEFAULT_WORKFLOW,
                        help="workflow YAML (default: .github/workflows/quality-matrix.yml)")
    args = parser.parse_args(argv)
    try:
        check_path(args.workflow)
    except QualityWorkflowError as exc:
        print(f"quality-matrix: {exc}", file=sys.stderr)
        return 2
    print("quality-matrix: PASS (supported static profile)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
