# Roadmap

Ordered by what most threatens the [north star](NORTH-STAR.md). The order is a
proposal and there are no dates. Every item starts with a failing test.

**Reproduced** means it was observed. **From reading** means the code suggests it
and nobody has reproduced it yet: reproduce it first, and drop it if you can't.

## Now: close the known holes

| Item | Promise | Evidence |
|---|---|---|
| `cmd` prefixes allow dangerous arguments (`git log --output=<path>`), and children inherit the environment. **Needs a decision:** argument rules per command, or a sandbox | only what you allowed | reproduced: the file write. From reading: the environment |
| Timeouts leave grandchildren running (a 2s timeout took 15s). Likely fix: kill the process group and set `cmd.WaitDelay`. Output is also capped only after capture | fails loudly | reproduced: the timeout. From reading: the output cap |
| The guide names `ahub agent run`, `agent tick` and `agent scratchpad`; the commands are under `schedule`. Add a test that checks guide commands against the binary | fails loudly | reproduced |

## Next: survive concurrency and crashes

| Item | Evidence |
|---|---|
| Replay should tolerate unanswered tool calls left by a crash or store failure | from reading |
| Compaction should not split a tool exchange | from reading |
| Concurrent chats on one thread can reuse a `seq` | from reading |
| Schedule lease: takeover race, no renewal, release without an owner check, exit while holding it | from reading |
| A job's PID is stored without a hostname, so stop and reap may act on the wrong machine | from reading |
| Report a failure to record spend | from reading |
| `cmd` silently drops extra words in `command` | from reading |
| memgraph output over 2 MiB may block until the timeout | from reading |
| `IsLoopbackHost` is never called: wire it in or remove it | from reading |

## Then: make the tests carry the promises

- Unit tests for the CLI, `bkn`, `memory` and `update`, added as each is touched.
- Make `test/fake-provider.py` reject unanswered tool calls, and add live cases
  for long threads, the step limit and spend.
- Prove that the characterization tests for the budget and history fixes can
  fail, by breaking the code briefly.

## Later: pulled by real use

- cli-feedback-spec and cli-telemetry-spec.
- A second provider implementation, when a vendor's shape demands it.
- Prompt improvement from failed eval runs, scored against the run it came from.
- OpenAPI import, only if allowlisted `curl` proves too little.

What we have decided not to build is in [VISION.md](VISION.md#not-the-goal).
