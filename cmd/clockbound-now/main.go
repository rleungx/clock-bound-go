package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	clockbound "github.com/rleungx/clock-bound-go"
)

func main() { os.Exit(run()) }

func run() int {
	shm := flag.String("shm", clockbound.DefaultClockBoundPath, "ClockBound v3 shared-memory file")
	vm := flag.String("vmclock", clockbound.DefaultVMClockPath, "VMClock device (required when enabled by daemon)")
	flag.Parse()
	client, err := clockbound.NewWithPaths(*shm, *vm)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer client.Close()
	result, err := client.Now()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	output := struct {
		Earliest     string `json:"earliest"`
		Latest       string `json:"latest"`
		Status       string `json:"status"`
		ErrorBoundNS int64  `json:"error_bound_ns"`
		Trusted      bool   `json:"trusted"`
	}{result.Earliest.Format(time.RFC3339Nano), result.Latest.Format(time.RFC3339Nano), result.Status.String(), int64(result.ErrorBound), result.IsTrusted()}
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !result.IsTrusted() {
		return 2
	}
	return 0
}
