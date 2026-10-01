# ADR-007: Managed module protocol, canonical HA model and action outcomes

- **Status:** Accepted
- **Date:** 2026-10-01
- **Owners:** Housefold maintainers
- **Supersedes:** Proposed ADR-007 dated 2026-09-30
- **Implementation gate:** ADR-008 also applies

## Context

Runtime is the sole Housefold boundary to Home Assistant. Initial modules are official first-party components installed and launched by Runtime, not arbitrary local clients. Modules do not care whether Runtime currently reaches HA through WebSocket, REST where appropriate, or Bridge.

Installation is the trust decision for official modules. A manifest declares intended access so the user can review it before installation. Runtime does not enforce per-entity, per-attribute or per-service grants against an installed official module. Modules never receive HA/Supervisor credentials and never talk directly to HA.

## Private Runtime-owned IPC

Runtime creates a private inherited full-duplex socketpair, or equivalent private descriptor pair, for each module generation. There is no shared discoverable module socket or general local client API initially.

A session is bound internally to module identity/version, Runtime boot epoch, generation and lifecycle state. Unrelated descriptors and credentials are never inherited. The protocol is versioned and bounded; length-prefixed JSON is an acceptable initial representation. Exact limits require measurement.

## Manifest declarations

Official manifests declare intended capabilities such as HA observation, discovery, HA actions, Runtime scheduling, persistent state/handover and outbound networking. The UI shows these before install/update.

Declarations are disclosure and diagnostics metadata, not fine-grained authorization. Runtime may compare declared and observed use for diagnostics.

Official modules may make declared outbound non-HA network connections. Runtime is not a generic network proxy. External secret handling is separate.

## Canonical Home Assistant model

Runtime exposes a cleaned-up canonical HA model while retaining HA concepts. Transport details never escape Runtime.

The generic model represents unknown domains, entities, attributes, services and payloads without requiring a Runtime release. Known concepts use structured domain/service/state representations rather than unnecessary magic strings. Bridge may enrich this model but cannot create a separate module-facing model.

## Generated strong Go bindings

Developer tooling generates strong Go bindings from the connected home's discovery data, especially for the Automations module.

Typing is assistance, not fiction. Generate strong types only where discovery supports them, weaker/open representations where HA schemas are lossy, and always retain generic entity/action escape hatches. Generation works without Bridge; Bridge may improve quality.

Generated bindings use stable provider/registry identity underneath friendly Go symbols wherever HA supplies a defensible stable identity. Entity/display renames therefore do not break an existing automation binding. A binding identity manifest preserves an established Go symbol unless the developer explicitly accepts a source rename.

Where HA provides no defensible stable identity, tooling marks the binding weak rather than inventing stability.

Automations is one first-party platform module containing many automation workloads. Individual automations are not Runtime modules.

## State streams

Modules may request canonical snapshots, reads and ordered streams. Preserve the existing guarantees: complete reset before deltas, explicit freshness, generation reset on source change, contiguous ordering, bounded queues, explicit overflow/disconnect and no fabricated durable HA event history.

Official modules receive the canonical view without per-entity grant filtering. Bounds still protect Runtime reliability.

## Action gateway and outcomes

Runtime is the only HA action gateway. Only the active generation may admit new work. A draining generation may finish work admitted before cutover under ADR-008 but may admit nothing new.

Every action distinguishes:

| Outcome | Meaning |
| --- | --- |
| not_sent | Runtime can prove the request was not submitted to HA. |
| accepted | HA returned success; this is not proof of physical completion. |
| rejected_by_ha | HA explicitly rejected the request. |
| unknown | Submission may have occurred but Runtime cannot determine the result. |
| observed_matches / not_observed | Optional observation matched/did not match a predicate; evidence, not proof of causality. |

Runtime never blindly retries an unknown action. Restart preserves uncertainty for actions that may have been sent. Request IDs, deadlines, bounded in-flight accounting and generation epochs are safety mechanisms, not permissions.

## Compatibility

The first exchange negotiates protocol major/minor and optional capabilities. Incompatible major or required capability rejects a candidate before readiness. Minor additions are optional and bounded. Module state compatibility is separate and defined by ADR-008.

## Consequences

Runtime does not need the earlier fine-grained grant engine for official modules. The official catalog/lifecycle trust model in ADR-008 becomes security-critical.

Opening Housefold to third-party repositories changes the threat model and requires a new authorization/containment review.

## Initial implementation slices

1. Versioned bounded inherited IPC with synthetic peers.
2. Canonical generic HA state/reset/delta protocol.
3. Generation identity and active/draining action fencing.
4. Explicit action outcomes using fake HA transport, including unknown and restart recovery.
5. Discovery plus generated Go binding prototype with stable-reference manifest and generic fallback.
6. HAOS validation of the process/IPC model before production modules.

Remote clients, third-party repositories, generic secret brokerage and unrestricted external control surfaces remain outside this ADR.
