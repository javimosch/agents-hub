package main

import (
	"reflect"
	"testing"
)

func allowedEnv(t *testing.T, env []string) []any {
	t.Helper()
	body, err := cli(t, env, "config", "show")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := body["config"].(map[string]any)["allow_env"].([]any)
	return got
}

func TestConfigStoresAndRevokesAllowedVariables(t *testing.T) {
	env := cliEnv(t, "http://127.0.0.1:1")
	mustCLI(t, env, "config", "set", "--allow-env", "GH_TOKEN", "--allow-env", "KUBECONFIG")
	mustCLI(t, env, "config", "set", "--allow-env", "GH_TOKEN") // again: a no-op that succeeds
	if got, want := allowedEnv(t, env), []any{"GH_TOKEN", "KUBECONFIG"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("allow_env = %v, want %v", got, want)
	}
	mustCLI(t, env, "config", "set", "--deny-env", "GH_TOKEN")
	if got, want := allowedEnv(t, env), []any{"KUBECONFIG"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after deny: %v, want %v", got, want)
	}
}

// A value would be stored in the profile and look like it was being set.
func TestConfigRefusesAnythingButAVariableName(t *testing.T) {
	env := cliEnv(t, "http://127.0.0.1:1")
	for _, bad := range []string{"GH_TOKEN=abc", "two words", "1BAD", "*"} {
		if _, err := cli(t, env, "config", "set", "--allow-env", bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if got := allowedEnv(t, env); len(got) != 0 {
		t.Errorf("stored %v", got)
	}
}
