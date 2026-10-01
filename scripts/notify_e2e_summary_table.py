#!/usr/bin/env python3
"""notify_e2e_summary_table.py — renders .github/workflows/notify-e2e.yml's
job-summary markdown table from a models.NotifyVerifyReport JSON file
(SPEC-v0.8 §3-2's project-CI-visibility requirement).

Kept as a standalone script rather than inline in the workflow YAML so
its indentation-sensitive Python body isn't at the mercy of a YAML
block scalar's own indentation rules (a heredoc/`-c` string embedded
directly in the workflow previously broke both actionlint's YAML
parser and Python's own indentation parsing at once).

models.NotifyVerifyResult is secret-free by construction (see
internal/models/notifyverify.go) — no masking pass is needed here.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path


def render_table(report: dict) -> str:
    lines = ["| Platform | Status | Attachment verified | Cleaned up | Diagnosis |", "|---|---|---|---|---|"]
    results = report.get("results", [])
    if not results:
        lines.append("| _(no results in report)_ | | | | |")
        return "\n".join(lines)
    for result in results:
        platform = result.get("platform", "?")
        status = result.get("status", "?")
        attachment = "yes" if result.get("attachment_verified") else "n/a"
        cleaned = result.get("cleaned")
        cleaned_cell = "n/a" if cleaned is None else ("yes" if cleaned else "no")
        diagnosis = (result.get("diagnosis") or {}).get("code", "")
        lines.append(f"| {platform} | {status} | {attachment} | {cleaned_cell} | {diagnosis} |")
    return "\n".join(lines)


def main(argv: list[str]) -> int:
    if len(argv) != 1:
        print("usage: notify_e2e_summary_table.py <report.json>", file=sys.stderr)
        return 2

    path = Path(argv[0])
    try:
        report = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        print("| _(no report was produced)_ | | | | |")
        return 0

    print(render_table(report))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
