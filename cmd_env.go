package main

import (
	"regexp"
	"slices"

	"github.com/javimosch/agents-hub/internal/out"
)

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// applyEnvRules handles --allow-env and --deny-env: which of this machine's
// environment variables a permitted command may see.
func applyEnvRules(fl *flags, p *Config) (changed []string, asked bool) {
	for _, name := range fl.list("allow-env") {
		asked = true
		if !envNameRe.MatchString(name) {
			// The name only. A value belongs in the environment, not the profile.
			out.Fail(out.InvalidValue, "invalid_value",
				"--allow-env takes the name of a variable, without a value",
				"ahub config set --allow-env GH_TOKEN")
		}
		if !slices.Contains(p.AllowEnv, name) {
			p.AllowEnv = append(p.AllowEnv, name)
			changed = append(changed, "allow_env")
		}
	}
	for _, name := range fl.list("deny-env") {
		asked = true
		p.AllowEnv = without(p.AllowEnv, name)
		changed = append(changed, "allow_env")
	}
	return changed, asked
}
