package hub

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/javimosch/agents-hub/internal/flow"
)

func toolRegistry(t *testing.T, policy flow.ToolPolicy, names ...string) *Registry {
	t.Helper()
	h, _ := newTestHub(t)
	reg, err := NewRegistry(RegistryOptions{Names: names, Policy: policy, Hub: h})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// The reproduction: permitting `git log` let a model write any file.
func TestAllowingGitLogDoesNotLetAModelWriteFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@hub.test", "commit", "-q", "--allow-empty", "-m", "x"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	t.Setenv("GIT_DIR", filepath.Join(repo, ".git"))
	target := filepath.Join(t.TempDir(), "written-by-a-model")
	reg := toolRegistry(t, flow.ToolPolicy{Commands: []string{"git log"}}, "cmd")
	args, _ := json.Marshal(map[string]any{"command": "git log", "args": []string{"--output=" + target}})

	result, err := reg.tools["cmd"].Run(context.Background(), args)

	if err == nil {
		t.Errorf("got result %q, want a refusal", result)
	}
	if _, err := os.Stat(target); err == nil {
		t.Error("the file was written")
	}
}

func TestJobToolRefusesUnlistedArgumentsBeforeRecordingAJob(t *testing.T) {
	h, f := newTestHub(t)
	reg, err := NewRegistry(RegistryOptions{Names: []string{"job_start"}, Hub: h,
		Policy: flow.ToolPolicy{Commands: []string{"echo"}}})
	if err != nil {
		t.Fatal(err)
	}
	// StartJob would spawn a real supervisor if it got past the check.
	t.Setenv("HOME", t.TempDir())
	_, err = reg.tools["job_start"].Run(context.Background(),
		json.RawMessage(`{"command":"echo","args":["unlisted"]}`))
	if err == nil {
		t.Error("got success, want a refusal")
	}
	if n := len(f.all(RefJobs)); n != 0 {
		t.Errorf("recorded %d jobs, want 0", n)
	}
}

// A job record lives in bkn, out of this process's hands, between being
// written and being run.
func TestSupervisorRechecksArgumentsAgainstCurrentRules(t *testing.T) {
	job := Job{Command: "seq", Args: []string{"3"}}
	allowed := flow.ToolPolicy{Commands: []string{"seq"}, Args: map[string][]string{"seq": {"*"}}}
	if _, _, err := ResolveJobCommand(allowed, job); err != nil {
		t.Fatalf("with the rule: %v", err)
	}
	if argv, _, err := ResolveJobCommand(flow.ToolPolicy{Commands: []string{"seq"}}, job); err == nil {
		t.Errorf("rule revoked: got %q, want a refusal", argv)
	}
}

func TestToolDescriptionsTellTheModelWhichArgumentsAreAllowed(t *testing.T) {
	policy := flow.ToolPolicy{Commands: []string{"git log", "uptime"},
		Args: map[string][]string{"git log": {"--oneline", "--since=*"}}}
	reg := toolRegistry(t, policy, "cmd", "job_start")
	for _, name := range []string{"cmd", "job_start"} {
		got := reg.tools[name].Spec.Description
		for _, want := range []string{"git log (may add: --oneline --since=*)", "uptime (no arguments)"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s description lacks %q: %s", name, want, got)
			}
		}
	}
}

// TestMain stops a runaway: Spawn re-executes the current binary as a job
// supervisor, which here is this test binary. If a refusal under test ever
// regressed, each copy would run the tests and spawn another.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "job" {
		os.Exit(0)
	}
	os.Exit(m.Run())
}
