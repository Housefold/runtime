#!/usr/bin/env python3
"""Persistent task ledger; a planning aid, not permission enforcement."""
import argparse
import json
from pathlib import Path
import os
import tempfile

ROOT = Path(__file__).resolve().parents[1]
LEDGER = ROOT / "docs/agent/tasks.json"

def main():
    p = argparse.ArgumentParser()
    p.add_argument("command", choices=["next", "list", "start", "done", "block", "resume"])
    p.add_argument("id", nargs="?")
    p.add_argument("--evidence")
    p.add_argument("--reason")
    a = p.parse_args()
    data = json.loads(LEDGER.read_text())
    rows = data["tasks"]
    ids = {r["id"]: r for r in rows}
    if len(ids) != len(rows):
        p.error("duplicate task IDs")
    for row in rows:
        if any(d not in ids for d in row["depends_on"]):
            p.error("unknown dependency")
    def ready(row):
        return (row["status"] == "todo" and not row["approval_required"]
                and all(ids[d]["status"] == "done" for d in row["depends_on"]))
    active = [r for r in rows if r["status"] == "in_progress"]
    if a.command == "list":
        for row in rows:
            print(row["id"], row["status"], row["title"])
        return
    if a.command == "next":
        candidates = active or [r for r in rows if ready(r)]
        print(json.dumps(candidates[0], indent=2) if candidates else "No ready task. Review blocked/gated entries; no approval is implied.")
        return
    if a.id not in ids:
        p.error("valid task ID required")
    row = ids[a.id]
    if a.command == "start":
        if active or not ready(row):
            p.error("task is not ready or another task is active")
        row["status"] = "in_progress"
    elif a.command == "done":
        if row["status"] != "in_progress" or not a.evidence:
            p.error("active task and --evidence required")
        evidence = (ROOT / a.evidence).resolve()
        evidence_root = (ROOT / "docs/agent/evidence").resolve()
        if not evidence.is_relative_to(evidence_root) or not evidence.is_file() or evidence.stat().st_size == 0:
            p.error("evidence must be a nonempty file under docs/agent/evidence")
        if evidence.name == "TEMPLATE.md":
            p.error("copy and fill the evidence template for this task")
        row.update(status="done", evidence=str(evidence.relative_to(ROOT)), blocker=None)
    elif a.command == "block":
        if row["status"] not in ("todo", "in_progress") or not a.reason:
            p.error("pending/active task and --reason required")
        row.update(status="blocked", blocker=a.reason)
    elif a.command == "resume":
        if row["status"] != "blocked" or row["approval_required"]:
            p.error("only ungated blocked tasks may resume")
        row.update(status="todo", blocker=None)
    with tempfile.NamedTemporaryFile(mode="w", dir=LEDGER.parent, delete=False) as f:
        json.dump(data, f, indent=2)
        f.write("\n")
        temporary = f.name
    os.replace(temporary, LEDGER)
    print(row["id"], row["status"])

if __name__ == "__main__":
    main()
