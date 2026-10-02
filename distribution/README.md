# Housefold distribution preparation

These tools prepare distribution and enforce artifact identity. They do not provide
signing custody, prove measured gates, or substitute for actual disposable HAOS.
V1P05/V1P11 remain blocked until real official custody/hosting and remote operations
are verified. No current production key is replaced by a synthetic key.

Runtime source remains Housefold/runtime. An actual dedicated Housefold App
repository was attempted and denied by the integration. Supervisor supports Git
repository branches; the bounded alternative is metadata-only distribution branches
in the existing Housefold/runtime repository. `candidate/<full-source-sha>` is an
immutable staging tree for exact-artifact disposable HAOS. `apps` is the stable
metadata tree. Runtime `main` is never replaced by the publisher. Intended normal
HA App repository URL is `https://github.com/Housefold/runtime#apps`; candidate
acceptance adds `#candidate/<full-source-sha>`. Actual remote/HAOS discovery remains
a required gate; root source config is development packaging, not stable release.

## Offline candidate

`python3 scripts/release_candidate.py prepare --source <40hex> --version 1.0.0
--out /absolute/new/output --images` uses only link-free Git-archived source, Go
1.26.8, trimpath/buildvcs=false, commit-derived SOURCE_DATE_EPOCH and normalized
OCI layer timestamps. It produces amd64/aarch64 Runtime/launcher/reference-module
binaries, OCI archives, an installable metadata-only App repository and hashes,
manifest IDs, source/version/toolchain/compatibility/pinned-catalog-authority receipt.
No mutable workspace files, signing seed or unverified external catalog authority
are incorporated. Both archives retain their manifest identities when copied.

Candidate workflow performs repository verification on the requested exact source,
prepares archives and retains engineering build artifacts only. They are explicitly
unvalidated and never installed/published to the App feed by that workflow.
Runtime and reference modules declare Runtime/IPC compatibility independently;
adding a signed compatible module release never rebuilds Runtime.

## Stage and promote

`publish_candidate.py stage` requires V1P05/11/12/13 done and security/soak receipts
matching the exact source, all file hashes and both OCI manifest IDs. It transfers
existing OCI archives with skopeo --preserve-digests, refuses immutable tag conflicts,
rechecks remote manifest IDs and creates only the metadata candidate branch. Registry
credentials use a private auth file and never command arguments or image inputs.

`promote` also requires V1P14 done and matching actual disposable HAOS evidence:
repository installation, versions, complete acceptance matrix and same artifact.
Promotion requires the staged registry artifacts already present: it never uploads
or rebuilds a substitute. Only the dedicated `apps` metadata branch changes. No
force pushes and no Runtime source branch replacement. Existing branch contents
must match a strict metadata allowlist. Current ledger and missing production key
cause refusal before publication. Gate JSON is recorded evidence, not independent
review authority: the harness requires real reviewed findings/measurements/matrix.
Publication workflow serializes distribution and guards main-only invocation.

## Official catalog

`cmd/catalog-release` is an offline signer, never a publisher. Production CLI
requires the externally provisioned private 0600 seed file outside source and the
actual Runtime-pinned public authority/key ID. Empty current authority rejects all
fixture keys. `internal/catalogrelease` verifies target ELF, declarations, resources,
compatibility, metadata/artifact signatures and content-addressed bytes, rejects
traversal/links/special/excessive inputs, enforces increasing sequence against the
prior signed catalog, and rejects mutation of retained published versions.

Prepared output is a new atomic directory with only public signed catalog,
manifest envelopes, native bytes and artifact signatures. Existing release output
is never replaced. Keep immutable prior-version history and cached objects for at
least the supported catalog validity window; provision and validate this policy
before enabling publication. The `.yml.in` workflow is inactive preparation for the
actual official modules repo with protected secret custody and full tool-source pin.
Real Pages publication/retention and remote-source verification still need operator
permissions and authoritative signer/source provisioning; template existence does
not satisfy them. Nothing installs Bridge automatically or touches live HAOS.
