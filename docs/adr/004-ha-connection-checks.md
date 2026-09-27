# ADR-004: Bounded Home Assistant connection and retry lifecycle

- **Status:** Accepted
- **Date:** 2026-09-27
- **Owners:** Housefold maintainers

## Context

ADR-002 permits local Runtime access to the Home Assistant Core API proxy. Runtime needs a long-lived WebSocket session for state ingestion, bounded recovery when HA is unavailable or rejects authentication, and process health independent of HA. State synchronization and freshness are defined in [ADR-003](003-ha-state-cache-and-reconnection.md).

## Decision

- Start the local health listener before attempting HA connection.
- The WebSocket session is authoritative for operational HA connectivity and state readiness. A REST `GET /api/` may be used for startup or diagnostics, but must not override WebSocket state.
- Bound connection, authentication, subscription, and snapshot synchronization to 30 seconds unless an earlier caller deadline applies. Bound event buffering to ADR-003 limits and live-session liveness with the configured ping/pong deadline. These initial limits require HAOS measurement before target-readiness claims.
- After unavailable, denied, timeout, or synchronization failure, retry after 1, 2, 4, 8, 16, then 30 seconds. Keep later retry delays capped at 30 seconds until a candidate publishes successfully. Reset the delay after a successful generation.
- On WebSocket continuity loss, mark the current generation stale immediately, retain its data, discard any candidate, and reconnect. Never repair the stale generation incrementally.
- Log only coarse phase/status transitions. Do not log error details, response content, endpoint details, credentials, or household data.
- Keep process health independent of HA and cancel pending operations or retry waits promptly during shutdown.

These are initial operational bounds, not latency or availability targets. Measure request duration and recovery behavior on supported HAOS hardware before claiming target readiness.

## Consequences

- HA failure and denial do not restart Runtime or disable the local recovery surface.
- `Ready` describes the observed snapshot and buffered-event reconciliation from one continuous WebSocket session under ADR-003's limits. The public API does not provide event sequence numbers or silent-gap detection.
- Token changes require the normal app restart/recovery path.

## Alternatives considered

1. **Probe periodically and close every WebSocket.** Rejected for state readiness because polling can miss disconnects and cannot carry the state event stream.
2. **Retry at a fixed short interval.** Rejected because it creates unnecessary request load during long outages.
3. **Treat HA unavailability as Runtime unhealthy.** Rejected because Supervisor could restart Runtime repeatedly while Core is unavailable and weaken local recovery.

## Verification

- Focused tests cover state-machine transitions, authentication outcomes, bounded backoff, reset after successful publication, stale marking, reconnect, and cancellation.
- The adapter tests cover shared deadlines and do not expose credentials or HA payloads.
- Earlier REST and WebSocket proxy authentication was verified on a clean HAOS 18.3 generic AArch64 VM, including startup while Core was unavailable and later connectivity recovery. That test predates state ingestion and does not verify the new synchronization path. Measure recovery on supported appliance hardware.
