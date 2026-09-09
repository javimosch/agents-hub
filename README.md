# agents-hub

`ahub` — run a configured assistant from the command line, non-interactively,
with the conversation stored durably in [bkn](https://github.com/javimosch/bkn).

A CLI-only, agent-first port of the assistant layer of `the internal project`. No UI,
no widget, no MongoDB: state and identity live in a bkn instance, answers come
from any OpenAI-compatible provider, and everything this binary prints follows
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

59 assertions against a throwaway bkn and a real HTTP provider that echoes its
prompt back — so history replay and the system prompt are proved to reach the
model, not merely assumed. Nothing inside the binary under test is mocked.

## Spec conformance

| Spec | State |
|---|---|
| cli-output-spec | conforms — stream separation, exit codes, typed errors, `help-json` |
| cli-guide-spec | conforms — embedded `guide`, `--human` renders markdown |
| cli-update-spec | not yet |
| cli-feedback-spec | not yet |
| cli-telemetry-spec | not yet |
| cli-daemon-spec | not applicable yet — no long-lived server |

## What v0.1 is not

One turn per invocation. No tool calling, no MCP gateway, no RAG, no sub-agents,
no HITL, no workflows, no scheduling. Those are the actual substance of
`the internal project`'s agentic engine and they are not here yet — this is the
spine they attach to.
