# Start here

1. Read [AUDIT](AUDIT.md), [WORKFLOW](WORKFLOW.md), [DECISIONS](DECISIONS.md), [STATE](STATE.md), [HANDOFF](HANDOFF.md), and [tasks.json](tasks.json).
2. Reconcile the audit with the current Git HEAD and preserve existing work.
3. Run `python3 scripts/agent_tasks.py next`; claim R01 with `start R01`.
4. Implement and verify each ready task. Copy [evidence template](evidence/TEMPLATE.md), record real results, then mark it done.
5. Update STATE/HANDOFF and make a small local commit. Work alone; do not push, deploy or release.

At repository root, use `bash scripts/agent_verify.sh` for Go checks and `bash scripts/agent_verify.sh --images` for packaging changes. HAOS validation is a separate task. Missing tools/tests are BLOCKED, never PASS. See [the kickoff prompt](START_PROMPT.md).
