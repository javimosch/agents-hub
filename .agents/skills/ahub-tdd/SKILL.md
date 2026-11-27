---
name: ahub-tdd
description: Mandatory test-driven workflow for the ahub repo. Use BEFORE changing any behaviour here - bug fix, feature, or refactor. Covers red-green-refactor, which test layer to use, the fake bkn and strict provider, unit test hygiene, and the commit gate.
---

# TDD in ahub

Every behaviour change starts with a test that fails. No exceptions for
"small" fixes: the 50-row history bug was one missing query parameter and
passed 351 live checks.

## The loop

1. **Red.** Write the smallest test that states the behaviour. Run it.
2. **Read the failure.** It must fail for the reason you claim
   (`got 50 messages, want 60`). A compile error, a nil panic or a typo in the
   fake is not red. If it passes straight away, it does not test your change.
3. **Green.** Write the least code that passes it.
4. **Refactor** with the tests green. No new behaviour in this step.
5. Run the gate (below).

Report both runs: what failed before, what passes after.

| Change | First step |
|---|---|
| Bug | A test reproducing it. Confirm on the real stack too if a fake is involved |
| Feature | A test for the first observable behaviour, then one at a time |
| Refactor | Tests green before and after. If the code has none, first add characterization tests that pin what it does now |
| Untested code you must touch | Characterize first, then red for the change. Most of the repo predates this rule |

A characterization test passes on first run. Prove it can fail: break the
code briefly, see it go red, restore.

If a fix exposes a second bug (paging made `--history` cut tool exchanges in
half), that gets its own failing test too.

## Which layer

| Layer | Where | Use for |
|---|---|---|
| Unit (default) | `*_test.go` beside the code | logic, boundaries, error paths. No network beyond `httptest`, no subprocess unless it is the thing under test, no real model |
| Live | `test/t-hub.sh` | wiring: CLI flags, exit codes, error types, real bkn, tenancy |
| Real model | by hand, see AGENTS.md | what a mock cannot tell you |

Many unit, some live. A change to CLI-visible behaviour needs a live check as
well as the unit test.

## Test doubles (`internal/hub`)

- `newTestHub(t)` (`fakebkn_test.go`): a fresh in-memory bkn over HTTP, so the
  real `bkn.Client` runs. `seed` writes around the code under test; `all`
  reads the unpaged truth.
- `scripted` (`fakeprovider_test.go`): replays `answer`, `asksFor` or error
  steps, records every prompt in `seen`, and rejects unpaired tool calls.
- `noopRegistry(&n)`: one tool that counts its runs.

**A fake must be at least as strict as the real thing.** Both bugs above hid
behind lenient doubles: the mock provider accepted unanswered tool calls, and
no test went past bkn's default page. When code starts relying on a new bkn or
provider behaviour, read it in the source (`~/ai/bkn`), copy it into the fake
with a comment saying where it came from, and check it once on the live stack.

Fake at the boundary (HTTP, `provider.Provider`, an executable on disk). Do
not add an interface to production code only so a test can mock it.

## Hygiene

- **One behaviour per test**, named as the sentence it proves:
  `TestMaxStepsLeavesAResumableThread`. Not `TestChat2`.
- **Table tests** for boundaries and for every way out of a function. Test the
  edges (0, page-1, page, page+1), not one comfortable value. Enumerate exit
  paths: the budget bug lived in the returns nobody listed.
- **Assert outcomes**, not call sequences: what is stored, what was sent to
  the provider, what came back.
- **No shared state.** Each test builds its own hub, using `t.TempDir()`,
  `t.Setenv` and `t.Cleanup`. No order dependence.
- **No sleeps, no wall clock, no random ports by hand.** Pass times in
  (`fixedNow`) and use `httptest`.
- **Assert on errors by type** (`errors.As`), not message text, unless the
  text is the contract.
- **Failure messages say got and want.**
- Helpers call `t.Helper()`. Test files stay under 500 lines.
- Never weaken, skip or delete a failing test to get green. Fix the code, or
  say why the test's expectation was wrong.
- A flaky test is a bug. Reproduce it with `-count=50 -shuffle=on`.

## Commands

```sh
# inner loop: targeted, with a timeout so a hang is obvious
go test ./internal/hub/ -run TestMaxSteps -timeout 20s

# after green: the whole package, since a new test can break its siblings
go test ./internal/hub/ -timeout 60s

# the gate, before every commit
.githooks/pre-commit && go build -o bin/ahub . && bash test/t-hub.sh
```

Enable the hook once per clone: `git config core.hooksPath .githooks`.
The hook cannot tell whether a test came first. That part is on you.

`go test -cover ./...` finds untested code. It is a map, not a target: a line
executed without an assertion is not tested.

## Origin

Adapted from `the internal project`'s testing skills (`unit-testing-best-practices`,
`unit-functional-e2e-tests`, `the internal project-tests-knowledge`) and its
`.githooks`: layered tests, targeted inner loop with a full confirm, isolation,
mock-fidelity traps and a commit gate. Their Jest, MongoDB and Vue specifics
were left behind. The red-first rule is ahub's own.
