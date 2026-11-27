package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

// goodArtifact passes the smoke test: it answers `version` as ahub.
const goodArtifact = "#!/bin/sh\necho '{\"ok\":true,\"tool\":\"ahub\",\"tool_version\":\"v9\"}'\n"

// publisher serves one artifact and its version document.
type publisher struct {
	url      string
	version  string
	requests atomic.Int32
}

// publish serves artifact; advertised, when set, is what the version document
// claims to be serving instead.
func publish(t *testing.T, artifact string, advertised string) *publisher {
	t.Helper()
	if advertised == "" {
		advertised = artifact
	}
	sum := sha256.Sum256([]byte(advertised))
	full := hex.EncodeToString(sum[:])
	p := &publisher{version: full[:12]}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.requests.Add(1)
		if r.URL.Path == "/ahub.json" {
			fmt.Fprintf(w, `{"ok":true,"version":%q,"download":"/ahub","sha256":%q}`, p.version, full)
			return
		}
		_, _ = w.Write([]byte(artifact))
	}))
	t.Cleanup(server.Close)
	p.url = server.URL
	return p
}

func (p *publisher) env(t *testing.T, extra ...string) []string {
	t.Helper()
	return append(append(cliEnv(t, "http://127.0.0.1:1"),
		"AHUB_SERVER="+p.url, "AHUB_UPDATE_URL="+p.url+"/ahub.json"), extra...)
}

// installedCopy is a throwaway ahub that an update may replace.
func installedCopy(t *testing.T) (path string, contents []byte) {
	t.Helper()
	contents, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "ahub")
	if err := os.WriteFile(path, contents, 0o700); err != nil {
		t.Fatal(err)
	}
	return path, contents
}

func wantUnchanged(t *testing.T, path string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(path)
	if err != nil || len(after) != len(before) {
		t.Errorf("the installed binary changed (%d -> %d bytes, err %v)", len(before), len(after), err)
	}
}

func TestUpdateCheckExits5OnlyWhenAnUpdateIsAvailable(t *testing.T) {
	binary, contents := installedCopy(t)
	other := publish(t, goodArtifact, "")
	r := runCLI(t, binary, other.env(t), "update", "--check")
	if r.code != 5 || !strings.Contains(r.stdout, `"update_available":true`) {
		t.Errorf("newer build: got exit %d and %q, want 5 with update_available", r.code, r.stdout)
	}
	same := publish(t, string(contents), "")
	if r := runCLI(t, binary, same.env(t), "update", "--check"); r.code != 0 {
		t.Errorf("same build: got exit %d, want 0: %s", r.code, r.stderr)
	}
	wantUnchanged(t, binary, contents)
}

func TestUpdateRefusesABadDownloadWithExit100(t *testing.T) {
	cases := map[string]struct{ artifact, advertised, typ string }{
		"hash mismatch":               {goodArtifact, "something else", "verify_failed"},
		"does not run":                {"#!/bin/sh\nexit 3\n", "", "smoke_test_failed"},
		"runs but is not a version":   {"#!/bin/sh\necho hello\n", "", "smoke_test_failed"},
		"answers as a different tool": {"#!/bin/sh\necho '{\"ok\":true,\"tool\":\"bkn\"}'\n", "", "smoke_test_failed"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			binary, contents := installedCopy(t)
			p := publish(t, c.artifact, c.advertised)
			wantError(t, runCLI(t, binary, p.env(t), "update"), 100, c.typ)
			wantUnchanged(t, binary, contents)
		})
	}
}

func TestUpdateSwapsInAGoodBuildAndKeepsABackup(t *testing.T) {
	binary, contents := installedCopy(t)
	p := publish(t, goodArtifact, "")
	if r := runCLI(t, binary, p.env(t), "update"); r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if got, _ := os.ReadFile(binary); string(got) != goodArtifact {
		t.Error("the new build was not installed")
	}
	wantUnchanged(t, binary+".bak", contents)
}

func TestASecondUpdateAtOnceIsRefused(t *testing.T) {
	binary, contents := installedCopy(t)
	lock, err := os.OpenFile(binary+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	p := publish(t, goodArtifact, "")
	wantError(t, runCLI(t, binary, p.env(t), "update"), 95, "update_in_progress")
	wantUnchanged(t, binary, contents)
}

// nudgeEnv is a release build talking to p, with the nudge left on.
func nudgeEnv(t *testing.T, p *publisher, extra ...string) []string {
	t.Helper()
	return p.env(t, append([]string{"AHUB_NO_NUDGE=", "CI=", "AHUB_TEST_VERSION=v1"}, extra...)...)
}

func TestNudgeSpeaksOnStderrOncePerPeriodOnBackendCommands(t *testing.T) {
	p := publish(t, goodArtifact, "")
	env := nudgeEnv(t, p)
	first := runCLI(t, os.Args[0], env, "agent", "list")
	if !strings.Contains(first.stderr, "[update]") || !strings.Contains(first.stderr, p.version) {
		t.Errorf("got stderr %q, want a nudge naming %s", first.stderr, p.version)
	}
	if strings.Contains(first.stdout, "[update]") {
		t.Error("the nudge went to stdout")
	}
	typedError(t, first) // the error stays the last line
	if again := runCLI(t, os.Args[0], env, "agent", "list"); strings.Contains(again.stderr, "[update]") {
		t.Error("nudged twice in a row")
	}
}

func TestNudgeStaysQuiet(t *testing.T) {
	cases := map[string]struct {
		args []string
		env  []string
	}{
		"on a local command":    {[]string{"version"}, nil},
		"on a request for help": {[]string{"agent", "list", "--help"}, nil},
		"when switched off":     {[]string{"agent", "list"}, []string{"AHUB_NO_NUDGE=1"}},
		"in CI":                 {[]string{"agent", "list"}, []string{"CI=true"}},
		// A source build is not behind the published one: it is a different
		// thing, and `update` could replace it with older code.
		"in a build from source": {[]string{"agent", "list"}, []string{"AHUB_TEST_VERSION=dev"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := publish(t, goodArtifact, "")
			r := runCLI(t, os.Args[0], nudgeEnv(t, p, c.env...), c.args...)
			if strings.Contains(r.stderr, "[update]") || p.requests.Load() != 0 {
				t.Errorf("got %d requests and stderr %q, want silence", p.requests.Load(), r.stderr)
			}
		})
	}
}

func TestInstallerVerifiesBeforeInstalling(t *testing.T) {
	real, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		advertised string
		installs   bool
	}{"a good download": {"", true}, "a tampered download": {"something else", false}} {
		t.Run(name, func(t *testing.T) {
			p := publish(t, string(real), c.advertised)
			prefix := t.TempDir()
			// The downloaded test binary only behaves as ahub with runMainEnv.
			env := p.env(t, "PATH="+os.Getenv("PATH"), "AHUB_PREFIX="+prefix)
			r := runCLI(t, "/bin/sh", env, "install.sh")
			_, statErr := os.Stat(filepath.Join(prefix, "ahub"))
			if installed := statErr == nil; installed != c.installs || (r.code == 0) != c.installs {
				t.Errorf("got installed=%v exit=%d, want installed=%v: %s", installed, r.code, c.installs, r.stderr)
			}
		})
	}
}
