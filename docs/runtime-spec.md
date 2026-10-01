# Runtime specification

**Status:** Runtime and HA foundation with accepted module/action/trust/Bridge decisions in ADR-007/008/009. Implementation evidence is tracked in docs/agent/tasks.json.

This specification narrows the platform docs to the Runtime repository. It records what an implementation agent may rely on and what must be resolved before work crosses a security or compatibility boundary.

## Purpose

Housefold Runtime is the stable, local supervisor and control plane for Housefold on Home Assistant OS. It connects Housefold to HA, manages separate-process Go modules, and keeps the installation inspectable and recoverable when optional components fail.

## Accepted decisions

| Topic | Baseline |
|---|---|
| First deployment form | Supervisor-managed Home Assistant OS app, auto-started with `system` startup ordering, protected mode and AppArmor enabled, no host port or extra host privileges. Supervisor owns process lifecycle and initial local recovery. See [ADR-001](adr/001-haos-runtime-lifecycle.md). |
| HA connection | WebSocket state session through the Supervisor Core API proxy (`homeassistant_api: true`) is authoritative for operational HA status and freshness; REST is diagnostic only. General Supervisor API access stays disabled. The custom HA integration Bridge is optional and cannot be a boot or core-operation dependency. Reconnect uses the bounded schedule in [ADR-004](adr/004-ha-connection-checks.md). |
| Optional Go modules | Separate processes supervised by Runtime. The control protocol and process containment model remain open. |
| Operating principle | Essential, latency-sensitive behavior remains local when the VPS, internet, cloud AI, or an optional module is unavailable. |
| HA ownership | HA remains authoritative for device integrations, raw entity state, and service execution. Runtime keeps a private, memory-only state view and rebuilds it after disconnects. See [ADR-003](adr/003-ha-state-cache-and-reconnection.md). |
| Management | Runtime exposes a read-only status page through authenticated HA ingress; Supervisor app controls/logs and the HAOS host console provide recovery. The full PWA is optional. See [ADR-005](adr/005-local-operator-status.md). |

## First implementation boundary

The first implementation should establish the smallest dependable Runtime foundation:

- Start on the HAOS add-on target and report its own health.
- Connect to HA through standard APIs and show connection/permission status with bounded reconnect behavior.
- Provide a local management/recovery path that does not require the VPS or PWA.
- Establish the Runtime's internal boundaries so later module supervision does not couple feature logic to HA internals.

The HA state view is an internal Runtime facility. It is not a public module API, Bridge endpoint, or network control endpoint. Runtime may collect all states visible to its HA identity under the approved memory-only boundary in [ADR-003](adr/003-ha-state-cache-and-reconnection.md).

Implementations may use small internal packages, but must not prematurely freeze a public module API or add feature modules just to demonstrate extensibility.

## Implemented foundation

- The HAOS app starts the foreground Go process, exposes a read-only `/healthz` watchdog check, and handles bounded termination without publishing a host port.
- Runtime opens a long-lived WebSocket through the Supervisor Core API proxy. The synchronization state machine is `Disconnected → Connecting → Authenticating → Subscribing → Syncing → Ready`. Runtime bounds and buffers `state_changed` frames as soon as it requests the subscription; only after acknowledgement does it request `get_states`, reconcile the candidate, and publish it atomically. HA WebSocket health controls operational connection and freshness; REST diagnostics cannot override it. See [ADR-003](adr/003-ha-state-cache-and-reconnection.md) and [ADR-004](adr/004-ha-connection-checks.md).
- Runtime health is independent of HA/state readiness. `/healthz` remains healthy while HA is denied or unavailable; loss of WebSocket continuity immediately marks the published state generation stale and retains it until a new candidate is synchronized. Bounded retry and shutdown behavior remain local and do not require HA.
- The in-memory view stores current state, attributes, `last_changed`, and `last_updated`; no history, context identity, persistence, or service calls are included. Each successful synchronization increments a local generation. Canonical normalized payload accounting is capped at 64 MiB per generation; this is not a Go heap usage guarantee. The separately bounded event buffer is limited to 8 MiB of received event JSON or 4,096 events, whichever comes first. Deletion watermarks are capped at 4,096 tombstones and 8 MiB of canonical payload; overflow makes the session stale and forces resynchronization.
- HA ingress serves a read-only local status page to authenticated HA users. Only `GET /` from ingress peer `172.30.32.2` is accepted; unknown paths and other methods are rejected. The page shows process health and coarse HA/cache metadata only (phase, freshness, generation, entity count, and last successful sync), never entity contents. The host port remains unpublished; `/healthz` remains available to Supervisor. See [ADR-005](adr/005-local-operator-status.md).
- A clean HAOS 18.3 generic AArch64 VM test stopped and restarted Core while Runtime stayed healthy; its connection status moved from connected to unavailable and back without an app restart. This does not verify the new state-sync path. See ADR-004 for the VM verification limits.
- The optional Bridge, VPS, internet, and cloud are not startup dependencies.

