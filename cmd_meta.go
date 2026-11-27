package main

import (
	"fmt"

	"github.com/javimosch/agents-hub/internal/guide"
	"github.com/javimosch/agents-hub/internal/out"
)

// helpJSON is the command catalog per cli-output-spec §4. It is hand-written
// rather than derived: a catalog generated from a parser describes the parser,
// and the thing an agent needs described is the contract.
func helpJSON() { out.Raw(catalog()) }

func catalog() map[string]any {
	return map[string]any{
		"version": Version,
		"output":  "json",
		"commands": map[string]any{
			"setup":     cmd([]string{}, []string{"--admin-token <t>", "--bkn <url>"}, false),
			"login":     cmd([]string{}, []string{"--email <e>", "--password <p>", "--password-stdin", "--org <o>"}, false),
			"whoami":    cmd([]string{}, []string{}, true),
			"config":    cmd([]string{"show|set|set-profile"}, []string{"--profile <n>", "--bkn <url>", "--provider <n>", "--provider-url <url>", "--provider-key <k>", "--model <m>", "--timeout <s>", "--provider-timeout <s>", "--allow-tool-cmd <prefix>", "--deny-tool-cmd <prefix>", "--allow-tool-arg <prefix pattern...>", "--deny-tool-arg <prefix pattern...>", "--model-limit <model=tokens>", "--compact-at <0.6>", "--allow-cmd <c>", "--deny-cmd <c>", "--allow-host <h>", "--deny-host <h>", "--secret <NAME=v>", "--daily-token-budget <n>", "--job-retention-days <n>"}, false),
			"agent":     cmd([]string{"create|list|get|update|rm", "[name-or-id]"}, []string{"--name <n>", "--model <m>", "--system <s>", "--provider-name <p>", "--memory-project <p>", "--skills <a,b>", "--tools <a,b>", "--temperature <f>", "--max-tokens <n>", "--limit <n>", "--agent <n>"}, true),
			"thread":    cmd([]string{"list|show|rm", "[thread-id]"}, []string{"--agent <n>", "--limit <n>", "--thread <id>"}, true),
			"chat":      cmd([]string{"[message...]"}, []string{"--agent <n>", "--message <m>", "--thread <id>", "--model <m>", "--history <n>", "--stream", "--no-compact", "--compact-at <f>", "--memory-project <p>", "--memory-limit <n>", "--memory-chars <n>", "--memory-required", "--no-memory", "--skill <a,b>", "--skills-required", "--no-skills", "--tools <a,b>", "--allow-writes", "--max-steps <n>", "--no-tools", "--compact-keep <n>", "--memory-per <n>"}, true),
			"memory":    cmd([]string{"status|recall|save|read|projects", "[id-or-text]"}, []string{"--query <q>", "--text <t>", "--project <p>", "--type <t>", "--tags <a,b>", "--limit <n>", "--format index|full|paths", "--chars <n>", "--id <id>", "--per-memory <n>"}, false),
			"tool":      cmd([]string{"list"}, []string{}, false),
			"model":     cmd([]string{"list|set", "[model=tokens]"}, []string{"--limit <n>"}, true),
			"job":       cmd([]string{"list|show|output|wait|stop|start|prune|reap", "[job-id]"}, []string{"--command <prefix>", "--args <a> (repeatable)", "--timeout <s>", "--bytes <n>", "--status <s>", "--limit <n>", "--older-than-days <n>", "--dry-run", "--grace <s>", "--job <id>"}, true),
			"spend":     cmd([]string{}, []string{}, true),
			"feedback":  cmd([]string{"[message]"}, []string{"--kind <bug|idea|praise|note>", "--context <text>", "--reporter <name>"}, false),
			"update":    cmd([]string{}, []string{"--check", "--force"}, false),
			"install":   cmd([]string{}, []string{"--prefix <dir>"}, false),
			"uninstall": cmd([]string{}, []string{"--prefix <dir>"}, false),
			"eval":      cmd([]string{"validate|create|list|show|rm|run|runs|compare", "[name-or-run-ids]"}, []string{"--file <path>", "--agent <a>", "--judge <a>", "--strict", "--limit <n>", "--eval <name>", "--allow-writes"}, true),
			"schedule":  cmd([]string{"create|list|show|rm|enable|disable|run|due|tick|runs|scratchpad", "[name]"}, []string{"--name <n>", "--agent <a>", "--prompt <p>", "--every <dur>", "--at <HH:MM>", "--tools <a,b>", "--skills <a,b>", "--memory-project <p>", "--max-steps <n>", "--timeout <s>", "--set <text>", "--clear", "--status <s>", "--limit <n>", "--schedule <name>", "--lease-ttl <s>", "--allow-writes", "--memory-limit <n>", "--no-compact", "--compact-at <f>", "--compact-keep <n>"}, true),
			"flow":      cmd([]string{"validate|create|list|show|rm|run|resume|runs", "[name-or-run-id]"}, []string{"--file <path>", "--input <k=v> (repeatable)", "--max-steps <n>", "--step-timeout <s>", "--timeout <s>", "--flow <name>", "--status <s>", "--limit <n>", "--run <id>"}, true),
			"skill":     cmd([]string{"search|recommend|show", "[name-or-text]"}, []string{"--query <q>", "--task <t>", "--limit <n>", "--name <n>"}, false),
			"guide":     cmd([]string{}, []string{"--human"}, false),
			"help-json": cmd([]string{}, []string{}, false),
			"version":   cmd([]string{}, []string{}, false),
		},
		"exit_codes": map[string]string{
			"0":   "success",
			"1":   "an eval run completed with failing assertions (only with --strict)",
			"5":   "update --check: a different build is published",
			"80":  "invalid arguments / unknown command",
			"82":  "validation error (missing required field)",
			"85":  "invalid argument value",
			"90":  "not authenticated (no token, or bkn rejected it)",
			"95":  "conflict (the run you tried to resume already finished)",
			"92":  "not found (no such agent, thread, record or local tool), or a flow run needs input",
			"100": "bkn unreachable",
			"105": "model provider, memgraph or a flow step failed, or the turn was interrupted",
			"110": "internal error",
		},
		"env": []string{
			"AHUB_BKN_URL", "AHUB_TOKEN", "AHUB_PROFILE", "AHUB_CONFIG",
			"AHUB_PROVIDER", "AHUB_PROVIDER_URL", "AHUB_PROVIDER_KEY",
			"AHUB_MODEL", "AHUB_TIMEOUT", "AHUB_PROVIDER_TIMEOUT", "AHUB_MEMGRAPH",
			"AHUB_SERVER", "AHUB_UPDATE_URL",
			"AHUB_JOB_TIMEOUT", "AHUB_DAILY_TOKEN_BUDGET", "AHUB_NO_NUDGE", "FEEDBACK_RELAY",
		},
		"see_also": map[string]string{
			"guide":  "ahub guide - the mental model: what to run in what order, and why",
			"bkn":    "state and identity live in a bkn instance; ahub stores nothing locally but a profile",
			"memory": "recall is delegated to the memgraph CLI; ahub implements no retrieval of its own",
		},
	}
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
