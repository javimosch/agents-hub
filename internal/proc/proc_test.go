package proc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

const helperEnv = "AHUB_TEST_PENDING_SIGNAL"

// A signal that arrives as a command finishes is caught by our listener but
// not yet passed on. Stopping the listener must not swallow it.
func TestASignalPendingWhenTheCommandEndsIsNotSwallowed(t *testing.T) {
	if os.Getenv(helperEnv) == "1" {
		f := forwardSignals() // never given a group: the command is over
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		for len(f.signals) == 0 {
			time.Sleep(time.Millisecond)
		}
		f.stop()
		time.Sleep(5 * time.Second) // the re-raised signal ends us first
		os.Exit(0)
	}
	helper := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	helper.Env = append(os.Environ(), helperEnv+"=1")
	var exit *exec.ExitError
	if err := helper.Run(); !errors.As(err, &exit) {
		t.Fatalf("got %v, want the signal to end the process", err)
	}
	status := exit.Sys().(syscall.WaitStatus)
	if !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Errorf("got status %v, want death by SIGTERM", exit)
	}
}
