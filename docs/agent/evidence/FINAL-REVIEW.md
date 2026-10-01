# Final solo review

- Source HEAD before review: `b73a01d91de060c51ab61fbeefa930fa6bc9bed4`.
- Result: PASS on developer Linux; environment gates remain BLOCKED.
- Final source: local commit containing this record (`git log -- docs/agent/evidence/FINAL-REVIEW.md`).
- Queue: 13 newly ready implementation tasks completed with per-task evidence and
  local commits. `python3 scripts/agent_tasks.py next` reports no ready task.
- Authority: accepted ADR-007/008/009 and the solo harness; no sub-agents used.

## Review findings and fixes

1. Uncertain execution commits could leave old in-memory tokens appearing
   authoritative. Manager now fences delivery/admission/ownership, and router
   authority also requires healthy execution persistence. Regression:
   `TestUncertainPersistenceFencesExistingAuthority`.
2. Retirement deleted child ownership before join success; candidate/crash paths
   could also forget an unjoined writer. Generations remain tracked until join,
   failed joins are retriable, restart waits for old writer join, and retirement
   follows join. Regressions: failed join/candidate/crash tests in router/recovery.
3. Go-created descriptors are CLOEXEC, but a preexisting externally opened handle
   might lack that flag. Launch marks unrelated handles CLOEXEC before explicit
   ExtraFiles duplication. Synthetic sentinel test deliberately clears the flag.
   Parent descriptors remain usable. Runtime owns descriptor creation; this is
   not hostile sandbox enforcement.
4. Discarding output through os/exec copier pipes could let descendants hold up
   child Wait. Output now goes directly to a file-backed /dev/null; local synthetic
   descendant test proves direct-child exit joins and kills its owned group.
5. Durable files now explicitly test interrupted partial writes and reject
   symlink state paths. Generation paths reject symlink directories. Raw JSON with
   literal HTML characters exposed checksum failure caused by envelope escaping;
   `TestRawJSONRoundTrip` reproduced it, then non-escaping envelope serialization
   fixed it. Escaped/Unicode/numeric JSON roundtrips are also checked.
6. Lost Bridge negotiation now marks retained discovery stale, without changing
   native HA state. Action input accepts valid object JSON with leading whitespace.
   Invalid initial timeline time no longer leaves a corrupt newly created file.
7. README/spec/decision notes reconcile historical Proposed language with accepted
   decisions and completed internal foundations. Production and environment limits
   remain explicit.

## Exact verification

`PATH=/workspace/scratch/go/bin:$PATH go test ./internal/module ./internal/execution ./internal/durable ./internal/action ./internal/bridge ./internal/timeline` passed.

```text
ok  	github.com/housefold/runtime/internal/module	(cached)
ok  	github.com/housefold/runtime/internal/execution	0.896s
ok  	github.com/housefold/runtime/internal/durable	(cached)
ok  	github.com/housefold/runtime/internal/action	0.011s
ok  	github.com/housefold/runtime/internal/bridge	0.011s
ok  	github.com/housefold/runtime/internal/timeline	0.023s
```

`PATH=/workspace/scratch/go/bin:$PATH go test ./internal/module ./internal/durable` passed after final output/JSON fixes.
`CGO_ENABLED=0 GOOS=linux GOARCH=arm64 PATH=/workspace/scratch/go/bin:$PATH go build ./...` passed, including the new library packages.
`PATH=/workspace/scratch/go/bin:$PATH bash scripts/agent_verify.sh` passed on final code (formatting, uncached tests, race tests, vet, build, Linux amd64/arm64 Runtime cross-builds, Python harness checks and diff whitespace).

```text
....
----------------------------------------------------------------------
Ran 4 tests in 2.037s

OK
go version go1.26.8 linux/amd64
linux
amd64
1
ok  	github.com/housefold/runtime/cmd/runtime	9.013s
ok  	github.com/housefold/runtime/internal/action	0.019s
ok  	github.com/housefold/runtime/internal/bridge	0.009s
ok  	github.com/housefold/runtime/internal/discovery	0.008s
ok  	github.com/housefold/runtime/internal/durable	0.004s
ok  	github.com/housefold/runtime/internal/execution	0.771s
ok  	github.com/housefold/runtime/internal/ha	0.964s
ok  	github.com/housefold/runtime/internal/module	0.265s
ok  	github.com/housefold/runtime/internal/packageverify	0.009s
?   	github.com/housefold/runtime/internal/state	[no test files]
ok  	github.com/housefold/runtime/internal/supervisor	0.007s
ok  	github.com/housefold/runtime/internal/timeline	0.023s
ok  	github.com/housefold/runtime/cmd/runtime	10.051s
ok  	github.com/housefold/runtime/internal/action	1.062s
ok  	github.com/housefold/runtime/internal/bridge	1.067s
ok  	github.com/housefold/runtime/internal/discovery	1.049s
ok  	github.com/housefold/runtime/internal/durable	1.016s
ok  	github.com/housefold/runtime/internal/execution	6.923s
ok  	github.com/housefold/runtime/internal/ha	7.852s
ok  	github.com/housefold/runtime/internal/module	4.930s
ok  	github.com/housefold/runtime/internal/packageverify	1.056s
?   	github.com/housefold/runtime/internal/state	[no test files]
ok  	github.com/housefold/runtime/internal/supervisor	1.023s
ok  	github.com/housefold/runtime/internal/timeline	1.109s
PASS: requested checks completed; HAOS/hardware validation is separate
```

## Remaining limits and handoff

R07 and V02 require authorized HAOS/appliance targets; none was supplied. B03
requires Python ordered-state barrier/sequence evidence. These remain BLOCKED,
not PASS. No production module artifacts, real-home HA actions, Bridge installation,
third-party execution, network listener, deployment, release or push occurred.

Tests exercise private foundations and synthetic/injected boundaries. cmd/runtime
still runs its existing HA foundation without module or Bridge dependencies.
Production installer/action/discovery/lifecycle wiring, cron parsing, a public SDK,
HAOS measurements and signing authority/catalog freshness integration are not
claimed by this queue. Serialized bounds are not heap/RSS guarantees. Packaging
was unchanged; image verification was not rerun or claimed. Prior R08 evidence
remains historical evidence only.

Continue only when a blocked gate gets its concrete restart condition or a new
accepted task is assigned. See STATE/HANDOFF and tasks.json.
