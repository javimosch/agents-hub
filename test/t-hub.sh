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
chk "help-json carries exit code 105" "model provider or memgraph failed, or the turn was interrupted" "$("$AHUB" help-json | j exit_codes.105)"
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
chkin "no memory without a project"   "mem=0"   "$(printf '%s' "$OUT" | j content)"
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

# --- memory ----------------------------------------------------------------
# Against a stand-in memgraph, so these assert ahub's behaviour rather than
# the contents of whoever's machine is running the suite.
export AHUB_MEMGRAPH="$ROOT/test/fake-memgraph.py"
export FAKE_MEMGRAPH_LOG="$WORK/memgraph.argv"

chk "memory status finds the binary"  "True"    "$(a memory status | j available)"
chk "recall returns memories"         "2"       "$(a memory recall --query "port release" --project demo | j count)"
chk "recall reports its budget"       "2"       "$(a memory recall --query "port release" --project demo | j context.used)"
chk "recall needs a query"            "82"      "$(code memory recall)"
chk "save reaches memgraph"           "333"     "$(a memory save --text "a fact" --project demo --type project | j saved.id)"
chk "projects passes through"         "demo"    "$(a memory projects | j projects.projects.0.name)"

# A value that could be read as a flag is refused rather than escaped.
chk "a dashed project is refused"     "85"      "$(code memory recall --query hi --project=-oops)"
chk "a dashed project is typed"       "invalid_value" "$(err_type)"

# The assistant carries the project; the chat turn injects a second system
# message. The echoing provider reports the roles it actually received.
a assistant create --name remembers --model m1 --system "sys" --memory-project demo >/dev/null
M="$(a chat --assistant remembers --message "how does the seeder release its port")"
chkin "memory reaches the model"      "mem=1"   "$(printf '%s' "$M" | j content)"
chk "the turn reports memory use"     "2"       "$(printf '%s' "$M" | j memory.used)"
chk "the memory project is reported"  "demo"    "$(printf '%s' "$M" | j memory.project)"
chkin "the recall query was the message" "how does the seeder release its port" "$(printf '%s' "$M" | j content)"

# argv, not a shell line: a message full of metacharacters arrives verbatim.
a chat --assistant remembers --message 'rm -rf /; $(touch /tmp/pwned) `id` && echo x' >/dev/null
chk "no shell interpretation"         "False"   "$([ -e /tmp/pwned ] && echo True || echo False)"
chk "the whole message reached memgraph" "True" "$(python3 -c '
import json,sys
want = "rm -rf /; $(touch /tmp/pwned) \x60id\x60 && echo x"
for line in open(sys.argv[1]):
    argv = json.loads(line)
    if want in argv: print("True"); break
else: print("False")' "$WORK/memgraph.argv")"

chkin "--no-memory skips recall"      "mem=0"   "$(a chat --assistant remembers --no-memory --message hi | j content)"

# A memory store that is down degrades the answer; it does not withhold it.
FAKE_MEMGRAPH_FAIL=1 "$AHUB" chat --assistant remembers --message "still answer me" >"$WORK/degraded" 2>/dev/null
chk "a failed recall still answers"   "True"    "$(j ok <"$WORK/degraded")"
chkin "the failure is reported"       "store on fire" "$(j memory.error <"$WORK/degraded")"
chk "nothing was injected"            "0"       "$(j memory.used <"$WORK/degraded")"
chk "--memory-required makes it fatal" "105"    "$(FAKE_MEMGRAPH_FAIL=1 code chat --assistant remembers --memory-required --message x)"

# A machine without memgraph: explicit memory is an error, a configured
# assistant still answers.
chk "missing memgraph exits 92"       "92"      "$(AHUB_MEMGRAPH=/nonexistent code memory status)"
chk "missing memgraph is typed"       "memgraph_missing" "$(err_type)"
chk "explicit --memory-project needs it" "92"   "$(AHUB_MEMGRAPH=/nonexistent code chat --assistant helper --memory-project demo --message hi)"
chk "a configured assistant degrades" "True"    "$(AHUB_MEMGRAPH=/nonexistent a chat --assistant remembers --message hi | j ok)"

# --- skills ----------------------------------------------------------------
# A skill is instruction, not reference: it is loaded only when named, and the
# path it is read from comes from memgraph, never from the caller.
cat > "$WORK/SKILL.md" <<'SKILL'
---
name: demo-skill
---
# Demo skill
When asked about ports, always mention the stop channel.
SKILL
export FAKE_SKILL_PATH="$WORK/SKILL.md"

