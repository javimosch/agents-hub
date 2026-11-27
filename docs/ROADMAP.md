# Roadmap

Ordered by what most threatens the [north star](NORTH-STAR.md). The order is a
proposal and there are no dates. Every item starts with a failing test.

**Reproduced** means it was observed. **From reading** means the code suggests it
and nobody has reproduced it yet: reproduce it first, and drop it if you can't.

## Now: close the known holes

| Item | Promise | Evidence |
|---|---|---|
| `cmd` prefixes allow dangerous arguments (`git log --output=<path>`), and children inherit the environment. **Needs a decision:** argument rules per command, or a sandbox | only what you allowed | reproduced: the file write. From reading: the environment |

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
| `schedule show`, `run` and others still take the schedule's name through `--agent`, a leftover that reads as an agent filter | from reading |
| Flow and `cmd` output is capped only after it has all been captured in memory | from reading |
| `update` runs the downloaded binary's smoke test with no timeout | from reading |
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
