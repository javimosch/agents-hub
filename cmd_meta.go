package main

import (
	"fmt"

	"github.com/javimosch/agents-hub/internal/guide"
	"github.com/javimosch/agents-hub/internal/out"
)

// helpJSON is the command catalog per cli-output-spec §4. It is hand-written
// rather than derived: a catalog generated from a parser describes the parser,
// and the thing an agent needs described is the contract.
func helpJSON() {
	out.Raw(map[string]any{
		"version": Version,
		"output":  "json",
		"commands": map[string]any{
			"setup":     cmd([]string{}, []string{"--admin-token <t>", "--bkn <url>"}, false),
			"login":     cmd([]string{}, []string{"--email <e>", "--password <p>", "--org <o>"}, false),
			"whoami":    cmd([]string{}, []string{}, true),
			"config":    cmd([]string{"show|set|set-profile"}, []string{"--profile <n>", "--bkn <url>", "--provider <n>", "--provider-url <url>", "--provider-key <k>", "--model <m>", "--timeout <s>"}, false),
			"assistant": cmd([]string{"create|list|get|update|rm", "[name-or-id]"}, []string{"--name <n>", "--model <m>", "--system <s>", "--provider-name <p>", "--temperature <f>", "--max-tokens <n>", "--limit <n>"}, true),
			"thread":    cmd([]string{"list|show|rm", "[thread-id]"}, []string{"--assistant <n>", "--limit <n>"}, true),
			"chat":      cmd([]string{"[message...]"}, []string{"--assistant <n>", "--message <m>", "--thread <id>", "--model <m>", "--history <n>", "--stream"}, true),
			"guide":     cmd([]string{}, []string{"--human"}, false),
			"help-json": cmd([]string{}, []string{}, false),
			"version":   cmd([]string{}, []string{}, false),
		},
		"exit_codes": map[string]string{
			"0":   "success",
			"80":  "invalid arguments / unknown command",
			"82":  "validation error (missing required field)",
			"85":  "invalid argument value",
			"90":  "not authenticated (no token, or bkn rejected it)",
			"92":  "not found (no such assistant, thread or record)",
			"100": "bkn unreachable",
			"105": "model provider failed or the turn was interrupted",
			"110": "internal error",
		},
		"env": []string{
			"AHUB_BKN_URL", "AHUB_TOKEN", "AHUB_PROFILE", "AHUB_CONFIG",
			"AHUB_PROVIDER", "AHUB_PROVIDER_URL", "AHUB_PROVIDER_KEY",
			"AHUB_MODEL", "AHUB_TIMEOUT",
		},
		"see_also": map[string]string{
			"guide": "ahub guide - the mental model: what to run in what order, and why",
			"bkn":   "state and identity live in a bkn instance; ahub stores nothing locally but a profile",
		},
	})
}

func cmd(args, flags []string, auth bool) map[string]any {
	return map[string]any{"args": args, "flags": flags, "auth": auth}
}

func guideCmd(fl *flags) {
	if fl.human {
		text, err := guide.Human(Version)
		if err != nil {
			out.Fail(out.InternalError, "internal_error", err.Error())
		}
		// Markdown straight to stdout: --human asked for the document, and
		// wrapping it in JSON would make a human pipe it through jq to read it.
		fmt.Println(text)
		return
	}
	body, err := guide.Body(Version)
	if err != nil {
		out.Fail(out.InternalError, "internal_error", err.Error())
	}
	out.Raw(body)
}
