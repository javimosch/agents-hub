#!/usr/bin/env bash
# A bad answer becomes a test: red on the answer you disliked, green after the
# fix. Needs a configured, logged-in ahub, jq, and AHUB_MODEL. Costs tokens.
. "$(dirname "$0")/lib.sh"

say "an agent that says too much"
new_agent "$RUN" "You are a friendly assistant. Always explain your working in full sentences."
FIRST="$("$AHUB" chat --agent "$RUN" --message "What is 17 * 23?")"
THREAD="$(jq -r .thread <<<"$FIRST")"
UNDO+=("$(printf '%q thread rm %q' "$AHUB" "$THREAD")")
jq -r .content <<<"$FIRST" | head -5

say "the bad thread becomes a case"
# Refused if the answer already passes: such a case would catch nothing.
"$AHUB" eval create --from-thread "$THREAD" --name "$RUN" --case bare-number \
  --expect equals=391 | jq -c '{case, on_the_original}'
UNDO+=("$(printf '%q eval rm %q' "$AHUB" "$RUN")")

# Each eval run keeps its own thread; these are removed with the rest.
forget_threads() {
  local t
  for t in $(jq -r '.cases[].thread // empty' <<<"$1"); do
    UNDO+=("$(printf '%q thread rm %q' "$AHUB" "$t")")
  done
}

say "red"
BEFORE="$("$AHUB" eval run "$RUN")"
forget_threads "$BEFORE"
[ "$(jq .failed <<<"$BEFORE")" = 1 ] || die "expected the case to fail before the fix"
pass "the case fails"

say "fix the prompt, then green"
"$AHUB" agent update "$RUN" --system "When asked for a value, reply with the bare value only." >/dev/null
AFTER="$("$AHUB" eval run "$RUN")"
forget_threads "$AFTER"
[ "$(jq .passed <<<"$AFTER")" = 1 ] || die "still failing: $(jq -r '.cases[0].answer' <<<"$AFTER")"
pass "the case passes"
"$AHUB" eval compare "$(jq -r .run <<<"$BEFORE")" "$(jq -r .run <<<"$AFTER")" | jq -r .summary
