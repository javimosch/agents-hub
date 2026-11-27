# Roadmap

Ordered by what most threatens the [north star](NORTH-STAR.md). The order is a
proposal and there are no dates. Every item starts with a failing test.

**Reproduced** means it was observed. **From reading** means the code suggests it
and nobody has reproduced it yet: reproduce it first, and drop it if you can't.

## Now: close the known holes

| Item | Promise | Evidence |
|---|---|---|
| A permitted command runs as your user: it can read the profile and `ahub`'s own environment. Variables are no longer passed on; a sandbox is what is left | only what you allowed | from reading |

## Next: survive concurrency and crashes

| Item | Evidence |
|---|---|
| A run that dies on another machine, or in another container, holds its thread or schedule for up to 30 minutes, and `schedule tick` reports it `skipped` with exit 0 | from reading |
| A lease is renewed between model calls, so one step longer than 30 minutes loses it and ends the turn | from reading |
| A job's PID is stored without a hostname, so stop and reap may act on the wrong machine | from reading |
| Report a failure to record spend | from reading |
| `schedule show`, `run` and others still take the schedule's name through `--agent`, a leftover that reads as an agent filter | from reading |
| Flow and `cmd` output is capped only after it has all been captured in memory | from reading |
| `schedule runs` leaves out the answer, error and tool calls, and no command shows one run | reproduced |
| A flag value that is not a number (`--limit abc`, `--daily-token-budget 5k`) is silently ignored; it should exit 85 | from reading |
| Updates are not signed, and an older build counts as an update | from reading |
| `IsLoopbackHost` is never called: wire it in or remove it | from reading |

## Then: make the tests carry the promises

- Unit tests for the CLI, `bkn`, `memory` and `update`, added as each is touched.
- Add live cases for long threads, the step limit and spend.
- Prove that the characterization tests for the budget and history fixes can
  fail, by breaking the code briefly.

## Later: pulled by real use

- A second provider implementation, when a vendor's shape demands it.
- A sandbox for permitted commands, if its mounts and network can be settled
  (see `spikes/c-sandbox`).
- Prompt improvement from failed eval runs, scored against the run it came from.
- OpenAPI import, only if allowlisted `curl` proves too little.

What we have decided not to build is in [VISION.md](VISION.md#not-the-goal).
