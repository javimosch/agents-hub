// Command ahub is the agent-first CLI for agents-hub: configurable assistants,
// durable threads, one non-interactive chat turn at a time.
//
// It holds no database of its own. State and identity live in bkn; answers
// come from a model provider. Everything it emits follows cli-output-spec:
// stdout is data, stderr is context, and the exit code says what to do next.
package main

import (
	"os"

	"github.com/javimosch/agents-hub/internal/bkn"
	"github.com/javimosch/agents-hub/internal/hub"
	"github.com/javimosch/agents-hub/internal/out"
)

// Version is stamped at build time (-ldflags "-X main.Version=v0.1.0").
var Version = "dev"

func main() {
	argv := os.Args[1:]
	if len(argv) == 0 {
		usage()
		os.Exit(out.InvalidArguments)
	}

	cmd := argv[0]
	fl := parse(argv[1:])

	switch cmd {
	case "help", "--help", "-h":
		usage()
	case "version", "--version":
		out.Data(map[string]any{"tool": "ahub", "tool_version": Version})
	case "help-json", "--help-json":
		helpJSON()
	case "guide":
		guideCmd(fl)
	case "setup":
		setupCmd(fl)
	case "login":
		loginCmd(fl)
	case "whoami":
		whoamiCmd(fl)
	case "config":
		configCmd(fl)
	case "assistant":
		assistantCmd(fl)
	case "thread":
		threadCmd(fl)
	case "chat":
		chatCmd(fl)
	case "memory":
		memoryCmd(fl)
	case "skill":
		skillCmd(fl)
	case "tool":
		toolCmd(fl)
	case "flow":
		flowCmd(fl)
	case "update":
		updateCmd(fl)
	case "install":
		installCmd(fl)
	case "uninstall":
		uninstallCmd(fl)
	default:
		out.Fail(out.InvalidArguments, "unknown_command",
			"unknown command: "+cmd, "ahub help-json", "ahub guide")
	}
}

// client builds a bkn client from the resolved config, refusing early when
// there is no token: "not authenticated" is a different problem from "the
// backend said no", and an agent must be able to tell them apart.
func client(fl *flags) (*bkn.Client, Config) {
	cfg, _, err := resolve(fl)
	if err != nil {
		out.Fail(out.ValidationError, "config_error", err.Error())
	}
	if cfg.Token == "" {
		out.Fail(out.NotAuthenticated, "not_authenticated",
			"no token; log in or set AHUB_TOKEN",
			"ahub login --email <email> --password <password>")
	}
	c := bkn.New(cfg.BknURL, cfg.Token, cfg.timeout())
	// The refresh token is only used when the access token has expired, and
	// only for the profile it came from: a token passed in by flag or
	// environment is the caller's to manage, and silently swapping it for one
	// from a config file would be a surprise.
	if cfg.Refresh != "" && fl.token == "" && os.Getenv("AHUB_TOKEN") == "" {
		c.RefreshToken = cfg.Refresh
		c.OnTokens = func(t bkn.Tokens) { persistTokens(fl, t) }
	}
	return c, cfg
}

// persistTokens writes a rotated pair back to the active profile. A failure
// here is reported but not fatal: the command in flight already has a working
// token, and losing the next one is better than losing this one's work.
func persistTokens(fl *flags, t bkn.Tokens) {
	f, err := loadFile()
	if err != nil {
		out.Log("token refreshed but not saved: %v", err)
		return
	}
	name := profileName(fl, f)
	p := f.Profiles[name]
	p.Token, p.Refresh = t.Access, t.Refresh
	f.Profiles[name] = p
	if err := saveFile(f); err != nil {
		out.Log("token refreshed but not saved: %v", err)
	}
}

func hubFor(fl *flags) (*hub.Hub, Config) {
	c, cfg := client(fl)
	return hub.New(c), cfg
}

// fail maps a bkn error onto the spec's exit codes. Every command funnels
// backend errors through here so the same failure always exits the same way.
func fail(err error, what string) {
	switch {
	case bkn.Unauthorized(err):
		out.Fail(out.NotAuthenticated, "not_authenticated", err.Error(),
			"ahub login --email <email> --password <password>")
	case bkn.NotFound(err):
		out.Fail(out.NotFound, "not_found", err.Error())
	default:
		var ae *bkn.APIError
		if e, ok := err.(*bkn.APIError); ok {
			ae = e
		}
		if ae == nil {
			// No HTTP status at all: the request never landed.
			out.Fail(out.ConnectionError, "connection_error", err.Error(),
				"check --bkn and that the instance is up")
		}
		if ae.Status >= 500 {
			out.Fail(out.ExternalError, "backend_error", err.Error())
		}
		typ := ae.Type
		if typ == "" {
			typ = "validation_error"
		}
		out.Fail(out.ValidationError, typ, what+": "+err.Error())
	}
}

func usage() {
	out.Log(`ahub %s - agent-first CLI for configurable assistants over bkn

  ahub setup --admin-token <t>          declare the collections (once, by a human)
  ahub login --email <e> --password <p> store a user token in the active profile
  ahub whoami                           who bkn thinks you are
  ahub config [show|set-profile <n>]    inspect or switch profiles

  ahub assistant create --name <n> --model <m> [--system <s>]
  ahub assistant list|get|update|rm

  ahub chat --assistant <n> --message <m> [--thread <id>] [--stream]
  ahub thread list|show|rm

  ahub memory recall --query <q> [--project <p>]   read memgraph directly
  ahub memory save --text <t> [--project <p>]      write one memory
  ahub memory status|projects|read <id[/slug]>

  ahub skill search <q> | recommend <task> | show <name>
  ahub tool list                                   what a model may be given

  ahub flow validate --file <f.json>               check a path before running it
  ahub flow create --file <f.json>                 store it
  ahub flow run <name> [--input k=v]               run it; pauses when it needs input
  ahub flow resume <run-id> --input k=v            continue a paused run
  ahub flow list|show|rm|runs

  ahub install [--prefix <dir>]         put this binary on $PATH
  ahub update [--check] [--force]       fetch the published build

  ahub guide [--human]                  the mental model
  ahub help-json                        the command catalog

stdout is JSON data, stderr is context, exit codes follow cli-output-spec.`, Version)
}
