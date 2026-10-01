# Bridge negotiation fixture contract v1

Accepted authority: ADR-009. Runtime has an injected private authenticated Core
session, separate from native state ingestion. `internal/bridge/testdata` contains
synthetic request/response and hello JSON Schema fixtures suitable for sharing
with the Python repository. They are protocol fixtures, not Python/Core evidence.

Commands have Core request ID, type and a body. Responses use the Core `result`
envelope with the same ID, success and result/error. `housefold/hello` body asks
for protocol major 1/minor 0. Minor additions are optional. Unknown top-level
required semantics reject negotiation. Discovery v1 recognizes `bounded_chunks`;
unknown required capability semantics make that capability unavailable.
Ordered state v1 additionally requires both `snapshot_barrier` and
`contiguous_sequence`. The private B03 library can select a proven candidate;
production cmd/runtime does not wire it in.

Response frames cap at 64 KiB, nesting at 32, capabilities at 32, and version/name
strings at 64 bytes. Remote limits clamp to Runtime maxima of 16 chunks/1 MiB.
Each operation has a two-second context deadline. Only one optional operation
runs per client; concurrent callers fail explicitly. Failed probes use finite
1/2/30-second backoff, then at most one probe per 30 seconds; there is no retry
loop or Core/Runtime restart action. Absent, incompatible and temporarily
unavailable status do not change native state/process health.

Unknown-command means absent. Denial/timeout/malformed response means temporarily
unavailable; incompatible major means incompatible. Runtime never installs Bridge,
broadens credentials or constructs a remote listener. Python-side version-pinned
fixtures proving snapshot barriers and sequences remain required for production
activation. The stakeholder waived that implementation gate on 2026-10-01,
authorizing the Runtime library and synthetic fixtures before Python evidence.

## Discovery v1

`housefold/discovery` body contains cursor (empty initially) and bounded response
frame limit. Results follow `discovery_response_v1.json`: schema, epoch, contiguous
zero-based index, more, next cursor and status/rows sections for entities, devices,
areas and services. All pages share one provider epoch and section statuses.
Final page has no cursor; repeated cursors, gaps, changed epoch/status, duplicate
identities, over-limit counts/bytes or failed pages discard the candidate.

Collection status is available, missing, unsupported or permission_redacted.
Non-available collections have no rows. Entity registry IDs establish strong
provider identities only when supplied; otherwise the entity-ID reference is
weak. Device/area relationships and open service schemas enter the same canonical
model used by native discovery/bindings. No page envelope, Bridge epoch or Python
model escapes to modules. This is enrichment, never a selected state source.

## Ordered state v1 (B03)

The request/result/events in `ordered_*_v1.json` are synthetic sharing fixtures,
not a captured Python session. The schema checks payload shape; the client also
checks the following stateful rules. On its dedicated authenticated session,
request ID 1 is used only for `housefold/state/subscribe`. The successful result
is `snapshot_begin` with a nonempty bounded epoch and a snapshot barrier sequence.
Chunks carry that same barrier and contain canonical entities. Each entity ID
appears once across all chunks. Interleaved deltas start at barrier+1 and must be
contiguous. `snapshot_end` repeats the original barrier, rather than the newest
delta sequence. The server must guarantee the chunks are the complete view at
that barrier and no subsequent mutation is omitted. A fresh epoch identifies a
new subscription, including after reconnect. Sequence wrap is rejected.

During preparation the old selected source continues publishing. Only complete
validated snapshot plus replay commits one new local generation at revision zero.
Late old writer tokens are fenced. Live deltas use the next sequence; heartbeat
uses the current sequence and carries no state. Add requires an absent entity,
update requires an existing entity and matching entity ID, remove requires an
existing entity. Incoming position/freshness are not canonical authority. Invisible
updates advance transport sequence without inventing a canonical revision.

Limits: frames 64 KiB/depth 32, at most 16 chunks and 1 MiB wire data per candidate,
30 seconds total preparation; live operations have two-second deadlines. The
source owner allows two private candidates, each 10,000 entities/8 MiB canonical
payload and 256 buffered deltas/8 MiB. Existing subscriber limits still apply;
serialized budgets are not heap bounds. Context-aware transports must enforce
bounds before allocation. The caller owns finite retry/backoff; there is no
background optional-source loop.

Selected stream failure fences it and marks retained canonical data stale. The
injected native resync must start a new native subscription and full reconciliation,
never reuse the pre-failure cache. Only its complete fresh view publishes the next
generation; its newly returned native stream is forwarded through ActiveWriter.
A later complete Bridge candidate may fence a pending native fallback. Neither
source feeds the other's cache and no timestamp merge occurs. All downstream
module projection uses the canonical StateSession view. Production activation
still needs version-pinned Python barrier/sequence and HAOS fault evidence.
