# Agent guide

This file is a map and a set of guardrails. The product and architecture documents are the source for Housefold-wide intent; this repository's [Runtime specification](docs/runtime-spec.md) records the current Runtime scope and decisions.

## Read before changing behavior

1. [Project Vision](https://github.com/Housefold/docs/blob/main/Housefold_Project_Vision.md)
2. [System Architecture](https://github.com/Housefold/docs/blob/main/Housefold_System_Architecture.md)
3. [Domain Model](https://github.com/Housefold/docs/blob/main/Housefold_Domain_Model.md)
4. [Runtime specification](docs/runtime-spec.md)
5. Relevant records in [`docs/adr/`](docs/adr/)

## Guardrails

- Work only on the assigned slice. Do not add adjacent features, abstractions, or dependencies without a clear requirement.
- Follow the accepted baseline: HAOS add-on, HA REST/WebSocket first, optional Bridge, and separate-process Go modules.
- Keep module execution, updates, and the public module contract out of an initial bootstrap slice unless the assigned task explicitly includes an approved design.
- Do not invent public APIs, module protocols, permissions, credentials, update trust, or remote-access behavior. If the task depends on an unresolved decision, stop and write the question and options in a proposed ADR.
- Do not execute downloaded or discovered module code automatically. Module installation, trust, permissions, and updates are blocked until their security design is approved.
- Keep management and recovery local. Do not expose an unauthenticated control surface to the network or make local operation depend on the VPS or cloud.
- Preserve the last healthy runtime/module version during updates. Never permit overlapping active automation versions.
- Keep secrets and real household identities/activity out of source, fixtures, logs, and examples.
- Update the Runtime specification or an ADR when implementation changes an accepted contract.
- Make changes small and reviewable on `main`. Do not create a PR before the first release unless requested. Do not tag or release without explicit user direction.

## Completion

For behavior changes, add or update focused tests and report the exact checks run. The standard static checks are `gofmt`, `go vet ./...`, and `go build ./...`; run focused tests for behavior changes. Do not claim acceptance criteria are met by compilation alone.

<!-- HOUSEFOLD_SOLO_HARNESS_V1 -->
## Solo development harness

The assigned scope is the accepted ADR-007/008/009 implementation queue in docs/agent/tasks.json. Read AUDIT.md, WORKFLOW.md, STATE.md and the selected task before coding. ADR-007, ADR-008 and ADR-009 were explicitly accepted on 2026-10-01 and are authoritative for their stated module, action, trust, lifecycle and Bridge boundaries.

Work independently through ready tasks. Make ordinary internal implementation decisions, document rationale, prove failure behavior and continue. Do not stop at a plan or compilation, and do not infer implemented work from earlier chat. Work alone; no sub-agents.

Do not reopen accepted ADR-007/008/009 or recreate their superseded grant/sandbox/rollback assumptions. Record blockers and continue independent ready tasks. New boundaries still require an explicit decision. Use small cohesive changes on main per existing policy; no reset, force push, release, production HA actions, or production module downloads/execution. Local verified commits are allowed; pushing needs direction.

Run bash scripts/agent_verify.sh for code changes and --images for packaging when Docker is available. Missing checks are BLOCKED. Update evidence, STATE.md and HANDOFF.md after every task.
<!-- END_HOUSEFOLD_SOLO_HARNESS_V1 -->
