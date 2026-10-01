# ADR-009: Bridge-preferred Home Assistant capability adapter

- **Status:** Accepted
- **Date:** 2026-10-01
- **Owners:** Housefold maintainers
- **Supersedes:** Proposed ADR-009 dated 2026-09-30

## Context

Runtime must boot and operate through standard Home Assistant APIs without Bridge. Bridge is a separate thin Python integration inside HA Core whose purpose is to expose richer data and access to HA capabilities that standard WebSocket/REST APIs do not expose adequately.

Bridge does not own automations, scheduling, module lifecycle, Runtime recovery or a competing Housefold model. Modules never communicate with Bridge directly. Runtime normalizes Bridge/native data into ADR-007's canonical HA model.

## Bridge preferred, never required

Runtime prefers compatible healthy Bridge capabilities where they provide the richer or stronger implementation. Native HA WebSocket remains the baseline fallback, with REST used where appropriate for supported cases.

Bridge is preferred once installed, compatible and proven ready. Runtime remains fully usable without it. Bridge failure must not take Runtime down.

## Thin extensible capability adapter

Bridge is deliberately thin but extensible. It exposes HA-Core capabilities Runtime cannot obtain adequately from normal external APIs.

Capabilities are negotiated individually and versioned, for example richer registry/device/area data, service/schema discovery, ordered state/events and future Core facilities. Capability absence means unsupported, not an empty successful result.

Adding capabilities does not move Housefold business logic into Python. Runtime remains responsible for normalization, source ownership, freshness, module projection and lifecycle.

## Transport

Preferred initial topology is custom versioned commands on an authenticated HA Core WebSocket reached through Runtime's existing Supervisor Core proxy.

This avoids another listener, certificate system or assumed shared Unix filesystem between Core and the Runtime app container.

Bridge negotiation uses a separate private Core WebSocket/session from the baseline native state session so optional capability failure cannot corrupt the fallback path.

A separate Bridge listener/callback topology requires another ADR.

## Installation UX

Runtime never silently installs Bridge and does not require installation authority.

When Bridge is absent, Housefold may present an optional setup guide explaining concrete benefits such as richer discovery/data and access to HA facilities unavailable through standard APIs. The user performs the HA installation/configuration.

The UI must not repeatedly nag a user who declines. Runtime detects later installation and can negotiate it then.

Runtime distinguishes absent, incompatible, available and temporarily unavailable Bridge states.

## Capability negotiation

Bridge hello negotiates protocol major/minor, Bridge/Core version metadata, supported capabilities and finite limits.

Incompatible major, unknown required semantics, denied command or timeout means the capability is unavailable. Runtime does not broaden credentials, restart Core or install software in response.

Minor additions are optional and bounded. Identity, permission or sequencing changes require an explicit version/capability change. Exact Core/Bridge compatibility is tested in both repositories.

## Discovery and typing

Bridge should expose richer registry/device/area relationships, service/schema information and stable provider identities where HA actually has them.

This enrichment feeds Runtime's canonical discovery model and ADR-007's generated Go bindings. It preserves unsupported, missing and permission-redacted distinctions and never invents stable identity where HA has none.

Bridge improves typing quality but is not required for typed tooling to function.

## State and event source switching

Runtime has one authoritative canonical state source at a time. Native and Bridge streams are never merged using timestamps or last-event-wins rules.

Runtime may prefer a proven Bridge ordered-state capability only after it demonstrates a complete snapshot barrier, bounded buffering and contiguous sequencing.

Switching sources is atomic:

1. the current source remains authoritative while the candidate synchronizes;
2. the candidate produces a complete valid generation;
3. Runtime fences the old source writer and publishes the candidate as a new generation at revision zero;
4. late old-source events are rejected.

If candidate preparation fails, no switch occurred.

If selected Bridge state later fails, Runtime marks retained canonical state stale, fences Bridge writes and synchronizes a complete native HA candidate. Successful fallback publishes a new generation/reset.

This is transport fallback, not module-version rollback. ADR-008's prohibition on automatic module rollback is unaffected.

Enrichment-only Bridge capability failure does not make native HA state stale.

## Heartbeat and recovery

Bridge heartbeat/liveness may detect loss but cannot by itself prove state freshness. Epoch/sequence discontinuity in a selected ordered capability makes that source stale.

Retry is bounded with backoff/jitter and must not create Runtime/Core restart loops. Exact timers require HAOS evidence.

After native fallback Runtime may later prepare Bridge again and switch only after it is fully compatible and synchronized.

## Security boundary

Runtime authenticates to HA Core through the existing local HAOS trust boundary. Supervisor credentials are never sent to modules, Bridge-specific remote endpoints or logs.

Bridge custom commands enforce the permissions/visibility of the authenticated HA identity. Runtime owns canonicalization and downstream module delivery.

No remote plaintext topology, arbitrary peer claiming to be Bridge or new external control surface is accepted here.

## Separate repository responsibilities

The Python Bridge repository implements custom command/capability behavior and version-pinned Core tests.

Runtime implements negotiation, normalization, capability health, source preparation/fencing and native fallback.

Both repositories share protocol fixtures and compatibility expectations. Runtime does not fetch or install the Python integration.

## Initial implementation slices

1. Define shared version/capability fixtures and hello negotiation.
2. Implement Runtime optional Bridge detection/negotiation while native HA remains untouched.
3. Implement richer discovery/service capabilities and canonical normalization.
4. Validate Bridge installation guidance/status without installation authority or nagging.
5. Implement ordered-state capability only after the Python side proves barrier/sequence semantics.
6. Add atomic Bridge-preferred source switching and native fallback.
7. Run cross-repository compatibility and HAOS fault tests.

Bridge remains optional for boot and recovery even though it is preferred when available.
