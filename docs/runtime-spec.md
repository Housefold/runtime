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
| Optional Go modules | Trusted official separate processes with private inherited IPC under accepted ADR-007/008; implemented internal foundations remain separate from production installation/activation. |
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
- Production wiring and HAOS validation of the accepted module protocol, readiness, timeouts and backpressure (ADR-007/008).
- Measured HAOS resource controls for trusted official modules. Third-party containment requires a new decision; official modules have no per-entity grant engine (ADR-008).
- Production official signing authority/catalog freshness and explicit installation review wiring under ADR-008; offline verification is implemented.
- Production integration and HAOS validation of durable timeline/admission, generation drain and negotiated state handover under ADR-008.
- Persistence, diagnostic retention, household privacy, and remote/VPS access boundaries.
- Measured resource and latency budgets for the target HAOS hardware.

Accepted ADR-007/008/009 authorize the internal implementation slices described below. Remaining environment and cross-repository gates do not authorize production installation, third-party execution, extra control endpoints or real-home actions.

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

## Local process boundary (M03)

Linux `LaunchLocal` launches explicit local artifacts with one CLOEXEC socketpair,
inherited IPC fd 3, empty parent environment and discarded stdout/stderr. The
launcher binds module/version/boot/generation itself. At most 16 children can be
owned at once; cancellation kills the process group and joins within a two-second
caller budget. Linux proc observation is capped at 64 KiB and may kill a child
exceeding its configured observed RSS limit. This is sampled observation, not
kernel memory enforcement or an HAOS sandbox claim. Synthetic test executables
prove credential/descriptor isolation, negotiation, cancel/kill/join and pressure.
Production selection/install/launch is not wired into cmd/runtime.

## Generation routing (M04)

The durable router keeps one selected active generation and one preparing
candidate per module (32 modules/128 retained generation records maximum).
Only a launcher-bound ready-and-accepting acknowledgment permits atomic cutover.
Admission and cutover share a mutex; execution tokens persist their assigned
generation. Durable dispatch claims each running token once before sending.
Transport failure after a claim may interrupt work, and recovery does not
blindly reexecute it. Preparation failure leaves the old selection intact;
post-cutover failure never selects an older generation automatically.

Old admitted work may drain for five seconds, then becomes interrupted and its
owned child is stopped/joined. Callers drive Drain with explicit time. On Runtime
restart no generation is ready until a new boot-bound handshake; stale sessions
cannot issue work/actions. Post-rename persistence uncertainty fences admission.
This is an internal tested coordinator, not production module enablement.

## Generation state and handover (M05)

Generation stores use module-hashed paths and independent generation directories,
private atomic state files, 64 safe keys, 1 MiB per value and 8 MiB aggregate.
Scoped store handles never expose another generation's write target. This protects
Runtime's ownership paths; trusted processes are not hostile filesystem sandboxes.
Module schemas remain opaque.

Negotiated handover sends at most 512 KiB with source version/schema metadata.
Warm transfer may occur while v1 serves work. Compatible final transfer freezes
the source, adopts the consistent final state and cuts over under the admission
barrier, then resumes/releases the source. The final context is capped at 250 ms;
IPC peers honor that deadline. Filesystem syscall stalls still need hardware
measurement. Incompatible state requires an explicitly permitted clean start;
required-state refusal or final adoption failure keeps v1 active. Protocol support
alone never establishes version/state compatibility.

## Retention and recovery (M06)

Selections retain current and immediately previous successful artifact/state
references. Obsolete retired generations become collectable after another
successful activation; selected/previous/draining references stay protected.
Manual rollback creates a new prepared epoch and copies retained state into a new
store, requiring normal readiness/cutover. It never revives a retired process.

A selected-version crash interrupts running work, retains pending work and fences
readiness. Up to three automatic same-version restart attempts use one/two/four
second backoff. Failed restart candidates also consume the finite budget. Pending
work can rebind to a prepared replacement before its cutover, with durable IDs
unchanged. Exhaustion persists terminal QUARANTINED; no automatic rollback/restart
or new admission occurs. Explicit OperatorRecover resets the selected version's
budget while still requiring a new epoch and readiness. Other modules continue.
Callers drive restart attempts with explicit time; no production recovery loop or
artifact deletion is enabled by these libraries.

## Internal action gateway (A01)

Canonical `action_request`/`action_result` messages bind requests to server-owned
session identity and optional execution work ID. Cutover serializes admission:
active generation may admit work; draining generations require their previously
admitted token. The gateway reserves durable `unknown` metadata before invoking
an injected transport. It distinguishes not_sent, accepted, rejected_by_ha and
unknown; neither disconnect nor restart retries uncertain submissions. Request
IDs deduplicate across generations/restarts and reject changed request digests.

Requests cap data at 64 KiB, in-flight calls at 16, retained records at 1,024,
with five-second context deadlines; transports must honor contexts. At retained
capacity new requests fail not_sent; records are not silently evicted. Persisted
metadata contains hashes/IDs/outcomes, not HA request payloads. A failed response
commit leaves uncertainty. Observation matches/not-observed annotate evidence
without rewriting submission outcomes or asserting causality. Tests use fake HA
only; cmd/runtime has no production action wiring.

