# clock-bound-go

A Go client for [AWS ClockBound](https://github.com/aws/clock-bound), with no CGO
or third-party Go dependencies. It samples the hardware counter and reads the
daemon's calibration through read-only shared memory.

The client supports **Linux amd64 and arm64**, ClockBound **v3**, and VMClock
**v1** disruption markers. A ClockBound daemon must run on the same host.
Default paths are `/var/run/clockbound/shm1` and `/dev/vmclock0`. ClockBound v1/v2
segments and unverified version ranges are rejected. The currently verified
header version range is `3..3`. This project implements the client, not a Go daemon.

## Quick start

### 1. Prepare the Linux host

- Use Linux amd64 or arm64 and Go 1.22 or later.
- Install and run the official AWS ClockBound daemon on the same host. Follow
  the [upstream daemon setup instructions](https://github.com/aws/clock-bound/blob/526b8a89d2a8b8ac461a3ab61cadc8c93d48a479/docs/clockbound-daemon.md)
  for the required clock source, permissions, and service configuration.
- Use a daemon that writes the verified v3 header range `3..3`. This client was
  checked against ClockBound 3.0.0-beta.0; v1/v2 daemons are incompatible.
- Give the application read access to `/var/run/clockbound/shm1` and, when the
  daemon enables disruption detection, `/dev/vmclock0`.

The Go package reads calibration from the daemon. Installing the package alone
will not start a daemon or synchronize the host clock.

### 2. Install the Go package

Inside your application's Go module:

```sh
go get github.com/rleungx/clock-bound-go@latest
```

If this is a new application, run `go mod init example.com/myapp` first.
Before a release is tagged, Go resolves `@latest` to a pseudo-version of the
repository's default branch. Commit the resulting `go.mod` to pin that revision.

### 3. Read a bounded timestamp

Save this as `main.go`, then run `go run .` on the prepared Linux host:

```go
package main

import (
    "fmt"
    "log"

    clockbound "github.com/rleungx/clock-bound-go"
)

func main() {
    client, err := clockbound.New()
    if err != nil {
        log.Fatal(err)
    }
    defer client.Close()

    now, err := client.Now()
    if err != nil {
        log.Fatal(err)
    }
    if !now.IsTrusted() {
        log.Fatalf("no bounded-time guarantee: %s", now.Status)
    }
    fmt.Printf("true time at the sample was in [%s, %s], bound=%s\n",
        now.Earliest, now.Latest, now.ErrorBound)
}
```

A `Client` is safe for concurrent calls to `Now` and `Close`. Close it when it is
no longer needed. A zero-value client returns `ErrClosed`.

To override paths, use `NewWithPaths(clockBoundPath, vmClockPath)`. The VMClock
device is opened only when the daemon requests disruption detection. If it is
required but unavailable or unreadable, calls return an error. Enabling
VMClock support after a daemon restart is handled automatically. Once opened,
the VMClock remains monitored even if a later daemon restart clears the flag,
matching the pinned Rust client.

## Interpreting results

`StatusSynchronized` and `StatusFreeRunning` allow using the returned interval.
`StatusUnknown` and `StatusDisrupted` carry no bounded-time guarantee. Always
check `IsTrusted`, including when `Now` returns no error.

Two event samples establish a strict real-time order only when their trusted
intervals do not overlap:

```go
if clockbound.DefinitelyBefore(eventA, eventB) {
    // eventA's sample definitely preceded eventB's sample.
}
```

The interval describes the counter sample inside `Now`. Scheduling delays can
occur before the method returns. It does not describe the later return instant
or impose a total order on overlapping intervals.

## Command line

On a Linux host with a running daemon:

```sh
go install github.com/rleungx/clock-bound-go/cmd/clockbound-now@latest
"$(go env GOPATH)/bin/clockbound-now"
```

From a source checkout, you can also run `go run ./cmd/clockbound-now`.

The command prints JSON containing `earliest`, `latest`, `status`,
`error_bound_ns`, and `trusted`. Exit codes: `0` for a trusted result, `1` for an
error, and `2` for an untrusted result. Flags `-shm` and `-vmclock` override paths.

## Containers

Run the daemon on the container's host. Mount the host's shared-memory directory
at the same path in the application container, with read-only access. For
example, add this option to your container launch command:

```sh
--mount type=bind,src=/var/run/clockbound,dst=/var/run/clockbound,readonly
```

When the daemon enables VMClock, also expose `/dev/vmclock0` read-only through
your container runtime's device configuration. The container user needs read
permission for both resources. The client never connects to a remote daemon.
If paths differ inside the container, use `NewWithPaths` or the CLI flags.

## Troubleshooting

| Symptom | Action |
| --- | --- |
| `ErrUnsupportedPlatform` | Run the application on Linux amd64 or arm64. macOS supports portable tests only. |
| Opening shared memory fails | Start the daemon on this host, check the path, and grant the application read access. |
| `ErrUnsupportedVersion` | Check the daemon's shared-memory header; this client accepts only the verified `3..3` range. |
| VMClock open/read error | Expose the device and grant read access when the daemon requires disruption detection. |
| `IsTrusted()` is false | Treat the sample as unavailable for bounded-time decisions; check the daemon and clock source. |
| `ErrCounterBackwards` | Reject this sample and investigate counter continuity and daemon calibration. |

For local development against an unpublished checkout, replace the module path
with its local directory:

```sh
go mod edit -replace=github.com/rleungx/clock-bound-go=../clock-bound-go
go get github.com/rleungx/clock-bound-go@v0.0.0
```

## Protocol and numerical behavior

The ABI and same-input behavior were checked against upstream commit
[`526b8a89`](https://github.com/aws/clock-bound/tree/526b8a89d2a8b8ac461a3ab61cadc8c93d48a479)
(ClockBound 3.0.0-beta.0). The independent writer fixture and its reproducible
Rust generator are under `testdata` and `tools/upstream-fixture`. There are also
835 actual Rust projection outcomes and 21 actual Rust client trace steps.
See [ALIGNMENT.md](ALIGNMENT.md) for acceptance criteria, reproduction commands,
observed differences and performance limits.

Counter conversion uses integer arithmetic with 192-bit intermediates. Error
bounds round outward, including sub-nanosecond projection error; they may be a
few nanoseconds wider than the Rust client's floating-point truncation. A
backwards hardware counter returns `ErrCounterBackwards` rather than a trusted
sample. Overflow or calibration timestamps outside years 0001..9999 return
`ErrOutOfRange`. Returned uncertainty endpoints may extend beyond that calendar
range when the error bound itself is exceptionally large.

The reader uses acquire-ordered payload loads and the protocol's generation
checks. It rereads the payload even if the 16-bit generation matches an old
sample, since that number wraps. During a writer update or reset it may use
its last calibration, expanding uncertainty and applying the daemon's expiry
and 60-second free-running threshold. VMClock's live disruption marker is
checked after the time sample.

The daemon must preserve the mapped file/device while clients use it. If an
operator replaces the file or device with a new inode, reopen the client;
an existing mapping continues to reference the old object. Do not truncate a
live shared-memory file.

## Self-check

```sh
go test -race -cover ./...
go vet ./...
```

Tests include actual Rust-writer bytes, exact `math/big` interval checks,
malformed input, counter regression and overflow, expiry, generation reuse,
concurrent external writes, VMClock changes, and concurrent close. Portable
protocol and mapping tests run on macOS; public client constructors deliberately
reject macOS. Linux tests also exercise native counters and the public API.

The local check results and limits are recorded in `SELF_CHECK.md`. The supplied
GitHub Actions workflow runs the suite on native Linux amd64 and arm64 once
this project is pushed to GitHub.

## License

MIT. The upstream notice is retained in `LICENSE`; see `NOTICE` for attribution.
