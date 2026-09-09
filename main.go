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
	return bkn.New(cfg.BknURL, cfg.Token, cfg.timeout()), cfg
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

  ahub guide [--human]                  the mental model
  ahub help-json                        the command catalog

stdout is JSON data, stderr is context, exit codes follow cli-output-spec.`, Version)
}
