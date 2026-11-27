# Vision

**An agent is something you call from a command line, not an app you log in to.**

`ahub` runs a configured agent non-interactively and keeps the conversation
durably in [bkn](https://github.com/javimosch/bkn). The caller is usually another
agent, a script or a timer, so everything is built for a caller that reads JSON,
branches on exit codes and is not there to answer a prompt.

It is the agentic spine alone: no UI, no widget, no vector store.

## What we believe

- **One binary, no state of its own.** What should travel lives in bkn. What
  must not travel (tokens, keys, secrets, capability grants) stays in the
  machine's profile. The binary uses the Go standard library only.
- **Data never grants capability.** Model output, tool arguments and flow
  definitions are all data. Only the operator says what may be run or reached.
- **Pay for judgement, not for plumbing.** A known path is a flow, where only
  `ask` calls a model. An agent improvises when the path is not known.
- **Context should be readable.** Memories and skills are text that somebody
  wrote on purpose and a human can correct. There is no RAG, by choice.
- **The caller owns retries and branching.** No internal retries and no
  prompts. A failure is typed, and says what to do next.
- **A claim should be checkable.** Cost is reported, behaviour changes are
  scored by evals, and code changes start with a failing test.

## Not the goal

| Not building | Why |
|---|---|
| A chat UI or embeddable widget | the caller is a command line |
| RAG or an embedding pipeline | memgraph memories and skills replace it |
| An MCP gateway | a CLI is the better integration surface |
| Sub-agents, `loop` or `subflow` steps | the caller already loops, branches and composes |
| A shell tool | commands are reached through operator-written prefixes |
| A long-running daemon | a timer calling `ahub schedule tick` is enough |

These are current decisions, not permanent ones. Reversing one takes a real use
that the existing parts cannot serve.

See [NORTH-STAR.md](NORTH-STAR.md) for how far along we are and
[ROADMAP.md](ROADMAP.md) for what comes next. The rules that code must follow
are in [AGENTS.md](../AGENTS.md).
