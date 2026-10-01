# Current authority

PRODUCTIZATION.md is accepted stakeholder direction. ADR-007/008/009 authorize module/action/trust/Bridge behavior; ADR-010 defines production composition and explicitly supersedes foundation-only restrictions. Ordinary engineering and disposable infrastructure decisions require no routine approval. No v1 release gate can be self-waived.

The following records earlier harness history and must not be used to block authorized v1 implementation or claim v1 release acceptance.

# Accepted decision reconciliation

ADR-007, ADR-008 and ADR-009 were explicitly accepted on 2026-10-01. Their accepted forms and the current task queue supersede the historical proposal-only guidance below. The accepted implementation slices, including resumed B03, are complete. V02 has actual synthetic HAOS execution; R07 has an explicit stakeholder waiver with failed/unexecuted criteria recorded separately.

## Historical proposal harness

# Decisions the solo agent must make reviewable

Existing AGENTS.md says: “Do not invent public APIs, module protocols, permissions, credentials, update trust, or remote-access behavior. If the task depends on an unresolved decision, stop and write the question and options in a proposed ADR.” It also blocks module installation/trust/permissions/updates until their security design is approved.

This harness therefore assigns proposal work and continues independent implementation. It does not ask for approval now or pretend that approval has occurred.

| Decision | Concrete proposal deliverable | Implementation unlocked after acceptance |
| --- | --- | --- |
| Consumer/action contract | Versioned schemas, grants, state freshness/positions, deadlines, action accepted/observed/unknown semantics, compatibility tests | Scoped external consumer API, discovery and typed bindings, action gateway |
| Module containment and trust | Threat model, feasible HAOS enforcement, token isolation, resource controls, manifest/signatures/approval | Safe local launch, quarantine, installation |
| Activation and recovery | Single-active fencing, drain, timer ownership, failed candidate rollback, ambiguous action recovery | Module replacement without overlapping automation versions |
| Bridge interoperability | Cross-container authenticated transport, negotiation, source switching and standard-API fallback | Runtime Bridge adapter coordinated with Python integration |

Keep these proposed until explicit acceptance is referenced. Break accepted epics into small tasks before execution. Do not automatically treat a completed proposal task as an accepted decision.
