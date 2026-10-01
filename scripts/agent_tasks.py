#!/usr/bin/env python3
"""Persistent productization task ledger. It records work; it does not waive release criteria."""
import argparse, json, os, tempfile
from pathlib import Path

ROOT=Path(__file__).resolve().parents[1]
LEDGER=ROOT/"docs/agent/tasks.json"

def main():
    p=argparse.ArgumentParser()
    p.add_argument("command",choices=["next","list","start","done","block","resume"])
    p.add_argument("id",nargs="?")
    p.add_argument("--evidence")
    p.add_argument("--reason")
    a=p.parse_args()
    data=json.loads(LEDGER.read_text())
    rows=data["tasks"]; ids={r["id"]:r for r in rows}
    if len(ids)!=len(rows): p.error("duplicate task IDs")
    for r in rows:
        if any(d not in ids for d in r["depends_on"]): p.error("unknown dependency")
    def ready(r):
        return r["status"]=="todo" and not r["approval_required"] and all(ids[d]["status"]=="done" for d in r["depends_on"])
    active=[r for r in rows if r["status"]=="in_progress"]
    blocked=[r for r in rows if r["status"]=="blocked"]
    if a.command=="list":
        for r in rows: print(r["id"],r["status"],r["title"])
        return
    if a.command=="next":
        candidates=active or [r for r in rows if ready(r)]
        if candidates: print(json.dumps(candidates[0],indent=2))
        elif blocked: print("No ready task. BLOCKED criteria remain and prevent Runtime v1 completion:\n"+json.dumps(blocked,indent=2))
        else: print("No ready task.")
        return
    if a.id not in ids: p.error("valid task ID required")
    r=ids[a.id]
    if a.command=="start":
        if active or not ready(r): p.error("task is not ready or another task is active")
        r["status"]="in_progress"
    elif a.command=="done":
        if r["status"]!="in_progress" or not a.evidence: p.error("active task and --evidence required")
        ev=(ROOT/a.evidence).resolve(); er=(ROOT/"docs/agent/evidence").resolve()
        if not ev.is_relative_to(er) or not ev.is_file() or ev.stat().st_size==0 or ev.name=="TEMPLATE.md": p.error("nonempty task evidence under docs/agent/evidence required")
        r.update(status="done",evidence=str(ev.relative_to(ROOT)),blocker=None)
    elif a.command=="block":
        if r["status"] not in ("todo","in_progress") or not a.reason: p.error("pending/active task and --reason required")
        r.update(status="blocked",blocker=a.reason)
    elif a.command=="resume":
        if r["status"]!="blocked" or r["approval_required"]: p.error("only ungated blocked tasks may resume")
        r.update(status="todo",blocker=None)
    with tempfile.NamedTemporaryFile(mode="w",dir=LEDGER.parent,delete=False) as f:
        json.dump(data,f,indent=2); f.write("\n"); tmp=f.name
    os.replace(tmp,LEDGER); print(r["id"],r["status"])

if __name__=="__main__": main()
