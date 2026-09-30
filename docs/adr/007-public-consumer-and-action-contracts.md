# ADR-007: Public consumer and HA action contracts

- **Status:** Proposed
- **Date:** 2026-09-30
- **Owners:** Housefold maintainers
- **Harness task:** D01
- **Implementation gate:** G01 also requires accepted ADR-008

## Context

ADR-003 permits private memory-only state reads and forbids HA service calls.
ADR-006 implements private consumers, not permission enforcement or a public
module protocol. Separate Go modules need scoped observations and explicit
actor authority; the Supervisor proxy credential is broader than either.
Nothing in this proposal authorizes public endpoints, child execution or actions.

## Recommended protocol and topology

Recommend a local authenticated Unix socket with length-prefixed UTF-8 JSON:
four-byte unsigned network-order length, then one envelope. Maximum frame 1 MiB;
stream reset uses bounded chunks as described below. No socket assumes sharing
across HA/Runtime containers. Runtime/module same-host filesystem and containment
must be demonstrated by ADR-008 before this transport can ship. The socket is
not exposed through ingress, a host port or remote PWA.

Bind a session to a launcher-assigned principal, launch epoch and granted scope.
Use peer credentials plus a one-use per-launch secret delivered via inherited
private descriptor; do not infer authority from claimed actor JSON, module name,
socket possession alone or HA user headers. Secret lifetime and descriptor
isolation depend on accepted containment. A module never sees Supervisor or
Bridge credentials. Remote clients require a separate authentication/remote ADR.

Alternative: inherited dedicated pipe/socketpair avoids listening/discovery and
shares no reusable secret, but complicates reconnect and process recovery.
Alternative: loopback HTTP/JSON or gRPC gives tooling/typed streaming, but adds
listener/authentication complexity (loopback is not an identity boundary) or a
protobuf dependency. JSON socket is recommended for inspectable initial local
compatibility with standard Go; performance must be benchmarked first. No format
is durable event storage.

### Compatibility and lifecycle

First envelope is hello with supported major/minor ranges, required features,
package identity/version and nonce. Runtime chooses one supported major and
highest mutually supported minor; missing mandatory features or incompatible
major rejects before any state/grant is exposed. One session per principal/launch
epoch. Client cannot request a broader grant by listing capabilities.

```json
{"type":"hello","id":"h1","versions":[{"major":1,"min_minor":0,"max_minor":0}],"required_features":["scoped_state","terminal_overflow"],"module":{"id":"synthetic-observer","version":"0.1.0"},"nonce":"launch-bound-nonce"}
```

A successful response identifies negotiated version, boot/session epochs, bounds,
available grant references and connection health only; never credentials:

```json
{"type":"hello_ok","id":"h1","version":{"major":1,"minor":0},"boot_epoch":"opaque-boot","session_epoch":"opaque-session","features":["scoped_state","terminal_overflow"],"limits":{"frame_bytes":1048576,"stream_bytes":8388608,"queue_events":256,"streams":1},"grant_refs":["synthetic-read"]}
```

Minor additions are optional fields/features with bounded validation; ignore
unknown optional fields but reject unknown required features and unsupported
methods/enums. A major changes incompatible meaning. Versioned codecs never
reinterpret an old action or grant as a more powerful new one. Hello deadline
5 s, request admission 2 s, heartbeat 10 s/pong 5 s, idle/request/write deadlines
and bounded decode depth (64) apply. Initial numbers are proposed limits requiring
worker and HAOS evidence. Runtime process health remains distinct from transport,
HA state readiness and module readiness.

## Scoped state, ordering and reconnect

Runtime evaluates the grant for every snapshot/read/stream admission and every
outbound projection, including nested attributes and removals. Recommended grant
has a finite set of source-qualified entity IDs and finite permitted attribute
keys; default excludes all attributes. New entities are not added by a wildcard
silently. Grant replacement/revocation terminates the stream, clears server
queues and fences actions; reconnect renegotiates approved scope. A recipient
cannot be made to forget previously delivered data; containment/trust policy
must account for that. Session grant IDs are references, never bearer authority.

```json
{"type":"state_subscribe","id":"s1","grant_ref":"synthetic-read","scope":{"provider":"ha","entities":["sensor.housefold_test_0"],"fields":["state","last_updated"],"attributes":[]}}
```

Scope must be a subset of grant and implementation-supported fields. State has
source identity, value, permitted attributes and timestamps (UTC RFC3339Nano),
availability if reported by HA, freshness and a scope-local stream position.
Unknown initial cache, entity absent, HA's string state `unknown`/`unavailable`
and stale retained state are distinct. No context/person/activity history or
raw event payload is emitted. Entity rename is not silently relabeled from a
mutable display name; a future registry revision controls stable references.

