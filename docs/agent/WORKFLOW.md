# Solo workflow

Read AGENTS.md, the Runtime specification, accepted ADR-007/008/009, AUDIT.md, STATE.md, HANDOFF.md and tasks.json. Reconcile all claims with current Git HEAD and preserve existing work.

Run python3 scripts/agent_tasks.py next and claim exactly one ready task with start ID. Implement the smallest complete behavior satisfying that task. Continue independently through ready tasks after recording evidence and committing each cohesive slice.

## Engineering rules

- Accepted ADR-007/008/009 are implementation authority for their stated boundaries. Do not reopen them merely because the old harness called them Proposed.
- Do not broaden scope into remote clients, third-party repositories, generic secret brokerage, Runtime self-update, production HA actions or Bridge installation.
- Production module downloads/execution remain out until the task explicitly reaches an accepted package/install slice; tests use local synthetic fixtures.
- Bridge Python implementation belongs to its separate repository. Runtime may implement protocol fixtures/client behavior. On 2026-10-01 the stakeholder explicitly waived waiting for server evidence before implementing B03. Production selection still requires proven server barrier/sequence semantics.
- Real HAOS/appliance claims require an authorized target. Docker/Linux tests are not HAOS evidence.
- Use interfaces at real boundaries: clock/timeline persistence, process launcher/IPC, HA action transport, Bridge transport and filesystem/package verification. Avoid interface-per-struct ceremony.
- Prefer deterministic clocks/barriers/channels over sleeps. Race/model/fuzz tests must be bounded and reproducible.
- Every queue, frame, replay horizon, retry loop, child join and persisted collection needs an explicit bound or a documented reason it is finite.
- Never log or fixture real household identities, entity payloads, credentials or activity.

## Verification

For code changes run focused tests then bash scripts/agent_verify.sh. Use --images for packaging/Dockerfile changes. Add deterministic task-specific model/fault tests; compilation is never acceptance.

Review each diff for duplicate execution, dual-generation admission, blind action retry, stale authority, unbounded replay/queues, descriptor/credential leaks, persistence corruption and weakened fallback.

Evidence under docs/agent/evidence must record exact commands/results, entering/final HEAD, acceptance mapping, fault tests, limitations and review findings. Missing tools/environment are BLOCKED, never PASS.

Update STATE.md and HANDOFF.md after each task and make a small local commit when identity is available. Do not push, deploy, release, force-reset or operate the real home unless explicitly directed.

Stop only when no ready task remains. A blocked environment task does not block independent ready work.


## Explicit environment waivers

The 2026-10-01 stakeholder instruction to ignore blockers authorizes terminal
`waived` dispositions for unexecutable environment criteria in this run. Run
available checks first and record exact PASS/FAIL/unexecuted criteria separately.
`agent_tasks.py waive ID --reason ... --evidence ...` requires an active ungated
environment task, explicit reason and nonempty evidence. A waiver is not PASS and
does not satisfy implementation dependencies. Do not use it for implementation
work or to manufacture maintainer acceptance.
