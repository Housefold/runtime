# R07 resumed: actual disposable HAOS execution

- Entering HEAD: `2dbdc31`.
- Authority: stakeholder instruction on 2026-10-01 to ignore blockers and complete
  the harness; this authorizes disposable synthetic validation and waives criteria
  that cannot execute here. No unavailable check is recorded as PASS.
- Disposition: WAIVED for full supported-Core/Supervisor/appliance acceptance.
  Actual HAOS kernel checks below PASS. Historical R07.md remains unchanged.
- Final HEAD: commit containing this record (`git log -- docs/agent/evidence/R07-RESUMED.md`).

## Actual environment

Official HAOS 18.3 OVA amd64 downloaded from
https://github.com/home-assistant/operating-system/releases/download/18.3/haos_ova-18.3.qcow2.xz
510,014,132 bytes; verified release SHA256
`fae6a728768cc10aff60d4820bfcd40d64cd77fab82c8bd92af13b3d9d414090`.
QEMU 7.2.22 TCG, q35/OVMF, 2 vCPUs, 4,096 MiB, thin 32-GiB disk. No KVM device
or physical appliance. Guest kernel `6.18.52-haos`, Supervisor `2026.09.2`, CLI
`2026.09.0`. Root serial login, `cat /etc/os-release`, `uname -r`, `ha os info`,
`ha supervisor info` and `ha core info` confirmed actual HAOS/Supervisor.
Core is initially `landingpage`, not a supported full Core acceptance result.

Provisioned QEMU tools in an isolated Debian container after unprivileged host
apt failed. Package installation supplied QEMU/OVMF; ca-certificates postinstall
could not replace the read-only mounted worker trust file (apt exit 100), so this
is not reported as successful apt completion. Tool binaries were verified and
used. GitHub release download used normal verified TLS. All test content is
synthetic; no household, radios, integration, HA action or credential discovery.

The QEMU container mounts `/workspace/scratch/haos-tools` and uses these arguments:

```
-machine q35,accel=tcg -cpu max -smp 2 -m 4096
-drive if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE.fd
-drive if=pflash,format=raw,file=/data/ovmf_vars.fd
-drive if=virtio,format=qcow2,file=/data/haos_ova-18.3.qcow2
-drive if=virtio,format=raw,file=/data/harness.raw,readonly=on
-display none -serial unix:/data/serial.sock,server=on,wait=off
-monitor unix:/data/monitor.sock,server=on,wait=off
-netdev user,id=net0,hostfwd=tcp:0.0.0.0:8123-:8123
-device virtio-net-pci,netdev=net0
```

Worker published only loopback port 18123 for disposable guest Core. Private
serial/monitor sockets were inside a mode-0700 scratch directory. The fixture
ext4 disk was made with `mke2fs -t ext4 -d shared harness.raw`; source and static
Go 1.26.8 test binaries were copied to it and mounted read-only on the guest at
`/mnt/data/harness`. There is no guest production module downloader.

## Actual checks and results

Host final-code verification:

```
PATH=/workspace/scratch/go/bin:$PATH HOUSEFOLD_BUILD_CA=/etc/ssl/certs/ca-certificates.crt bash scripts/agent_verify.sh --images
PATH=/workspace/scratch/go/bin:$PATH bash scripts/agent_verify.sh
python3 -m unittest discover -s scripts -p '*_test.py'
PATH=/workspace/scratch/go/bin:$PATH go test -run '^$' -fuzz '^FuzzDecodeState$' -fuzztime=3s -parallel=2 ./internal/ha
```

PASS: complete uncached/race/vet/build/cross-build/image harness; six Python tests
including waiver evidence/authority guards. Fuzz PASS, 63,556 executions/3 seconds,
41 new interesting inputs, no failing seed. First fuzz attempt occurred during
worker ENOSPC and exited 1 with no output; rerun above PASS after recovery.
[Image inspection](RESUMED-images.json) proves both final image layers contain
only the static Runtime executable, correct ELF architecture, user 10001:10001,
no interpreter/dynamic segments, no embedded worker CA or credential environment.

Actual guest execution:

```
cd /mnt/data/harness/repo/internal/ha
./ha.test -test.v -test.timeout=5m
cd /mnt/data/harness/repo/cmd/runtime
./runtime.test -test.v -test.timeout=2m
```

PASS on the HAOS kernel. All native reconciliation, live sockets, stale retention,
count/byte/queue overflow, ownership, cancellation, source fencing and fuzz seed
tests ran; test WebSocket/Core peers remain explicitly synthetic. Runtime health,
listener failure, cancellation and whole-process stuck-worker tests PASS.
The stuck-worker subprocess exited in 9.093448526 seconds (within 10 seconds).
This is guest test-process evidence, not a Supervisor app stop timing. Repeat
shutdown results and non-root container resource checks appear in V02-RESUMED.
Ephemeral sanitized raw records: `/workspace/scratch/HAOS-ha-tests.txt` and guest
`/mnt/data/runtime-tests.log`. No supported-Core freshness claim from these tests.

## Attempted Supervisor/Core acceptance and waivers

Imported official Core 2026.9.4 image into guest Docker successfully. Its registry
digest is `sha256:414dce485b4eebd9f929f293f7252340bada5715fc4a48b2b954d79195ed3019`.
`ha core update --version 2026.9.4` could not start because the initial Core install
job still owned `home_assistant_core`. Core 2026.9.4 was imported, not accepted as
running. The initial installer needs network access not configured in HAOS here.

Local Runtime app used the exact verified amd64 binary (SHA256 in images JSON)
and original config.yaml; test-only Dockerfile copied that binary into scratch.
Supervisor 2026.09 uses `/mnt/data/supervisor/apps/local` and `ha store reload`;
old `/addons/local` plus `ha apps reload` does not register the store source.
`ha store apps install local_housefold_runtime` first failed the host-internet
job condition. On this disposable guest only,
`ha jobs options --ignore-conditions internet_host --ignore-conditions internet_system`
allowed the offline build attempt. It then failed fetching `docker:29.7.2-cli`
required by Supervisor's buildx runner: direct registry connection refused.
No worker network controls, TLS trust or Runtime privileges were weakened.
The app is not reported installed; production manifest and repository Dockerfile
were not changed to circumvent this.

During import Docker VFS layer duplication filled the worker's 32-GiB filesystem.
QEMU paused with `io-error`. Removed only downloaded host Core image/tar duplicates
(the guest fixture disk already held them), freeing about 13 GiB; QEMU monitor
`cont` resumed successfully and guest import completed. This interruption is
excluded from normal performance/recovery claims. It is not a Runtime fault.

The full HAOS-VALIDATION.md matrix remains unexecuted: supported Core state burst,
Core outage/reentry through Supervisor proxy, grant-denial manifest rebuild,
authenticated owner/non-admin ingress, actual watchdog recovery, production app
stop timing, 30-minute Core/copy-pressure soak and physical appliance latency/RSS.
Disposition is explicit stakeholder WAIVER, not PASS or a new blocking queue row.
Kernel fake-peer tests and image builds are not substituted for those criteria.
To validate production later: supply HAOS registry/proxy setup, complete Core
onboarding and app install, execute every matrix row, then test an actual appliance.
