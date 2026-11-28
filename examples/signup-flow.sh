#!/usr/bin/env bash
# A sign-up that pauses for an email, then for a password it never keeps.
# Needs a configured, logged-in ahub, jq, curl and python3. No model, no
# tokens. It uses port 8765 and a stand-in API.
. "$(dirname "$0")/lib.sh"
HERE="$(cd "$(dirname "$0")" && pwd)"

if "$AHUB" flow show example-signup >/dev/null 2>&1; then die "a flow named example-signup already exists"; fi
if ! listed allow_hosts 127.0.0.1; then
  "$AHUB" config set --allow-host 127.0.0.1 >/dev/null
  UNDO+=("$(printf '%q config set --deny-host 127.0.0.1' "$AHUB")")
fi

python3 -B "$HERE/signup-api.py" 8765 "$WORK/seen" 2>"$WORK/api.log" &
API=$!
UNDO+=("kill $API")
for _ in $(seq 50); do
  kill -0 "$API" 2>/dev/null || die "the stand-in API did not start: $(tail -1 "$WORK/api.log")"
  (exec 3<>/dev/tcp/127.0.0.1/8765) 2>/dev/null && break
  sleep 0.1
done

"$AHUB" flow create --file "$HERE/signup-flow.json" >/dev/null
UNDO+=("$(printf '%q flow rm example-signup' "$AHUB")")

# A failing command's last line of stderr is its typed error.
run_flow() { CODE=0; OUT="$("$AHUB" flow "$@" 2>"$WORK/err")" || CODE=$?; }
err() { tail -1 "$WORK/err" | jq -r "$1"; }
run_id() { err '.error.suggestions[1] | fromjson | .run'; }
# A password with the characters that used to break a JSON body.
PASSWORD='correct "horse" \ battery & staple'

say "it pauses for the email, then for the password"
run_flow run example-signup
[ "$CODE" = 92 ] && [[ "$(err .error.message)" == *"needs email"* ]] || die "expected a pause for the email: $(cat "$WORK/err")"
RUN_ID="$(run_id)"
run_flow resume "$RUN_ID" --input email=ada@example.test
[ "$CODE" = 92 ] && [[ "$(err '.error.suggestions[0]')" == *"--secret-input password"* ]] || die "expected a pause for the password"
pass "two pauses, exit 92; the second asks for --secret-input"

say "the password is refused on the command line"
run_flow resume "$RUN_ID" --input password=not-this-way
[ "$CODE" = 85 ] && [ "$(err .error.type)" = secret_on_command_line ] || die "expected a refusal, got exit $CODE"
pass "exit 85, and nothing ran"

say "on stdin, it reaches the service and is not kept"
OUT="$(printf '%s' "$PASSWORD" | "$AHUB" flow resume "$RUN_ID" --secret-input password)"
jq -r '.messages[]' <<<"$OUT"
[ "$(jq -r .password "$WORK/seen")" = "$PASSWORD" ] || die "the service got something else"
pass "the service received it exactly"
kept() { jq -e --arg p "$PASSWORD" '[.. | strings | select(contains($p))] | length > 0 or false' >/dev/null; }
STORED="$("$AHUB" flow runs --run "$RUN_ID")"
if kept <<<"$OUT" || kept <<<"$STORED"; then die "the password is in the output or the stored run"; fi
jq -e '.run.data | has("password") | not' <<<"$STORED" >/dev/null || die "the stored run has a password field"
pass "it is in neither the output nor the stored run"

say "a service that repeats it in an error"
run_flow run example-signup --input email=ada@example.test
RUN_ID="$(run_id)"
CODE=0; printf 'zq7-shrt' | head -c 7 | "$AHUB" flow resume "$RUN_ID" --secret-input password >"$WORK/out" 2>"$WORK/err" || CODE=$?
[ "$CODE" = 105 ] || die "expected the sign-up to be refused, got exit $CODE"
if grep -q 'zq7-shr' "$WORK/out" "$WORK/err" || "$AHUB" flow runs --run "$RUN_ID" | grep -q 'zq7-shr'; then
  die "the echoed password got through"
fi
pass "refused with exit 105; the echo is not in the error or the stored run"
