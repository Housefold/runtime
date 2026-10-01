# Synthetic HAOS validation and local recovery

This runbook is for a separately authorized disposable HAOS test VM or appliance,
with no real household integrations, radios, accounts, automations or secrets.
Do not run it on the owner's home. Builds, fake WebSockets and the R04 worker
measurements are separate evidence. This file describes experiments; it records
no successful HAOS execution by itself.

## Environment and evidence

Start a fresh supported HAOS image using its documented VM/appliance install.
Record exact image URL/digest, HAOS/Core/Supervisor versions and architecture,
virtualization/hardware model, CPU, RAM and storage. For V1P14, install the exact candidate from the actual Housefold HA App
repository; do not use a local checkout or sideloaded image. Local builds may
only supplement that gate. Record the app ID assigned by Supervisor (`APP_ID` below), build
architecture, image digest and manifest; retain the protected/AppArmor/default
role, no-host-port baseline. Do not add Docker API access to Runtime for testing.

Create a synthetic-only HA test user. For the state-generator helper use that
test instance's temporary token in the process environment `HA_TEST_TOKEN`;
never use/inspect the Runtime Supervisor token. Supply the exact test Core origin
as `HA_TEST_URL`. Keep token values out of command history, files, screenshots,
stdout and captured logs. Remove the test token/account at the end.

Run from a shell with Python 3:

```sh
python3 scripts/haos_synthetic.py seed --isolated-test --base-url "$HA_TEST_URL" --count 16
python3 scripts/haos_synthetic.py burst --isolated-test --base-url "$HA_TEST_URL" --count 16 --rounds 32 --interval-ms 5
python3 scripts/haos_synthetic.py cleanup --isolated-test --base-url "$HA_TEST_URL" --count 16
```

The helper writes only artificial `sensor.housefold_test_N` states via the
synthetic test Core API; it never calls services or discovers entities. Requests
are bounded (256 entities, 65,536 total writes/invocation, 256 KiB attribute
blob, ten-second request timeout), redirects refused, response data not retained.
POST `/api/states` generates state observations; it does not actuate hardware.
Do not treat its exit alone as ingestion proof. Correlate test sequence/time with
Runtime freshness/generation and the private regression tests below. There is
no production entity-read endpoint to inspect from the test helper.

