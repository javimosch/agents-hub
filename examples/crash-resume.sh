#!/usr/bin/env bash
# Kill ahub in the middle of a tool call, then carry on in the same thread.
# Needs a configured, logged-in ahub, jq and AHUB_MODEL. Costs tokens. It
# permits `sleep 8` on this profile while it runs.
. "$(dirname "$0")/lib.sh"

permit_cmd "sleep 8"
new_agent "$RUN" "You use the tools you are given."

say "start a slow turn"
"$AHUB" chat --agent "$RUN" --tools cmd --stream \
  --message "Use the cmd tool to run 'sleep 8', then say done." >"$WORK/stream" 2>"$WORK/err" &
PID=$!
for _ in $(seq 300); do
  grep -q '"type":"tool_call"' "$WORK/stream" && break
  kill -0 "$PID" 2>/dev/null || die "the turn ended early: $(cat "$WORK/err")"
  sleep 0.2
done
grep -q '"type":"tool_call"' "$WORK/stream" || die "no tool call arrived"
THREAD="$(head -1 "$WORK/stream" | jq -r .thread)"   # the first line names it
UNDO+=("$(printf '%q thread rm %q' "$AHUB" "$THREAD")")
pass "the stream named the thread and announced the call"

say "a second turn on the same thread"
code=0; "$AHUB" chat --agent "$RUN" --thread "$THREAD" --message "me too" >/dev/null 2>&1 || code=$?
[ "$code" = 95 ] || die "expected exit 95 (thread_busy), got $code"
pass "refused with exit 95"

say "kill -9, then resume"
kill -9 "$PID"; wait "$PID" 2>/dev/null || true
REPLY="$("$AHUB" chat --agent "$RUN" --thread "$THREAD" \
  --message "Do you know whether your last command finished? Answer briefly.")" || die "the thread did not resume"
pass "resumed at once"
jq -r .content <<<"$REPLY"
