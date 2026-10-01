# Current state

- Stakeholder accepted ADR-007, ADR-008 and ADR-009 on 2026-10-01; accepted forms are authoritative.
- New solo implementation queue baseline: 4fa37d484c385ccf3eecda95c77190f7e63e9d53.
- tasks.json decomposes the accepted architecture into independently verifiable Runtime slices: module IPC/state, durable timeline/execution modes, launcher/cutover/handover/quarantine, action outcomes, package verification, generated bindings and Bridge negotiation/discovery.
- B03 is intentionally blocked until the separate Python Bridge proves ordered-state barrier/sequence semantics with version-pinned fixtures/evidence.
- V02 is intentionally blocked until an authorized disposable HAOS target exists. Existing R07 remains a historical blocked ledger entry from the prior harness; V02 is the new module-lifecycle HAOS gate.
- Production module downloads, third-party repositories, real-home action tests, Bridge installation and remote clients are outside the active queue.
- Final queue state: all 13 implementation tasks added by the accepted queue are done. `python3 scripts/agent_tasks.py next` reports no ready task. R07/B03/V02 remain blocked with unchanged concrete restart conditions.

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

Stakeholder explicitly authorized ignoring blockers and completing the harness.
B03 Runtime implementation is complete with atomic source ownership, bounded
ordered-state fixtures/client, stale fencing, full native fallback and reentry.
Full `agent_verify.sh --images` passed on its final code, including race tests and
both architecture images. See [B03 evidence](evidence/B03.md).

A disposable official HAOS 18.3 amd64 VM has been provisioned with QEMU TCG,
2 vCPUs/4 GiB and Supervisor 2026.09.2. Its initial Core is the landing page.
R07/V02 are being resumed against this synthetic-only guest; any unexecuted
criteria will be explicitly waived under stakeholder direction, never marked PASS.
The earlier “final queue state” and blockers above describe the previous stopping
point and are superseded by this resumed section.


R07 resumed: actual HAOS kernel state/runtime tests PASS and both image layers
inspected. Full supported-Core/Supervisor/appliance matrix is explicitly waived
under stakeholder instruction after concrete install/network attempts. See
[R07 resumed evidence](evidence/R07-RESUMED.md). No production readiness claim.
