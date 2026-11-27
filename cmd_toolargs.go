package main

import (
	"slices"

	"github.com/javimosch/agents-hub/internal/out"
)

// applyToolArgRules adds and removes the argument patterns of --allow-tool-arg
// and --deny-tool-arg. It runs after the prefixes changed, so a command and
// its rules can be permitted in one call.
func applyToolArgRules(fl *flags, p *Config) (changed []string, asked bool) {
	for _, rule := range fl.list("allow-tool-arg") {
		asked = true
		prefix, patterns := splitToolRule(p, rule)
		for _, pattern := range patterns {
			if !contains(p.AllowToolArgs[prefix], pattern) {
				if p.AllowToolArgs == nil {
					p.AllowToolArgs = map[string][]string{}
				}
				p.AllowToolArgs[prefix] = append(p.AllowToolArgs[prefix], pattern)
				changed = append(changed, "allow_tool_args")
			}
		}
	}
	for _, rule := range fl.list("deny-tool-arg") {
		asked = true
		prefix, patterns := splitToolRule(p, rule)
		kept := slices.DeleteFunc(slices.Clone(p.AllowToolArgs[prefix]), func(have string) bool {
			return contains(patterns, have)
		})
		if len(kept) != len(p.AllowToolArgs[prefix]) {
			changed = append(changed, "allow_tool_args")
		}
		if len(kept) == 0 {
			delete(p.AllowToolArgs, prefix)
		} else {
			p.AllowToolArgs[prefix] = kept
		}
	}
	return changed, asked
}

// splitToolRule refuses a rule that would silently do nothing.
func splitToolRule(p *Config, rule string) (string, []string) {
	prefix, patterns, err := p.toolPolicy().SplitRule(rule)
	if err != nil {
		out.Fail(out.InvalidValue, "invalid_value", err.Error(),
			"ahub config set --allow-tool-cmd '<prefix>' --allow-tool-arg '<prefix> <pattern>'")
	}
	return prefix, patterns
}