Use [HAOS host-console CLI](https://www.home-assistant.io/common-tasks/os/)
for `ha core`, `ha supervisor`, `ha apps` controls/info/logs. Check `ha apps --help`
on the installed release; older releases may expose `ha addons` equivalents.
Record exact commands supported by that version. `APP_ID` is the slug returned
by the test Supervisor, not guessed from repository labels.

## Matrix

Capture UTC/monotonic test timings, result and sanitized evidence for every row.
Keep clocks aligned. Repeat each disruption at least three times and include a
30-minute event/copy-pressure soak; longer appliance tests remain separate.

| Case | Controlled experiment | Expected outcome and evidence |
| --- | --- | --- |
| Minimal boot | No Bridge, feature modules, VPS/cloud or custom integrations; disable test VM WAN after app build; start Runtime before Core, then start Core. | Watchdog remains healthy while HA unavailable; bounded retry (1,2,4,8,16,30 s cap); only complete snapshot/replay moves to Ready/fresh. First successful generation >0, no external dependency. Capture health/status timings separately. |
| Grant denial and recovery | In a separate throwaway copy, disable `homeassistant_api`, bump only the test app version/rebuild, and restart it; then restore the original manifest/rebuild/restart. Never mutate the production app. | HA is denied or token unavailable (record which Supervisor behavior); process healthy, no state published while denied. Restore original grant and observe a complete fresh generation. Manifest variant, version, actual failure category and recover latency recorded. Restart is expected here; Core restart below proves recovery without app restart. |
| Core restart/resync | Seed 16 states, start bounded burst; from host console `ha core stop`, wait for observed stale, then `ha core start`. Re-seed artificial states if Core did not retain them. | Runtime PID/app start time unchanged, health remains available, previous generation retained stale. Retry capped; after Core ready a higher generation resets revision 0 internally. No partial candidate publication. Record disconnect detection, Core-ready-to-fresh and total recovery latency. |
| Snapshot overlap | Start a burst while restarting Runtime; change/delete/recreate synthetic IDs using helper cleanup/seed. | Only a reconciled complete generation is fresh. Timestamp ambiguity may fail closed and retry; never silently resolve a conflicting equal timestamp. Correlate coarse status with the committed reconciliation/model tests. Record supported Core version; no claim of silent-gap detection. |
| Sustained burst | Increase rounds/count/blob in bounded helper while status/watchdog are sampled; stop generator before pressure threatens host. | Cache/queues stay bounded and health remains responsive. If ingestion buffer or payload bound is hit, retain last generation stale, discard candidate, resync once load ends. Record memory peak, reconnects and event throughput. A successful burst without overflow is not an overflow test. |
| Deterministic overflow fault | Run `go test -race -count=1 ./internal/ha -run 'TestBufferedEventOverflowAndRemovalRecreateOrdering|TestDistinctRemovalTombstonesAreBounded|TestExactPayloadAndInputBoundaries|TestPrivateSubscriptionLimitsAndTerminals'` against the same source. For actual HAOS overflow require a controlled test build capable of filling the buffer during sync and record that build/fault setup explicitly. | Unit faults prove count/byte fail-closed and independent private-consumer overflow. Current packaged Runtime has no private consumer attached or fault-control endpoint; label actual HAOS queue-overflow execution BLOCKED unless that test setup exists. Do not substitute unit output for HAOS evidence or expose a production fault API. |
| Ingress authorization/privacy | Sign in as test owner and non-admin; open status via ingress; try unknown path and POST. From a separately authorized test peer on the app network request GET / with forged forwarded/HA identity headers; also request /healthz. | Admin can manage BIOS; non-admin is denied server-side (panel_admin alone is not authorization); other peer gets 403 on / despite spoofed headers; unknown path 404; ingress POST 405; /healthz accessible as watchdog. No entity IDs/values/attributes or tokens in page/logs. Record actual peer IP and container network topology without granting Runtime host-network access. |
| Watchdog/lifecycle | Use local Supervisor app controls to stop/start/restart. In isolated VM stop the test app process to exercise configured watchdog recovery, following Supervisor-supported controls; do not grant Runtime Docker API access. | Supervisor detects failure/restarts according to its policy; healthy Runtime is not restarted merely because HA is down. Record watchdog interval and recovery time, app PID/start time; remove fault before repeating. |
| Graceful shutdown | While healthy, while Core down, and during sustained burst, use `ha apps stop "$APP_ID"`; measure from SIGTERM/control request through actual process exit, not just disappearance of health route. Repeat. | Entire process exits within Supervisor 10-second grant; HTTP has an 8-second sub-budget, shared process budget 9 seconds. Record exit code, whether Supervisor SIGKILL was needed, logs/timings; stop generator independently. The worker-host stuck-worker subprocess test is separate evidence. |
| Offline local recovery | With WAN disabled and Core UI down, inspect logs/app info and stop/start using HAOS host console. | Recovery requires no Runtime API, VPS, internet, Bridge or cloud. Restore Core locally; fresh generation and ingress return. Capture exact CLI used. |

No Core API promises an atomic snapshot/event barrier, replay positions or
silent-gap detection; these tests cannot strengthen ADR-003's freshness claim.
Synthetic entity state, timestamps and grants must never include household data.

## Measurement record

Copy this block into a new evidence file per VM/appliance run; do not overwrite
prior records or the harness audit. Report PASS/FAIL/BLOCKED per matrix row.

```text
Run ID/date/operator authority:
Runtime commit / app version / manifest / image digest:
HAOS image+digest / HAOS version / Core version / Supervisor version:
Architecture / hardware model / hypervisor / CPUs / RAM / storage:
Protected mode / AppArmor / Core proxy grant / published ports:
Bridge/modules absent / WAN outage method:
Synthetic fixture count / blob bytes / rounds / write rate:
UTC and monotonic clock method / sampling interval / repetitions / soak duration:
Initial sync latency / event observation latency (measurement method):
Disconnect detection / reconnect attempts / Core-ready-to-fresh latency:
Peak RSS / Go heap if instrumented / baseline RSS / CPU / host pressure:
Ingress peer / user roles / route and spoof-header results:
Watchdog interval / app PID/start time / recovery timing:
SIGTERM/control-request-to-process-exit / exit status / SIGKILL needed:
Each matrix result + exact commands + sanitized evidence:
Failures / limits / skipped checks / concrete restart conditions:
Appliance-specific versus VM-only conclusion:
```

Only coarse status is available in the production app, so event latency, revision
and Go heap require test-only measurement instrumentation or a private test
harness. Mark those fields unmeasured if absent; do not infer them from HTTP
health, helper request completion or compile success. RSS/CPU can be observed via
authorized host/VM facilities without changing the app's privileges. Record the
instrumentation and how its overhead changes results.

## Local recovery and cleanup

1. Stop the synthetic generator. Preserve only sanitized timing/metadata evidence.
2. Inspect test app status/logs through Supervisor or HAOS host console.
3. Stop the app; restore original manifest/grants/test permissions if changed.
4. Start app and Core; observe healthy process and new complete fresh generation.
5. Delete synthetic states with cleanup, remove temporary test credential/account,
   remove test ingress peer and dispose/reset the VM when evidence is saved.
6. If app cannot boot, Supervisor/host console remains the authority. Restore the
   last known-good app build using local app controls; do not download or execute
   modules as recovery. Record any inability to retain an older Runtime image.

No unattended install/update/module execution is enabled by this runbook.
