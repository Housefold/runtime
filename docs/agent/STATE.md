# Current state

Runtime v1 is IN PROGRESS, not release-ready. V1P01 reconciled actual source/contracts (COMPOSITION.md, ADR-010). V1P02 ships zero-config normal App packaging, private writable estate bootstrap, privilege dropping, CA roots, digest-pinned dual architecture builder and CI verification. See evidence/V1P01.md and V1P02.md.

V1P02 focused plus repository --images verification PASS, including ordinary/race tests, vet/build, cross-builds, both images and disposable packaging/UID/recovery/restart/shutdown checks. Docker Hub 429 was resolved using the matching digest from the official public ECR mirror. These checks are not HAOS acceptance.

Actual runtime still primarily composes native HA/status; modules/action/discovery/catalog/Bridge need production wiring. Admin panel visibility is enabled; BIOS server-side enforcement remains pending. Nothing published, no live HAOS touched. Historical waivers satisfy no v1 criterion.

Use PATH=/workspace/toolchain/go/bin:$PATH and HOUSEFOLD_BUILD_CA=/etc/ssl/certs/ca-certificates.crt for image checks on this worker. Continue first ready ledger task. Security, soak and exact-artifact real-repository disposable HAOS gates remain mandatory.