chk "skill search finds it"           "demo-skill" "$(a skill search "ports" | j skills.0.name)"
chk "skill recommend answers"         "demo-skill" "$(a skill recommend "release a port cleanly" | j skills.0.name)"
chkin "skill show loads the file"     "stop channel" "$(a skill show demo-skill | j content)"
chk "an unknown skill is not found"   "92"      "$(code skill show no-such-skill)"

K="$(a chat --assistant helper --skill demo-skill --message "how do I release a port")"
chkin "the skill reaches the model"   "skl=1"   "$(printf '%s' "$K" | j content)"
chk "the turn reports the skill"      "demo-skill" "$(printf '%s' "$K" | j skills.loaded.0)"
chkin "no skill without one named"    "skl=0"   "$(a chat --assistant helper --message plain | j content)"
chkin "--no-skills wins"              "skl=0"   "$(a chat --assistant helper --skill demo-skill --no-skills --message x | j content)"

# An assistant can carry its skills, and a missing one degrades by default.
a assistant create --name skilled --model m1 --system "sys" --skills demo-skill,ghost-skill >/dev/null
SK="$(a chat --assistant skilled --message "how do I release a port")"
chkin "a configured skill is loaded"  "skl=1"   "$(printf '%s' "$SK" | j content)"
chk "the missing one is reported"     "True"    "$(printf '%s' "$SK" | python3 -c 'import json,sys;print("ghost-skill" in (json.load(sys.stdin)["skills"].get("skipped") or {}))')"
chk "--skills-required makes it fatal" "92"     "$(code chat --assistant skilled --skills-required --message x)"

# A skill whose file is not markdown is refused, wherever the path came from.
# 85, not 105: this tool refused the path, memgraph did not fail.
chk "a non-markdown skill is refused" "85"      "$(FAKE_SKILL_BADPATH=1 code skill show bad-skill)"
chkin "and it says why"               "not markdown" "$(j error.message <"$WORK/err")"

# --- tools -----------------------------------------------------------------
# The fake model asks for one tool, then answers with the result in its
# history: the same two-pass shape a real model produces.
chk "the catalog needs nothing"       "4"       "$("$AHUB" tool list | python3 -c 'import json,sys;print(len(json.load(sys.stdin)["tools"]))')"
chk "the catalog marks what writes"   "memory_save" "$("$AHUB" tool list | python3 -c 'import json,sys;print(",".join(t["name"] for t in json.load(sys.stdin)["tools"] if t["writes"]))')"
chk "an unknown tool is refused"      "85"      "$(code chat --assistant helper --tools no_such_tool --message x)"
chkin "and it lists the real ones"    "memory_recall" "$(j error.message <"$WORK/err")"
chk "a writing tool needs consent"    "85"      "$(code chat --assistant helper --tools memory_save --message x)"
chkin "and it says which flag"        "--allow-writes" "$(j error.message <"$WORK/err")"
chk "consent enables it"              "True"    "$(a chat --assistant helper --tools memory_save --allow-writes --message x | j ok)"

T1="$(a chat --assistant helper --tools memory_recall --message "how do I release a port")"
chk "the loop ran two steps"          "2"       "$(printf '%s' "$T1" | j steps)"
chk "the tool ran"                    "memory_recall" "$(printf '%s' "$T1" | j tool_runs.0.name)"
chk "the tool produced output"        "True"    "$(printf '%s' "$T1" | python3 -c 'import json,sys;print(json.load(sys.stdin)["tool_runs"][0]["chars"] > 0)')"
chkin "the result reached the model"  "toolout=## Recalled memories" "$(printf '%s' "$T1" | j content)"
chk "usage is summed across steps"    "True"    "$(printf '%s' "$T1" | python3 -c 'import json,sys;print(json.load(sys.stdin)["usage"]["completion_tokens"] > 7)')"

TT="$(printf '%s' "$T1" | j thread)"
chk "the tool turns are stored"       "tool"    "$(a thread show "$TT" | j messages.2.role)"
chk "the call is stored with them"    "True"    "$(a thread show "$TT" | python3 -c 'import json,sys
msgs = json.load(sys.stdin)["messages"]
print(any(m["role"]=="assistant" and m.get("tool_calls") for m in msgs))')"
chk "the tool turn keeps its id"      "call_1"  "$(a thread show "$TT" | j messages.2.tool_call_id)"

