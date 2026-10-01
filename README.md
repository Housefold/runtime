# Housefold Runtime

Housefold's Go supervisor and local Home Assistant boundary. Runtime owns its
managed official module estate, generation-fenced work/actions, isolated storage,
resource/lifecycle recovery and optional Bridge enrichment.

## Current status

**Runtime v1 is incomplete and not release-ready.** The stakeholder authorized
continuing independent engineering while official catalog provisioning (V1P05)
remains externally blocked. Production catalog trust stays unconfigured; no
fixture authority is trusted. HA-admin BIOS is implemented; diagnostics/audit,
actual distribution and mandatory security/soak/exact-artifact disposable-HAOS
acceptance remain to complete. See [STATE](docs/agent/STATE.md),
[HANDOFF](docs/agent/HANDOFF.md) and [blocker evidence](docs/agent/evidence/BLOCKED.md).

Native HA state, actions, discovery and operational signals are composed;
verified native modules have transactional dependency/lifecycle management,
private IPC, handover/drain/retention, manual rollback, restart/quarantine,
bounded resource/pressure/storage policies and conservative cold-backup recovery.
HA/Bridge/module failure stays separate from Runtime process health. No
functional module is preinstalled. Credentials stay at Runtime's HA adapters.
BIOS checks current HA-admin membership server-side for every read and management
request. It exposes estate lifecycle, signed catalog review, resource/storage and
recovery operations; Runtime lifecycle/update/backup/live logs use normal HA App
controls. There is no host port mapping. Exact HAOS verification remains mandatory.

HA state is memory-only and excluded from status/logs. Native WebSocket freshness
has the boundary documented in [ADR-003](docs/adr/003-ha-state-cache-and-reconnection.md).
Bridge remains optional. Compatible discovery enrichment is detected over a
separate local Core socket; production ordered Bridge state stays disabled
without version-pinned Python snapshot/sequence proof. See
[Runtime specification](docs/runtime-spec.md) and accepted ADR-007/008/009/010.

## Development

Requires Go 1.26.8. Root config.yaml/Dockerfile are the development App build
context; real Housefold repository/catalog distribution is still V1P11.

```sh
bash scripts/agent_verify.sh
bash scripts/agent_verify.sh --images
```

Verification includes ordinary/race tests, vet/build and amd64/arm64 runtime and
launcher cross-builds. Image mode builds both architectures and runs isolated
synthetic packaging checks. These are development evidence, not HAOS acceptance.

```sh
docker buildx build --load --platform linux/amd64 --build-arg BUILD_ARCH=amd64 -f Dockerfile -t housefold-runtime:dev .
```

For ARM64 use `--platform linux/arm64 --build-arg BUILD_ARCH=aarch64`. When a
managed worker's HTTPS proxy needs its CA inside the builder, set
`HOUSEFOLD_BUILD_CA=/path/to/bundle.pem`; the optional build secret is absent
from final images. Ordinary builds use normal system trust.

No release candidate has passed the v1 security, representative soak or real-
repository clean-HAOS acceptance gates, and nothing was published in this run.
Historical foundation HAOS smoke is not evidence for this source/artifact.
The stakeholder's live installation is never a productization test target.

See [AGENTS](AGENTS.md), [CONTRIBUTING](CONTRIBUTING.md), and the authoritative
[productization contract](docs/agent/PRODUCTIZATION.md). The task ledger has no
autonomous waivers; `python3 scripts/agent_tasks.py next` exposes remaining blockers.
