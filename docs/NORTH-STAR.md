# North star: agent runs you can leave unattended

The bet: you can put `ahub` on a timer, walk away, and trust what happens. Every
run either does its job or **fails loudly**, leaves a record you can **resume or
read**, stays **inside its budget**, and **cannot do more than you allowed**.

A feature that weakens one of these four is not progress. Same rule as
[machin's](https://github.com/javimosch/machin) domains: **grown by building real
things.** A capability is added when a real flow, schedule or agent needs it, and
it is believed after being run against a real model.

## The four promises

✅ holds as far as we know · 🟡 holds with known holes · ❌ gap

| Promise | Status | What holds | Known holes |
|---|---|---|---|
| **Fails loudly** | 🟡 | typed exit codes; `schedule tick` exits 105 if any run failed; flows never prompt | the embedded guide names commands that do not exist |
| **Resumable record** | 🟡 | question stored before the model call; full history is paged; a step-limited turn can be resumed | a crash or store failure mid tool loop can leave unanswered calls; threads damaged before the fix are not repaired |
| **Inside budget** | 🟡 | checked before a turn; spend recorded however the turn ends, including compaction and eval judges | a failure to record spend is ignored |
| **Only what you allowed** | 🟡 | allowlists live in the profile; argv, never a shell line; writes need `--allow-writes` | an allowed prefix such as `git log` also permits `--output=<path>`, which writes files; child processes inherit the environment |

## Capability matrix

| Capability | Status | Notes |
|---|---|---|
| Agents, threads, messages | ✅ | owner-scoped in bkn |
| OpenAI-compatible providers, streaming | ✅ | one interface, one implementation |
| Memory and skills (memgraph) | ✅ | budgeted; degrade unless marked required |
| Tool loop | 🟡 | bounded by `--max-steps`; see "Resumable record" |
| `cmd` tool | 🟡 | prefix allowlist; see "Only what you allowed" |
| Background jobs | 🟡 | survive the turn; see the timeout and ownership items in the roadmap |
| Flows | ✅ | validate, run, pause, resume; only `ask` calls a model |
| Schedules | 🟡 | scratchpad and lease; the lease has suspected races |
| Evals | ✅ | run and compare |
| Compaction | 🟡 | its kept boundary can split a tool exchange |
| Install and self-update | ✅ | content hash, verified before swapping |
| cli-output-spec, cli-guide-spec | 🟡 | conforming shape, but the guide content has drifted |
| cli-feedback-spec, cli-telemetry-spec | ❌ | not started |

✅ in a capability row means shipped with no hole that we know of. It does not
mean proven free of bugs.

## How we know

| Signal | 2026-11-27 |
|---|---|
| Live suite (`test/t-hub.sh`) | 351 passed, 0 failed |
| Unit coverage: `eval` / `provider` / `flow` / `hub` | 76% / 54% / 46% / 24% |
| No unit tests | CLI, `bkn`, `memory`, `update`, `out`, `guide` |
| TDD | mandatory from 2026-11-27; older code is not yet covered |

**The north star is reached** when all four promises are ✅, and each is held by
a test that has been seen to fail.

## Milestones

| Date | What it added or surfaced |
|---|---|
| 2026-09-09 | Chat over bkn, memory and skills, tool loop, flows, schedules, evals, jobs, compaction, self-update |
| 2026-09-10 | Fixes for unattended runs; flow working directory and run read-back |
| 2026-11-27 | Review found and fixed three bugs: history cut at 50 rows, `max_steps` leaving threads unresumable, failed turns not counted. TDD became mandatory |
