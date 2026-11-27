package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestThreadListFiltersByAgent(t *testing.T) {
	asked := make(chan string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked <- r.URL.Query().Get("agent")
		_, _ = w.Write([]byte(`{"records":[]}`))
	}))
	t.Cleanup(backend.Close)
	cmd := exec.Command(os.Args[0], "thread", "list", "--agent", "helper")
	cmd.Env = cliEnv(t, backend.URL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("thread list failed: %v: %s", err, out)
	}
	if got := <-asked; got != "helper" {
		t.Errorf("agent filter sent to bkn: got %q, want %q", got, "helper")
	}
}

// cliEnv isolates a CLI process from this machine's profile.
func cliEnv(t *testing.T, bknURL string) []string {
	t.Helper()
	home := t.TempDir()
	return []string{runMainEnv + "=1", "HOME=" + home,
		"AHUB_CONFIG=" + filepath.Join(home, "config.json"),
		"AHUB_TOKEN=test-token", "AHUB_BKN_URL=" + bknURL}
}
