# ADR-010: Runtime v1 product composition and recovery ownership

- **Status:** Accepted (implements stakeholder PRODUCTIZATION.md direction)
- **Date:** 2026-10-01
- **Supersedes in v1:** Foundation-only scope in ADR-001/002/003/005; historical approval and waiver restrictions

## Decision

Runtime is one foreground Supervisor-managed App, with no functional modules preinstalled. Supervisor owns Runtime startup, boot, watchdog, update, logs and backup lifecycle. Runtime owns its official module estate. Native HA and BIOS are baseline; Bridge, modules, remote catalog and cloud are optional dependencies.

`cmd/runtime` is the composition root, not a library demonstration. It creates a single boot identity and one owner per durable store. Production services are explicit owners with bounded cancellation/join; no module or optional transport can terminate the control plane. The token stays in the HA adapters, never child environments, module state, logs, diagnostics or audit.

Startup order:

1. Bind the unpublished health/ingress listener and start BIOS. Health represents Runtime itself, not HA/Bridge/module readiness. Recovery-required integrity failure is explicit Runtime degradation, never repaired by dropping data.
2. Open/validate the durable estate: inventory/desired state, execution, router, timeline, action uncertainty, catalog sequence/cache, bindings and bounded administrative audit. Validate cross-store references before module admission. Incomplete recovery prohibits new work; BIOS stays up where ingress is available. No corrupt store is overwritten to make boot work.
3. Start independent native HA ingestion, action/discovery adapters and optional Bridge negotiation. Core loss marks state stale, keeps Runtime/modules alive, and requires complete resync. Discovery enrichment cannot change state ownership. Ordered Bridge activation needs version-pinned Python proof.
4. Reconcile persisted selected modules: preserve disabled and terminal quarantine; recreate authority only after boot-bound verified launch/handshake/readiness. Clean up interrupted preparation conservatively. Resume pending work only after durable recovery and generation rebinding; interrupt prior running work. Replay only bounded Runtime schedules, never unobserved household events or unknown actions.
5. Run bounded supervision, drain/restart, resource/storage policy, operational HA signals and catalog refresh. All queues, retention, retries, deadlines and estate limits are finite. Shutdown fences admission, stops owned children and joins all workers within the App budget.

BIOS is HA-ingress-only and HA-admin authenticated on the server for reads and mutations. Peer allowlisting plus `panel_admin` is insufficient. Missing/unverifiable identity fails closed, including during Core loss. The management plane performs explicit lifecycle/recovery operations through the estate owner; module domain configuration remains module-owned. Destructive data reset needs an explicit operation, separate from removal preserving data. Runtime self-recovery uses normal Supervisor controls; there is no independent LAN endpoint.

Module activation verifies signed official provenance and declared compatibility/dependencies/resources/networking before staging. Readiness precedes atomic generation cutover; admission and actions are fenced under the router. Old admitted work drains, current/immediately previous artifacts/state stay retained, manual rollback is a fresh activation, and automatic recovery selects only the same version. Failure of candidate preparation preserves the active generation. Persist desired state before acting; interrupted lifecycle intent is reconciled on reboot.

Module state is Runtime-owned persistent/cache/generation/temp storage with quotas. Backup consistency fences writes and coordinates module state through normal HA App backup hooks/lifecycle. Restore validates integrity and ownership before admission. Safe GC never removes active/retained durable coordination or module data. Storage inability to commit fails closed for write-requiring operations and is visible in BIOS.

## Authority and evidence

This ADR restates accepted productization decisions; it does not waive prerequisites or claim implementation. [COMPOSITION.md](../agent/COMPOSITION.md) is the source-grounded wiring audit. All gates V1P12–V1P14 must pass before publication/closure. Historical R07/V02 evidence is not a substitute. Only disposable environments and synthetic data/actions may be used during this run.
