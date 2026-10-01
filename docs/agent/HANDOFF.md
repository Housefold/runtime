# Handoff

V1P01 and V1P02 are complete; evidence records/logs identify tested source and local image IDs. Continue `python3 scripts/agent_tasks.py next` with one active task. PRODUCTIZATION/tasks remain authoritative; no v1 release gate has passed.

Production module/HA/catalog/storage owners are still unwired. BIOS must enforce HA-admin access server-side and expose working estate operations, not stub controls. Private library tests alone do not prove composition. Preserve generation fencing, explicit unknown actions, bounded resources, credentials isolated from children, conservative recovery, native HA fallback and independent Runtime watchdog health.

Toolchain: PATH=/workspace/toolchain/go/bin:$PATH. Image verification also needs HOUSEFOLD_BUILD_CA=/etc/ssl/certs/ca-certificates.crt here. Digest-pinned official ECR Go builder avoids observed Docker Hub rate limiting. Docker/GitHub read access works. Push/publish only where accepted tasks/gates require and authorize it.

Only disposable environments and synthetic actions/data are authorized. Never touch the live HAOS household. V1P12 independent adversarial security, V1P13 measured soak and V1P14 exact candidate through actual App repository on clean supported HAOS remain mandatory.
