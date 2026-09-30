# Solo workflow

Read repository instructions/spec/ADRs and agent audit/state/handoff. Inspect Git status and HEAD, preserve user edits, and reconcile newer source before relying on audit claims. Run agent_tasks.py next and claim exactly one ready task with start ID. Establish baseline checks before implementation.

Define a complete small behavior and regression first. Keep HA wire handling under internal/ha and private consumer types independent of transport. Use interfaces at actual boundaries; avoid wrappers around every Go concrete type. Prefer standard library and the existing dependency; justify additions.

For subscriptions, serialize initial snapshot and registration against publication/live updates/freshness. Each consumer owns its nested data. Slow consumers cannot stall ingestion. Bound count/bytes including reset/initial snapshots and total subscriber cost. Overflow ends only that subscription visibly. Avoid send/close races. Generation/revision metadata must describe the exact returned data.

Run focused tests, then bash scripts/agent_verify.sh; use --images for packaging. Review the diff separately for data leaks, lost updates, unbounded work, weakened tests and undocumented authority. Do not invent an independent reviewer.

Write evidence using evidence/TEMPLATE.md: exact commands/results, HEAD, acceptance criteria, faults, limitations and review findings. Complete the ledger only when required evidence exists. A proposal being done means a reviewable proposal, not approval. Update STATE.md/HANDOFF.md and make a small local commit if identity is available; continue ready tasks. Missing identity blocks the commit, not the work.

For absent tools/environments, record attempted safe alternatives and a concrete restart condition. For public/security decisions, write a Proposed ADR with options, recommendation, failure/permission/compatibility implications and tests. Existing AGENTS.md requires review before those boundaries. Do not self-accept or silently amend accepted ADRs. Continue independent tasks.

The ledger is advisory, not sandbox enforcement. Only explicit maintainer acceptance referenced by an accepted ADR or concrete instruction may unlock gated epics. Stop when authorized work is done or genuinely blocked; report exact checks and limits. Never add feature modules, remote control, unattended downloads or production actions just to finish the queue.
