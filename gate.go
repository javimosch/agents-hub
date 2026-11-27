package main

import (
	"maps"
	"slices"
	"strings"

	"github.com/javimosch/agents-hub/internal/out"
)

// globalFlags are accepted by every command: the connection flags parse reads,
// and the ones cli-output-spec expects any tool to take. JSON is already the
// output and nothing ever prompts, so those have nothing left to change.
var globalFlags = []string{"--profile", "--server", "--bkn", "--token",
	"--provider", "--provider-url", "--model", "--human", "--stream",
	"--json", "--no-interactive", "--yes", "--help"}

// gate runs before any command. It answers a request for help without running
// anything, and refuses a flag the command would silently ignore: an ignored
// flag is how a filtered list returns everything, and how `update --help`
// updated.
func gate(command string, fl *flags) (handled bool) {
	entry, ok := catalog()["commands"].(map[string]any)[command].(map[string]any)
	if !ok {
		return false // dispatch reports the unknown command
	}
	if fl.has("help") {
		help := map[string]any{"command": command, "see_also": "ahub guide"}
		maps.Copy(help, entry)
		out.Data(help)
		return true
	}
	accepted := slices.Clone(globalFlags)
	for _, f := range entry["flags"].([]string) {
		accepted = append(accepted, strings.Fields(f)[0])
	}
	var given []string
	for name := range fl.bools {
		given = append(given, name)
	}
	for name := range fl.kv {
		given = append(given, name)
	}
	slices.Sort(given)
	for _, name := range given {
		if !slices.Contains(accepted, "--"+name) {
			out.Fail(out.InvalidArguments, "unknown_flag",
				command+" has no flag --"+name+"; nothing was run",
				"ahub "+command+" --help")
		}
	}
	return false
}
