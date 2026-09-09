#!/usr/bin/env bash
# Live suite: a real bkn, a real HTTP provider, the real binary.
#
# Nothing here is mocked inside the process under test. The provider is a
# separate server that echoes its prompt back, so an assertion can prove the
# system message and the replayed history actually reached the model rather
# than merely that a call was made.
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
AHUB="$ROOT/bin/ahub"
BKN="${BKN_BIN:-$HOME/ai/bkn/bin/bkn}"
WORK="$(mktemp -d)"
BKN_PORT="${BKN_PORT:-$((20000 + RANDOM % 20000))}"
PROV_PORT="${PROV_PORT:-$((20000 + RANDOM % 20000))}"
PASS=0; FAIL=0

cleanup() {
  [ -n "${BKN_PID:-}" ] && kill "$BKN_PID" 2>/dev/null
  [ -n "${PROV_PID:-}" ] && kill "$PROV_PID" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

chk() { # chk <label> <expected> <actual>
  if [ "$2" = "$3" ]; then PASS=$((PASS+1)); printf '  ok   %s\n' "$1"
  else FAIL=$((FAIL+1)); printf '  FAIL %s\n       want: %s\n       got:  %s\n' "$1" "$2" "$3"; fi
}
chkin() { # chkin <label> <needle> <haystack>
  case "$3" in *"$2"*) PASS=$((PASS+1)); printf '  ok   %s\n' "$1";;
  *) FAIL=$((FAIL+1)); printf '  FAIL %s\n       want substring: %s\n       got: %s\n' "$1" "$2" "$3";; esac
}
j() { python3 -c 'import json,sys;d=json.load(sys.stdin)
for k in sys.argv[1].split("."):
    d = d[int(k)] if isinstance(d, list) else d.get(k)
    if d is None: break
print(d if d is not None else "")' "$1"; }

[ -x "$AHUB" ] || { echo "build first: go build -o bin/ahub ."; exit 1; }
[ -x "$BKN" ] || { echo "no bkn binary at $BKN (set BKN_BIN)"; exit 1; }

# --- the stack -------------------------------------------------------------
export BKN_DATA="$WORK/bkn.db"
export BKN_ADMIN_TOKEN="admin-$RANDOM$RANDOM"
"$BKN" serve --host 127.0.0.1 --port "$BKN_PORT" >"$WORK/bkn.log" 2>&1 &
BKN_PID=$!
python3 "$ROOT/test/fake-provider.py" "$PROV_PORT" >"$WORK/prov.log" 2>&1 &
PROV_PID=$!

B="http://127.0.0.1:$BKN_PORT"
P="http://127.0.0.1:$PROV_PORT"
for _ in $(seq 1 50); do curl -sf "$B/_health" >/dev/null && break; sleep 0.1; done
for _ in $(seq 1 50); do curl -sf -X POST "$P/control/fail" -d '{"fail":false}' >/dev/null && break; sleep 0.1; done

export AHUB_CONFIG="$WORK/config.json"
export AHUB_BKN_URL="$B"
export AHUB_PROVIDER_URL="$P"
export AHUB_PROVIDER_KEY="not-checked"
a() { "$AHUB" "$@" 2>"$WORK/err"; }
code() { "$AHUB" "$@" >"$WORK/out" 2>"$WORK/err"; echo $?; }
err_type() { j error.type <"$WORK/err"; }

echo "== agents-hub live suite (bkn :$BKN_PORT, provider :$PROV_PORT) =="

# --- introspection works with no backend at all ----------------------------
chk "help-json lists chat"            "chat"    "$("$AHUB" help-json | python3 -c 'import json,sys;print("chat" if "chat" in json.load(sys.stdin)["commands"] else "no")')"
chk "help-json carries exit code 105" "model provider failed or the turn was interrupted" "$("$AHUB" help-json | j exit_codes.105)"
chk "guide is machine-readable"       "ahub"    "$("$AHUB" guide | j tool)"
chk "unknown command exits 80"        "80"      "$(code frobnicate)"
chk "unknown command is typed"        "unknown_command" "$(err_type)"

# --- auth gating -----------------------------------------------------------
chk "no token exits 90"               "90"      "$(code assistant list)"
chk "no token is typed"               "not_authenticated" "$(err_type)"

# --- setup and login -------------------------------------------------------
"$BKN" auth user create dev@hub.test --password hunter2hunter2 --role admin >/dev/null 2>&1
chk "setup declares 3 collections"    "3"       "$(a setup --admin-token "$BKN_ADMIN_TOKEN" | python3 -c 'import json,sys;print(len(json.load(sys.stdin)["collections"]))')"
chk "setup without admin token fails" "82"      "$(code setup)"
chk "login stores a profile"          "dev@hub.test" "$(a login --email dev@hub.test --password hunter2hunter2 | j email)"
chk "whoami answers"                  "dev@hub.test" "$(a whoami | j me.user.email)"
chk "config hides the token"          "True"    "$(a config show | j config.token_set)"

# --- assistants ------------------------------------------------------------
chk "create needs a model"            "82"      "$(code assistant create --name nomodel)"
chk "create needs a name"             "82"      "$(code assistant create --model m1)"
AID="$(a assistant create --name helper --model m1 --system "you are helper" --temperature 0.2 | j assistant.id)"
chk "created assistant has an id"     "True"    "$([ -n "$AID" ] && echo True || echo False)"
chk "assistant is listed"             "helper"  "$(a assistant list | j assistants.0.name)"
chk "resolve by name"                 "$AID"    "$(a assistant get helper | j assistant.id)"
chk "resolve by id"                   "helper"  "$(a assistant get "$AID" | j assistant.name)"
chk "unknown assistant exits 92"      "92"      "$(code assistant get ghost)"
chk "unknown assistant is typed"      "not_found" "$(err_type)"
chk "update changes the system"       "sterner" "$(a assistant update helper --system sterner | j assistant.system)"
chk "update needs a field"            "82"      "$(code assistant update helper)"
chk "bad temperature exits 85"        "85"      "$(code assistant create --name t --model m --temperature abc)"

# --- a chat turn -----------------------------------------------------------
chk "chat needs a message"            "82"      "$(code chat --assistant helper)"
OUT="$(a chat --assistant helper --message "first question")"
TID="$(printf '%s' "$OUT" | j thread)"
chk "chat opened a thread"            "True"    "$([ -n "$TID" ] && echo True || echo False)"
chkin "system prompt reached the model" "roles=system,user" "$(printf '%s' "$OUT" | j content)"
chkin "the question reached the model"  "echo=first question" "$(printf '%s' "$OUT" | j content)"
chk "usage is recorded"               "7"       "$(printf '%s' "$OUT" | j usage.completion_tokens)"
chk "model is recorded"               "m1"      "$(printf '%s' "$OUT" | j model)"

# --- history replay --------------------------------------------------------
OUT2="$(a chat --assistant helper --thread "$TID" --message "second question")"
chkin "history was replayed"          "roles=system,user,assistant,user" "$(printf '%s' "$OUT2" | j content)"
chk "same thread continued"           "$TID"    "$(printf '%s' "$OUT2" | j thread)"
chk "thread show returns 4 messages"  "4"       "$(a thread show "$TID" | j count)"
chk "messages are in order"           "user"    "$(a thread show "$TID" | j messages.0.role)"
chk "seq increments"                  "3"       "$(a thread show "$TID" | j messages.3.seq)"
chk "thread counts its messages"      "4"       "$(a thread show "$TID" | j thread.messages)"
chk "thread is titled from turn one"  "first question" "$(a thread show "$TID" | j thread.title)"
chk "threads are listed"              "helper"  "$(a thread list | j threads.0.assistant)"
chk "unknown thread exits 92"         "92"      "$(code chat --assistant helper --thread nope --message hi)"

# --- streaming -------------------------------------------------------------
S="$(a chat --assistant helper --message "stream me" --stream)"
chk "stream emits NDJSON deltas"      "delta"   "$(printf '%s' "$S" | head -1 | j type)"
chk "every stream line is JSON"       "ok"      "$(printf '%s' "$S" | python3 -c 'import json,sys
[json.loads(l) for l in sys.stdin if l.strip()]
print("ok")')"
chk "stream ends with the envelope"   "True"    "$(printf '%s' "$S" | tail -1 | j ok)"
chkin "streamed content is assembled" "echo=stream me" "$(printf '%s' "$S" | tail -1 | j content)"

# --- provider failure ------------------------------------------------------
curl -sf -X POST "$P/control/fail" -d '{"fail":true}' >/dev/null
chk "provider failure exits 105"      "105"     "$(code chat --assistant helper --thread "$TID" --message boom)"
chk "provider failure is typed"       "provider_error" "$(err_type)"
chk "provider failure is recoverable" "True"    "$(j error.recoverable <"$WORK/err")"
chk "the question survived the failure" "5"     "$(a thread show "$TID" | j count)"
chk "the stored question is the failed one" "boom" "$(a thread show "$TID" | j messages.4.content)"
chk "the thread count stays honest"   "5"       "$(a thread show "$TID" | j thread.messages)"
curl -sf -X POST "$P/control/fail" -d '{"fail":false}' >/dev/null

# --- tenancy ---------------------------------------------------------------
# The whole security claim of this design is that bkn, not ahub, keeps users
# apart. These assertions are that claim: a second user with a valid token,
# using the same binary, holding the first user's ids.
"$BKN" auth user create other@hub.test --password hunter2hunter2 >/dev/null 2>&1
b() { AHUB_PROFILE=other "$AHUB" "$@" 2>"$WORK/err"; }
bcode() { AHUB_PROFILE=other "$AHUB" "$@" >"$WORK/out" 2>"$WORK/err"; echo $?; }
b login --email other@hub.test --password hunter2hunter2 >/dev/null
chk "second user is a different identity" "other@hub.test" "$(b whoami | j me.user.email)"
chk "second user sees no assistants"   "0"       "$(b assistant list | j count)"
chk "second user sees no threads"      "0"       "$(b thread list | j count)"
chk "second user cannot read the assistant by id" "92" "$(bcode assistant get "$AID")"
chk "second user cannot read the thread"          "92" "$(bcode thread show "$TID")"
chk "second user cannot chat into the thread"     "92" "$(bcode chat --assistant helper --thread "$TID" --message steal)"
chk "second user cannot delete the assistant"     "92" "$(bcode assistant rm "$AID")"
chk "the assistant is still there"     "helper"  "$(a assistant get "$AID" | j assistant.name)"

# --- bkn down --------------------------------------------------------------
kill "$BKN_PID" 2>/dev/null; wait "$BKN_PID" 2>/dev/null
chk "bkn down exits 100"              "100"     "$(code assistant list)"
chk "bkn down is typed"               "connection_error" "$(err_type)"
chk "bkn down is recoverable"         "True"    "$(j error.recoverable <"$WORK/err")"
BKN_PID=""

echo
echo "[$PASS passed, $FAIL failed]"
[ "$FAIL" -eq 0 ]
