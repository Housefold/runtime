# Runtime v1 productization contract

Status: Accepted stakeholder direction, 2026-10-01.

## Finish line

Runtime v1 is complete only when the exact release artifact can be installed from the Housefold Home Assistant App repository onto a clean supported HAOS system and the end-to-end acceptance gate passes without manual container/filesystem intervention. After that gate, the release is suitable for a separately authorized live-house installation. No agent may waive a release criterion.

## Product contract

- Runtime is a normal HAOS App/add-on installed from a Housefold repository. It starts on boot and uses the HA Supervisor watchdog convention.
- First start requires no Housefold wizard, HA URL, token or credentials. Runtime uses the local HAOS/Supervisor environment and native HA APIs.
- HA/Bridge/module failure degrades capability without making Runtime watchdog-unhealthy. Runtime durable-state integrity failure enters recovery-required and fails closed for normal operation while preserving the BIOS recovery plane where ingress is available.
- Use normal HA App conventions for lifecycle, updates, backup/restore and logging. Runtime mirrors HA Supervisor semantics for its managed modules.
- Opening Runtime shows the lightweight BIOS-style admin UI. It is the full Runtime management/recovery plane, HA-ingress only, and server-side restricted to HA admins. Non-admin users are denied.
- BIOS manages Runtime, official module install/remove/update/start/stop/restart, desired state, dependencies, versions, rollback, quarantine, resources, Bridge status, logs, diagnostics, storage and recovery. Domain configuration belongs to modules; module UI health is independent of module service health.
- Runtime installs alone: Runtime + BIOS + official catalog capability. Functional modules are explicit installs.
- One official signed Housefold catalog is remotely maintained, cryptographically verified and cached locally. No arbitrary repositories/unsigned modules in v1. Network/catalog failure never prevents existing local operation.
- Module installation/update is staged and transactional: verify, prepare, ready, atomic cutover, drain, retain previous. Required dependencies are resolved transactionally. Removal preserves module data by default; destructive removal is explicit.
- Runtime supervises child modules using standardized health/readiness, persisted desired state, bounded restart/backoff and terminal quarantine. No automatic rollback. Quarantined modules stay quarantined across reboot.
- Runtime owns module storage (persistent/cache/generation/temp), quotas, backup consistency and lifecycle. Factory reset removes all Runtime-owned state; smaller reset operations remain separate.
- Runtime owns module resource requests/limits and host safety ceilings, exposes usage, and uses priority classes for Housefold-only pressure eviction/recovery. It never manages HA Core or unrelated Apps.
- Disk pressure performs deterministic safe GC only. Active/retained durable coordination and module state are protected; inability to write safely becomes explicit storage degradation.
- Runtime and module logs flow through the native HA App live log pipeline with module/version/generation attribution and bounded module logging. BIOS adds structured diagnostics and privacy-safe export.
- Runtime emits actionable HA persistent notifications and stable machine-readable operational signals/events for native HA automations. HA receives no Runtime admin controls in v1.
- Runtime maintains a bounded durable administrative audit trail with HA-admin attribution where ingress reliably supplies identity.
- Bridge is optional, user-installed through normal HA mechanisms, automatically detected/negotiated, preferred where compatible, and never a boot dependency. Native HA remains fallback. Production ordered-state selection requires proven Python barrier/sequence compatibility evidence.
- Runtime is the only HA action gateway for modules. Official modules are trusted actors; actions retain generation fencing, attribution and not_sent/accepted/rejected_by_ha/unknown semantics. Unknown actions are never blindly retried.
- HA Core restart/update is expected dependency loss: Runtime/modules stay alive, state becomes stale, and full resync restores service. Full HAOS/Runtime restart performs durable recovery before new module work is admitted.
- Runtime backup through HA includes a consistent recoverable snapshot of its managed module estate.
- Module manifests declare compatibility, dependencies, resources, priority, networking and management capabilities. Outbound non-HA networking is declared; modules receive no HA/Supervisor credentials and expose no independent LAN service in v1.
- v1 architectures: amd64 and aarch64. Supported HA version floor is whatever the release validation pipeline actually proves.

## Delivery authority

The implementation agent has full authority to refactor/replace code and harness internals while preserving accepted behavior/safety invariants; create sensible Housefold repositories when GitHub permissions permit; establish GitHub Pages/catalog/release infrastructure; configure/run CI and publish tested distribution artifacts after gates pass; and create/destroy disposable test environments.

The agent must not touch the stakeholder's live HAOS instance, use real household actions/identities/secrets, weaken safety boundaries, or self-waive gates. A genuine external blocker remains blocked and prevents declaring v1 complete.

## Mandatory release gates

Before clean-HAOS acceptance:
1. independent adversarial security review covering ingress admin enforcement, forged module identity, credential/FD leakage, catalog/signature/package attacks, traversal/symlink/archive/resource attacks, IPC/log flooding, stale-generation actions, rollback authority, backup tampering and recovery corruption; high-severity findings block release;
2. bounded performance/resource soak covering HA loss/recovery, module crash/quarantine, repeated cutovers, state/event/log pressure, CPU/memory/disk pressure, Runtime restart and leak/goroutine/FD growth; unexplained unsafe growth blocks release;
3. exact distribution artifact installed through the actual Housefold HA App repository on disposable supported HAOS, exercising install/start/start-on-boot/watchdog/BIOS/admin auth/native HA, synthetic catalog module lifecycle/logging, HA and Runtime restart recovery, module update/cutover, backup/restore, uninstall/reinstall and failure/resource/storage recovery.

Mocks, Docker-only runs and library tests supplement but never replace the final HAOS gate.