Registration and projection of the initial complete scoped view occur at the
same publication gate. Public stream epoch is opaque; sequence starts at zero
for reset and increments only for permitted visible deltas/freshness. Raw global
revision is kept private so filtered changes do not leak activity counts. New
source generation, grant change or source switch requires an authoritative reset;
no consumer can compare sequences across stream epochs. Private ADR-006 source
positions remain useful within Runtime and are not blindly exported.

```json
{"type":"state_reset","stream":"s1","epoch":"opaque-stream-1","seq":0,"known":true,"fresh":true,"states":[{"source":{"provider":"ha","entity_id":"sensor.housefold_test_0"},"value":"0","last_updated":"2026-09-30T00:00:00Z"}]}
```

Reset larger than one frame is staged as `reset_begin` (epoch, total entities,
canonical payload bytes), bounded indexed `reset_chunk`, then `reset_end`.
Consumer validates declared bounds/order and publishes only at end; disconnect,
missing/duplicate chunk, timeout or bounds mismatch discards staged reset. Deltas
are never interleaved before reset_end. The full projected reset plus envelope
is capped at 8 MiB; reject admission explicitly if it exceeds the bound rather
than pretend the snapshot is complete. Sender accounts staged initial/reset
bytes, queued frames and deltas together, not separately to multiply budgets.
One stream per module, at most four streams initially (32 MiB reserved total)
plus explicitly capped transport/read buffers. Client staging limits are also
negotiated. A follow-up scope expansion requires explicit grant review.

`state_delta` carries add/update with full permitted entity or remove with source
ID. Freshness loss preserves data and marks it unusable for fresh-dependent
work. Every public envelope uses contiguous seq within epoch; a gap closes the
stream. Slow consumer exceeds 256 events/8 MiB or bounded writer deadline:
terminate only that stream visibly with `overflow` if terminal can be sent within
its own deadline; otherwise disconnect with no resumable guarantee. Closed
transport always means uncertain delivery and fresh resnapshot is mandatory.
Terminal reason available to supervision even if peer cannot receive it. No
blocking writes hold the ingestion gate.

No replay/history/resume cursor is promised. On disconnect/Runtime restart,
clear readiness and staged data; retain previous data only explicitly stale,
renegotiate and obtain a new complete scoped reset. HA fresh retains ADR-003's
observed-continuity guarantee, not silent-gap detection or atomic HA snapshot.

## Actors, grants and actions (requires new accepted authority)

Runtime is the sole action gateway. Recommended grant record is locally
maintainer-approved and ties principal/module identity, grant revision, allowed
HA domain/service, exact targets, parameter schema/ranges, rate/concurrency
limits, freshness policy and activation fencing epoch. Attribute/state access
does not imply service authority. Service discovery does not grant execution.
Runtime validates again immediately before send; revoked grant/lease cancels
unsent work. Raw access_token, arbitrary endpoint, user/context actor claims,
unbounded target templates and arbitrary service names are forbidden.

Concrete proposed grant example (configuration/provenance format is ADR-008):

```json
{"grant_id":"synthetic-switch","principal":"synthetic-controller","revision":1,"actions":[{"domain":"input_boolean","service":"turn_on","targets":["input_boolean.housefold_test"],"parameters":{},"require_fresh":true}],"rate":{"per_minute":6,"burst":1},"max_inflight":1}
```

This is a synthetic review example, not installed permission. High-impact real
home operations must have operation-specific policy/confirmation decisions;
no generic grant escalation or remote action authority is proposed here.

```json
{"type":"action_request","id":"a1","request_id":"opaque-session:42","grant_ref":"synthetic-switch","activation_epoch":"opaque-active-7","deadline_ms":3000,"operation":{"provider":"ha","domain":"input_boolean","service":"turn_on","targets":["input_boolean.housefold_test"],"parameters":{}}}
```

Actor is set server-side from the authenticated principal and active epoch.
Caller IDs/trace parent are untrusted references validated for length/format;
never authorization. No accepted activation epoch means no action permission.
Request deadline is relative to server receipt, capped at 5 s, measured
monotonically; already-expired requests are not sent. State-dependent grants
reject stale/unknown cache. The action still cannot guarantee the device was
reachable or caused a particular resulting state.

Track an explicit lifecycle:

