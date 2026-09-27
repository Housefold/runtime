# ADR-002: Runtime access to the Home Assistant Core API

- **Status:** Accepted
- **Date:** 2026-09-25
- **Owners:** Housefold maintainers

## Context

The Runtime must establish REST and WebSocket connectivity to Home Assistant Core, report connection and permission status, and later reconcile HA-owned state after reconnects. The accepted HAOS app decision keeps the Runtime protected and denies Supervisor API access by default. The Runtime must not persist or log a credential, and modules must not receive it.

Home Assistant documents two relevant paths: an HA app can request access to the Core API proxy and use a Supervisor-provided token, or an operator can configure a Home Assistant user's long-lived access token for direct API access. Neither is documented as a per-endpoint, read-only app permission. A Core API credential can authorize more than the initial connection check, including state reads and service calls.

This decision covers only the Runtime-to-Core credential and minimum initial operations. It does not grant HA API credentials to modules, authorize actions, define remote access, or decide what household state is retained.

## Decision

Use the HAOS Supervisor Core API proxy for Runtime connectivity. Enable only `homeassistant_api: true`; keep `hassio_api: false` and the default Supervisor role. Use the proxy's REST and WebSocket URLs and its runtime-provided token. Do not ask the user to paste or store a long-lived token.

Under the current Runtime boundary, the adapter may:

- Authenticate a WebSocket session and use `subscribe_events` for `state_changed`, `get_states`, and `ping` for session liveness as defined by [ADR-003](003-ha-state-cache-and-reconnection.md).
- Use REST `GET /api/` only as an optional startup or diagnostic check; it cannot override WebSocket connection or freshness status.
- Report only coarse phase/status transitions and cache metadata; never include tokens, host details, or household state in health output, status, or logs.
- Do not issue HA service calls or use other Core API operations without a separate approved decision.

Keep the token inside the Runtime's HA adapter. Do not put it in Runtime configuration, persistent files, child-process environments, diagnostics, or logs. If token retrieval or either transport fails, report HA as unavailable/degraded while keeping the Runtime and local recovery surface available.

The memory-only state-fetch contract, staleness, bounds, and ordering behavior are accepted in [ADR-003](003-ha-state-cache-and-reconnection.md). Before issuing any HA service call, approve the operation scope and authority separately.

## Consequences

- The native HAOS path avoids manual credential entry, local token persistence, and token rotation UX.
- `homeassistant_api: true` grants the Runtime process access to the Core API proxy; it is broader than the approved operations above. The Go adapter must enforce the operation boundary, and no module receives the token.
- `hassio_api` remains disabled, so this decision does not grant the app general Supervisor API access.
- If the process is compromised, the Core proxy grant may permit more Core operations than the approved state-read boundary. Protected mode and least-privilege container settings reduce other host access but do not narrow Core API authorization.
- The integration depends on Supervisor providing a usable proxy token and proxy availability. It remains local to HAOS and does not depend on internet, VPS, or cloud services.

## Alternatives considered

1. **Operator-provided long-lived token for a dedicated HA user.** Could be evaluated if the Core proxy grant is too broad. It requires deciding HA account permissions, secure app-option/storage handling, token expiry and rotation, revocation, and user recovery. Home Assistant documents user-generated long-lived tokens, but the Runtime currently has no approved secret-storage path.
2. **Grant Supervisor API access or an elevated Supervisor role.** Rejected for this purpose; Core API proxy access is separate and does not require a general Supervisor API role.
3. **Do not connect to HA until a narrower app permission exists.** This avoids accepting a broad Core API capability but prevents the baseline REST/WebSocket connection outcome. Revisit if the owner rejects the proposed trust boundary.

## Evidence and follow-up

- The initial implementation used repeated REST/WebSocket connectivity probes. The current state-session operation and retry lifecycle are in [ADR-003](003-ha-state-cache-and-reconnection.md) and [ADR-004](004-ha-connection-checks.md); the old probe tests do not establish the state-sync behavior.
- State-session tests cover authentication denial, bounded synchronization, and reconnect behavior. A clean HAOS 18.3 generic AArch64 VM previously verified proxy authentication in protected mode with the default Supervisor role, but it predates state ingestion. Verify the new session on supported HAOS hardware and confirm no credential appears in logs or status output.

- Home Assistant [app communication](https://developers.home-assistant.io/docs/apps/communication/) documents Core API proxy access using `homeassistant_api: true`, `SUPERVISOR_TOKEN`, `http://supervisor/core/api/`, and `ws://supervisor/core/websocket`.
- Home Assistant [app configuration](https://developers.home-assistant.io/docs/apps/configuration/) documents `homeassistant_api` separately from `hassio_api`; its default is false.
- Home Assistant [app security](https://developers.home-assistant.io/docs/apps/security/) documents that `hassio_role` governs Supervisor API access, distinct from Core API proxy access.
- Home Assistant's [REST](https://developers.home-assistant.io/docs/api/rest/) and [WebSocket](https://developers.home-assistant.io/docs/api/websocket/) docs describe user-token authentication; the [permissions model](https://developers.home-assistant.io/docs/auth_permissions/) is user/group based, not an app endpoint allowlist.
- On a supported appliance target, measure recovery after a Core restart and confirm no credential appears in logs or health output. The app permission grants Core API proxy access; it is broader than the approved operations and is not an endpoint-level allowlist.
