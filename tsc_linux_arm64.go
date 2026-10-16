//go:build linux && arm64

package clockbound

func readTSC() (uint64, error) { return readTimestampCounter(), nil }

func readTimestampCounter() uint64
