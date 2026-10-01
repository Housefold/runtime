# Current state

- Stakeholder accepted ADR-007, ADR-008 and ADR-009 on 2026-10-01; accepted forms are authoritative.
- New solo implementation queue baseline: 4fa37d484c385ccf3eecda95c77190f7e63e9d53.
- tasks.json decomposes the accepted architecture into independently verifiable Runtime slices: module IPC/state, durable timeline/execution modes, launcher/cutover/handover/quarantine, action outcomes, package verification, generated bindings and Bridge negotiation/discovery.
- Final ledger: 25 tasks done, R07 explicitly waived; no todo, in_progress, blocked or approval-gated rows.
- B03 private source switching is verified; Python ordered-state production activation evidence remains unprovided. cmd/runtime still uses native HA.
- V02 passed on an actual disposable HAOS 18.3 amd64 VM with non-root synthetic containers. Full supported-Core/Supervisor/appliance acceptance is not claimed.
- Production module downloads, third-party repositories, real-home actions, Bridge installation and remote clients remain outside the active queue.

- M01 completed: Added bounded private JSON sessions, capability negotiation and launcher-owned identity; reconciled stale spec proposal text. Evidence: [record](evidence/M01.md).

- M02 completed: Added canonical chunked reset and ordered state delta projection with atomic bounded replicas. Evidence: [record](evidence/M02.md).

- T01 completed: Added checksummed atomic private storage and bounded durable schedule/watermark recovery. Evidence: [record](evidence/T01.md).

- T02 completed: Added durable bounded execution modes, cooperative restart cancellation and common timeline admission. Evidence: [record](evidence/T02.md).

- M03 completed: Added private inherited Linux IPC launcher with sanitized environment, process group ownership, bounded joins and sampled RSS enforcement. Evidence: [record](evidence/M03.md).

- M04 completed: Added durable ready-before-cutover generation routing, once-only dispatch claims, boot fencing and bounded drain/child retirement. Evidence: [record](evidence/M04.md).

- M05 completed: Added isolated bounded generation state stores and optional version/schema-aware warm/final IPC handover. Evidence: [record](evidence/M05.md).

- M06 completed: Added artifact/state retention references, fresh-epoch manual rollback, finite selected-version backoff and persisted terminal quarantine. Evidence: [record](evidence/M06.md).

- A01 completed: Added boot/generation-fenced canonical action gateway with durable unknown-before-send metadata, bounded deduplication and distinct HA outcomes. Evidence: [record](evidence/A01.md).

- P01 completed: Added source-neutral official manifest/catalog review and offline Ed25519/digest compatibility verification. Evidence: [record](evidence/P01.md).

- G03 completed: Added bounded canonical discovery and deterministic Go binding prototype with durable stable identity-to-symbol mapping. Evidence: [record](evidence/G03.md).

- B01 completed: Added optional separate Core-style Bridge negotiation with bounded schemas/fixtures and explicit availability/backoff. Evidence: [record](evidence/B01.md).

- B02 completed: Added bounded chunked Bridge discovery into canonical provider identities, relationships, schemas and per-collection visibility statuses. Evidence: [record](evidence/B02.md).

- Supplementary final review: uncertainty fences, child join/retention safety, descriptor inheritance, partial writes/symlink checks and Bridge discovery freshness. See [evidence](evidence/FINAL-REVIEW.md).


## Resumed completion (2026-10-01)

Stakeholder explicitly authorized proceeding through blockers. B03 is complete
with atomic source ownership, bounded ordered-state fixtures/client, stale fencing,
full native fallback and reentry. [B03 evidence](evidence/B03.md) records authority,
failure tests and the full `agent_verify.sh --images` PASS.

[R07 resumed evidence](evidence/R07-RESUMED.md) records actual HAOS kernel tests,
image layer inspection, successful Core image import, failed network-dependent
Supervisor app/Core provisioning, and explicit waiver of the unexecuted matrix.
[V02 evidence](evidence/V02-RESUMED.md) records real non-root HAOS container
lifecycle tests, health under child pressure and measured shutdown. VM evidence
is distinct from physical appliance and production integration readiness.

`python3 scripts/agent_tasks.py next` reports no ready task. Available checks were
executed; missing environment criteria are not labeled PASS. No changes pushed.
