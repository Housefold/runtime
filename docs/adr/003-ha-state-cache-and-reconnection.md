# ADR-003: HA state ingestion and reconciliation

- **Status:** Accepted
- **Date:** 2026-09-27
- **Owners:** Housefold maintainers

> Runtime v1 extension: [ADR-010](010-runtime-v1-product-composition.md) and [PRODUCTIZATION.md](../agent/PRODUCTIZATION.md) supersede the foundation-only restrictions where they authorize production modules, fenced actions, discovery and admin-only BIOS. The original decision and evidence remain historical; no historical waiver satisfies a v1 release gate.

## Context

Runtime needs an in-memory Home Assistant state view that can recover after a WebSocket disconnect without exposing partially synchronized data. The Core API proxy credential authorizes more than state reads, so this decision narrows Runtime behavior; it does not narrow the credential itself. Runtime has no approved persistence, module data contract, or HA action authority.

Home Assistant documents `subscribe_events` and `get_states` as separate WebSocket commands. The public API does not specify an atomic snapshot/event barrier, replay sequence number, gap detection, or unique and monotonic state timestamps. Runtime therefore establishes its synchronization boundary using one continuous WebSocket session, an acknowledged `state_changed` subscription, the `get_states` response on that same session, and a bounded buffer of events received while the snapshot is being built. HA timestamps resolve overlap only when ordering is unambiguous; they are not the synchronization mechanism.

## Decision

- Runtime may read all state objects visible to its HA Core API identity through WebSocket `get_states` and `state_changed`. This is an explicit broad household-data scope. Keep current state and attributes in memory only. Do not persist it, log it, expose entity content in health/status, or send it to a child process. Discard prior values and event history; do not retain actor, user, or context identifiers.
- Store the HA state value, attributes, `last_changed`, and `last_updated` per entity. Do not add `last_reported` or `state_reported` without a concrete requirement. Do not call HA services.
- Drive synchronization through `Disconnected → Connecting → Authenticating → Subscribing → Syncing → Ready`. Begin reading and bounding subscription events as soon as the subscription request is sent, including any event frame that races ahead of the acknowledgement; send `get_states` only after the `state_changed` subscription is acknowledged.
- Parse the full snapshot into a new candidate map. Reconcile buffered entity additions, state changes, attribute-only changes, and removals (`new_state == null`) against that candidate. Track state `last_updated` and event `time_fired` separately to reject clearly older updates and removals. Missing, equal, malformed, or otherwise ambiguous ordering fails the candidate closed; a removal cannot be ordered from a missing timestamp.
- Keep one ordered WebSocket reader. Under a synchronization gate, drain and reconcile every buffered event received before the live-stream handoff, then atomically publish the candidate. Events after handoff update the published generation in stream order. Consumers see either the prior published generation or the complete candidate, never an intermediate map.
- Increment a local monotonic `generation` only after a candidate is successfully published. On WebSocket continuity loss, mark the published generation stale immediately and retain it for diagnostics/consumers that honor freshness. Never repair a stale generation in place; reconnect and build a new candidate.
- Malformed frames or states, ambiguous ordering, snapshot or synchronization timeout, snapshot limit, event-buffer overflow, failed reconciliation, or loss of continuity discard the candidate and leave state stale. Retry using the bounded backoff in ADR-004. A detected connection failure never makes the Runtime process unhealthy.
- Keep Runtime process health separate from HA/cache readiness. The WebSocket session is authoritative for operational HA connectivity and state freshness. REST is not a competing readiness signal; it may be used only for startup/diagnostic checks that do not override WebSocket status.
- The authenticated read-only ingress page may show only coarse metadata: HA connection phase, cache freshness (`none`, `synchronizing`, `fresh`, `stale`), generation, entity count, and last successful sync. It must never show entity IDs, values, attributes, or event data.
- Define the 64 MiB cache limit as the sum of canonical serialized normalized entity payload bytes in one candidate or published generation. It is not a measurement or guarantee of Go heap usage. During a rebuild, a stale published generation and candidate may each approach the limit; transient copies, map overhead, and buffers add memory. Bound the event buffer separately to 8 MiB of received event JSON bytes and 4,096 events, whichever is reached first. Retain at most 4,096 deletion tombstones and 8 MiB of canonical tombstone payload; overflow fails closed and forces a fresh synchronization.

### Freshness guarantee

When Runtime reports a generation as fresh, it is the complete valid `get_states` response received on that WebSocket session, reconciled with all buffered `state_changed` events observed before the atomic handoff, with no known continuity loss or unresolved event ordering. The snapshot and reconciled events belong to one uninterrupted WebSocket generation. This is a guarantee about Runtime's observed view, not a promise that every HA entity was visible to the Runtime identity or serializable by Home Assistant, and not a guarantee against an undetected server-side event omission. The public WebSocket contract does not provide sequence numbers or silent-gap detection; supported HAOS behavior and limits must be exercised before making broader claims.

## Consequences

- HA is authoritative. Runtime publishes only a complete snapshot plus its successfully reconciled overlap, while consumers can see stale state retained across an outage.
- A failed synchronization may leave an older generation stale until a later full synchronization succeeds. This is safer than exposing partial state or silently repairing it.
- Full state access collects potentially sensitive attributes for every entity visible to Runtime. It is memory-only and private to the Runtime process, but is broader than a consumer-specific allowlist.
- `homeassistant_api: true` remains broader than these Runtime operations and can authorize HA service calls. Runtime code must not issue those calls; no module receives the token.
- Status metadata describes observed session and cache state, not household contents. Runtime remains healthy when HA is denied, unavailable, or synchronizing.

## Alternatives considered

1. **Wait for an entity allowlist.** Rejected for this increment because the user approved all HA-visible states and the Runtime needs a state ingestion foundation before its first concrete state consumer is selected.
2. **Publish each event directly into the current cache while fetching the snapshot.** Rejected because snapshot replacement could overwrite newer events or expose an incomplete view.
3. **Treat HA timestamps as a snapshot barrier.** Rejected because timestamps do not define ordering between the two commands and may be equal or absent.
4. **Persist state across restarts.** Rejected because no persistence or household-data retention design is approved.

## Verification

Focused tests must cover the state machine, successful and rejected authentication, unavailable HA, subscription acknowledgement, snapshot parsing and overflow, add/update/attribute-only/remove reconciliation, timestamp ordering and ambiguity, atomic publication, event and tombstone limits, timeout, continuity loss, stale generation retention, bounded reconnect, and generation monotonicity. Status and logs must contain no entity data or credentials. Exercise the behavior against a clean supported HAOS/Core installation when available. A generic VM smoke does not establish appliance hardware or silent-gap guarantees.

## Evidence and boundary

- Home Assistant [WebSocket API](https://developers.home-assistant.io/docs/api/websocket/) documents authentication, `subscribe_events`, `get_states`, and ping/pong, but not an atomic snapshot/event barrier or gap-free replay contract.
- Current Home Assistant Core source shows the event listener registered before the subscription result and a separate `get_states` handler. This is implementation evidence only, not a version-pinned API guarantee: [websocket_api/commands.py](https://github.com/home-assistant/core/blob/dev/homeassistant/components/websocket_api/commands.py).
- The HAOS app uses the local Supervisor Core API proxy under [ADR-002](002-ha-api-credentials.md); reconnect timing is bounded under [ADR-004](004-ha-connection-checks.md).
- Module execution, public module contracts, Bridge integration, HA service calls, historical state, and persistence remain out of scope.
