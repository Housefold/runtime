# ADR-001: HAOS Runtime app lifecycle and local recovery

- **Status:** Accepted
- **Date:** 2026-09-25
- **Owners:** Housefold maintainers

> Runtime v1 extension: [ADR-010](010-runtime-v1-product-composition.md) and [PRODUCTIZATION.md](../agent/PRODUCTIZATION.md) supersede the foundation-only restrictions where they authorize production modules, fenced actions, discovery and admin-only BIOS. The original decision and evidence remain historical; no historical waiver satisfies a v1 release gate.

## Context

The Runtime is a required local component deployed as a Home Assistant OS app (formerly called an add-on). It must start without the optional Bridge or feature modules, remain manageable when Home Assistant Core is unavailable, and leave a recovery path that does not depend on the Runtime process, VPS, internet, or PWA.

The Runtime specification leaves the app configuration, startup ordering, privileges, Supervisor integration, health reporting, and local recovery behavior open. This ADR is limited to those boundaries. HA credential provisioning, remote access, module execution and updates, and the public module contract remain out of scope.

Home Assistant apps run as Supervisor-managed containers. The current app configuration supports startup ordering, protected mode, AppArmor, a default container init, an optional health watchdog, ingress, and explicit port mappings. The app security guidance recommends avoiding host networking and requesting only required privileges.

## Decision

Ship the Runtime as a Supervisor-managed HAOS app and keep Supervisor responsible for container lifecycle and operator recovery.

- Start automatically on boot and use the `system` startup class so the local Runtime does not wait for Home Assistant Core. HA connectivity will be a separately reported dependency in a later slice.
- Keep app protection and AppArmor enabled. Do not request host networking, host namespaces, privileged capabilities, full access, Docker API access, or Supervisor API roles for the foundation.
- Run the Go Runtime as the foreground application process with the default container init enabled. The Runtime handles termination signals and exits within a bounded shutdown period; it does not launch or supervise child modules in this slice.
- Report non-sensitive Runtime liveness/readiness through a read-only Supervisor watchdog check and structured stdout/stderr logs. The initial decision did not add an ingress panel; [ADR-005](005-local-operator-status.md) later accepts a read-only ingress status page without adding a host port or control API. The health response must contain no household state, credentials, or identifiers.
- Use the Supervisor app controls and app logs as the initial local recovery path. A user can inspect logs and stop or start the app even when the Runtime is unhealthy. The recovery path must not require a Runtime endpoint.
- Persist no application state in this slice. Add persistent data/config mappings only when a separate decision defines the data and retention needs.

The watchdog endpoint is a health check only. It cannot change configuration, invoke HA actions, or operate modules. Its internal network reachability and Supervisor restart behavior must be verified on the supported HAOS test target before acceptance. If a safe internal-only check cannot be demonstrated without publishing a host port, use structured logs and Supervisor container state for this slice and record the health limitation.

## Consequences

- The Runtime can start and be recovered independently of HA Core and optional components.
- Supervisor remains the recovery authority if the Go process fails; there is no second in-container supervisor or separate control surface.
- Protected mode and the absence of host access reduce the consequences of a Runtime defect, while leaving HA API access for its own credential/permission decision.
- The watchdog provides process health only. HA connection health, stale state, and reconciliation are later Runtime health dimensions.
- The initial recovery path depends on access to the local Home Assistant Supervisor UI and its app logs. Headless recovery and a dedicated local console remain open for a later decision.
- HAOS behavior, including startup order, shutdown timeout, and watchdog reachability, must be verified on HAOS; a container build alone cannot establish acceptance.

## Alternatives considered

1. **Run a host-level service outside Supervisor.** Rejected because HAOS apps are the accepted deployment form and Supervisor would not own its installation or recovery lifecycle.
2. **Run with host networking, elevated privileges, or host namespaces.** Rejected because the foundation has no need for these rights and they weaken isolation.
3. **Expose a general Runtime management API or ingress console immediately.** A general management API remains out of scope. The initial ingress console was deferred until its access boundary was decided; [ADR-005](005-local-operator-status.md) later accepts a read-only ingress status page.
4. **Use only container running state and logs, without an app health check.** Retained as a fallback if HAOS verification shows a watchdog cannot be kept internal without opening a host port; it provides weaker readiness information.

## Evidence and follow-up

- The [Runtime specification](../runtime-spec.md) records the accepted app configuration and current implementation boundary.
- Home Assistant's [app configuration](https://developers.home-assistant.io/docs/apps/configuration/) documents startup classes, default init behavior, ingress, port mappings, and watchdog checks.
- Supervisor source at commit [`d0d259c`](https://github.com/home-assistant/supervisor/blob/d0d259cf73e9191185b00a2a14c8f8f04b42f9c2/supervisor/apps/app.py#L827-L867) constructs the watchdog URL from the app IP and container port when `host_network` is false, including when the host-port mapping is disabled. This matches the repository's internal-only `8099/tcp: null` configuration; confirm on the target HAOS release.
- Home Assistant's [app security guidance](https://developers.home-assistant.io/docs/apps/security/) recommends avoiding host networking, using AppArmor, mapping only necessary storage, and granting only required API access.
- Clean-install smoke test passed on the official HAOS 18.3 generic AArch64 image in QEMU/HVF on Apple Silicon. With no Bridge or feature modules installed, Supervisor discovered, built, and started the local app. The app started while the Core API was unavailable, then connected after Core became available. Supervisor reached `/healthz` over the app network and received `200 {"status":"healthy"}` with `8099/tcp` unpublished, `protected: true`, and the default AppArmor profile.
- In the same VM, killing the app container triggered the enabled Supervisor watchdog to restart it; an explicit stop/start completed cleanly; and a host reboot preserved the installed app and auto-started it. Logs showed the connection transition from unavailable to connected. HAOS app logs contained status and lifecycle messages only.
- This verifies HAOS 18.3 lifecycle behavior in a generic AArch64 VM, not appliance hardware, LAN discovery, USB-radio passthrough, or long-duration resource limits. Measure those on target hardware before claiming deployment readiness. If internal-only health reporting or `system` startup fails on a supported target, revise this decision and the implementation.
