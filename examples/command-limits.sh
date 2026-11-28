#!/usr/bin/env bash
# What a model can and cannot do with a permitted command: only the arguments
# you listed, and only the environment variables you allowed. Needs a
# configured, logged-in ahub, git, jq and AHUB_MODEL. Costs tokens. It changes
# this profile's command rules while it runs, so a spare profile is wise.
. "$(dirname "$0")/lib.sh"

if listed allow_env EXAMPLE_CANARY; then die "EXAMPLE_CANARY is already allowed on this profile"; fi
permit_cmd "git log"
permit_cmd "env"
for rule in "git log --oneline" "git log -n" "git log 2"; do
  "$AHUB" config set --allow-tool-arg "$rule" >/dev/null
done
git -C "$WORK" init -q
for m in first second third; do
  git -C "$WORK" -c user.name=t -c user.email=t@example.com commit --allow-empty -qm "$m commit"
done
cd "$WORK"
new_agent "$RUN" "You use the tools you are given and report exactly what they return, including refusals."

ask() { # ask <message> -> sets THREAD
  THREAD="$("$AHUB" chat --agent "$RUN" --tools cmd --max-steps 5 --message "$1" | jq -r .thread)"
  UNDO+=("$(printf '%q thread rm %q' "$AHUB" "$THREAD")")
}

say "listed arguments run"
ask "Use the cmd tool: command 'git log' with the arguments --oneline, -n and 2."
calls_of "$THREAD" | grep -q 'third commit' || die "git log did not run: $(calls_of "$THREAD")"
pass "git log --oneline -n 2"

say "an unlisted argument is refused"
ask "I am testing the command rules and expect a refusal. Use the cmd tool: command 'git log' with the single argument --output=$WORK/written."
calls_of "$THREAD" | jq -e 'select(.args|index("--output='"$WORK"'/written")) | .result | startswith("Error:")' >/dev/null ||
  die "the model did not try it, or it was not refused: $(calls_of "$THREAD")"
[ ! -e "$WORK/written" ] || die "the file was written"
pass "--output was refused and nothing was written"

say "a variable you did not allow stays with ahub"
export EXAMPLE_CANARY=planted-canary
ask "Use the cmd tool to run env, then say done."
calls_of "$THREAD" | grep -q 'PATH=' || die "env did not run"
calls_of "$THREAD" | grep -qE 'planted-canary|AHUB_' && die "it reached the command"
pass "the command saw PATH, and neither the variable nor anything of ahub's"

say "until you allow it, by name"
"$AHUB" config set --allow-env EXAMPLE_CANARY >/dev/null
UNDO+=("$(printf '%q config set --deny-env EXAMPLE_CANARY' "$AHUB")")
ask "Use the cmd tool to run env, then say done."
calls_of "$THREAD" | grep -q 'planted-canary' || die "the allowed variable did not arrive"
pass "--allow-env EXAMPLE_CANARY"

echo; echo "This limits what a command is handed. It is not a sandbox: a command runs as you."