# Resuming a thread that contains tool turns must replay them intact: a
# provider rejects a tool result whose matching call is missing.
R="$(a chat --assistant helper --thread "$TT" --tools memory_recall --message "and now?")"
chk "a thread with tool turns resumes" "True"   "$(printf '%s' "$R" | j ok)"
chkin "the replay kept the tool role" "roles=system,user,assistant,tool" "$(printf '%s' "$R" | j content)"

# A tool that fails answers the model instead of killing the turn.
chk "a failing tool still answers"    "True"    "$(FAKE_MEMGRAPH_FAIL=1 a chat --assistant helper --tools memory_recall --message x | j ok)"
chk "and the failure is reported"     "True"    "$(FAKE_MEMGRAPH_FAIL=1 a chat --assistant helper --tools memory_recall --message x | python3 -c 'import json,sys;print(bool(json.load(sys.stdin)["tool_runs"][0]["failed"]))')"

# The step cap is a budget, so hitting it is said out loud. When the model
# never produced text, that is a failure with a resumable thread, not an
# answer: ok:true with an empty string is the worst thing to hand an agent.
chk "max-steps without an answer fails" "105"   "$(code chat --assistant helper --tools memory_recall --max-steps 1 --message x)"
chk "and it is typed"                 "max_steps_exhausted" "$(err_type)"
chkin "and it says how to resume"     "--max-steps 20" "$(j error.suggestions.0 <"$WORK/err")"

# A tool called with a missing required parameter is told which one, rather
# than being handed whatever error an empty string produces downstream.
tool_mode() { curl -sf -X POST "$P/control/tool" -d "$1" >/dev/null; }
tool_mode '{"args":"{\"nope\":\"x\"}"}'
MA="$(a chat --assistant helper --tools memory_recall --message x)"
chkin "a missing parameter is named"  "requires the parameter" "$(printf '%s' "$MA" | j content)"
chk "and the run records it"          "missing required parameter query" "$(printf '%s' "$MA" | j tool_runs.0.failed)"

# A hallucinated tool name gets the real list back, so the model can correct.
tool_mode '{"tool":"memory_search","args":"{}"}'
HT="$(a chat --assistant helper --tools memory_recall,skill_show --message x)"
chkin "an unknown tool lists the real ones" "Available tools: memory_recall, skill_show" "$(printf '%s' "$HT" | j content)"
tool_mode '{"args":"{\"query\":\"ports\"}"}'   # back to the default for later sections

# Streamed tool calls are accumulated from fragments.
ST="$(a chat --assistant helper --tools memory_recall --stream --message "stream a tool")"
chk "a streamed tool call works"      "True"    "$(printf '%s' "$ST" | tail -1 | j ok)"
chk "the streamed loop ran twice"     "2"       "$(printf '%s' "$ST" | tail -1 | j steps)"

# An assistant can carry its tools.
a assistant create --name tooled --model m1 --system sys --tools memory_recall,skill_show >/dev/null
chk "a configured tool set is used"   "2"       "$(a chat --assistant tooled --message x | j steps)"
chk "--no-tools wins"                 "1"       "$(a chat --assistant tooled --no-tools --message x | j steps)"
chk "a turn without tools is one step" "1"      "$(a chat --assistant helper --message x | j steps)"

# --- token refresh ---------------------------------------------------------
# A bkn access token is a 15-minute JWT. An agent must not have to notice.
python3 -c '
import json,sys
p = sys.argv[1]; d = json.load(open(p))
d["profiles"]["default"]["token"] = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.dead.beef"
json.dump(d, open(p, "w"))' "$AHUB_CONFIG"
chk "an expired token is refreshed"   "True"    "$(a whoami | j ok)"
chk "the rotated pair is persisted"   "True"    "$(python3 -c '
import json,sys
d = json.load(open(sys.argv[1]))
print(not d["profiles"]["default"]["token"].endswith("beef"))' "$AHUB_CONFIG")"
# A token handed in explicitly is the caller'"'"'s to manage, not ours to swap.
chk "an explicit bad token is not refreshed" "90" "$(AHUB_TOKEN=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.dead.beef code whoami)"

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
