# Productization harness

Read PRODUCTIZATION.md first, then WORKFLOW.md, STATE.md, HANDOFF.md, the Runtime spec/ADRs and tasks.json.

Use `python3 scripts/agent_tasks.py next` to select work. Implement one task completely, create a filled evidence record under `docs/agent/evidence/`, then mark it done. Continue until the queue closes or a genuine blocker prevents a release gate.

Use `bash scripts/agent_verify.sh` for code and `bash scripts/agent_verify.sh --images` for packaging. Release completion additionally requires the independent security gate, soak gate and exact-artifact disposable HAOS gate in tasks.json.

There are no agent waivers in the productization harness. A blocked release criterion remains blocked until the stakeholder explicitly changes the contract.
