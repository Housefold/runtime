# Current state — BLOCKED

Runtime v1 is **incomplete and not release-ready**. Started from current main
at df3ebbe3d7dd11e4edd6832526dd88cdad6f54de. All independent ready work is committed;
there is no active task. `python3 scripts/agent_tasks.py next` reports V1P05 BLOCKED.

Completed implementation tasks: V1P01 contract/composition reconciliation,
V1P02 zero-config App packaging, V1P04 production module estate, V1P06 native
HA actions/discovery/signals, V1P08 resources/pressure/storage, V1P09 cold
backup/recovery/resets and V1P10 optional Bridge/native fallback. Each has focused,
ordinary/race/repository/image development evidence under evidence/V1P*.md.
V1P05 catalog backend/cache/verification/download/staging is implemented, but
production trust remains unconfigured: no official published signed catalog or
provisioned Housefold signing key. No synthetic key is trusted in production.

External blocker: current GitHub integration denies Housefold repository creation
and signing-secret access (403), despite reported runtime admin/push permission.
Existing runtime Actions configuration is also denied (403); Pages is absent
(404). Original and final recheck evidence/resume requirements are in
[evidence/BLOCKED.md](evidence/BLOCKED.md) and evidence/V1P05.md. Readable workflow/
OIDC metadata does not supply an actual catalog signing authority or source.

Remaining criteria: V1P05 official signed source and lifecycle UX; V1P03 full
server-authenticated HA-admin BIOS; V1P07 diagnostics/audit/log integration;
V1P11 real App/catalog distribution; V1P12 independent adversarial security;
V1P13 supported measured soak; V1P14 exact artifact through the real repository
on clean supported disposable HAOS; V1P15 publish that validated candidate.
Current ingress status is peer-restricted/read-only, not completed admin BIOS.
Ordered Bridge state stays disabled without pinned Python proof.

**No v1 release gate passed, no validated release artifact exists, nothing was
published, and no live Home Assistant/HAOS household was accessed.** Docker,
native-process and loopback tests are supplementary development evidence only.
No acceptance criterion or gate was weakened/waived.

Resume by provisioning maintainable official Housefold signing custody plus
hosting (or an equivalent actual signed source with its pinned public authority),
then `python3 scripts/agent_tasks.py resume V1P05` and `next/start`, complete its
actual remote-source checks and proceed through remaining dependencies/gates.
Use PATH=/workspace/toolchain/go/bin:$PATH and, on this worker only,
HOUSEFOLD_BUILD_CA=/etc/ssl/certs/ca-certificates.crt for image verification.
