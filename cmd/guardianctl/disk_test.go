package main

import (
	"errors"
	"testing"
)

func TestDiskSpaceVerdict(t *testing.T) {
	mk := func(pct float64) func(string) (float64, float64, error) {
		return func(string) (float64, float64, error) { return pct, 5, nil }
	}
	if err := diskSpaceVerdict("/x", mk(50)); err != nil {
		t.Errorf("50%%: %v", err)
	}
	if err := diskSpaceVerdict("/x", mk(85)); err == nil || !errors.As(err, new(errWarn)) {
		t.Errorf("85%% debería avisar: %v", err)
	}
	if err := diskSpaceVerdict("/x", mk(93)); err == nil || errors.As(err, new(errWarn)) {
		t.Errorf("93%% debería fallar: %v", err)
	}
	if _, _, err := diskUsage(t.TempDir()); err != nil {
		t.Errorf("diskUsage real: %v", err)
	}
}
