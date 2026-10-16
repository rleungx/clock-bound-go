# Rust alignment and bounded regression criteria

The reference is AWS ClockBound `3.0.0-beta.0`, commit
`526b8a89d2a8b8ac461a3ab61cadc8c93d48a479`.
This record describes tested equivalence and intentional deviations. It is not
a claim that every possible input, availability metric, or performance metric
is identical or superior to Rust.

## What is gated

| Dimension | Acceptance criterion |
| --- | --- |
| ABI | Read the unmodified Rust writer's 104-byte v3 snapshot correctly |
| Error-bound correctness | The Go interval contains the exact rational interval defined by the upstream mathematical model |
| Precision | Error radius is at most `ceil(exact radius) + 4 ns` on supported, non-overflowing input |
| Trust status, same calibration | No upgrade from an untrusted Rust result to a trusted Go result in the differential corpus; threshold differences can only be conservative |
| Client state | Actual Rust constructor/reader/VMClock traces agree, except documented generation-reuse behavior and nanosecond rounding |
| Protocol evolution | Accept only the verified `3..3` header range; reject unverified layouts |
| Concurrency | Go race checks and concurrent read/publication/close tests pass |
| Performance | Measured separately; there is currently no full-client non-regression guarantee |

Normal `go test` consumes the checked-in Rust outputs offline. Therefore changes
to Go code must continue to satisfy these criteria in CI; Rust is not needed to
run normal tests.

## Why outward rounding is bounded

Let `x` be the exact projected elapsed nanoseconds, `c = ceil(x)`, `B` the integer
base bound, `d = max_drift_ppb / 1e9`, and `e` the decoded period-error fraction.
The protocol and validation give `0 <= d < 1` and `0 <= e < 1`.

The reference mathematical radius is:

```text
R = B + x*d + x*e
```

Go returns a center at `floor(x)` and a symmetric radius:

```text
G = B + ceil(c*d) + ceil(c*e) + (1 if x is fractional, otherwise 0)
```

The final term covers shifting the center down to an integer nanosecond. Both
endpoints enclose the exact model interval. Each upward division adds less than
one nanosecond, and replacing `x` by `c` adds less than two nanoseconds in total.
Thus `G - R < 5 ns`, and, since `G` is integral:

```text
ceil(R) <= G <= ceil(R) + 4 ns
```

Production code uses checked fixed-width integer operations. Independent
`math/big` calculations validate the arithmetic and interval properties. This
argument assumes a coherent calibration and the daemon's mathematical error
model; it does not independently prove the daemon's synchronization accuracy
or cover a malicious producer, hardware failure, or every compiler behavior.

## Actual Rust comparison

`tools/rust-differential.py` checks the upstream Git revision, clean tracked
files and SHA-256 of the original `shm.rs`. It copies the checkout to a temporary
directory and appends a helper that calls the original private
`compute_bound_at_tsc` method. The projection algorithm is unchanged.

For full client traces, the single hardware-counter read is replaced by an
injected deterministic counter. The actual Rust public client, constructor,
shared-memory reader, cache, mappings and VMClock reader then execute normally.
This does not exercise native hardware-counter instructions or migration.

Reproduce with a clean checkout at the pinned revision and the Rust dependencies
available in Cargo's cache:

```sh
python3 tools/rust-differential.py --upstream /path/to/aws/clock-bound --bench
go test -run 'TestDifferentialAgainstActualRust|TestReaderTracesAgainstActualRustClient' -v .
go test -run '^$' -bench '^BenchmarkProjection$' -benchtime=1s -count=3 .
```

Measured on 2026-10-16, Apple M2 Pro, macOS arm64, Go 1.27.2 and Rust 1.99.0:

- 831 comparable projection inputs: 819 equal statuses and 12 conservative
  status differences; no trust upgrade.
- Four explicitly classified inputs are rejected by Go: counter regression,
  negative bound, zero period, and invalid drift. These are not counted as
  equivalent successes.
- Maximum observed center difference: 1 ns.
- Maximum observed extra Go radius versus Rust: 4 ns.
- Maximum observed extra radius versus `ceil(exact radius)`: 2 ns in that corpus.
  The general budget above remains 4 ns and is also checked by randomized tests.
- Six full-client scenarios, 21 steps, pass: update/reset, sticky VMClock
  monitoring, enabling/recovering VMClock, generation reuse, unsupported daemon
  version, and unsupported VMClock version.

## Intentional differences and limits

Rust clamps a backwards counter delta to zero. Go returns an error. Go also
rejects malformed bounds and uninitialized periods explicitly, and reports
arithmetic overflow rather than constructing an interval. Calibration timestamps
are restricted to years 0001..9999 to keep Go calendar arithmetic within its
representable range. This is conservative
for correctness but does not imply equal availability on those inputs.

Go rereads payloads when a generation number repeats. Rust can reuse its cached
calibration. The generation-reuse trace verifies that Go observes the fresh
calibration instead of reproducing the stale-cache behavior.

Go's maximum retries are bounded at 1,000 and can be exhausted sooner than
Rust's limits. Consequently availability under sustained writer contention
has not been shown to be equal. Go uses a mutex for concurrent callers, while
Rust requires independent clients or external synchronization; contention cost
must be measured under the intended workload.

## Performance evidence

Only the pure timestamp-projection path has been compared locally, using the
same golden calibration and counter sequence, normal optimized builds, three
samples, and no race instrumentation:

| Implementation | Observed ns/op |
| --- | --- |
| Rust, client-enabled instrumented build | 23.370, 23.329, 23.368 |
| Go, after exact-arithmetic/status-path optimization | 30.10, 29.44, 29.50 |

Go allocates zero bytes per operation in this benchmark. Its median is about
26% slower in this particular comparison. An earlier Rust build without the
client feature measured 26.672–30.527 ns/op, illustrating sensitivity to build
and run conditions; it is not used to claim a speed advantage.

These measurements omit shared-memory reads, native counter instructions,
VMClock checks, mutex contention and daemon activity. They cannot establish
Linux `Now()` throughput or p99 latency. Native Linux amd64/arm64 and real-daemon
performance/integration remain unverified locally. The current evidence
supports bounded numerical and tested state-machine correctness, not a blanket
claim of "never worse".
