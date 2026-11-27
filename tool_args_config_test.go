package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"testing"
)

// cli runs ahub against one throwaway profile.
func cli(t *testing.T, env []string, args ...string) (map[string]any, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = env
	raw, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("ahub %v: %v: %s", args, err, raw)
	}
	return body, nil
}

func toolArgs(t *testing.T, env []string) any {
	t.Helper()
	body, err := cli(t, env, "config", "show")
	if err != nil {
		t.Fatal(err)
	}
	return body["config"].(map[string]any)["allow_tool_args"]
}

func mustCLI(t *testing.T, env []string, args ...string) {
	t.Helper()
	if _, err := cli(t, env, args...); err != nil {
		t.Fatalf("ahub %v: %v", args, err)
	}
}

func wantRules(t *testing.T, env []string, want map[string]any) {
	t.Helper()
	got, _ := toolArgs(t, env).(map[string]any)
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rules: got %v, want %v", got, want)
	}
}

func TestConfigStoresAndRevokesArgumentRules(t *testing.T) {
	env := cliEnv(t, "http://127.0.0.1:1")
	// The prefix may be permitted in the same call as its rules.
	mustCLI(t, env, "config", "set", "--allow-tool-cmd", "git", "--allow-tool-cmd", "git log",
		"--allow-tool-arg", "git log --oneline --since=*")
	mustCLI(t, env, "config", "set", "--allow-tool-arg", "git log --oneline -n")
	wantRules(t, env, map[string]any{"git log": []any{"--oneline", "--since=*", "-n"}})

	mustCLI(t, env, "config", "set", "--deny-tool-arg", "git log --since=* -n")
	wantRules(t, env, map[string]any{"git log": []any{"--oneline"}})

	// Permitting a prefix again later must not bring old rules back.
	mustCLI(t, env, "config", "set", "--deny-tool-cmd", "git log")
	wantRules(t, env, nil)
}

func TestConfigRefusesARuleThatWouldDoNothing(t *testing.T) {
	for name, rule := range map[string]string{
		"no permitted prefix": "rm -rf",
		"no pattern":          "git log",
	} {
		t.Run(name, func(t *testing.T) {
			env := cliEnv(t, "http://127.0.0.1:1")
			mustCLI(t, env, "config", "set", "--allow-tool-cmd", "git log")
			if _, err := cli(t, env, "config", "set", "--allow-tool-arg", rule); err == nil {
				t.Errorf("--allow-tool-arg %q: got success, want a refusal", rule)
			}
		})
	}
}
