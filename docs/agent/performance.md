# Provisional developer-host measurements

30 September 2026; R04, Go 1.26.8, Linux amd64, Intel Xeon Platinum 8573C,
GOMAXPROCS 5. These describe this worker and synthetic shapes, not the Mac mini,
HAOS, deployment targets or a latency service-level guarantee.

| Shape | Entities | Attribute blob/entity | Canonical cache | Snapshot mean | Snapshot allocation | Per-entity read | Delta mean | Maximum delta delay with one snapshot reader |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Small | 100 | 32 B | 21,381 B | 0.084 ms | 84,856 B / 604 allocs | 2.75 µs | 30.3 µs | 0.205 ms |
| Large | 5,000 | 512 B | 3,487,781 B | 5.76 ms | 4,478,452 B / 30,018 allocs | 2.39 µs | 56.1 µs | 9.34 ms |
| Near limit | 16,000 | 3,600 B | 60,585,781 B | 24.43 ms | 15,063,344 B / 96,066 allocs | 2.10 µs | 67.3 µs | 27.32 ms |

Command: `go test ./internal/ha -run '^$' -bench '^BenchmarkPrivateState$' -benchmem -benchtime=10x -count=1`.
[Raw output](evidence/R04-bench.txt) includes allocations for delta and concurrent
reads. Ten iterations are exploratory; they do not establish percentiles.
Deltas preserve each shape's blob and nested attributes. Concurrent benchmark
allocation totals include the reader. Reader scheduling can affect the maximum;
there is no assertion of worst-case execution time.

The near-limit cache retained about 83.5 MB Go heap. Retaining a second candidate
and isolated snapshot used about 182 MB (3.00x canonical payload). This is
HeapAlloc after GC, not RSS, and excludes raw frames, JSON decode temporaries,
subscription resets, process overhead and maximal/deep attributes. Immutable
strings can be shared safely, while nested containers are cloned. Real shape
amplification may be much greater. [Memory/cancellation output](evidence/R04-memory.txt).

Provisional regression review budgets on this worker: one near-limit copy under
1 s; a single update delayed by one reader under 100 ms; cooperative session
cancellation under 1 s; normal SIGTERM under 1 s. Treat exceeding a budget as an
investigation trigger, not a new HAOS guarantee. Record CPU/load and repeat before
changing the ownership architecture. Current results do not justify a rewrite.

The old whole-process shutdown could wait indefinitely after HTTP shutdown;
a deliberately cancellation-insensitive worker exceeded ten seconds. Runtime
now starts a nine-second deadline at shutdown request covering HTTP and both
worker joins. The HTTP service retains its eight-second budget; these budgets
are concurrent, not additive. A deadline error makes main exit nonzero, terminating
remaining goroutines. This is a software timer with scheduler margin, not a
hard real-time claim. Supervisor owns recovery. Candidate decode/replay checks
cancellation between bounded operations; the ping worker is canceled and joined.
Standalone large JSON decode/serialization and deep copy are still synchronous.

An actual Runtime binary, without a token, served healthy and exited zero
0.00054 s after SIGTERM. The blocked-worker subprocess test must exit at the
whole-process deadline within the ten-second grant. Wire-stage tests synchronize
at dial/auth/subscribe/snapshot; a gated pong write proves cancellation during
an outstanding ping. A counted context proves cancellation between decode/replay
steps; near-limit copy cancellation while holding the read gate measured 17.5 ms.
Repeat R06 on HAOS/Core and appliance hardware before claiming readiness.
