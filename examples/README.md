# Examples

Each script checks what it claims and exits non-zero if that does not hold.
All need a configured, logged-in `ahub` and `jq`. The first three also need a
model that supports tool calls, and cost tokens. The sign-up flow calls none.

```sh
export AHUB_MODEL=<model-id>     # AHUB=<path> to use a particular binary
bash examples/eval-from-thread.sh
bash examples/command-limits.sh  # also needs git
bash examples/crash-resume.sh    # Linux
bash examples/signup-flow.sh     # also needs python3; no AHUB_MODEL, no tokens
```

| Example | What it shows |
|---|---|
| [eval-from-thread.sh](eval-from-thread.sh) | a bad answer becomes a test: red before the fix, green after |
| [command-limits.sh](command-limits.sh) | only listed arguments run; commands see only allowed variables |
| [crash-resume.sh](crash-resume.sh) | a turn killed mid tool call resumes; a second turn is refused |
| [signup-flow.sh](signup-flow.sh) with [its flow](signup-flow.json) | a sign-up that pauses for an email, then a password it never keeps |
| [log-budget.json](log-budget.json) | a flow that keeps the system journal under a budget, with no model call |

**What they touch.** Each creates its own agent and removes it, with its threads
and eval, when it ends. The scores of the eval runs stay as history, and tokens
spent count toward your daily budget. `command-limits.sh` and `crash-resume.sh` add command
rules to the profile and take back what they added; they stop if a rule they
need is already there. A script killed with `kill -9` cannot clean up. To keep
your own profile out of it, point `AHUB_PROFILE` at a spare one.

**A model is not deterministic.** A script can fail because the model did not do
what was asked. The message says which of the two happened where it can tell.
These were last checked on 2026-11-28; the three that call a model used
`z-ai/glm-5.3-flash`. `signup-flow.sh` needs `ahub` 0.15.0 or later, listens
on port 8765, allows host `127.0.0.1` while it runs, and removes its flow. Its
finished runs stay as history.
