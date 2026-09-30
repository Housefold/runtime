# Remaining gates

[AGENTS.md](../../AGENTS.md) requires: “Keep proposals proposed until explicit
maintainer acceptance.” [DECISIONS.md](DECISIONS.md) adds: “Do not automatically
treat a completed proposal task as an accepted decision.” The ledger's original
approval_required fields are unchanged; the instruction to carry out the task
queue authorizes the concrete proposal work, not acceptance of unwritten designs.

- G01 needs explicit maintainer acceptance of [ADR-007](../adr/007-public-consumer-and-action-contracts.md)
  and [ADR-008](../adr/008-module-containment-trust-and-activation.md), recorded
  references and executable task decomposition. Required HAOS containment
  feasibility remains unverified; no same-UID fallback or implied privileges.
- G02 needs explicit maintainer acceptance of [ADR-009](../adr/009-optional-bridge-compatibility-and-fallback.md),
  a recorded reference and executable tasks coordinated with the separately owned
  Python integration. Bridge must stay optional.
- R07 needs an authorized disposable HAOS/appliance test target and local access;
  see [availability evidence](evidence/R07.md) and [runbook](HAOS-VALIDATION.md).

All other ready authorized tasks are complete. No approval request is needed for
work already completed; these are concrete reviewable results for future decisions.