| Outcome | Meaning | Retry/recovery |
| --- | --- | --- |
| rejected / not_sent | Invalid grant/schema/lease, expired before send, unavailable transport before write; Runtime can prove no bytes submitted. | Correct request/policy; a new request only after explicit decision. |
| accepted | HA success response received for this exact request. | Acceptance only; never equate to device completion. |
| rejected_by_ha | HA explicitly rejected this request. | Report coarse reason; do not broaden grant or fall back to another identity. |
| unknown | Write may have occurred but disconnect, timeout, restart or ambiguous response prevents determination. | Never blind retry; inspect permitted state or ask actor to decide. |
| observed_matches / not_observed | Optional, independently granted bounded observation predicate matched/did not match before observation deadline. | Matching state is evidence, not proof of causality or physical completion. |

```json
{"type":"action_result","id":"a1","request_id":"opaque-session:42","submission":"accepted","observation":"not_requested"}
```

Unknown after a send includes original safe request identity, never raw HA error
content. One in-memory bounded dedup window per authenticated session (256
requests/1 MiB metadata, no parameter/state logging, maximum 5 min), evict only
completed records; reject new requests when unknown/inflight records fill budget.
Identical request IDs return the recorded outcome within that session; mismatched
payload under the same ID is rejected. This is not durable exactly-once HA
delivery. Runtime restart invalidates epoch and old session request IDs: results
are unknown unless future approved durable reconciliation proves otherwise.
A timeout/cancel after possible write cannot roll back an action. ADR-008 drains
and fences action-capable versions; fencing cannot cancel an action HA already
accepted. Do not duplicate an unknown request during rollback.

## Discovery and typed Go bindings — requirements only

A future separately scoped discovery grant can read entity/device/area registries
and HA service descriptions, preserving provider identity, optional fields,
registry revision and permission-redacted missing data. Fresh state is not
registry validity. Discovery should report unsupported/unknown capabilities
rather than invent behavior from domain names. Bound registry sizes and changes;
renames/removals invalidate generated references explicitly.

Generation workflow needs a versioned sanitized discovery snapshot, pinned
schema/compiler/generator versions, deterministic Go output, source-qualified
IDs separate from display names, safe handling of unavailable/unrecognized state,
unknown attributes/services, parameter validation and grant checks at Runtime.
Generated code is developer tooling, not permission and not trusted installation.
Never embed tokens/household payload in committed examples; generated real
household files require an explicit local privacy/retention decision. No registry
fetches, bindings, automation module or generator are implemented by D01.

## Migration and implementation slices after acceptance

1. Accept concrete public authority and ADR-008 containment prerequisites;
   resolve supported HAOS kernel/filesystem topology before launch.
2. Add versioned bounded codec/session identity with fake peers; no actions.
3. Add atomic scoped projection at source gate. Do not adapt whole-cache
   Subscribe by sending all states then filtering in a module; oversized unscoped
   private resets must not silently limit approved scoped consumers.
4. Add finite read grants, staged resets, revocation, delivery/overflow faults.
5. Add action grants/gateway only under approved authority with fake HA actions,
   acceptance/unknown tests, fencing/drain and recovery. No production actions
   until separately authorized acceptance test setup.
6. Review discovery and typed-binding tasks separately, then a first module.

Preserve ADR-006 private consumers and current ingress/status behavior; expose
neither raw private types nor Supervisor token. Record accepted references and
split G01 into small executable tasks before enabling it.

## Adversarial and compatibility acceptance plan

- Incompatible majors/features, oversized/partial/invalid/deep frames, unknown
  required enums, hello replay, spoofed actors and cross-principal grant IDs.
- Subscribe racing add/remove/reset/disconnect/revocation, nested attribute scope
  leaks, invisible-change sequence leaks, initial unknown versus absent/stale,
  incomplete reset chunks and independent ownership.
- Flood/slow/stopped reader, saturated global admission/transport buffers,
  terminal frame impossible to send, cleanup on process death and grant revoke.
- Unauthorized services/targets/parameters, expired/revoked/old epoch requests,
  TOCTOU revocation between validation/write, request ID collision and exhaustion.
- HA accepted but device unchanged, accepted but response lost, partial write,
  cancel after send, Runtime restart and rollback with unknown action; zero blind
  resends and no false physical-success/causality claim.
- Old/new minor peers, explicit required-feature rejection, malformed generated
  bindings and registry rename/removal; generation never bypasses grant enforcement.

All network/action fixtures synthetic. Race/model/fuzz and byte accounting gates
apply. Supported HAOS measurements remain a separate environment requirement.

## Decision requested and unresolved points

Accept/reject JSON socket versus inherited socketpair, scoped/reset bounds,
principal authentication and action outcome/grant model after ADR-008 feasibility.
An accepted proposal must explicitly add HA action authority; this Proposed ADR
and a completed D01 ledger entry do not do so. Remote PWA, persistence, history,
installation and unrestricted wildcard scopes remain outside this decision.
