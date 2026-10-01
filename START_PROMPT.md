Complete Housefold Runtime v1 independently using the in-repository productization harness.

Read AGENTS.md, docs/runtime-spec.md, accepted ADRs, docs/agent/PRODUCTIZATION.md, AUDIT.md, WORKFLOW.md, STATE.md, HANDOFF.md and tasks.json. PRODUCTIZATION.md records the stakeholder decisions for the live-ready Runtime and is authoritative where the old completed harness differs.

Run `python3 scripts/agent_tasks.py next`, claim the first ready task and continue through the entire queue. Do not return only a plan and do not ask for routine engineering approval. You have full authority to refactor existing code, create sensible Housefold repositories when GitHub permissions permit, configure distribution/Pages/CI, publish candidate artifacts after gates pass, and create/destroy disposable test environments.

The finish line is not library completion. The exact published candidate must install from the real Housefold HA App repository on clean supported disposable HAOS and pass V1P14. Security and soak gates are mandatory first. No task or release criterion may be self-waived. If a genuine external blocker remains after reasonable alternatives, mark it BLOCKED, preserve evidence and do not claim Runtime v1 complete.

Never touch the stakeholder's live HAOS instance, use real household actions/identities/secrets, weaken admin/security boundaries, or replace the final HAOS gate with Docker/mocks. Runtime should follow normal HA App conventions and mirror HA Supervisor lifecycle semantics for its child modules.

For each task: inspect actual HEAD, implement production behavior, add deterministic failure/concurrency/security tests as appropriate, run focused checks plus `bash scripts/agent_verify.sh` (and `--images` for packaging), review the diff, record evidence, update STATE/HANDOFF and commit cohesive work. Push/publish only where required by the accepted productization/distribution tasks and only after their prerequisite gates.