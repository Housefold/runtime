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
Ordered state is deliberately never selected, even if advertised.

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
fixtures proving snapshot barriers and sequences remain required for B03.
