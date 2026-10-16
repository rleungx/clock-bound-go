//go:build !linux || (!amd64 && !arm64)

package clockbound

func readTSC() (uint64, error) { return 0, ErrUnsupportedPlatform }