## Offline official package verification (P01)

Source-neutral schema-1 manifests declare identity/version/architecture,
Runtime/protocol major and minimum minor, artifact digest and capabilities.
A configured official Ed25519 authority verifies signed bounded catalog,
manifest and domain-separated artifact statement. Catalog sequence floor,
expiry, exact expected identity/version/architecture and compatibility are
checked offline. Metadata is capped at 64 KiB, catalogs at 128 entries, artifacts
at 16 MiB and declarations at 32 entries. Review data reports provenance and
compatibility; declarations are disclosures, never per-entity grants. No arbitrary
repository, unsigned path, downloader, installer or unattended activation exists.
Production signing keys/catalog freshness persistence require later wiring.

## Canonical discovery and Go binding prototype (G03)

Discovery schema 1 represents HA entities, services, devices/areas and capability
visibility. Stable HA registry keys remain provider-qualified; entity-ID-only
bindings are explicitly weak. Missing, unsupported and permission-redacted differ
from available empty results. Normalization deep-copies and strips display control
characters, rejecting malformed/duplicate identity and bounded-data violations.
Snapshots cap canonical JSON at 1 MiB, entities/devices/areas at 512 each,
services at 256 and supported fields at 32 per object.

The internal generator emits deterministic standalone Go references and typed
attribute/request fields only from supplied schemas. Unknown field types use
`any`; GenericEntity/GenericService preserve unknown-domain/service access.
A private durable binding manifest retains up to 1,024 established symbols across
provider entity/display renames; changing a Go symbol requires explicit Rename.
Generated private-home files remain local; only synthetic tests are committed.
This is a tooling prototype, not a frozen public SDK or native discovery collector.

## Optional Bridge negotiation (B01)

`internal/bridge` probes a separate injected authenticated Core-style transport.
[Shared protocol fixtures](agent/BRIDGE-PROTOCOL.md) define bounded major/minor,
capability and required-semantic negotiation. Status distinguishes absent,
incompatible, available and temporarily unavailable. Unknown/denied/malformed/
timeout responses leave native HA ingestion and process health untouched. Probe
backoff is finite and caller-driven; there is no install, broadened credential,
Core restart, extra listener or Bridge state selection. Synthetic schema fixtures
are not Python compatibility or HAOS evidence.

## Bridge discovery normalization (B02)

Discovery v1 fetches at most 16 contiguous chunks/1 MiB with a two-second total
context deadline and negotiated per-frame cap. Epoch/index/cursor/status
continuity is mandatory. Only a complete normalized candidate replaces retained
discovery; failure marks its discovery freshness false without touching native
HA state freshness or Runtime health. Per-collection missing/unsupported/redacted
status, stable/weak IDs, device/area relationships and open service schemas survive
canonicalization. Module projection uses only `discovery_reset` canonical data;
large single IPC projections fail explicitly. Wire/candidate/retained copies are
bounded separately; serialized limits are not heap/RSS guarantees. Fixtures are
synthetic and versioned; Python server/HAOS compatibility remains unverified.

## Final solo review

All 13 accepted implementation tasks M01/M02/T01/T02/M03–M06/A01/P01/G03/B01/B02
are recorded done with developer Linux evidence. Supplementary regression review
fences execution authority after uncertain commits, excludes inherited descriptors
that lack CLOEXEC, rejects symlink state paths, tests raw JSON roundtrips and interrupted partial writes
and preserves child ownership until successful joins. No live generation becomes
collectable before joining. Failed joins remain retriable while the active module
selection remains independent. Bridge negotiation loss marks only retained
discovery stale. See [final review evidence](agent/evidence/FINAL-REVIEW.md).

The cmd/runtime foundation still operates without modules or Bridge. New libraries
are exercised through synthetic fixtures/injected boundaries; this queue does not
claim a production installer, restart loop, real HA action adapter, native discovery
collector, cron expression parser or complete public SDK. HAOS/appliance gates
R07/V02 validation and Python production compatibility remain separate evidence.
The resumed harness records image checks and stakeholder-waived criteria explicitly.


## Atomic source ownership (B03)

The stakeholder authorized proceeding past implementation blockers on 2026-10-01.
Private `ha.Sources` owns a canonical externally managed StateSession; separate
native and Bridge sessions prepare bounded candidates, with one selected local
writer token. Complete reset, fencing, revision accounting, stale retention and
fresh native fallback follow ADR-009. `bridge.PrepareOrdered` consumes the versioned
[ordered-state contract](agent/BRIDGE-PROTOCOL.md). Failure during preparation leaves
the existing source healthy. Selected sequence/epoch/liveness failure requires a
new complete native reconciliation. Later reentry requires another complete Bridge
candidate. Source changes produce generation resets; streams are never merged.

Production cmd/runtime remains on native HA. This implementation does not assert
Python barrier correctness, automatically install Bridge or activate an unproven
server. Version-pinned cross-repository and HAOS integration evidence is required
before production activation. Synthetic library tests prove local ownership and
failure behavior under the stated injected transport/resync contracts.
