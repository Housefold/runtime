# ADR-005: Local Runtime status and recovery surface

- **Status:** Accepted
- **Date:** 2026-09-27
- **Owners:** Housefold maintainers

> Runtime v1 extension: [ADR-010](010-runtime-v1-product-composition.md) and [PRODUCTIZATION.md](../agent/PRODUCTIZATION.md) supersede the foundation-only restrictions where they authorize production modules, fenced actions, discovery and admin-only BIOS. The original decision and evidence remain historical; no historical waiver satisfies a v1 release gate.

## Context

Supervisor owns app start, stop, restart, and logs. Runtime exposes an internal `/healthz` watchdog check and a read-only ingress status page. The page must separate Runtime process health from HA WebSocket connection and state readiness without exposing household state or the Supervisor-provided Core API credential.

HA ingress authentication is available to signed-in HA users. The status view is low sensitivity and does not authorize management operations.

## Decision

Any authenticated Home Assistant user may view the page. `panel_admin` controls menu visibility only; it is not an authorization check. The page may show:

- Runtime process health.
- HA WebSocket lifecycle phase and coarse status (`connected`, `denied`, or `unavailable`).
- State freshness (`none`, `synchronizing`, `fresh`, or `stale`), local generation, entity count, and last successful sync time.
- Instructions to use Supervisor app controls/logs and HAOS host-console recovery.

Never show entity IDs, state values, attributes, event contents, credentials, or actor/user/context data. Runtime process health remains healthy while HA is unavailable or state is stale. See [ADR-003](003-ha-state-cache-and-reconnection.md) for the freshness boundary.

Keep `8099/tcp` unpublished and `hassio_api: false`. Serve only `GET /`; return 404 for unknown paths and 405 for other methods. Accept status-page requests only from Supervisor's documented ingress address `172.30.32.2`. Preserve `/healthz` for the Supervisor watchdog without the ingress-only source filter. Do not log ingress identity headers or add another credential.

The page is a status convenience, not a recovery authority. It is unavailable when HA Core's UI is down; use the HAOS host console and `ha apps` CLI then.

## Alternatives

1. **Keep logs and Supervisor controls only.** Safest, but no consolidated Runtime-versus-HA view.
2. **Use HA app ingress for read-only status.** Chosen because it reuses HA's authenticated local UI without a host port or separate credentials.
3. **Publish a host port or add separate local credentials.** Deferred because it adds another exposure and authentication boundary.

## Consequences

- Operators can see process health separately from HA session and cache state before optional modules exist.
- The Runtime still enforces ingress-source filtering because the container port is reachable on the app network while the host port is unpublished.
- Supervisor controls/logs remain the app recovery path; the HAOS host console remains necessary when HA Core UI is unavailable.
- The page reports metadata only and is not a control surface.

## Verification

- Unit tests cover ingress source filtering, forwarded-header spoof rejection, GET-only routes, unknown paths, watchdog access, safe metadata normalization, and the absence of entity contents.
- An earlier clean HAOS 18.3 generic AArch64 QEMU/HVF smoke verified the prior connectivity-only page for owner and non-admin users, protected mode, unpublished port, watchdog, and app lifecycle. It predates the state-sync metadata implementation; repeat HAOS verification when a VM is available.
- An isolated container test covered the ingress source allowlist and route/method behavior. Appliance hardware, direct non-ingress rejection against the running HAOS app, and long-duration behavior remain unverified.

## Evidence

- Home Assistant [app presentation guidance](https://developers.home-assistant.io/docs/apps/presentation/) documents ingress and the `172.30.32.2` gateway source address.
- Home Assistant [app security guidance](https://developers.home-assistant.io/docs/apps/security/) describes authenticated ingress sessions and user identity headers. Supervisor's [ingress handler](https://github.com/home-assistant/supervisor/blob/main/supervisor/api/ingress.py) does not authorize requests by HA admin membership; `panel_admin` is menu visibility, not endpoint authorization.
- [HAOS local CLI guidance](https://www.home-assistant.io/common-tasks/os) documents host-console recovery commands when Core UI is unavailable.
- [ADR-001](001-haos-runtime-lifecycle.md) defines Supervisor recovery; [ADR-003](003-ha-state-cache-and-reconnection.md) and [ADR-004](004-ha-connection-checks.md) define state and reconnect semantics.
