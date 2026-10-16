# clock-bound-go

A Go client for [AWS ClockBound](https://github.com/aws/clock-bound). It returns
an interval that bounds UTC time at the instant of each sample.

It requires a ClockBound daemon on the same host. There are no CGO or
third-party Go dependencies.

## Requirements

- Go 1.22 or later.
- Linux on amd64 or arm64.
- A running ClockBound v3 daemon (tested with 3.0.0-beta.0).
- Read access to `/var/run/clockbound/shm1` and, when the daemon enables VMClock,
  `/dev/vmclock0`.

Follow the [daemon setup instructions](https://github.com/aws/clock-bound/blob/526b8a89d2a8b8ac461a3ab61cadc8c93d48a479/docs/clockbound-daemon.md)
to configure the host. The Go package does not install or start the daemon.

## Install

Run this in your application's Go module:

```sh
go get github.com/rleungx/clock-bound-go@latest
```

For a new application, run `go mod init example.com/myapp` first.

## Read the time

Save this as `main.go` and run `go run .` on the configured Linux host:

```go
package main

import (
	"fmt"
	"log"

	clockbound "github.com/rleungx/clock-bound-go"
)

func main() {
	if err := sample(); err != nil {
		log.Fatal(err)
	}
}

func sample() error {
	client, err := clockbound.New()
	if err != nil {
		return err
	}
	defer client.Close()

	now, err := client.Now()
	if err != nil {
		return err
	}
	if !now.IsTrusted() {
		return fmt.Errorf("clock is not trusted: %s", now.Status)
	}

	fmt.Printf("earliest: %s\nlatest:   %s\nstatus:   %s\nbound:    %s\n",
		now.Earliest, now.Latest, now.Status, now.ErrorBound)
	return nil
}
```

In a service, create one client at startup, reuse it for calls to `Now`, and
close it at shutdown. A client is safe to share between goroutines.

For custom paths, use `clockbound.NewWithPaths(shmPath, vmClockPath)`.

## Use the result

Always check both the error and `IsTrusted()`. A successful read can still
return an untrusted clock status.

| Status | `IsTrusted()` | Meaning |
| --- | --- | --- |
| `StatusSynchronized` | `true` | The daemon reports synchronization and its calibration is still recent. |
| `StatusFreeRunning` | `true` | The clock is free running; the interval includes accumulated uncertainty. |
| `StatusUnknown` | `false` | A time bound is unavailable or the calibration has expired. |
| `StatusDisrupted` | `false` | Clock continuity has been disrupted. |

For a trusted result, UTC time at the sample is between `Earliest` and `Latest`.
`ErrorBound` is half the interval width. The interval describes the time sampled
inside `Now()`; your application may resume later.

To compare two samples:

```go
if clockbound.DefinitelyBefore(a, b) {
    // Both samples are trusted, and a.Latest is before b.Earliest.
}
```

A `false` result means this check cannot establish that order. Intervals that
overlap or touch cannot establish a strict order.

## Command line

```sh
go install github.com/rleungx/clock-bound-go/cmd/clockbound-now@latest
clockbound-now
```

Add Go's binary directory to your `PATH`: `$GOBIN` if set, otherwise
`$(go env GOPATH)/bin`. From a source checkout, use `go run ./cmd/clockbound-now`.

The command prints one JSON object with `earliest`, `latest`, `status`,
`error_bound_ns`, and `trusted`. Exit codes are `0` for a trusted sample, `1` for
an error, and `2` for an untrusted sample.

Use `-shm /path/to/shm1` and `-vmclock /path/to/vmclock0` to override the paths.

## Containers

Run the daemon on the host and mount its shared-memory directory into the
application container:

```sh
docker run --rm \
  --mount type=bind,src=/var/run/clockbound,dst=/var/run/clockbound,readonly \
  your-app-image
```

If the daemon enables VMClock, also add
`--device=/dev/vmclock0:/dev/vmclock0:r`. The container user needs read access to
both resources. Use custom client paths if they are mounted elsewhere.

## Common issues

- **Cannot open shared memory:** check that the daemon is running and the
  application can read `/var/run/clockbound/shm1`.
- **Cannot read VMClock:** check `/dev/vmclock0` permissions and, in containers,
  device access. VMClock is required when the daemon enables it.
- **Unsupported version:** use a compatible v3 daemon. v1/v2 are unsupported.
- **Untrusted result:** check the daemon and its time source. Do not use this
  sample for decisions that depend on a bounded time interval.

## License

[MIT](LICENSE). See [NOTICE](NOTICE) for attribution.
