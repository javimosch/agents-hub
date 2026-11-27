package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// result is what a caller of the CLI can observe.
type result struct {
	code           int
	stdout, stderr string
}

// runCLI runs a binary as ahub. Later entries in env win.
func runCLI(t *testing.T, binary string, env []string, args ...string) result {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("ahub %v: %v", args, err)
	}
	return result{cmd.ProcessState.ExitCode(), stdout.String(), stderr.String()}
}

// run isolates a CLI call from this machine and from the network.
func run(t *testing.T, args ...string) result {
	t.Helper()
	return runCLI(t, os.Args[0], cliEnv(t, "http://127.0.0.1:1"), args...)
}

// typedError reads the error envelope: the last line of stderr.
func typedError(t *testing.T, r result) (code int, typ string) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(r.stderr), "\n")
	var body struct {
		Error struct {
			Code int    `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &body); err != nil {
		t.Fatalf("stderr does not end in a typed error: %q", r.stderr)
	}
	return body.Error.Code, body.Error.Type
}

func wantError(t *testing.T, r result, code int, typ string) {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit: got %d, want %d (stdout %q, stderr %q)", r.code, code, r.stdout, r.stderr)
	}
	if gotCode, gotType := typedError(t, r); gotCode != code || gotType != typ {
		t.Errorf("error: got %d %s, want %d %s", gotCode, gotType, code, typ)
	}
}
