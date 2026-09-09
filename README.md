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
go build -o bin/ahub .

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

99 assertions against a throwaway bkn, a real HTTP provider that echoes its
prompt back, and a stand-in memgraph. Nothing inside the binary under test is
mocked, so history replay, the system prompt, the memory block and the skill
block are each proved to reach the model rather than assumed — including one
assertion that a message full of shell metacharacters arrives at memgraph
verbatim and touches nothing.

## Spec conformance

| Spec | State |
|---|---|
| cli-output-spec | conforms — stream separation, exit codes, typed errors, `help-json` |
| cli-guide-spec | conforms — embedded `guide`, `--human` renders markdown |
| cli-update-spec | not yet |
| cli-feedback-spec | not yet |
| cli-telemetry-spec | not yet |
| cli-daemon-spec | not applicable yet — no long-lived server |

## What v0.2 is not

One turn per invocation. No tool calling, no MCP gateway, no sub-agents, no
HITL, no workflows, no scheduling. Those are the substance of
`the internal project`'s agentic engine and they are not here yet — this is the
spine they attach to.

RAG is not on the list: memory and skills replace it deliberately. A note
somebody wrote on purpose, retrievable by name and readable as text, beats a
chunk retrieved by cosine similarity from a corpus nobody can audit.
