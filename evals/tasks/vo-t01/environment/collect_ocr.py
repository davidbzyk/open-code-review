"""Hand the grader Open Code Review's report in the format its judge reads.

    python3 collect_ocr.py --out DIR --artifacts DIR [--max-bytes N]

Runs as the agent user in the collect hook after the report step. It reads `report.json`, the
file the report step wrote, and writes its comments as the flat `findings.json` the team task's
grader reads, with the report and the merge beside it for analysis. A report that is missing,
too big or not `{"comments": [...]}` leaves no findings file, which the grader scores 0.0.
Standard library only.
"""

from __future__ import annotations

import argparse
import json
import shutil
import sys
from pathlib import Path

MAX_BYTES = 5_000_000  # the default; the task passes the bundle's agent.max_report_bytes
REPORT = "report.json"
# Open Code Review's severities in the scale the grader's judge prompt uses.
SEVERITY = {"critical": "high", "high": "high", "medium": "medium", "low": "low"}
# Kept beside the findings so a trial can be read without the container.
KEPT = ("merged.json", "groups.json", "grouping.json")
TITLE_CHARS = 120


def title(content: str) -> str:
    """A comment's first line, cut to a title's length."""
    first = next((line.strip() for line in content.splitlines() if line.strip()), "")
    return first if len(first) <= TITLE_CHARS else first[: TITLE_CHARS - 1].rstrip() + "…"


def flat(comment: dict) -> dict:
    """One report comment in the format the grader and its judge prompt read."""
    content = comment["content"]
    start = comment.get("start_line")
    return {
        "file": comment["path"],
        "line_start": start,
        "line_end": comment.get("end_line") or start,
        "severity": SEVERITY.get(str(comment.get("severity", "")).lower(), "low"),
        "title": title(content),
        "body": content,
    }


def read_report(path: Path, max_bytes: int) -> dict | None:
    """The report document, or None when the report step left nothing gradable."""
    if not path.is_file() or path.is_symlink() or path.stat().st_size > max_bytes:
        return None
    try:
        doc = json.loads(path.read_text(encoding="utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        return None
    if not isinstance(doc, dict) or not isinstance(doc.get("comments"), list):
        return None
    return doc


def collect(out: Path, artifacts: Path, max_bytes: int = MAX_BYTES) -> int | None:
    """Write the grader's files; returns how many findings were handed over, None for no report."""
    artifacts.mkdir(parents=True, exist_ok=True)
    for name in KEPT:
        source = out / name
        if source.is_file() and not source.is_symlink() and source.stat().st_size <= max_bytes:
            shutil.copyfile(source, artifacts / name)
    doc = read_report(out / REPORT, max_bytes)
    if doc is None:
        return None
    findings = [
        flat(c)
        for c in doc["comments"]
        if isinstance(c, dict) and isinstance(c.get("path"), str) and isinstance(c.get("content"), str)
    ]
    (artifacts / "ocr-report.json").write_text(json.dumps(doc, indent=2) + "\n")
    (artifacts / "findings.json").write_text(json.dumps(findings, indent=2) + "\n")
    return len(findings)


def main(argv: list[str] | None = None) -> int:
    """Parse the arguments and collect; always exits 0, since a collect hook is best effort."""
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    for name in ("--out", "--artifacts"):
        p.add_argument(name, type=Path, required=True)
    p.add_argument("--max-bytes", type=int, default=MAX_BYTES)
    args = p.parse_args(argv)
    n = collect(args.out, args.artifacts, args.max_bytes)
    print("collect_ocr: no report to grade" if n is None else f"collect_ocr: {n} findings")
    return 0


if __name__ == "__main__":
    sys.exit(main())
