# ADR-008: Official module trust, durable execution and zero-downtime activation

- **Status:** Accepted
- **Date:** 2026-10-01
- **Owners:** Housefold maintainers
- **Supersedes:** Proposed ADR-008 dated 2026-09-30
- **Implementation gate:** ADR-007 also applies

## Trust boundary

Housefold initially uses a walled-garden module ecosystem. Modules are official Housefold artifacts selected from the Housefold catalog, reviewed by the user, installed by Runtime and launched as separate native processes.

Official signed modules are trusted components. Process separation is initially for reliability, lifecycle ownership and fault containment. A proven nested namespace/seccomp/cgroup sandbox is not a prerequisite for official modules. This decision must be revisited before third-party repositories or arbitrary binaries execute.

Modules never receive HA/Supervisor credentials and use ADR-007's Runtime API for HA access.

## Official catalog and packages

Initial Runtime accepts modules only from the official Housefold catalog/signing authority. Users cannot add arbitrary repositories. The package format remains source-neutral for a possible future ecosystem.

Runtime verifies signed provenance, artifact digest/signature, identity/version, Runtime/protocol compatibility, architecture and manifest metadata before staging. The UI shows provenance, version, compatibility and declared capabilities before installation/update. Runtime never silently installs a module.

## Process isolation

Runtime launches modules as separate processes with private inherited IPC. It uses practical HAOS resource/isolation controls where supported, observes resource use and prevents accidental descriptor/credential inheritance. Missing nested sandbox capability does not prohibit an official module.

Module crashes must not become Runtime failure, state generations must not share writable ownership, child processes must be stoppable/joinable, and protocol/resource pressure must be bounded where HAOS provides enforcement.

Official modules may make declared outbound non-HA network connections.

## Durable event timeline

Runtime is event-driven and owns durable execution coordination.

It persists enough state to survive Runtime failure or HAOS reboot: schedules, timeline/watermark state, pending execution IDs, execution-mode queues, active/preparing/draining generation metadata, cutover state, action uncertainty and package/state-generation references.

Current HA entity state is not durable Runtime history. Runtime reconstructs it from HA.

### Recovery replay

After restart Runtime reconstructs deterministic Runtime-owned occurrences between its durable watermark and now over a bounded recovery window. Replayed occurrences retain their original logical timestamp and are marked recovered.

Runtime defines a global default recovery window. Individual schedules may override it subject to an absolute Runtime maximum.

Runtime does not fabricate external events it did not observe while offline. Future trustworthy HA/Bridge replay requires an explicit contract.

### Execution modes

Runtime provides trigger admission semantics analogous to single, restart, queued and parallel. Exact cancellation, queue, concurrency and expiry rules are versioned protocol details.

The same rules apply during normal operation, module restart and deployment. Cron is a trigger source, not a special execution path.

Automations owns individual automation definitions/business logic. Runtime owns scheduling/timeline and admission primitives.

## Zero-downtime forward activation

Zero interruption is a core requirement.

1. v1 remains ACTIVE and receives all work.
2. Runtime starts v2 as PREPARING.
3. v2 negotiates protocol, loads configuration/state, establishes required subscriptions/schedules and warms up.
4. v2 explicitly reports ready and accepting work.
5. Runtime performs one atomic admission cutover.
6. v2 becomes ACTIVE and receives all newly admitted work.
7. v1 becomes DRAINING and may finish only work admitted before cutover.
8. At zero in-flight work Runtime terminates/joins v1 and marks it RETIRED.

Both processes may briefly have already-admitted work in flight, but exactly one generation receives new work.

If v2 fails before cutover, activation never occurred and v1 remains active. After cutover Runtime never automatically returns to v1.

Runtime owns trigger admission. An occurrence at the cutover boundary is represented once and routed once. It may be held across the tiny atomic transition but is never delivered to both generations.

## Module-owned state and handover

Modules may own persistent state, including Automations. Runtime allocates/protects per-module and per-generation storage but does not interpret module schemas. Preparing v2 never gets writable access to v1's state directory.

Runtime state handover is optional. Protocol support does not imply version-to-version state compatibility. The candidate receives source version/schema metadata and decides whether it can import that state. A future v3 may support handover while explicitly declaring v1/v2 state incompatible.

Compatibility and migrations belong to the module developer. Runtime brokers bounded opaque export/import payloads.

A compatible candidate may take an initial snapshot for expensive warm-up. Before cutover, Runtime establishes a short handover barrier, obtains the final consistent state/delta required by the negotiated protocol, v2 confirms final adoption/readiness, and only then cuts over.

If old state is incompatible, the candidate may explicitly permit clean initialization or fail readiness. The UI surfaces destructive state-reset consequences before such an update.

## Previous generation and manual rollback

After successful cutover Runtime retains the immediately previous artifact and its isolated state generation for diagnostics and explicit manual rollback.

There is no automatic rollback.

After the next successful upgrade, anything older than the immediately previous generation is outside the supported rollback path and may be deleted.

Manual rollback is a new activation with a new generation/epoch and the same preparation, readiness and atomic-cutover rules. Runtime never resurrects stale process authority or rewinds timeline history.

## Restart backoff and quarantine

A crash of the selected active version triggers bounded automatic restart of that same selected version with exponential backoff and a finite retry budget.

When exhausted, the module enters terminal QUARANTINED state:

- no further automatic restart;
- no automatic rollback;
- no new work delivered;
- pending work follows retention/expiry rules;
- diagnostics/crash evidence remains available;
- previous version remains available for explicit manual rollback;
- other modules and Runtime continue;
- operator action is required to leave quarantine.

Runtime may automatically recover the currently selected version. It may never automatically select a different version.

## Runtime restart recovery

On Runtime restart it recovers and validates durable coordination state, reconnects to HA and rebuilds canonical state, resolves selected/retained module generations, fences stale child authority, restarts selected modules, reconstructs deterministic timeline occurrences and resumes eligible work.

Any action that may have reached HA remains unknown unless explicit evidence resolves it. Recovery never blindly resends it.

Persistence must be crash-safe and tested for corruption, partial writes and disk-full behavior. This ADR does not select the storage engine.

## Runtime recovery remains separate

Supervisor owns Runtime app startup/watchdog/update. Modules cannot replace Runtime, alter protected mode or acquire Supervisor credentials. Runtime self-update/rollback is separate from module activation.

## Initial implementation slices

1. Durable timeline/watermark persistence and restart/replay tests.
2. Official package/catalog verification using signed local fixtures.
3. Trusted synthetic process launcher with bounded inherited IPC.
4. Warm preparation/readiness plus atomic active-to-draining router.
5. Execution modes and cutover-boundary tests.
6. Per-generation storage and negotiated state-handover fixtures.
7. Previous-generation retention/manual rollback.
8. Restart backoff and terminal quarantine.
9. HAOS validation of lifecycle, crash and resource behavior.

Third-party module execution remains prohibited until a new threat-model decision.
