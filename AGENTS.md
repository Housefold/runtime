# Agent guide

Housefold-wide intent lives in the project documents. Runtime behavior is defined by docs/runtime-spec.md, accepted ADRs, and the active productization contract.

## Read before changing behavior

1. Project Vision in Housefold/docs
2. System Architecture in Housefold/docs
3. Domain Model in Housefold/docs
4. docs/runtime-spec.md
5. relevant docs/adr records
6. docs/agent/PRODUCTIZATION.md
7. docs/agent/WORKFLOW.md, STATE.md, HANDOFF.md and tasks.json

## Guardrails

- PRODUCTIZATION.md is accepted stakeholder direction for the active Runtime v1 delivery and overrides obsolete restrictions from the completed earlier harness.
- Runtime is a normal HAOS App; native HA is baseline, Bridge is optional, and managed modules are separate processes supervised by Runtime.
- Keep management local through admin-only HA ingress. Do not add an unauthenticated or independent LAN management surface.
- Runtime is the only HA boundary for modules. Preserve generation fencing, bounded resources/backpressure, explicit action outcomes and no blind retry of unknown actions.
- Keep credentials and real household identities/activity out of source, fixtures and evidence.
- Preserve last-healthy module retention and exactly one generation accepting new work.
- Update the Runtime specification/ADR when implementation changes an accepted contract.
- The stakeholder's live HAOS instance is never a productization test target.

## Productization authority

The agent may make ordinary engineering decisions, refactor existing code, create required Housefold repositories when permissions allow, build BIOS/catalog/distribution infrastructure, configure CI/release automation, publish artifacts when the active task's gates permit it, and freely operate disposable test environments.

There are no autonomous release waivers. A missing mandatory criterion is BLOCKED. Security, soak and exact-artifact disposable-HAOS gates must pass before Runtime v1 is declared complete.

## Completion

Work through docs/agent/tasks.json until no ready, active or blocked task remains. For each behavior change add focused tests, run bash scripts/agent_verify.sh and use --images for packaging changes. Compilation alone is not acceptance. Record exact evidence under docs/agent/evidence and update STATE/HANDOFF after each task.
