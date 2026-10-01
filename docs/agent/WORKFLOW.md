# Runtime v1 productization workflow

PRODUCTIZATION.md is the accepted product contract. The previous ADR implementation harness is complete historical evidence; this queue assembles and proves the installable product.

Run `python3 scripts/agent_tasks.py next`, start exactly one ready task, implement it completely, record evidence, mark it done and continue. Ordinary engineering decisions are delegated to the agent. Refactor existing packages/tests when necessary, but preserve accepted safety invariants.

## Authority and boundaries

- Full authority over Runtime code, packaging, BIOS, persistence, module protocol/lifecycle, catalog tooling, CI/release infrastructure and disposable test environments.
- Create Housefold repositories/Pages/distribution infrastructure when GitHub permissions allow and the real installation path requires them.
- Never touch the stakeholder's live HAOS instance or use real household actions, identities, activity or secrets.
- No self-waivers. A genuine external blocker stays BLOCKED and prevents dependent release work.
- Do not weaken a gate, replace HAOS acceptance with mocks, or publish a materially different artifact after acceptance.
- Normal HA App/Supervisor conventions are the default where PRODUCTIZATION.md does not require different behavior.

## Verification discipline

Use deterministic interfaces/clocks/barriers where practical. Bound queues, frames, logs, retries, downloads, archives, persistence and resource accounting. Review for duplicate execution, stale authority, blind unknown-action retry, dual active generations, descriptor/credential leaks, traversal/symlink/archive attacks, unbounded pressure, destructive recovery and privilege bypass.

Run focused tests and `bash scripts/agent_verify.sh`; use `--images` for packaging. Missing required evidence is BLOCKED, never PASS.

Release gates V1P12–V1P14 are mandatory. V1P12 must be adversarial and independent in method/context from implementation review. V1P13 must detect trend/leak behavior rather than a single happy-path benchmark. V1P14 must install the exact candidate via the actual Housefold HA App repository on clean disposable HAOS.

Evidence records entering/final HEAD, exact commands/results, acceptance mapping, failures/fixes, limitations and artifact identifiers/digests. Update STATE/HANDOFF after every task.

## Authorized catalog scheduling bypass

The stakeholder explicitly authorized continuing the rest of the harness while
V1P05 remains externally blocked. V1P03 and V1P11 may implement their independent
BIOS and distribution work against the existing verified catalog backend; neither
requires provisioning the official signer before implementation can begin.
Catalog unavailability must be explicit in BIOS. This changes scheduling only:
V1P05 acceptance is unchanged, signed hosting remains a V1P11 criterion, and
security/soak/HAOS/publication dependencies and gates remain mandatory.
