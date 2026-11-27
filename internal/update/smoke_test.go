package update

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSmokeTestGivesUpOnABuildThatNeverAnswers(t *testing.T) {
	old := smokeTimeout
	smokeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { smokeTimeout = old })
	path := filepath.Join(t.TempDir(), "ahub")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 8\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := smokeTest(path); !errors.Is(err, ErrSmokeTest) {
		t.Errorf("got %v, want a smoke test failure", err)
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("took %s", took.Round(time.Millisecond))
	}
}
