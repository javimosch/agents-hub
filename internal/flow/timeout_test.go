package flow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests use real processes and real time: what a deadline does to a
// process tree is the behaviour. Children sleep far longer than any bound, so
// a pass never depends on how fast the machine is.
const (
	shortTimeout = 300 * time.Millisecond
	promptly     = 5 * time.Second
)

var shLimits = Limits{Commands: []string{"sh"}, Timeout: shortTimeout}

// childScript starts a long-lived grandchild, records its pid, then waits.
func childScript(t *testing.T, spawn string) (script, pidFile string) {
	t.Helper()
	pidFile = filepath.Join(t.TempDir(), "pid")
	t.Cleanup(func() {
		if pid := readPID(pidFile); pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return spawn + " & echo $! > " + pidFile + "; wait", pidFile
}

func readPID(file string) int {
	raw, _ := os.ReadFile(file)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return pid
}

// running is false once a process has exited, reaped or not.
func running(pid int) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	_, after, _ := strings.Cut(string(raw), ") ")
	return !strings.HasPrefix(after, "Z")
}

func eventually(ok func() bool) bool {
	for end := time.Now().Add(promptly); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return true
		}
	}
	return false
}

func wantGone(t *testing.T, pidFile string) {
	t.Helper()
	pid := readPID(pidFile)
	if pid == 0 {
		t.Fatal("the grandchild never started; the test proves nothing")
	}
	if !eventually(func() bool { return !running(pid) }) {
		t.Errorf("grandchild %d is still running", pid)
	}
}

func wantTimeout(t *testing.T, err error, since time.Time) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("got error %v, want a timeout", err)
	}
	if took := time.Since(since); took > promptly {
		t.Errorf("a %s timeout took %s", shortTimeout, took.Round(time.Millisecond))
	}
}

func TestTimedOutCommandTakesItsChildrenWithIt(t *testing.T) {
	script, pidFile := childScript(t, "sleep 20")
	start := time.Now()
	_, err := RunCmd(context.Background(), shLimits, "sh", []string{"-c", script}, "")
	wantTimeout(t, err, start)
	wantGone(t, pidFile)
}

func TestTimedOutJobCommandTakesItsChildrenWithIt(t *testing.T) {
	script, pidFile := childScript(t, "sleep 20")
	log, err := os.Create(filepath.Join(t.TempDir(), "job.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	start := time.Now()
	_, err = RunCmdTo(context.Background(), shLimits, "sh", []string{"-c", script}, log)
	wantTimeout(t, err, start)
	wantGone(t, pidFile)
}

// A child that leaves the process group cannot be killed with it. The caller
// must still get its answer on time.
func TestTimeoutReturnsEvenIfAChildLeavesTheProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not installed")
	}
	script, _ := childScript(t, "setsid sleep 20")
	start := time.Now()
	_, err := RunCmd(context.Background(), shLimits, "sh", []string{"-c", script}, "")
	wantTimeout(t, err, start)
}

// A command that starts a daemon has finished when it exits. It is not a
// timeout, and the daemon is not ours to kill. That holds even when the
// deadline passes while its output is still being drained.
func TestCommandThatLeavesABackgroundProcessStillSucceeds(t *testing.T) {
	for name, timeout := range map[string]time.Duration{
		"deadline far off":              10 * time.Second,
		"deadline passes while waiting": shortTimeout,
	} {
		t.Run(name, func(t *testing.T) {
			script, pidFile := childScript(t, "sleep 20")
			script = strings.TrimSuffix(script, "; wait") + "; echo started"
			start := time.Now()
			out, err := RunCmd(context.Background(),
				Limits{Commands: []string{"sh"}, Timeout: timeout}, "sh", []string{"-c", script}, "")
			if err != nil {
				t.Fatalf("got error %v, want success", err)
			}
			if out.Code != 0 || strings.TrimSpace(out.Text) != "started" {
				t.Errorf("got code %d and %q, want 0 and %q", out.Code, out.Text, "started")
			}
			if took := time.Since(start); took > promptly {
				t.Errorf("waited %s for a command that had already exited", took.Round(time.Millisecond))
			}
			if !running(readPID(pidFile)) {
				t.Error("the background process was killed")
			}
		})
	}
}

const runnerEnv = "AHUB_TEST_RUNNER_SCRIPT"

// TestRunnerProcess is not a test: re-executed, it stands for an ahub process
// that is running a command when a signal arrives.
func TestRunnerProcess(t *testing.T) {
	script := os.Getenv(runnerEnv)
	if script == "" {
		t.Skip("only runs as a helper process")
	}
	_, _ = RunCmd(context.Background(),
		Limits{Commands: []string{"sh"}, Timeout: time.Minute}, "sh", []string{"-c", script}, "")
}

// `job stop` signals the supervisor's process group, and Ctrl-C signals the
// terminal's. Either must still reach the command and what it started.
func TestSignallingTheRunnerStopsTheCommand(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			// A shell's background child ignores SIGINT, so exec in the
			// foreground to model an ordinary command and its subprocess.
			pidFile := filepath.Join(t.TempDir(), "pid")
			script := "sh -c 'echo $$ > " + pidFile + "; exec sleep 20'; true"
			runner := exec.Command(os.Args[0], "-test.run=^TestRunnerProcess$")
			runner.Env = append(os.Environ(), runnerEnv+"="+script)
			runner.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := runner.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- runner.Wait() }()
			t.Cleanup(func() {
				_ = syscall.Kill(-runner.Process.Pid, syscall.SIGKILL)
				if pid := readPID(pidFile); pid > 0 {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})
			if !eventually(func() bool { return readPID(pidFile) > 0 }) {
				t.Fatal("the command never started")
			}
			if err := syscall.Kill(-runner.Process.Pid, sig); err != nil {
				t.Fatal(err)
			}
			wantGone(t, pidFile)
			select {
			case <-exited:
				status := runner.ProcessState.Sys().(syscall.WaitStatus)
				// The Go runtime turns some signals into an exit status, so
				// only a clean exit would mean the signal was swallowed.
				if !status.Signaled() && status.ExitStatus() == 0 {
					t.Errorf("the runner swallowed %s and carried on", sig)
				}
			case <-time.After(promptly):
				t.Errorf("the runner survived %s", sig)
			}
		})
	}
}
