# Runtime v1 — externally BLOCKED after authorized catalog bypass

Initial main: df3ebbe3d7dd11e4edd6832526dd88cdad6f54de. Stakeholder explicitly
authorized continuing independent work around V1P05 (CATALOG-BYPASS.md).
V1P03 BIOS and V1P07 log/diagnostics/audit are now DONE. V1P11 production
distribution preparation is implemented and verified but its actual official
source/signing/hosting criteria remain BLOCKED. Initial stop evidence is preserved
in BLOCKED-initial.md; it is history, not current completion status.

Completed ledger tasks: V1P01/02/03/04/06/07/08/09/10. V1P05 and V1P11 BLOCKED;
mandatory V1P12 security, V1P13 supported measured soak, V1P14 exact-artifact
clean supported disposable HAOS and V1P15 same-artifact publication remain pending.
No active or independent ready work remains. No acceptance or release dependency
was waived; only implementation scheduling for V1P03/V1P11 changed.

## Exact external constraints

- Housefold/modules and Housefold/home-assistant-apps creation denied by integration
  (createRepository). Existing runtime is a viable App-repo boundary: Supervisor
  supports metadata-only candidate/<source> and apps branches. This fallback is
  implemented; repository creation alone is no longer the App blocker.
- Latest signing-secret public-key API HTTP403; Actions administration HTTP403;
  Pages absent404; intended official catalog endpoint HTTP404. No actual
  maintainable Housefold signing custody or public pinned authority is configured.
  Production authority stays empty and signing CLI rejects fixture keys.
- Source writes and ordinary CI dispatch DO work: all cohesive local commits were
  imported using Git data APIs with identical tree/commit SHAs and fast-forwarded
  to agent/v1-productization after Git HTTPS returned401. Hosted Go/race/image
  verification passed at 3486ab3 and hardened 4ca2934. Settings/secret limitations
  must not be confused with ordinary code/CI permissions.
- Exact commands/errors: V1P11-infrastructure.txt, V1P11-final-signing-permissions.txt,
  V1P11-final-pages.txt, V1P11-final-catalog-http.txt,
  V1P11-source-api-import.txt and V1P11-final-source-import.txt. Implementation/acceptance mapping: V1P11.md and V1P05.md.

Two isolated builds at hardened production source 4ca2934 matched all 11 candidate
files, OCI archives/manifests and full receipts (V1P11-final-repro-comparison.json).
Candidate receipt explicitly says release_validated=false and authority is empty.
There is no release artifact that passed security, soak or HAOS acceptance.
Green builds, Docker/loopback/native tests and reproducibility are supplementary.

## What is required to resume

Provision maintainable authorized Housefold signing custody and a real maintained
official signed source/hosting with immutable prior-version history and object
retention, plus its pinned public authority. Grant the corresponding administration
capabilities, or provide equivalent Housefold-owned custody/source. Existing runtime
repository/branches can host the App feed without creating another repository.
Do not substitute fixture keys or unsigned remote metadata.

Resume V1P05, prove actual remote refresh/download/provenance, use implemented BIOS
review/lifecycle UX, then resume V1P11 and verify actual hosting/distribution pipeline.
Prepare a new exact-source artifact after provisioning changes; current development
candidates cannot certify a changed signing authority. Run independent adversarial
security and representative supported measured soak, stage the same candidate
through the actual App repo on clean supported disposable HAOS and complete every
acceptance item. Only then promote/publish that exact artifact and close V1P15.

Source is reviewable at https://github.com/Housefold/runtime/tree/agent/v1-productization.
Remote main is unchanged. No registry artifact, signed catalog or stable App feed
was published. No household identities/actions were used; stakeholder live HAOS
was never accessed, modified, deployed to or tested against.

Final current-state verification: BLOCKED-current-verification.txt records PASS
for Python tests, Go ordinary/race tests, vet, build and both-architecture binary
builds. BLOCKED-current-list.txt and BLOCKED-current-next.txt record the exact
ledger and no ready task. Prior image/repro/hosted CI logs remain supplementary.
The task-specific disposable BuildKit builder was removed; no persistent
household or Runtime data was involved in that cleanup.
