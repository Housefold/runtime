# Housefold — Project Vision

**Status:** Draft for review  
**Date:** 25 September 2026  
**Scope:** The Housefold platform. *Rosehill* is the name of the owner's Home Assistant instance, not the product name.

## The idea

Housefold is a local-first layer around Home Assistant (HA) for people who want to build a home that behaves coherently, remains understandable, and can evolve beyond HA's default automation and interface workflows. Home Assistant remains the foundation for device integrations, entities, state, and service calls. Housefold adds a dependable Go runtime that connects to HA and supports independently installable capabilities for automations, presence, interactions, user interface, telemetry, and eventually intelligence.

The first measure of success is mundane: walking into a room should still turn on the lights promptly when the VPS, internet, cloud AI, or an optional Housefold module is unavailable. More ambitious features should earn their place without weakening that foundation.

## Why it exists

Home Assistant can connect and control the home, but creating and maintaining richer behaviour through YAML, visual editors, or Node-RED does not match the desired development experience. The owner wants native Go automations, a purpose-built interface, better presence and interaction handling, and a system that can explain what happened when the home behaves unexpectedly. Over time, the home should learn from history and propose useful improvements while leaving consequential choices under human control.

Housefold provides one coherent platform for those capabilities instead of a collection of unrelated scripts, dashboards, and glue services.

## Product promise

> Build the home in Go, operate it with confidence, and let its intelligence grow without making basic home behaviour depend on the cloud.

A Housefold installation should:

- **Keep essentials local.** Time-sensitive automation execution and the minimum control path run on the HAOS machine and survive loss of the VPS or internet.
- **Treat HA as the device foundation.** Discover and use HA's existing entities, services, areas, and integrations rather than reimplementing device support.
- **Make development pleasant.** Support typed Go code, entity discovery, fast feedback, and safe deployment of new or changed automations.
- **Stay usable when small.** The runtime works on its own with a lightweight management interface. Optional modules add capability without becoming prerequisites for first boot.
- **Expose failures clearly.** A human or authorized diagnostic agent can see what ran, what failed, which dependency is unavailable, and what can safely be done next.
- **Protect household context.** Minimize and transform personal information before any cloud AI request; make cloud features optional and explicit.

## Product boundaries

| Component | Its role in Housefold |
| --- | --- |
| Home Assistant on HAOS | Authoritative connection to devices and integrations; source of entity state and service actions. |
| Housefold runtime on the HAOS machine | Stable local supervisor and control plane for Housefold modules. Connects to HA, manages module lifecycle, health, upgrades, and recovery; serves a minimal management UI. |
| Optional HA integration bridge | Offers deeper HA event and discovery access when enabled. The runtime initially connects over HA REST and WebSocket APIs and remains functional without the bridge. |
| Optional modules | Independently adopted capabilities such as automations, presence, interactions, UI, telemetry, and intelligence. A household can install only the modules it wants. |
| VPS | A possible host for remote-facing experiences or heavier, noncritical workloads. It must not be required for essential local reactions. |
| Cloud services and LLMs | Optional assistance, expression, and analysis behind explicit privacy and authority boundaries. They do not become the sole path for basic control. |

These are product boundaries, not a commitment to a specific process layout, IPC mechanism, module manifest, or network topology. Those belong in architecture and subsystem specifications.

## The experience we want

### First run

Housefold starts with its own small, resilient management interface, styled like a clear system console. It discovers the local HA instance and asks only for the access needed to connect through HA's standard APIs. The user can see connection status and runtime health before installing any optional module. If they want richer discovery, Housefold can guide them through enabling the HA integration bridge and show that the connection has changed successfully.

### Building automations

A developer writes normal Go code with typed access to discovered HA concepts, tests it, and deploys it with minimal interruption. The running version remains available through a failed build or deployment; a faulty new version can be rolled back. Local motion-to-light behaviour remains responsive while other parts of the system are updating.

### Living with the system

The household interacts through familiar controls, voice surfaces, notifications, and an optional Housefold PWA. Housefold can present a consistent picture of the home's state across those channels. The PWA can combine Housefold and selected adjacent systems, but the core runtime remains manageable without it.

### Understanding and improving the home

When something goes wrong, the user can trace the relevant observations, decisions, actions, and failures. Later intelligence features can find patterns in historical data and recommend automations or optimizations. Recommendations should say why they were made and what evidence supports them; applying a change is a distinct, authorized action.

## Principles and invariants

1. **The local control path survives disconnection.** Essential automations continue when the VPS, internet, cloud services, or optional intelligence module is down.
2. **The supervisor favors continuity.** A failed module, upgrade, or deployment cannot silently replace the last healthy version. Recovery and rollback are first-class behaviours.
3. **Modules are optional by design.** The runtime can boot, connect to HA, report health, and provide management without the bridge or any feature module. Installing one feature must not require unrelated features.
4. **HA owns device truth.** Housefold may derive higher-level concepts and cache state for operation, but it must reconcile with HA after disconnects instead of assuming a stale cache is authoritative.
5. **Household privacy has a local boundary.** Raw identities, household activity, and secrets do not flow to a cloud LLM by default. Any permitted cloud workflow uses explicit data minimization and a reviewable mapping policy.
6. **Observability is part of the product.** Important decisions and failures should be explainable without exposing private data unnecessarily.
7. **Human authority grows with impact.** Insight and recommendation can be automated; changes to household behaviour and external actions require bounded permissions and clear review rules.
8. **Extension does not imply unchecked code execution.** Module discovery, provenance, compatibility, permissions, and updates must have a defined trust model before general distribution.

## What success looks like

For the initial usable release:

- A clean HAOS installation can bring up the Housefold runtime, connect through HA's standard APIs, and show a usable status and management screen without installing the bridge.
- A user can opt into the bridge and see its availability and benefit; loss of the bridge has a documented fallback.
- The automation capability can safely deploy and roll back native Go automation code. A local motion-to-light scenario continues through loss of the VPS and internet.
- A module crash or invalid upgrade leaves the runtime manageable and either restores a healthy version or reports an actionable failure.
- The system exposes enough health and event history to answer why a representative automation did or did not run.

The subsystem specifications should convert these outcomes into measurable latency, resource, recovery, and acceptance criteria on the target HAOS hardware.

## Evolution

**Foundation:** Establish the local runtime, its minimal UI, HA standard API connection, health reporting, and safe module lifecycle. Add the optional bridge when its deeper access demonstrably improves discovery or reliability.

**Useful home:** Build native Go automations and the custom PWA, then add presence, interactions, notifications, and telemetry around stable contracts. Households can adopt these independently.

**Helpful home:** Use history to suggest improvements and help diagnose issues. Add privacy-aware cloud language and authorized agent operations where useful. Intelligence remains additive to the reliable local core.

This order expresses dependencies and priorities, not release numbers or delivery dates.

## Decisions still to make

- Exact packaging and supervision mechanics on HAOS, including process isolation and atomic binary replacement.
- The runtime-to-bridge transport, permissions, and whether deeper HA integration merits its maintenance cost.
- Module manifest and compatibility contract, signed provenance, approved distribution sources, and runtime update policy.
- The precise split between HAOS and VPS workloads, and the remote access/authentication design.
- Privacy policy for telemetry sent to diagnostic agents, including pseudonym lifetime and re-identification boundaries.
- Quantified hardware budgets and response-time targets on the owner's 2012 Mac mini.

Record these in architecture documents and ADRs as evidence and prototypes resolve them. This vision defines the direction without pretending those choices are settled.

