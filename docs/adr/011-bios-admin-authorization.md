# ADR-011: HA-admin BIOS authorization and bounded management

- **Status:** Accepted (implements stakeholder PRODUCTIZATION direction)
- **Date:** 2026-10-01
- **Supersedes:** ADR-005 read-only/all-ingress-users behavior for Runtime v1

BIOS reads, catalog review, diagnostics and mutations accept only the actual
Supervisor ingress peer, 172.30.32.2, and exactly one Supervisor-injected
X-Remote-User-Id. Forwarded addresses, names and alleged admin headers are never
an authority. Supervisor strips client identity headers and injects session identity.

For every request a dedicated authenticated native Core command socket resolves
config/auth/list. The ID must identify an active non-system-generated HA owner or
system-admin member. Missing, ambiguous, revoked or unverifiable identity denies
access, including recovery/Core loss. No cached role survives revocation. User
names/credentials never leave the adapter. The unpublished port remains unchanged;
healthz is a separate coarse Supervisor watchdog route without admin authorization.

HTML forms use cryptographically random, five-minute, single-use approvals bound
to the verified user. At most 256 approvals exist. POST bodies are bounded to 8 KiB,
fields and operations allowlisted, duplicate fields rejected. One cancellable
30-second management worker and one result slot exist; concurrent operations are
rejected rather than queued. A disconnected browser does not cancel accepted
lifecycle work. New work is fenced during shutdown. All operations use the real
estate owner; there is no arbitrary path, binary or HA service management API.

Install review shows the verified dependency closure, compatibility, capabilities,
resources and outbound declaration. The submitted digest must match the catalog
at preparation; staging revalidates bytes/desired dependency state. Unconfigured
production catalog authority is explicitly unavailable, never replaced by fixtures.
Factory reset requires the literal DELETE ALL HOUSEFOLD DATA confirmation and
normal App restart. Recovery-only bootstrap retains reset authority without opening
or initializing damaged stores. Durable corruption never triggers automatic deletion.

Supervisor retains Runtime start/stop/restart/update/backup/live-log authority:
BIOS links to normal HA App controls. Bridge installation stays user-controlled.
Native fallback and ordered-state limitations are visible. Diagnostics exports use
an explicit allowlist, omit raw errors and never include household state or secrets.

Source checks: Supervisor api/ingress.py _init_header; HA Core components/config/
auth.py config/auth/list/_user_info. Real ingress-to-Core compatibility and role
behavior remain required exact-artifact disposable HAOS checks, not inferred from
loopback/unit tests. No acceptance gate is waived by this ADR.
