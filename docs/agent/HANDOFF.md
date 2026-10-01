# Handoff

The stakeholder review accepted ADR-007/008/009 and replaced the old G01/G02 epics with a small executable implementation queue.

Start with python3 scripts/agent_tasks.py next. Work one task at a time, prove fault behavior, write evidence, update STATE/HANDOFF and commit each cohesive slice. Continue through all ready tasks without waiting for routine implementation approval.

Key invariants: Runtime is the sole HA boundary; official modules are trusted Runtime-managed processes using private inherited IPC; no per-entity grant engine; zero-downtime readiness-before-cutover; new work routes only to the active generation while old work drains; no automatic module rollback; Runtime owns durable timeline/admission; state handover is optional and version-negotiated; unknown HA actions are never blindly retried; retry exhaustion quarantines the selected version.

Bridge is preferred when proven compatible but remains optional. Do not implement Bridge-selected ordered state (B03) until the Python side supplies the required sequencing/barrier evidence. Do not claim HAOS lifecycle readiness (V02) without an authorized HAOS target.

No push, deployment, release, real-home action, production module download or third-party module execution is authorized.
