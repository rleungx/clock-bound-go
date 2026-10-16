//go:build linux && (amd64 || arm64)

package clockbound

import (
	"encoding/binary"
	"testing"
)

func TestNativeCounterAndPublicClient(t *testing.T) {
	before, err := readTSC()
	if err != nil || before == 0 {
		t.Fatalf("counter: %d %v", before, err)
	}
	after, err := readTSC()
	if err != nil || after < before {
		t.Fatalf("counter regressed: %d -> %d %v", before, after, err)
	}
	data := golden(t)
	binary.LittleEndian.PutUint64(data[16:], before)
	data[97] = 30 // realistic sub-ns counter period for this synthetic calibration
	path := fixtureFile(t, "clockbound", data)
	c, err := NewWithPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	result, err := c.Now()
	if err != nil || !result.IsTrusted() {
		t.Fatalf("native read of synthetic calibration: %+v %v", result, err)
	}
}
