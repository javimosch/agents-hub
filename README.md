# agents-hub

<p align="center"><img src="docs/icon.svg" width="112" alt="ahub logo"></p>

`ahub` — run a configured agent from the command line, non-interactively,
with the conversation stored durably in [bkn](https://github.com/javimosch/bkn).

An agent-first CLI. No UI, no widget, no database of its own: state and
identity live in a bkn instance, answers come from any OpenAI-compatible
provider, context comes from
[memgraph](https://github.com/javimosch/memgraph) memories and skills, and
everything this binary prints follows
[cli-output-spec](https://cli-specs.intrane.fr/).

> **Early project, with known safety gaps.** Read [Limits](#limits) before
> giving an agent the `cmd` or job tools. They are not a sandbox.

```sh
go build -o bin/ahub . && ./bin/ahub install     # build from source
ahub update --check                              # content-hash versioning

ahub setup --admin-token "$BKN_ADMIN_TOKEN"        # once, by a human
ahub login --email you@example.com --password ...
ahub config set --provider-url https://api.mistral.ai/v1 --provider-key "$MISTRAL_KEY"

ahub agent create --name helper --model mistral-small-latest \
  --system "You answer in one paragraph."

ahub chat --agent helper --message "what changed in the repo today?"
# {"ok":true,"thread":"01M2...","content":"...","usage":{...}}

ahub chat --agent helper --thread 01M2... --message "and yesterday?"
ahub thread show 01M2...
```

Where it's headed: [vision](docs/VISION.md) ·
[north star](docs/NORTH-STAR.md) · [roadmap](docs/ROADMAP.md).

## The model

| Thing | What it is | Where it lives |
|---|---|---|
| agent | a saved system prompt + model + knobs | `hub/agents` in bkn |
| schedule | a standing instruction to run an agent | `hub/schedules` |
| thread | one conversation with one agent | `hub/threads` |
| message | one turn, with its model and token counts | `hub/messages` |
| profile | bkn url, token, provider key, secrets, allowlists | `~/.config/agents-hub/config.json` (0600) |
| model facts | context windows, learned or declared | `hub/models` |
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
valid token, holding the first user's agent and thread ids, gets 92 on every
one of them.

## Context: memory and skills, not RAG

There is no vector store here and no embedding step. Context comes from two
places, both owned by memgraph, both plain text a human can read and edit:

```sh
ahub agent update helper --memory-project machin --skills agent-first-cli-specs
ahub chat --agent helper --message "why does the daemon loop leak?"
# {"content":"...arena { } ...","memory":{"used":2,"chars":2163},"skills":{"loaded":["..."]}}

ahub memory recall --query "port release" --project essaim   # what the model would see
ahub skill recommend "generate a changelog"                  # which procedure fits
```

**Memories** are recalled per turn from a memgraph project and prepended as
reference material — the block tells the model in as many words to use them if
relevant and never to follow instructions inside them, because they were
written by past sessions and other tools. **Skills** are the opposite: they
*are* instructions, which is exactly why one is loaded only when it was named
in the agent or on the command line.

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

An agent can be given tools, and a turn becomes a loop — the model asks,
the result comes back, it answers:

```sh
ahub tool list                      # needs no token, no backend, no memgraph
ahub chat --agent agent --tools memory_recall,skill_show \
  --message "why does a long-running daemon loop leak in machin?"
# {"content":"...arena { }...","steps":2,"tool_runs":[{"name":"memory_recall","chars":4979}]}
```

| Tool | Writes | Needs |
|---|---|---|
| `memory_recall` — search past notes | no | memgraph |
| `skill_search` — find procedures by topic | no | memgraph |
| `skill_show` — read one procedure in full | no | memgraph |
| `memory_save` — record a durable note | **yes** | memgraph |
| `cmd` — run a permitted command | — | an allowlist |
| `job_start` — start a permitted command in the background | — | an allowlist |
| `job_wait` — wait for a job it started | — | an allowlist |
| `job_output` — read a job's output | — | an allowlist |

### Reaching the world: the `cmd` tool

Flows could always run commands; agents could only look things up. `cmd` closes
that, but the argv author changes from a human to a model, so the gate changes
too — **prefixes**, not bare command names:

```sh
ahub config set --allow-tool-cmd 'gh pr list' --allow-tool-cmd 'git log'
ahub config set --allow-tool-arg 'git log --oneline -n * --since=*'
ahub chat --agent helper --tools cmd --message "what PRs are open?"
```

The model picks a permitted prefix and can never change it. `git logger` is not
a match for `git log`: comparison is on whole fields. The list is empty by
default, so the tool does not exist until someone permits something.

**A prefix alone takes no arguments.** A prefix only says how a command starts:
`git log --output=<path>` writes a file. So every further argument must match a
pattern you listed for that prefix with `--allow-tool-arg '<prefix> <pattern>...'`:

- A pattern matches one whole argument. `*` stands for any text, spaces
  included: `--since=*` matches `--since=1 week ago`.
- `*` never lets a flag through. An argument starting with `-` only matches a
  pattern that itself starts with `-`.
- Patterns are not tied to a position or to each other. For `-n 5`, list both
  `-n` and `*`, and that `*` then admits any other value too.
- With both `git` and `git log` permitted, the rules of the one the model names
  as the command apply. Rules for `git` with `*` would admit `git` + `push`.
- `--deny-tool-arg` removes patterns. `--deny-tool-cmd` removes a prefix and
  its patterns.

Be careful with broad patterns. `-*` admits every flag, and `*` admits any path.
Rules do not limit what a permitted command does by itself (`npm test` runs
whatever the project says), and the command inherits `ahub`'s environment.
The `job_*` tools and `ahub job start` follow the same rules.

> **Upgrading:** earlier builds let any argument follow a prefix. Existing
> prefixes now take none until you add patterns.

It is deliberately **not** behind `--allow-writes`: the prefix list is a
stronger and more specific gate, and a flag you must pass even to run `git log`
would only teach people the flag means nothing.

The memory and skill tools need memgraph; `cmd` and the job tools do not.
There is no built-in shell tool. Permitting a prefix such as `sh -c` would
create one, so don't.

Writes are gated twice — naming a tool enables it, and a tool that changes
state needs `--allow-writes` as well. "Give it tools" and "let it change
things" are different decisions; one flag would collapse them into one.

The loop is bounded by `--max-steps` (default 8), and usage is summed across
every step. Running out of steps without an answer is **exit 105**, not a
successful empty string — the thread is stored, so the error tells you how to
resume it. A tool failure goes back to the model as that tool's result rather
than killing the turn, and stays visible in `tool_runs[].failed`.

## Flows: the deterministic path

An agent improvises and pays per turn. A flow follows a path somebody
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

### Steps that need root

Don't run ahub as root — that hands every step of every flow the privilege one
step needed. Allowlist the privileged **shape** instead:

```sh
ahub config set --allow-cmd 'sudo journalctl'    # journalctl as root, nothing else
```

Allowlist entries are whole-field prefixes, so this permits
`sudo journalctl --vacuum-size=250M` and refuses `sudo rm -rf /`. A one-word
entry still allows any arguments — which is exactly why `sudo` alone should
never be one.

The **real** gate is the sudoers rule, enforced and audited by the OS rather
than by this process:

```
# /etc/sudoers.d/ahub-journal   (visudo -f, mode 0440)
you ALL=(root) NOPASSWD: /usr/bin/journalctl --vacuum-size=*
```

Two gates, and they narrow differently: ahub says which shapes a flow may
attempt, sudoers says what root will actually run. A compromised ahub cannot
widen the second one.

For anything genuinely dangerous, go one further: put the work in a root-owned
systemd unit and let the flow only start it
(`--allow-cmd 'sudo systemctl start ahub-vacuum'`). Then even a fully
compromised ahub can trigger one predefined action rather than compose a new
one — the definition lives in a file it cannot edit.

### How deterministic is it?

The control flow is: no step but `ask` calls a model, the operators are a
closed set with first-match-wins, an unset `{{variable}}` fails the step rather
than substituting empty, and arguments are argv. A run reports `llm_calls` and
`tokens`, so "this costs nothing" is checkable rather than claimed.

What isn't deterministic is what the commands say — that's the point of asking
them. A worked example, keeping the systemd journal under 300 MB:

```
path:   budget -> keep -> measure -> size -> check -> over -> warn -> over_end
cost:   llm_calls=0 tokens=0 steps=8
```

Two limits worth knowing. A flow has **no arithmetic** — it can compare numbers
(`gt`/`lt` read the leading number, so `du -sb` output compares straight against
a budget) but not compute one; do the calculation in the command. And a
conditional reads its variable *by name*, so an unset one compares as empty
rather than failing the way a template does — declare optional ones in the
flow's `inputs` list, and `validate` reports anything else no step sets.

`ahub flow validate` is the cheap win. Because the path is data, unknown
targets, unreachable steps, variables nothing sets and commands this machine
will refuse are all findable before a run spends a cent.

## What lives where

**bkn holds everything that should travel** — agents, threads, messages,
flows, schedules, evals, jobs, and what has been learned about models. Log in
on a new machine and it is all there:

```sh
ahub model list      # context windows every machine on the account shares
```

A window learned from one provider refusal is a window no other machine has to
pay to discover. An explicit `--model-limit` in the profile is an override and
still wins.

**The profile holds what should not travel** — your token, provider key,
secrets, and the three allowlists. A credential and a capability grant are
policies of *one machine*, and a new machine must not inherit permission to run
`sudo journalctl` merely because you logged in.

**Job output is split deliberately.** The last 16 KB rides along with the
record so another machine can read what happened; the full log stays where it
ran, because a build log is not database content. `ahub job output` tells you
which one you got:

```json
{"source": "local file (complete)"}      // on the machine that ran it
{"source": "recorded tail (portable)"}   // anywhere else
```

**memgraph holds memories and skills** in its own store, by the earlier
decision to prefer them over RAG.

## Guards for unattended work

Everything here exists because of a run nobody is watching:

```sh
ahub spend                        # what today cost, against the allowance
ahub job prune                    # finished jobs and their output, gone after 14 days
ahub job reap                     # jobs whose supervisor died stop saying "running"
ahub config set --daily-token-budget 200000
```

- **One run at a time per schedule.** A tick takes a lease before running, so a
  5-minute timer and a 4-minute agent can't overlap and race on the same
  scratchpad. The lease expires after 30 minutes, because a killed process
  can't release its own.
- **A daily token ceiling**, checked *before* a turn — a budget enforced
  afterwards is a report. Counted atomically in bkn with `$inc`, so two
  machines spending at once can't lose an increment. Refuses with exit 95.
- **Jobs are reaped and pruned.** A tick marks jobs whose supervisor is gone;
  `job prune` clears finished ones and their output after `job_retention_days`.
- **A flow has its own deadline** as well as a per-step one, and the error names
  which clock ran out.

## Output contract

stdout is data, stderr is context, and the exit code says what to do next:

| Code | Meaning | What an agent should do |
|---|---|---|
| 0 | success | continue |
| 5 | `update --check` only: a different build is published | `ahub update` |
| 80 / 82 / 85 | unknown command / missing field / bad value | fix the command |
| 90 | not authenticated | `ahub login` |
| 92 | no such agent, thread or record | check the name |
| 100 | bkn unreachable, or an update that could not be downloaded or verified | retry with backoff |
| 105 | provider failed, or the turn was interrupted | retry the same `--thread` |
| 110 | internal error | report it |

`--stream` emits NDJSON delta objects (`{"type":"delta","text":"..."}`) followed
by the usual envelope, so a stream stays parseable line by line.

`ahub guide` is the mental model; `ahub help-json` is the command catalog.

## Testing

```sh
go build -o bin/ahub . && bash test/t-hub.sh
```

369 assertions against a throwaway bkn, a real HTTP provider that echoes its
prompt back, and a stand-in memgraph. Nothing inside the binary under test is
mocked, so history replay, the system prompt, the memory block and the skill
block are each proved to reach the model rather than assumed — including one
assertion that a message full of shell metacharacters arrives at memgraph
verbatim and touches nothing. The fake model drives a real two-pass tool loop,
so replaying a thread that contains tool turns is tested rather than hoped for.

## Spec conformance

[Agent-first CLI specs](https://cli-specs.intrane.fr/):

| Spec | State |
|---|---|
| cli-output-spec | adopted. `--help` never runs a command, and an unknown flag is refused. `--json`, `--no-interactive` and `--yes` are accepted: output is always JSON and nothing prompts |
| cli-guide-spec | adopted. `guide`, `--human` |
| cli-update-spec 1.1 | adopted. `update [--check] [--force]`, notice, [install.sh](install.sh), `install`/`uninstall`. The version is re-hashed, so no `VERSION` file is kept |
| cli-feedback-spec | adopted, relay only |
| cli-telemetry-spec | **not adopted, on purpose.** `ahub` holds credentials, and sends no usage data |
| cli-daemon-spec, cli-trial-spec | not applicable: no server, no hosted service |

Where `ahub` differs: `--stream` prints one JSON object per line rather than one
per call, and `feedback` sends the reporter `agent` instead of your OS username
unless you pass `--reporter`.

The live suite runs
[cli-spec-conformance](https://github.com/javimosch/cli-spec-conformance) for the
output and guide specs when it is installed (`CONFORMANCE_BIN`). It does not
cover the update or feedback specs; the unit tests do.

## What leaves your machine

Besides your bkn, your model provider and whatever you allow flows and tools to
reach:

- **Update check.** A release build asks the update server at most once a day,
  on commands that talk to bkn, whether a different build is published. It sends
  no data beyond the request itself. `AHUB_NO_NUDGE=1` or `CI` turns it off, and
  a build from source never checks.
- **`ahub update`** and **`install.sh`**, when you run them.
- **`ahub feedback "<message>" [--kind bug] [--context ...]`**, only when you run
  it: your message, kind, context and the tool's version, to
  `https://feedback.intrane.fr`. Put no secrets in it. `FEEDBACK_RELAY` points
  it elsewhere, or `off` sends nothing.

## Jobs: work that outlives a turn

A turn cannot wait — a test suite or a deploy outlives the provider timeout:

```sh
ahub config set --allow-tool-cmd 'npm test'
ahub job start --command 'npm test'      # returns immediately with an id
ahub job wait <id> --timeout 300
ahub job list | job output <id> | job stop <id>
```

A model gets three verbs — `job_start`, `job_wait`, `job_output` — and cannot
list or stop work it did not begin. Jobs go through the **same prefix
allowlist** as the `cmd` tool, so starting one by hand is bound by what the
machine permits a model to run.

The exit code survives because the process ahub spawns is **ahub itself**,
supervising the command and writing the result back to bkn. Spawning the
command directly would leave nobody to reap it once the turn exits — and a job
whose result nobody recorded is just a process.

## Compaction: threads that stop fitting

Every response reports `prompt_tokens`, so the size is **measured, not
estimated**. Cross a fraction of the model's window (60% by default) and the
older half of the thread is folded into a summary:

```json
"compaction": {"compacted": true, "reason": "prompt reached 900 tokens, 90% of this model's window",
               "folded_up_to_seq": 7, "messages_folded": 8, "tokens_spent": 412}
```

If the provider refuses a prompt anyway, the window **it names is learned and
written to the profile**, the thread is folded, and the turn is retried once:

```
learned that deepseek/deepseek-v4-flash has a 128000-token context window
```

The messages are never deleted — replay skips what the summary covers, but the
record survives, because a conversation that quietly lost its middle is
impossible to debug afterwards. Folding happens at most once per prompt size:
if it didn't shrink, the size is coming from somewhere a summary can't reach.
And when a prompt can't be made to fit at all, that's exit **85
`prompt_too_large`** with what would actually help — not a retryable error
inviting the same failure again.

## Evals: is it actually better?

Every other feature here changes behaviour. None of those changes can be judged
by reading them:

```sh
ahub eval validate --file quality.json     # before spending anything
ahub eval run quality
#   pass echoes-the-question (412 tokens)
#   FAIL cites-the-source (388 tokens) contains: not found in the answer
# {"passed":1,"failed":1,"pass_rate":"0.50","tokens":800,"judge_tokens":0}

ahub agent update helper --memory-project machin      # a change
ahub eval run quality --strict                        # exits 1 if it regressed
ahub eval compare <before> <after>
# {"summary":"0 fixed, 1 regressed, 0 still failing","regressed":["stays-clean"]}
```

| Assertion | Cost |
|---|---|
| `contains` `not_contains` `equals` `matches` | free |
| `max_tokens` `max_steps` | free |
| `tool_used` `tool_not_used` | free |
| `judge` — a second agent returns PASS/FAIL plus a reason | tokens |

An eval runs the agent **with its real memory, skills and tools** — measuring
it without them would measure a different agent than the one answering real
questions — and each case gets a fresh thread so it can't be influenced by the
case above it. A case whose model call failed counts as **failed**, never
skipped: an outage must not read as an improvement.

`ahub eval run` exits 0 whatever it scored; the exit code says the eval *ran*,
the data says how it went. `--strict` exits **1** — deliberately outside
cli-output-spec's 80–119 bands, because the tool worked and the agent didn't,
and 105 would tell an agent to retry a failing eval expecting a different
answer.

## Schedules: autonomous runs

A flow is a path someone designed. A **schedule** lets an agent decide what to
do, on a cadence, with nobody watching:

```sh
ahub schedule create --name nightly --agent helper --every 6h \
  --prompt "Check what changed and note anything worth following up." \
  --tools memory_recall --memory-project machin

ahub schedule tick     # runs everything due; this is what a timer calls
ahub schedule runs --schedule nightly
```

**Each run starts a fresh thread.** What carries forward is one bounded note —
the **scratchpad** — that the agent writes itself through a `scratchpad` tool,
and that a human can read or correct with `ahub schedule scratchpad <name>`:

```
run 1: 2108 tokens   note: "Covered Machin's formula. Next run: a different angle."
run 2: 2083 tokens   (fresh thread, note prepended)
```

Flat, not growing. That is the whole reason for the design: replaying an
autonomous agent's history makes every run cost more than the last and
eventually stop fitting, while a capped note cannot. Durable knowledge goes to
memgraph via `memory_save`; the scratchpad is only "where I got to last time".

`ahub schedule tick` exits 105 if any schedule failed, so a timer that always exits 0
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

### Installing and publishing a build

```sh
sh install.sh                          # Linux x86_64; verifies, then `ahub install`
ahub update --check                    # exit 5 when a different build is published
```

`ahub` has no infrastructure of its own: the artifact lives in a public bkn file
namespace. To publish:

```sh
scripts/publish.sh /tmp/ahub-release   # from a clean tree; builds, checks, uploads nothing
# once, on the bkn host: bkn files ns create ahub --public
```

The script prints the upload commands. **Delete before putting:** an overwrite
leaves the old bytes on the server's disk.

`AHUB_SERVER` points at another bkn, and `AHUB_UPDATE_URL` at another version
document. The built-in default server is the author's. `update` installs whatever
build differs from yours, even an older one, and builds are not signed: the
binary and its hash come from the same server. Only update from a server you
trust.

## Limits

- **`cmd` and job tools:** arguments are limited to your patterns, but this is
  not a sandbox. A permitted command runs with your user's rights and inherits
  the environment, including any tokens in it.
- **Updates:** unsigned, and not checked for being newer (see above).
- **Concurrency and crashes:** some cases are suspected from reading the code
  and not yet resolved. See the [roadmap](docs/ROADMAP.md).
- **Platforms:** developed and tested on Linux. macOS builds compile but are
  untested. Windows does not build.
- **Tests:** many parts have no unit tests yet.

## What v0.13 is not

No MCP gateway (set aside deliberately: a CLI is a better integration surface
in 2026), no sub-agents, and no OpenAPI import yet — with `curl` allowlisted by
prefix, much of what an OpenAPI tool would buy is already reachable.

There is no `loop` or `subflow` flow step, and there will not be one until
something needs it: the caller here is usually an agent, and an agent already
has a loop, branching and composition. `ahub flow run a && ahub flow run b`
is subflow. HITL is `collect_input`, which pauses and exits 92 with the resume
command — when the caller is an agent, "human in the loop" is "caller in the
loop". Nor is there prompt improvement from failed eval runs, which only becomes
worth building now that a proposal can be scored against the run it came from.
bkn already has `cron` and `hooks`, so triggering a flow on a schedule or a
webhook is wiring, not engine work.

RAG is not on the list: memory and skills replace it deliberately. A note
somebody wrote on purpose, retrievable by name and readable as text, beats a
chunk retrieved by cosine similarity from a corpus nobody can audit.

## License

[MIT](LICENSE)
