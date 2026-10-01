# Current state

- Stakeholder accepted ADR-007, ADR-008 and ADR-009 on 2026-10-01; accepted forms are authoritative.
- New solo implementation queue baseline: 4fa37d484c385ccf3eecda95c77190f7e63e9d53.
- tasks.json decomposes the accepted architecture into independently verifiable Runtime slices: module IPC/state, durable timeline/execution modes, launcher/cutover/handover/quarantine, action outcomes, package verification, generated bindings and Bridge negotiation/discovery.
- B03 is intentionally blocked until the separate Python Bridge proves ordered-state barrier/sequence semantics with version-pinned fixtures/evidence.
- V02 is intentionally blocked until an authorized disposable HAOS target exists. Existing R07 remains a historical blocked ledger entry from the prior harness; V02 is the new module-lifecycle HAOS gate.
- Production module downloads, third-party repositories, real-home action tests, Bridge installation and remote clients are outside the active queue.
- Agent should run python3 scripts/agent_tasks.py next and work independently through ready tasks, recording evidence per task.

- M01 completed: Added bounded private JSON sessions, capability negotiation and launcher-owned identity; reconciled stale spec proposal text. Evidence: [record](evidence/M01.md).

- M02 completed: Added canonical chunked reset and ordered state delta projection with atomic bounded replicas. Evidence: [record](evidence/M02.md).

- T01 completed: Added checksummed atomic private storage and bounded durable schedule/watermark recovery. Evidence: [record](evidence/T01.md).

- T02 completed: Added durable bounded execution modes, cooperative restart cancellation and common timeline admission. Evidence: [record](evidence/T02.md).

- M03 completed: Added private inherited Linux IPC launcher with sanitized environment, process group ownership, bounded joins and sampled RSS enforcement. Evidence: [record](evidence/M03.md).

- M04 completed: Added durable ready-before-cutover generation routing, once-only dispatch claims, boot fencing and bounded drain/child retirement. Evidence: [record](evidence/M04.md).

- M05 completed: Added isolated bounded generation state stores and optional version/schema-aware warm/final IPC handover. Evidence: [record](evidence/M05.md).
