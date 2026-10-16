# Self-check record

Checked on 2026-10-16 with Go 1.27.2, macOS arm64.
Upstream reference: `aws/clock-bound` commit
`526b8a89d2a8b8ac461a3ab61cadc8c93d48a479` (3.0.0-beta.0).

| Check | Result |
| --- | --- |
| Unmodified upstream Rust writer creates the golden fixture | Passed |
| Regenerated fixture matches checked-in bytes (`cmp`) | Passed |
| Actual Rust projection differential corpus (835 cases) | Passed: 831 comparable and 4 explicit policy rejections |
| Actual Rust client state traces (6 scenarios, 21 steps) | Passed; deterministic counter injection only |
| No trust upgrade on comparable differential inputs | Passed: 819 equal statuses, 12 conservative transitions |
| Numerical precision budget versus exact model | Passed: at most ceil(exact radius) + 4 ns |
| 4,000 192-bit shift cases against independent `math/big.Int` calculations | Passed |
| 3,000 projected intervals contain independent exact `math/big.Rat` intervals | Passed |
| Large integer bounds, backwards counter, overflow, sub-nanosecond rounding and expiry | Passed |
| Malformed ABI fields and unsupported versions | Passed |
| Generation reuse, daemon reset and stale calibration expiry | Passed |
| Concurrent shared-file publication and snapshot reads | Passed |
| VMClock mismatch after sampling; enabling support after restart; missing device | Passed |
| Concurrent `Now`/`Close`, resource release and zero-value client | Passed |
| `go test -race -coverprofile=coverage.out ./...` | Passed; library 93.6%, including CLI 86.2% |
| `go test -gcflags=all=-d=checkptr=2 ./...` | Passed |
| `go vet ./...` on host and Linux amd64/arm64 targets | Passed |
| Linux amd64/arm64 test binaries, including native-counter tests | Cross-compilation passed |
| Linux amd64/arm64 CLI builds with `CGO_ENABLED=0` | Passed |
| README's Go example imported by a separate module | Both Linux targets compiled |

The command-line executable was compiled, not exercised against a daemon;
its command dispatch has no local test coverage. The 93.6% figure applies to
the client library, not the entire repository.

The host cannot execute Linux binaries. Native Linux counter execution and
real EC2/ClockBound-daemon integration were not performed locally. The included
GitHub Actions workflow schedules native amd64 and arm64 checks when the
repository is pushed; that workflow has not been run in this session. These
checks do not independently establish the daemon's clock-synchronization
accuracy.

See `ALIGNMENT.md` for the precise acceptance criteria, the mathematical rounding
bound, instrumentation scope, and reproduction commands. In the local pure
projection microbenchmark, Go's median was 29.50 ns/op versus 23.368 ns/op for
the Rust client-enabled build (about 26% slower). This omits the full read path
and does not establish Linux throughput or tail latency. No blanket performance
or availability non-regression guarantee is claimed.
