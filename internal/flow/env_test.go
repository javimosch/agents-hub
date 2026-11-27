package flow

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
)

// envSeenBy runs `env` both ways a command can be run and returns each view.
func envSeenBy(t *testing.T, allow []string) map[string][]string {
	t.Helper()
	lim := Limits{Commands: []string{"env"}, Env: allow}
	out, err := RunCmd(context.Background(), lim, "env", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var job bytes.Buffer
	if _, err := RunCmdTo(context.Background(), lim, "env", nil, &job); err != nil {
		t.Fatal(err)
	}
	views := map[string][]string{}
	for name, text := range map[string]string{"RunCmd": out.Text, "RunCmdTo": job.String()} {
		for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
			k, _, _ := strings.Cut(line, "=")
			views[name] = append(views[name], k)
		}
	}
	return views
}

func TestACommandSeesOnlyBasicAndAllowedVariables(t *testing.T) {
	secrets := []string{"AHUB_TOKEN", "AHUB_PROVIDER_KEY", "GH_TOKEN", "SSH_AUTH_SOCK", "HTTPS_PROXY"}
	for _, name := range append(secrets, "WANTED", "LC_TIME", "TZ") {
		t.Setenv(name, "value")
	}
	cases := []struct {
		name         string
		allow        []string
		want, unwant []string
	}{
		{"nothing allowed", nil, []string{"PATH", "HOME", "LC_TIME", "TZ"}, append(secrets, "WANTED")},
		{"one allowed", []string{"WANTED", "NOT_SET"}, []string{"PATH", "WANTED"}, append(secrets, "NOT_SET")},
		{"a credential, on purpose", []string{"GH_TOKEN"}, []string{"GH_TOKEN"}, []string{"AHUB_TOKEN", "WANTED"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for how, got := range envSeenBy(t, c.allow) {
				for _, name := range c.want {
					if !slices.Contains(got, name) {
						t.Errorf("%s: %s is missing", how, name)
					}
				}
				for _, name := range c.unwant {
					if slices.Contains(got, name) {
						t.Errorf("%s: %s reached the command", how, name)
					}
				}
			}
		})
	}
}