## Acceptance outcomes

The HAOS Runtime foundation should demonstrate that:

1. It starts on a clean HAOS installation without the custom Bridge or optional feature modules.
2. It connects with the approved Core API proxy, builds a candidate state generation, and reports readiness only after reconciliation and atomic publication.
3. HA unavailability leaves process health available, keeps the last generation stale, and retries with bounded behavior.
4. VPS, internet, cloud, and optional PWA outages do not prevent local Runtime management.

Module failure isolation, last-healthy-version preservation, and single-active-automation guarantees belong to later module-supervision and activation designs.

Latency, memory, disk, reconnect, and recovery thresholds must be measured and added here before claiming target-hardware readiness.

## Decisions still requiring an ADR or explicit review

- Appliance-hardware behavior, LAN discovery and USB-radio passthrough; the clean HAOS 18.3 generic AArch64 VM smoke verified internal watchdog reachability, app recovery, and auto-start, but not appliance hardware.
- HA WebSocket guarantees beyond the observed snapshot-plus-buffer reconciliation, event-gap detection, supported-version behavior, and appliance performance remain verification limits in [ADR-003](adr/003-ha-state-cache-and-reconnection.md). No HA action permission is included.
- HA ingress user experience and source filtering on appliance hardware; see [ADR-005](adr/005-local-operator-status.md). The accepted page is not a recovery surface when HA Core UI is unavailable; use the HAOS host console.
- Measured HA REST/WebSocket rate limits, state reconciliation, event ordering, and stale-state behavior; the accepted cache/data boundary is recorded in [ADR-003](adr/003-ha-state-cache-and-reconnection.md).
- Runtime-to-module protocol, compatibility negotiation, health/readiness, timeouts, and backpressure.
- Module process containment, resource limits, requested capabilities, and least-privilege enforcement.
- Module package format, provenance/signatures, approved sources, installation approval, staged activation, rollback, and update policy.
- Automation execution leases, timers/state ownership, and draining in-flight work during activation.
- Persistence, diagnostic retention, household privacy, and remote/VPS access boundaries.
- Measured resource and latency budgets for the target HAOS hardware.

Until these are decided, agents may implement bounded local foundations that do not depend on them. They must stop before adding public contracts, executing modules, exposing control endpoints beyond loopback, or automating installation/update of module code.

## Non-goals for the foundation

- Replacing Home Assistant's integration and device ecosystem.
- Requiring the Bridge, VPS, internet, LLM, or full PWA for basic local operation.
- Defining a general module marketplace or allowing unreviewed code execution.
- Shipping every Housefold capability inside the Runtime repository.
- Treating event delivery as durable or exactly-once before those guarantees are specified.

## Private consumer foundation

Private in-process reads and subscriptions are implemented under harness R02;
see [ADR-006](adr/006-private-state-consumers.md). Reads carry atomic generation,
revision and freshness. Independent ordered streams begin with an authoritative
complete reset, then deliver visible deltas, freshness loss and generation resets.
Consumers own deep copies. Four subscribers reserve at most 32 MiB of canonical
queued payload, each capped at 256 events/8 MiB, including initial/reset views.
Oversized initial views fail explicitly; slow-consumer overflow, cancellation and
source shutdown are distinct terminal outcomes. This adds no module interface,
network surface, persistence or HA action authority.

## Measured developer-host limits and shutdown

[R04 measurements](agent/performance.md) cover synthetic reads/copies/deltas,
contention, heap amplification and cancellation; they are provisional worker
results, not appliance readiness. The whole process now shares one nine-second
shutdown deadline across HTTP shutdown and session/notification joins, inside
the ten-second Supervisor grant. HTTP retains its concurrent eight-second limit.
On deadline expiry Runtime exits nonzero so remaining goroutines cannot keep the
process alive. Decode/replay checks cancellation between bounded operations and
the ping worker is joined. Individual JSON decode/serialization and copies are
still synchronous; scheduler and target-hardware behavior must be measured.

