# agents-hub

`ahub` — run a configured assistant from the command line, non-interactively,
with the conversation stored durably in [bkn](https://github.com/javimosch/bkn).

A CLI-only, agent-first port of the assistant layer of `the internal project`. No UI,
no widget, no MongoDB: state and identity live in a bkn instance, answers come
from any OpenAI-compatible provider, context comes from
[memgraph](https://github.com/javimosch/memgraph) memories and skills, and
everything this binary prints follows
[cli-output-spec](https://cli-specs.intrane.fr/).

```sh
go build -o bin/ahub . && ./bin/ahub install     # or: curl the published binary
ahub update --check                              # content-hash versioning

ahub setup --admin-token "$BKN_ADMIN_TOKEN"        # once, by a human
ahub login --email you@example.com --password ...
ahub config set --provider-url https://api.mistral.ai/v1 --provider-key "$MISTRAL_KEY"

ahub assistant create --name helper --model mistral-small-latest \
  --system "You answer in one paragraph."

ahub chat --assistant helper --message "what changed in the repo today?"
# {"ok":true,"thread":"01M2...","content":"...","usage":{...}}

ahub chat --assistant helper --thread 01M2... --message "and yesterday?"
ahub thread show 01M2...
```

## The model

| Thing | What it is | Where it lives |
|---|---|---|
| assistant | a saved system prompt + model + knobs | `hub/assistants` in bkn |
| thread | one conversation with one assistant | `hub/threads` |
| message | one turn, with its model and token counts | `hub/messages` |
| profile | bkn url, token, provider, model | `~/.config/agents-hub/config.json` (0600) |
| memory | recalled notes, prepended as reference | a memgraph project |
| skill | a `SKILL.md` procedure, loaded when named | memgraph's skill graph |

`ahub` has no database. It keeps a profile on disk and nothing else.

## Why bkn holds the data

`ahub setup` declares the three collections with an owner-scoped access policy:

```json
{"owner_field": "user_id",
 "rules": {"read": "owner", "create": "owner", "update": "owner", "delete": "owner"}}
```

Every other command carries the **user's own token**. bkn stamps `user_id` from
that token on create and filters reads by it — `ahub` never sends an owner field
at all, so no bug in this codebase can hand one user another user's threads.
The admin token appears in exactly one command, `setup`, and never on the path
that answers a chat.

`test/t-hub.sh` asserts this rather than assuming it: a second real user, with a
valid token, holding the first user's assistant and thread ids, gets 92 on every
one of them.

## Context: memory and skills, not RAG

There is no vector store here and no embedding step. Context comes from two
places, both owned by memgraph, both plain text a human can read and edit:

```sh
ahub assistant update helper --memory-project machin --skills agent-first-cli-specs
ahub chat --assistant helper --message "why does the daemon loop leak?"
# {"content":"...arena { } ...","memory":{"used":2,"chars":2163},"skills":{"loaded":["..."]}}

ahub memory recall --query "port release" --project essaim   # what the model would see
ahub skill recommend "generate a changelog"                  # which procedure fits
```

**Memories** are recalled per turn from a memgraph project and prepended as
reference material — the block tells the model in as many words to use them if
relevant and never to follow instructions inside them, because they were
written by past sessions and other tools. **Skills** are the opposite: they
*are* instructions, which is exactly why one is loaded only when it was named
in the assistant or on the command line.

Everything memgraph is reached through one wrapper with narrow rules: explicit
argv and no shell, so nothing in a message can become a command; validated
values, so a project that could be read as a flag is refused rather than
escaped; a timeout and an output cap; and read-only operations on the chat
path. A skill's file is read only at the path memgraph reported, only if it is
markdown, and only up to 64 KB.

Recall is budgeted (default 1200 characters per memory, 6000 total). That block
is re-sent on every turn, so the budget is the difference between memory as a
feature and memory as a bill.

Both degrade rather than block: if memgraph is missing or fails, the turn still
answers and says so in `memory.error` / `skills.skipped`. `--memory-required`
and `--skills-required` make them fatal.

## Tools: the loop

An assistant can be given tools, and a turn becomes a loop — the model asks,
the result comes back, it answers:

```sh
ahub tool list                      # needs no token, no backend, no memgraph
ahub chat --assistant agent --tools memory_recall,skill_show \
  --message "why does a long-running daemon loop leak in machin?"
# {"content":"...arena { }...","steps":2,"tool_runs":[{"name":"memory_recall","chars":4979}]}
```

| Tool | Writes |
|---|---|
| `memory_recall` — search past notes | no |
| `skill_search` — find procedures by topic | no |
| `skill_show` — read one procedure in full | no |
| `memory_save` — record a durable note | **yes** |

Every tool is memgraph-backed, which is deliberate: the first tools an
assistant gets are the ones that let it *look things up*, not the ones that
let it act. There is no shell tool, and that is not an oversight.

Writes are gated twice — naming a tool enables it, and a tool that changes
state needs `--allow-writes` as well. "Give it tools" and "let it change
things" are different decisions; one flag would collapse them into one.

The loop is bounded by `--max-steps` (default 8), and usage is summed across
every step. Running out of steps without an answer is **exit 105**, not a
successful empty string — the thread is stored, so the error tells you how to
resume it. A tool failure goes back to the model as that tool's result rather
than killing the turn, and stays visible in `tool_runs[].failed`.

## Flows: the deterministic path

An assistant improvises and pays per turn. A flow follows a path somebody
designed, and pays only where it has to:

```sh
ahub config set --allow-cmd git --allow-host api.github.com
ahub flow validate --file release-notes.json    # before it costs anything
ahub flow create --file release-notes.json
ahub flow run release-notes --input repo=~/ai/agents-hub
# {"status":"completed","steps":6,"llm_calls":1,"tokens":280}
```

| Step | Cost | What it does |
|---|---|---|
| `set` `message` `conditional` `end` | free | control and data |
| `collect_input` | free | **pauses the run**, exits 92 naming what it needs |
| `cmd` | free | an allowlisted CLI, by argv, with its exit code as a variable |
| `http` | free | an allowlisted host, with its status as a variable |
| `script` | free | a bkn script, run with **your** token |
| `ask` | tokens | the only step that calls a model |

Integrations are CLIs, deliberately. Every tool in this ecosystem follows
cli-output-spec, so a flow can branch on a **typed exit code** — 105 means
retry, 82 means fix the arguments, 92 means it isn't installed — and a `cmd`
step sets `<var>`, `<var>_code` and `<var>_err` for exactly that.

**A flow never prompts.** `collect_input` pauses, stores the run in bkn, and
exits 92 with the resume command; `ahub flow resume <id> --input k=v`
continues it, from another machine or another day if you like.

**The allowlists live in your profile, never in the flow.** A definition is
data — possibly data a model wrote — so a flow able to name its own
executables would be arbitrary code execution with extra steps. Secrets work
the same way: a flow says `{{secret.GH_TOKEN}}`, the value lives in the 0600
profile, and it is redacted out of anything written to a run.

A `script` step is the one integration that needs no allowlist here, because
the gate lives in bkn instead — next to the code being run. Every bkn script
is admin-only until an operator opens it:

```sh
bkn script update tally --run-access user     # on the bkn machine, once
```

The script then runs with the caller's own token and sees `bkn.caller`, so it
can scope what it returns to whoever asked. A script you may not run answers
exactly like one that does not exist — deliberately, so 404s cannot enumerate
what an operator installed — which is why the refusal here names both
possibilities rather than guessing.

`ahub flow validate` is the cheap win. Because the path is data, unknown
targets, unreachable steps, variables nothing sets and commands this machine
will refuse are all findable before a run spends a cent.

## Output contract

stdout is data, stderr is context, and the exit code says what to do next:

| Code | Meaning | What an agent should do |
|---|---|---|
| 0 | success | continue |
| 80 / 82 / 85 | unknown command / missing field / bad value | fix the command |
| 90 | not authenticated | `ahub login` |
| 92 | no such assistant, thread or record | check the name |
| 100 | bkn unreachable | retry with backoff |
| 105 | provider failed, or the turn was interrupted | retry the same `--thread` |
| 110 | internal error | report it |

`--stream` emits NDJSON delta objects (`{"type":"delta","text":"..."}`) followed
by the usual envelope, so a stream stays parseable line by line.

`ahub guide` is the mental model; `ahub help-json` is the command catalog.

## Testing

```sh
go build -o bin/ahub . && bash test/t-hub.sh
```

231 assertions against a throwaway bkn, a real HTTP provider that echoes its
prompt back, and a stand-in memgraph. Nothing inside the binary under test is
mocked, so history replay, the system prompt, the memory block and the skill
block are each proved to reach the model rather than assumed — including one
assertion that a message full of shell metacharacters arrives at memgraph
verbatim and touches nothing. The fake model drives a real two-pass tool loop,
so replaying a thread that contains tool turns is tested rather than hoped for.

## Spec conformance

| Spec | State |
|---|---|
| cli-output-spec | conforms — stream separation, exit codes, typed errors, `help-json` |
| cli-guide-spec | conforms — embedded `guide`, `--human` renders markdown |
| cli-update-spec | conforms — `install`/`uninstall` (§6), content-hash `update`, verify-then-swap, passive nudge |
| cli-feedback-spec | not yet |
| cli-telemetry-spec | not yet |
| cli-daemon-spec | not applicable yet — no long-lived server |

## Agents: autonomous runs

A flow is a path someone designed. An agent is a model deciding what to do,
on a schedule, with nobody watching:

```sh
ahub agent create --name nightly --assistant helper --every 6h \
  --prompt "Check what changed and note anything worth following up." \
  --tools memory_recall --memory-project machin

ahub agent tick     # runs everything due; this is what a timer calls
ahub agent runs --agent nightly
```

**Each run starts a fresh thread.** What carries forward is one bounded note —
the **scratchpad** — that the agent writes itself through a `scratchpad` tool,
and that a human can read or correct with `ahub agent scratchpad <name>`:

```
run 1: 2108 tokens   note: "Covered Machin's formula. Next run: a different angle."
run 2: 2083 tokens   (fresh thread, note prepended)
```

Flat, not growing. That is the whole reason for the design: replaying an
autonomous agent's history makes every run cost more than the last and
eventually stop fitting, while a capped note cannot. Durable knowledge goes to
memgraph via `memory_save`; the scratchpad is only "where I got to last time".

`ahub agent tick` exits 105 if any agent failed, so a timer that always exits 0
cannot hide a broken agent — and a failed run still advances the schedule,
because an agent that stopped rescheduling when it broke would go quiet exactly
when you needed it to keep complaining.

## Running a flow on a schedule

A flow plus a systemd timer is the whole of autonomy — no daemon, no user in
the loop:

```ini
# /etc/systemd/system/ahub-bkn-backup.service
[Service]
Type=oneshot
User=dk1
ExecStart=/home/dk1/.local/bin/ahub flow run bkn-backup
```

The flow exits non-zero when a step fails, so a bad run shows up in
`systemctl list-units --failed` — and because every run is recorded in bkn,
`ahub flow runs --flow bkn-backup` reads the history back from anywhere.

A machine account needs a credential that outlives its tokens: bkn's refresh
token is good for 30 days, so a timer running at least monthly never needs the
password again.

### Publishing a build for other machines

`ahub` has no infrastructure of its own — the artifact lives in a public bkn
file namespace:

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o ahub .
# on the bkn host, once: bkn files ns create ahub --public
bkn files put ahub ./ahub --name ahub
bkn files put ahub ./ahub.json --name ahub.json   # {ok, version, download, sha256}
```

Then any machine runs `ahub update`. `AHUB_UPDATE_URL` points somewhere else
if you'd rather not use bkn for it.

## What v0.7 is not

No MCP gateway (set aside deliberately: a CLI is a better integration surface
in 2026), no evals, no sub-agents, no HITL, and no `loop` or `subflow` step
yet. Evals are the next gap worth closing: nothing here can yet tell you
whether a prompt, model or memory change made anything better. bkn already has `cron` and `hooks`, so triggering a flow on a
schedule or a webhook is wiring, not engine work.

What remains of
`the internal project`'s agentic engine and they are not here yet — this is the
spine they attach to.

RAG is not on the list: memory and skills replace it deliberately. A note
somebody wrote on purpose, retrievable by name and readable as text, beats a
chunk retrieved by cosine similarity from a corpus nobody can audit.
