# ADR-006: Private state reads and ordered subscriptions

- **Status:** Implemented private foundation under harness task R02
- **Date:** 2026-09-30
- **Owners:** Housefold maintainers

## Context and boundary

The single coalesced Changes channel is for coarse status notifications. It
cannot broadcast entity changes to independent consumers. The authorized
foundation needs private in-process consumers; it grants no module transport,
HA actions, endpoint, persistence, history, or new credentials. The values in
internal/state are independent of HA wire envelopes; internal/ha owns adapter
publication and subscription synchronization.

## Contract

Snapshot and Read return isolated nested JSON data with the exact generation,
revision and freshness read under the same gate. Generation zero is an unknown
initial cache. Read.Known distinguishes that from a missing entity in a complete
published generation. Found does not imply fresh. Each visible add/update/remove
increments revision; new authoritative generation publication starts at revision
zero. Ignored duplicates/older events and freshness changes do not advance the
position. Positions are process-local and not durable HA sequence numbers.

Subscribe(ctx) serializes registration and a complete initial Reset against
publication, live updates and stale marking. Each reset replaces the entire
consumer view, including removal of absent entities. Add/Update contain the new
entity; Remove contains its identifier. Freshness marks known continuity loss
at the unchanged position. A reconnect gives a new complete Reset; it never
repairs a stale generation in place. Phase/status updates remain on Changes.

Every subscription has an independent queue capped at 256 events and 8 MiB of
canonical JSON event payload, including the envelope and complete initial/reset
view. An oversized initial view returns ErrInitialTooLarge. There are at most
sixteen subscribers, each reserving its full allowance, hence 128 MiB maximum queued
canonical payload. This measures serialized payload, not heap or caller-owned
copies. The canonical size is computed before cloning. A queue overflow discards
that subscription's pending data, closes Done, releases resources and returns
ErrOverflow from every subsequent Next; other queues continue. Count/capacity
and initial-size failures admit no subscription.

Next transfers ownership of its isolated event to the consumer. Use one reader
per subscription. Its context cancels only that wait. The Subscribe context or
Close ends the subscription with ErrCanceled; completion of Runtime Run ends
remaining subscriptions with ErrShutdown. Terminal reasons are first-wins and
remain visible; queued data cannot hide terminal loss. Closing is idempotent.
Callbacks, queues, accounting, registration and termination use the same gate;
there is no channel carrying data that can race with close. The source is a
single-use lifecycle; callers must construct a new session after shutdown.

## Alternatives and consequences

Sharing mutable snapshots would reduce allocations but violate ownership.
Unbounded channels would let slow consumers exhaust memory. Separate full-size
reservations limit simultaneous subscribers to sixteen but avoid global pressure
arbitrarily evicting a healthy consumer. Full reset data is explicit and atomic;
views exceeding the subscription bound can still be read using Snapshot but
cannot establish this stream. A scoped/resnapshot public protocol is a later
proposal, not an implicit permission to publish all HA state.

Copying and canonical serialization occur under the state gate. R04 measures
contention and shutdown impact before changing ownership structure. JSON payload
limits do not imply RSS limits. No durable delivery, silent-gap detection, or
exactly-once HA event guarantee is added beyond ADR-003.

## Verification

state_subscription_test.go verifies ordered independent delivery, mutation of
reads/reset/deltas, known versus absent, duplicate/older positions, authoritative
reset and stale retention, registration racing publication/disconnect, count and
byte overflow, capacity, cancellation/shutdown, resource release and concurrent
mutation/read/update/termination under the race detector. All data is synthetic.

### Accounting validation (R03)

Tombstone accounting sums each canonical normalized JSON record with fields
`entity_id` and `deleted_at` (UTC RFC3339Nano). Field names, punctuation and values
are included exactly. The prior arithmetic allowance undercounted this envelope
by four bytes and is corrected without changing the accepted 8 MiB limit.
Standalone state decoding now rejects trailing JSON/content; timestamp semantics
remain those in ADR-003, with no new relationship rejection rule.

V1P04 extends the bounded subscription count to the production launcher ceiling
of sixteen children, including preparing peers. Per-consumer bounds are unchanged;
V1P13 must measure the increased maximum before release. Historical R04 evidence
does not establish appliance support for this production ceiling.
