# Current state

Runtime v1 is IN PROGRESS, not release-ready. V1P01 reconciled current source/contracts, V1P02 ships zero-config normal App packaging, and V1P04 now composes the production module estate alongside native HA and ingress. See COMPOSITION.md, ADR-010 and evidence/V1P01.md, V1P02.md, V1P04.md.

V1P04 complete verification --images PASS: ordinary/race tests, vet/build, dual architecture cross-build/images and supplementary disposable packaging checks. Real synthetic native children exercise install, handover, dependencies, schedules, execution/cancellation, rollback, restart/quarantine, offline recovery, preservation and failures. A snapshot/inventory alias found by the full race suite was fixed and retained as evidence.

Runtime owns checksummed desired inventory, router/timeline/execution/action state, verified artifacts and four bounded storage scopes. Corruption/uncertain writes enter recovery-required with ingress preserved; module/HA loss stays separate. No functional module is preinstalled. Catalog/action/discovery/resource/backup/Bridge services and full authenticated BIOS still need their queue tasks. V1P03 dependencies now require those working services before BIOS completion. Nothing published, no live HAOS touched, no v1 release gate passed.

Use PATH=/workspace/toolchain/go/bin:$PATH and HOUSEFOLD_BUILD_CA=/etc/ssl/certs/ca-certificates.crt for image verification here. Continue first ready ledger task. Security, measured soak and exact-artifact real-repository disposable HAOS acceptance remain mandatory; historical waivers satisfy no v1 criterion.

V1P05 BLOCKED: catalog transport/cache/review/download/estate integration is implemented and repository --images verification passes, but the official remote source and signing authority cannot be provisioned. GitHub integration denied Housefold/modules creation and existing runtime Actions secrets access; intended catalog endpoint is absent. See evidence/V1P05.md and exact API logs. Production stays explicitly unconfigured with no synthetic trust key. Continue independent ready V1P06/V1P08 work; dependent BIOS/distribution/release criteria remain unmet.
