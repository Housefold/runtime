# Runtime specification

**Status:** Initial implementation baseline; protocol and security details remain open.

This specification narrows the platform docs to the Runtime repository. It records what an implementation agent may rely on and what must be resolved before work crosses a security or compatibility boundary.

## Purpose

Housefold Runtime is the stable, local supervisor and control plane for Housefold on Home Assistant OS. It connects Housefold to HA, manages separate-process Go modules, and keeps the installation inspectable and recoverable when optional components fail.

## Accepted decisions

| Topic | Baseline |
|---|---|
| First deployment form | Home Assistant OS add-on. Exact add-on configuration, privileges, and supervisor integration still need an ADR. |
| HA connection | REST and WebSocket APIs first. The custom HA integration Bridge is optional and cannot be a boot or core-operation dependency. |
| Optional Go modules | Separate processes supervised by Runtime. The control protocol and process containment model remain open. |
| Operating principle | Essential, latency-sensitive behavior remains local when the VPS, internet, cloud AI, or an optional module is unavailable. |
| HA ownership | HA remains authoritative for its device integrations, raw entity state, and service execution. Runtime reconciles cached state after disconnects. |
| Management | Runtime has a small local management and recovery surface; the full PWA is optional. |

## First implementation boundary

The first implementation should establish the smallest dependable Runtime foundation:

- Start on the HAOS add-on target and report its own health.
- Connect to HA through standard APIs, show connection/permission status, and handle disconnect and reconnect without treating stale cached state as current.
- Provide a local management/recovery path that does not require the VPS or PWA.
- Establish the Runtime's internal boundaries so later module supervision does not couple feature logic to HA internals.

Implementations may use small internal packages, but must not prematurely freeze a public module API or add feature modules just to demonstrate extensibility.

## Acceptance outcomes

The initial usable Runtime should demonstrate that:

1. It starts on a clean HAOS installation without the custom Bridge or optional feature modules.
2. It connects to HA with the minimum required access and exposes useful health and connection status locally.
3. HA disconnection leaves Runtime management available, marks HA-derived data stale, reconnects with bounded behavior, and reconciles state.
4. VPS, internet, cloud, and optional PWA outages do not prevent local Runtime management or local critical-path behavior.
5. A module failure cannot make Runtime management unavailable or silently replace the last healthy version.
6. Later automation activation can guarantee that only one version is permitted to issue actions at a time.

Latency, memory, disk, reconnect, and recovery thresholds must be measured and added here before claiming target-hardware readiness.

## Decisions still requiring an ADR or explicit review

- HAOS add-on configuration, required privileges, startup ordering, and interaction with Supervisor lifecycle.
- HA API credential provisioning and secure storage.
- Local console binding, authentication, authorization, and recovery access.
- HA REST/WebSocket reconnect, rate limits, state reconciliation, event ordering, and stale-state behavior.
- Runtime-to-module protocol, compatibility negotiation, health/readiness, timeouts, and backpressure.
- Module process containment, resource limits, requested capabilities, and least-privilege enforcement.
- Module package format, provenance/signatures, approved sources, installation approval, staged activation, rollback, and update policy.
- Automation execution leases, timers/state ownership, and draining in-flight work during activation.
- Persistence, diagnostic retention, household privacy, and remote/VPS access boundaries.
- Measured resource and latency budgets for the target HAOS hardware.

Until these are decided, agents may implement bounded local foundations that do not depend on them. They must stop before adding public contracts, executing modules, exposing control endpoints beyond loopback, or automating installation/update of module code.

## Non-goals for the foundation

- Replacing Home Assistant's integration and device ecosystem.
- Requiring the Bridge, VPS, internet, LLM, or full PWA for basic local operation.
- Defining a general module marketplace or allowing unreviewed code execution.
- Shipping every Housefold capability inside the Runtime repository.
- Treating event delivery as durable or exactly-once before those guarantees are specified.
