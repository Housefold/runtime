# V02: Managed lifecycle validation on actual HAOS

- Entering HEAD: `e92219e`; tested Go source is B03 commit `2dbdc31` (no Go
  changes between it and this evidence).
- Authority: accepted ADR-007/008/009 and stakeholder instruction to continue
  through blockers with disposable synthetic validation.
- Result: PASS for the implemented private lifecycle libraries and synthetic
  process/container scope below on HAOS amd64. No appliance/production rollout
  or Supervisor-managed app acceptance claim.
- Final HEAD: local commit containing this record (`git log -- docs/agent/evidence/V02-RESUMED.md`).

## Environment and execution

Actual HAOS 18.3 OVA, kernel 6.18.52-haos, Supervisor 2026.09.2, Core landingpage;
QEMU 7.2.22 TCG q35/OVMF, 2 vCPUs, 4,096 MiB, thin 32-GiB disk. Exact official
image/digest and provisioning are in [R07](R07-RESUMED.md). Fake/injected actions
only; no Core onboarding, household integrations or production module artifacts.

Compiled static Go 1.26.8 test binaries from final Runtime source were copied to
a read-only ext4 fixture disk. Actual guest execution included child exec/socketpair,
private protocol, filesystem fsync/rename, persisted reopen, generation routing,
crash handling and real SIGTERM. This is not Linux-host output relabeled as HAOS.
The host complete harness separately ran race tests and both image architectures.

Imported the verified amd64 Runtime ELF into a guest-local scratch fixture image:

```
tar -C /mnt/data/harness/addon -c housefold-runtime | docker import -c 'USER 10001:10001' - housefold-runtime-fixture:resumed
```

Image ID `sha256:86c313aa747ec6a68e96bc4dc141c27e5987f70b9b6e607a1addfcb304439d1b`.
Binary SHA256 `66560e5cce91a2323800849b1a17f279a94347ec043f5338f84ec71942a271e6`
exactly matches the verified production architecture binary. This is a fixture
container, not an installed Supervisor app. No Docker API/socket or Supervisor
credential was mounted/passed to Runtime or module fixtures.

[Exact guest driver](V02-HAOS-command.sh) ran as `sh /mnt/data/v02-guest.sh`.
Both Runtime and test containers run user 10001:10001, read-only root, all
capabilities dropped, no-new-privileges, docker-default AppArmor. Module tests
have network=none, 256-MiB memory/one-CPU cgroup limits; Runtime has 64 MiB/one CPU,
only a loopback-published measurement port. The fixture source/binaries are
read-only; /tmp is a guest ext4-backed isolated writable test directory owned by
10001. The original artifact disk had private UID-1000 directory modes; first
non-root launch correctly failed with permission denied. Copied only public
synthetic sources to /mnt/data/v02-repo and granted read/traverse permission;
all final executable checks passed. An initial driver used absent wget; corrected
to installed curl. Neither setup failure is reported as a test PASS.

## Acceptance mapping and measured results

- Inherited IPC, descriptor/environment isolation, child cancel/kill/join,
  descendant output not holding joins, and actual RSS-budget kill: full module
  suite PASS three repetitions. Child code is the locally compiled synthetic
  helper. Parent test credentials are synthetic; child does not inherit them.
- Ready-before-cutover, once-only dispatch racing cutover, draining old authority,
  failed candidate/join, warm/final handover, isolated generation storage, retained
  versions/manual rollback and finite restart/quarantine: module suite PASS on
  HAOS under non-root/container limits. Durable storage reopens and corrupt/uncertain
  authority tests use actual guest filesystem; no event history is persisted.
- Execution admission/mode recovery, fake action accepted/rejected/unknown outcomes,
  boot/generation fencing, offline package signature/digest failures and Bridge
  source/fallback/wire fixtures: execution/action/packageverify/bridge suites PASS
  in separate non-root network-none containers.
- Availability during module crash/pressure: 12 curl health samples PASS; Runtime
  PID compared equal before/after. Module suite exit=0, OOMKilled=false. Runtime
  stayed available after the module test container exited.
- Sampled Runtime container memory 1.699–2.242 MiB of its 64-MiB cap, CPU at most
  0.20%; sampled active module test container 7.766–9.164 MiB of 256 MiB, CPU
  72.40–94.12%. These are Docker instantaneous samples, not RSS or true peak;
  exited containers report 0B and those values are excluded from active ranges.
- Actual Runtime Docker SIGTERM/control-request-to-exit 1.75 seconds (guest
  /proc/uptime, hundredth-second resolution); exit=0, running=false, OOMKilled=false,
  ten-second stop grant, no SIGKILL indicated.
- Whole-process stuck-worker fixture repeated three times on guest: 9.053249871,
  9.035392497 and 9.043618132 seconds; all PASS against the ten-second process
  bound. This tests the shared nine-second shutdown deadline, separately from
  normal actual Runtime stop. These repetitions ran after worker disk recovery.

Raw sanitized evidence: [driver/resource output](V02-HAOS-run.txt),
[container settings](V02-HAOS-container.txt), [test output](V02-HAOS-tests.txt),
[shutdown repeats](V02-HAOS-shutdown.txt). Driver ended V02_GUEST_PASS.

## Review and limits

Verified non-root process creation/private descriptors, ownership/reopen, cgroup
limits, no credential or Docker socket inheritance, ordered authority, fail-closed
uncertainty and health independent of child pressure. Synthetic actions could not
reach HA (network-none test containers). No downloaded production module executed.

This validates the implemented libraries with synthetic peers/router cases;
cmd/runtime still does not run a production module supervisor. No production
installer, full signed-artifact activation workflow, module daemon integration,
Supervisor-specific AppArmor profile or appliance resource envelope is claimed.
Storage tests cover actual durable writes/reopen, not a power-cut or VM-reboot
crash test. Module RSS sampling is provisional, not a mandatory nested sandbox.
Runtime memory is an empty/no-token baseline, not a 64-MiB HA cache pressure run.
TCG timing/CPU cannot be extrapolated to appliances or supported household loads.
The full supported-Core/Supervisor matrix remains explicitly waived in R07.
Physical appliance/arm64 execution remains unmeasured; arm64 image/binary checks
are build/ELF inspection only. A real supported-load envelope needs that later
production validation; this task's synthetic HAOS VM evidence is kept separate.


Cleanup: removed only the two named stopped synthetic fixture containers,
restored Supervisor job conditions using `ha jobs reset` (PASS), then requested
`ha host shutdown --no-progress` (command accepted). Private serial/test disks
are retained in scratch rather than committed. No production target was touched.
Final source documentation and task ledger passed `git diff --check`, driver
`bash -n`, and all six harness Python tests after terminal reconciliation.
