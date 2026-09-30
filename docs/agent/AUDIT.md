# Committed Runtime audit

30 September 2026. Baseline: [Housefold/runtime bd36beb](https://github.com/Housefold/runtime/tree/bd36bebf7bf922d5b19ea83428d4345758ef6ac5).

The commit is a credible HAOS process and HA state-ingestion foundation. It is not yet the module supervisor described by the platform vision. Preserve it and extend it in small slices; a rewrite or mandatory Bridge would discard useful boundaries.

The previously reported internal/state/state.go and ADR-006 are absent from this commit. Treat the state-access/subscription contract as unfinished. There are 35 source files, one external dependency (coder/websocket v1.8.15), and no Fuzz or Benchmark functions.

## Evidence and limits

All 35 committed files were fetched at the exact SHA and checked against Git blob hashes. Production code, tests, packaging, AGENTS.md, spec and ADR-001 through ADR-005 were inspected. Housefold vision/architecture/domain docs were read; offline copies are in references/.

[Go checks run 36333002024](https://github.com/Housefold/runtime/actions/runs/36333002024) for this exact commit passed formatting, tests, vet, Go build and both HAOS image builds. No race, fuzz, benchmark or HAOS smoke step appears in the workflow. This is observed CI evidence, not a local rerun.

The audit environment had neither Go nor Docker. Runtime tests, race tests, image builds, HAOS smoke and appliance measurements were not independently run. The recorded generic AArch64 VM smoke is a documentation claim for lifecycle/connectivity that explicitly excludes the current state-sync path. Actual 2012 Mac mini readiness remains unverified. This is not a penetration test.

## Implemented baseline

| Area | Source | Assessment |
| --- | --- | --- |
| Process/lifecycle | cmd/runtime/main.go, internal/supervisor/service.go | Signal cancellation, independent health, bounded HTTP shutdown. |
| HAOS packaging | config.yaml, Dockerfile | amd64/aarch64, non-root scratch image, no host port mapping, AppArmor/protected baseline, Core proxy grant without general Supervisor API access. |
| HA ingestion | internal/ha/state_session.go | One reader, subscription before snapshot, bounded buffering/reconciliation, atomic generations, ping/reconnect. |
| Cache ownership | state_view.go, StateSession.Snapshot | Memory-only normalized state, nested JSON copy isolation, stale retention, payload/event/tombstone bounds. |
| Operator status | internal/supervisor/service.go | Ingress peer filter, read-only coarse metadata, no entity data, health independent of HA. |
| Verification | *_test.go, go.yml | Useful fake-WebSocket, reconciliation, timeout, recovery, ingress and lifecycle tests; normal CI passed. |

## Findings and work needed

1. **P1 functional gap: internal state consumption.** Changes() is one shared channel of coalesced empty notifications. Multiple receivers compete; it is not ordered broadcast and carries no deltas or loss position. Snapshot() has generation/freshness but no revision. No per-entity read or subscriber lifecycle exists. Keep Changes for status. Add private atomic initial snapshot/registration, generation/revision positions, independent bounded fan-out, freshness/reset events and explicit overflow/cancellation/shutdown outcomes.

2. **P1 assurance gap: concurrency is not a CI gate.** Existing tests verify nested snapshot isolation, but CI runs only ordinary tests. Add race checks and deterministic read/update/subscribe/disconnect/shutdown interleavings, including independently mutable consumer data. Do not use short sleeps as correctness proofs.

3. **P1 verification gap: current ingestion on HAOS.** Documented VM smoke predates state synchronization. Build a repeatable synthetic HAOS matrix for Core restart, denial/recovery, event storms, resync, ingress filtering and Supervisor shutdown. Hardware evidence remains blocked until a test environment exists; Docker builds do not substitute for it.

4. **P2 risk: contention and resource amplification.** Snapshot deep-copies all state under an RWMutex read lock; applyLive needs its write lock. Near-limit copying can stall ingestion. The 64 MiB cap measures serialized normalized state, not heap/RSS; old generation, candidate, raw frame, decoded maps and buffers can coexist. Benchmark before restructuring and preserve atomicity. This is an unmeasured risk, not a reproduced test failure.

5. **P2 risk: whole-process shutdown budget.** HTTP shutdown is capped at 8 seconds; config.yaml grants 10 seconds. runListener then joins session/notification goroutines without a whole-process deadline. Synchronous decoding, candidate construction and copying are not all interruptible by context. Normal cancellation is covered, but near-limit/fault exit behavior needs evidence before claiming a timing guarantee.

6. **P2 test gap: input/accounting invariants.** Parser timestamps are validated individually, not for semantic relationships. Verify HA semantics before adding rejection rules. Add bounded fuzz/model cases for deep/malformed attributes, duplicate IDs, ambiguous ordering, repeated deletion/recreation and exact count/byte boundaries. Failed candidates must not replace published state.

7. **P2 maintenance gap: legacy diagnostics.** client.go/monitor.go retain REST+WS probing; main now uses StateSession. Trace callers and document intended diagnostic use or remove proven dead code. REST must never regain operational readiness authority.

8. **Strategic blockers, not bootstrap defects.** No module launcher, IPC/grants, trust/install/activation/rollback, action API, discovery registry or Bridge adapter exists. internal/supervisor currently serves health/status, not child processes. Separate processes alone do not establish containment. Passing the Supervisor token to modules would violate the architecture. Resolve contracts before implementation.

## Development sequence

Establish repeatable checks; implement the private state contract; add model/concurrency/fault evidence; measure copying/ingestion/shutdown; validate HAOS. In parallel within the task queue, produce concrete proposals for public module/action contracts, containment/trust/activation and optional Bridge. Implement these only after accepted decisions. Registry discovery, generated typed Go bindings and feature modules follow the consumer contract. Remote PWA/intelligence/unattended updates come later.

Preserve HA ownership, local operation without cloud/Bridge/modules, independent health, stale-state visibility and data privacy. Fresh means no known observed continuity loss, not durable/gap-free delivery. Current foundation has no HA action authority. Supervisor owns Runtime recovery; future module updates need their own fencing and rollback design.
