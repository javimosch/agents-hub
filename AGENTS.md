# AGENTS.md — agents-hub

Operating guide for agents working on this repo. Read `ahub guide` first; it is
the product's own mental model and it ships inside the binary.

## What this is

`ahub`, a Go CLI that runs configured agents non-interactively, storing
everything in a bkn instance. Zero third-party dependencies — standard
library only, like bkn. Keep it that way unless there is a reason that survives
the question "what would this cost us at deploy time?".

## TDD is mandatory

Every behaviour change starts with a failing test. **Read
`.agents/skills/ahub-tdd/SKILL.md` before changing code**: red-green-refactor,
the test doubles, and the hygiene rules.

## Spikes and examples

An experiment lives in `spikes/`, which git ignores: never commit or push it.
Once it is validated (tests, and a run with a real model), write it up as a
runnable, documented example in `examples/`, and commit that.

## Layout

| Path | What |
|---|---|
| `main.go`, `cmd_*.go`, `flags.go`, `config.go` | the CLI surface |
| `internal/hub` | the domain: agents, threads, messages, the chat turn, the tool loop |
| `internal/memory` | the memgraph wrapper: recall, skills, and the rules that keep it safe |
| `internal/flow` | the deterministic engine: definition, validator, templating, steps |
| `internal/proc` | runs a subprocess so its deadline holds for the whole process group |
| `internal/bkn` | HTTP client for the backend; the only place that knows bkn's wire shapes |
| `internal/provider` | the model layer: one interface, one OpenAI-compatible impl |
| `internal/out` | cli-output-spec plumbing (a deliberate copy of bkn's) |
| `internal/guide` | the embedded guide; edit `guide.json`, not the renderer |
| `examples/` | validated, runnable examples |
| `test/t-hub.sh` | the live suite; `test/fake-provider.py` is the echoing model |

Keep files under 500 LOC. Split rather than grow — the same rule the source
repo runs on.

## Rules that are load-bearing

- **The admin token belongs to `setup` alone.** If a command starts needing it,
  the access policy is wrong, not the command.
- **Never send `user_id`.** bkn stamps it. Sending it invites the day someone
  sends a different one.
- **Store the user message before calling the provider.** A timeout must cost
  the answer, not the question.
- **Order messages by `seq`, never by timestamp.** Two writes in the same
  millisecond must not be free to come back swapped.
- **No internal retries** (cli-output-spec §3). Report `recoverable: true` and
  exit; the agent decides.
- **Never build a shell command from model or user text.** Everything that
  reaches a subprocess goes through `internal/memory` with explicit argv and a
  validated value; a value that could read as a flag is refused, not escaped.
- **Run anything with a deadline through `proc.Run`.** A bare `cmd.Run` on an
  `exec.CommandContext` kills one process, and a child holding the output pipe
  can then outlive the timeout, or hang the call for good.
- **A new command or flag goes in `help-json` and the guide.** `gate` refuses
  any flag the catalog does not list, and `--help` answers from it. The specs at
  cli-specs.intrane.fr (output, guide, update, feedback) are held by
  `*_spec_test.go`.
- **Publish only with `scripts/publish.sh`.**
- **A tool failure goes back to the model, a turn failure goes back to the
  agent.** The first is information the model can act on; the second is a
  stored, resumable thread plus an exit code.
- **Anything that writes needs a second gate.** Naming a tool enables it;
  `--allow-writes` is what permits mutation.
- **Capability lists live in the operator's profile, never in content.** A
  flow definition, a tool argument and a model's output are all data. Data
  never names what may be executed or reached.
- **A flow step never prompts.** If it needs something it does not have, it
  pauses and exits 92. Nothing in this tool may block on a terminal.

## Before committing

```sh
.githooks/pre-commit && go build -o bin/ahub . && bash test/t-hub.sh
```

Once per clone: `git config core.hooksPath .githooks`.

The suite needs a `bkn` binary (`BKN_BIN`, default `~/ai/bkn/bin/bkn`) and
python3. It builds its own throwaway database and provider on random ports, so
it is re-runnable and leaves nothing behind.

## Test against a real model before believing it

The mock cannot tell you that DeepSeek drops your second system message, that
a model will omit a project name it considers context, or that it will invent
a tool that does not exist and retry it until the step cap. All three were
found by pointing the binary at OpenRouter and reading `prompt_tokens` and
`tool_runs`. Do that before claiming a feature works.

## Adding a provider

Implement `provider.Provider`, call `provider.Register` in an `init`. The
interface exists so the second one does not have to rewrite the first one's
assumptions — tool-call shapes are where vendors diverge, so put the divergence
behind `Chat`, not in `internal/hub`.