## Diagnostic ownership

R05 traced all one-shot Probe and Monitor callers: only their own obsolete tests
used them; main has used StateSession since state ingestion. Their implementation
and dedicated tests are removed. Shared status, retry constants and cancellation
wait remain in internal/ha/lifecycle.go. There is currently no REST diagnostic
entrypoint. The accepted permission to add an optional read-only diagnostic does
not give REST authority over WebSocket readiness or freshness. Historical VM
probe evidence continues to describe the old probe, not current ingestion.

## Accepted module and Bridge boundaries

ADR-007, ADR-008 and ADR-009 were accepted on 2026-10-01 and supersede older
proposal language above. Official modules use private inherited IPC and receive
canonical HA data without per-entity grants; Runtime remains the sole HA boundary.
Only one generation admits new work; old admitted work may drain. Unknown actions
are never blindly retried. There is no automatic module rollback. Bridge remains
optional and selected ordered state awaits Python barrier/sequence evidence.

## Private module protocol (M01)

`internal/module` uses a four-byte big-endian length followed by JSON. Protocol
major 1 negotiates optional minor/capabilities and rejects missing required
capabilities. Frames are at most 1 MiB, depth 32, with 64-byte type and 128-byte
request ID bounds; hello lists each contain at most 32 entries of 64 bytes.
There is one synchronous read and write per session, no transport queue, a
five-second IO deadline and context cancellation. Any partial write/read failure
closes the connection; reconnect needs a new session. Launcher-owned identity,
version, boot and generation cannot be overridden by peer payloads. This slice
has no process launch, production wiring, action authority or network listener.
These provisional developer bounds require HAOS measurement.

## Canonical module state (M02)

State IPC uses structured entity identity/state/timestamps and open JSON
attributes from `internal/state`. A reset is `reset_begin`, sorted
`reset_entity` frames, then matching `reset_end`; only the end publishes the
candidate. Staging/current canonical payload is bounded by 8 MiB and 10,000
entities, each frame by M01. Larger views/entities fail explicitly. Ordered
`state_event` deltas preserve generation/revision/freshness, with no grant
filtering. Replica rejects gaps, duplicate reset IDs and deltas before complete
reset. Disconnect marks retained data stale and requires a new reset. Existing
HA subscription bounds protect ingestion; transport writes never run under its
cache lock. Slow consumers terminate visibly. No HA event history is persisted.

## Durable timeline (T01)

`internal/durable` writes versioned SHA-256 checked JSON via private same-directory
temporary file, file fsync, atomic rename and directory fsync. Writes after an
uncertain post-rename failure are fenced until reopening. Corrupt/truncated or
oversized files fail closed. One Runtime owner controls each path; this is not
a multiprocess database. File payloads are capped at 16 MiB, mode 0600.

`internal/timeline` persists up to 128 UTC anchored interval/one-shot schedules,
4,096 pending occurrences, global watermark and recovery horizon. Default replay
is one hour, per-schedule overrides have a seven-day maximum. Occurrence IDs are
deterministic from schedule identity and original logical time. Replay marks
recovered occurrences and never reconstructs unobserved HA events or stores HA
state. An overflow or failed durable save preserves the old published watermark;
operators must drain pending work before advancing. Time is supplied explicitly
for deterministic tests. No scheduler is enabled in production by this library.

## Execution modes (T02)

All trigger sources feed durable `execution.Manager.Admit` with an occurrence ID
and logical time, including cron adapters and delayed timeline occurrences.
Timeline acknowledgment follows durable admission; retry after a crash returns
the same record. Cron expression parsing belongs to trigger adapters.
Single drops overlapping triggers; queued runs one with a FIFO queue; parallel
runs up to declared concurrency; restart cancels pending work and requests
cancellation of running work, starting the replacement only after cancellation
acknowledgment. Cancellation is cooperative; a process owner must bound hangs.
Queue maximum is 64, parallel maximum 32, definitions 128, records 4,096. TTL is
at most seven days. Expired pending work never starts. Records use monotonic
sequence ordering. Persistence failure publishes nothing. On restart previously
running/canceling work becomes interrupted (never automatically reexecuted),
while pending IDs remain eligible through ResumePending. Pruning terminal records
advances a durable logical-time floor; older occurrences cannot be readmitted.
