Develop Housefold Runtime independently using the in-repository solo-agent harness.

Read AGENTS.md, docs/runtime-spec.md, relevant ADRs, and docs/agent/AUDIT.md, WORKFLOW.md, DECISIONS.md, STATE.md, HANDOFF.md and tasks.json. Inspect actual source and reconcile the audit against current HEAD. Earlier chat claims do not prove implementation exists.

Run python3 scripts/agent_tasks.py next. Establish the verification baseline, claim the first ready task and implement a complete small slice. Work through ready tasks; do not return only a plan. Add meaningful fault/concurrency tests, run required checks, review the diff, write evidence and update continuity files. Make small commits when Git identity is configured. Do not push, deploy or release.

Immediate goal: private state consumption with per-entity reads and ordered independent subscriptions, atomic initial snapshots, generation/revision positions, bounded queues, explicit freshness and terminal outcomes. Preserve existing HA reconciliation/privacy. Keep coarse status notifications separate.

Make ordinary internal decisions and document rationale. For public/security proposals, write concrete ADR/spec and tests but do not implement unresolved contracts. Continue independent tasks when one is blocked; do not repeatedly ask routine questions. Run bash scripts/agent_verify.sh for code and --images for packaging. Missing checks/HAOS evidence are BLOCKED, never PASS. Synthetic household data only.

Work solo; do not spawn sub-agents. Continue until authorized work is complete or truly blocked. Finish with changes, exact checks, remaining decisions and next action. Preserve STATE.md and HANDOFF.md for the next session.
