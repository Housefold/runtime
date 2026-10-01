# Handoff

Latest completed task: M04. Added durable ready-before-cutover generation routing, once-only dispatch claims, boot fencing and bounded drain/child retirement.

Internal coordinator; dispatch loss after a durable claim is interrupted rather than repeated; no production activation or HAOS claim.

Run `python3 scripts/agent_tasks.py next` and continue independently. See STATE and per-task evidence for exact checks. Accepted ADR-007/008/009 govern implementation. No push, deployment, release, real-home actions or production downloads are authorized. R07/V02 require authorized HAOS targets; B03 requires Python ordered-state evidence.
