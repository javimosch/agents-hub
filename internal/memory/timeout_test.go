package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const promptly = 5 * time.Second

// fakeMemgraph is a client whose binary is a shell script.
func fakeMemgraph(t *testing.T, script string, timeout time.Duration) *Client {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "memgraph")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return &Client{Bin: bin, Timeout: timeout}
}

func TestTimeoutHoldsWhenAChildKeepsTheOutputOpen(t *testing.T) {
	c := fakeMemgraph(t, "sleep 20 & wait", 300*time.Millisecond)
	start := time.Now()
	if _, err := c.Projects(); err == nil {
		t.Error("got success, want a timeout")
	}
	if took := time.Since(start); took > promptly {
		t.Errorf("a 300ms timeout took %s", took.Round(time.Millisecond))
	}
}

func TestOversizedOutputIsCappedWithoutStalling(t *testing.T) {
	c := fakeMemgraph(t, "head -c 3000000 /dev/zero", 20*time.Second)
	start := time.Now()
	data, err := c.Projects()
	if err != nil {
		t.Fatalf("got error %v, want the capped output", err)
	}
	if len(data) != maxOutput {
		t.Errorf("got %d bytes, want %d", len(data), maxOutput)
	}
	if took := time.Since(start); took > promptly {
		t.Errorf("took %s", took.Round(time.Millisecond))
	}
}
