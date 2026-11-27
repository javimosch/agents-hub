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
# stderr may carry context lines before the error object (the spec allows it),
# so the harness reads the last line rather than the whole file.
errj() { tail -1 "$WORK/err" | j "$1"; }
err_type() { errj error.type; }

echo "== agents-hub live suite (bkn :$BKN_PORT, provider :$PROV_PORT) =="

# --- introspection works with no backend at all ----------------------------
chk "help-json lists chat"            "chat"    "$("$AHUB" help-json | python3 -c 'import json,sys;print("chat" if "chat" in json.load(sys.stdin)["commands"] else "no")')"
chk "help-json carries exit code 105" "model provider, memgraph or a flow step failed, or the turn was interrupted" "$("$AHUB" help-json | j exit_codes.105)"
chk "guide is machine-readable"       "ahub"    "$("$AHUB" guide | j tool)"
chk "help-json lists spend"          "spend"   "$("$AHUB" help-json | python3 -c 'import json,sys;print("spend" if "spend" in json.load(sys.stdin)["commands"] else "no")')"
chk "the served guide names the schedule tick" "yes" "$("$AHUB" guide | python3 -c 'import sys;g=sys.stdin.read();print("yes" if "ahub schedule tick" in g and "ahub agent tick" not in g else "no")')"
chk "unknown command exits 80"        "80"      "$(code frobnicate)"
chk "unknown command is typed"        "unknown_command" "$(err_type)"

# --- auth gating -----------------------------------------------------------
chk "no token exits 90"               "90"      "$(code agent list)"
chk "no token is typed"               "not_authenticated" "$(err_type)"

# --- setup and login -------------------------------------------------------
"$BKN" auth user create dev@hub.test --password hunter2hunter2 --role admin >/dev/null 2>&1
chk "setup declares every collection" "12"       "$(a setup --admin-token "$BKN_ADMIN_TOKEN" | python3 -c 'import json,sys;print(len(json.load(sys.stdin)["collections"]))')"
chk "setup without admin token fails" "82"      "$(code setup)"
chk "login stores a profile"          "dev@hub.test" "$(a login --email dev@hub.test --password hunter2hunter2 | j email)"
chk "whoami answers"                  "dev@hub.test" "$(a whoami | j me.user.email)"
chk "config hides the token"          "True"    "$(a config show | j config.token_set)"

# --- agents ------------------------------------------------------------
chk "create needs a model"            "82"      "$(code agent create --name nomodel)"
chk "create needs a name"             "82"      "$(code agent create --model m1)"
AID="$(a agent create --name helper --model m1 --system "you are helper" --temperature 0.2 | j agent.id)"
chk "created agent has an id"     "True"    "$([ -n "$AID" ] && echo True || echo False)"
chk "agent is listed"             "helper"  "$(a agent list | j agents.0.name)"
chk "resolve by name"                 "$AID"    "$(a agent get helper | j agent.id)"
chk "resolve by id"                   "helper"  "$(a agent get "$AID" | j agent.name)"
chk "unknown agent exits 92"      "92"      "$(code agent get ghost)"
chk "unknown agent is typed"      "not_found" "$(err_type)"
chk "update changes the system"       "sterner" "$(a agent update helper --system sterner | j agent.system)"
chk "update needs a field"            "82"      "$(code agent update helper)"
chk "bad temperature exits 85"        "85"      "$(code agent create --name t --model m --temperature abc)"

# --- a chat turn -----------------------------------------------------------
chk "chat needs a message"            "82"      "$(code chat --agent helper)"
OUT="$(a chat --agent helper --message "first question")"
TID="$(printf '%s' "$OUT" | j thread)"
chk "chat opened a thread"            "True"    "$([ -n "$TID" ] && echo True || echo False)"
chkin "system prompt reached the model" "roles=system,user" "$(printf '%s' "$OUT" | j content)"
chkin "no memory without a project"   "mem=0"   "$(printf '%s' "$OUT" | j content)"
chkin "the question reached the model"  "echo=first question" "$(printf '%s' "$OUT" | j content)"
chk "usage is recorded"               "7"       "$(printf '%s' "$OUT" | j usage.completion_tokens)"
chk "model is recorded"               "m1"      "$(printf '%s' "$OUT" | j model)"

# --- history replay --------------------------------------------------------
OUT2="$(a chat --agent helper --thread "$TID" --message "second question")"
chkin "history was replayed"          "roles=system,user,assistant,user" "$(printf '%s' "$OUT2" | j content)"
chk "same thread continued"           "$TID"    "$(printf '%s' "$OUT2" | j thread)"
chk "thread show returns 4 messages"  "4"       "$(a thread show "$TID" | j count)"
chk "messages are in order"           "user"    "$(a thread show "$TID" | j messages.0.role)"
chk "seq increments"                  "3"       "$(a thread show "$TID" | j messages.3.seq)"
chk "thread counts its messages"      "4"       "$(a thread show "$TID" | j thread.messages)"
chk "thread is titled from turn one"  "first question" "$(a thread show "$TID" | j thread.title)"
chk "threads filter by agent"        "0"       "$(a thread list --agent nobody-by-this-name | j count)"
chk "and the filter keeps its own"    "helper"  "$(a thread list --agent helper | j threads.0.agent)"
chk "threads are listed"              "helper"  "$(a thread list | j threads.0.agent)"
chk "unknown thread exits 92"         "92"      "$(code chat --agent helper --thread nope --message hi)"

# --- streaming -------------------------------------------------------------
S="$(a chat --agent helper --message "stream me" --stream)"
chk "stream opens with start"         "start"   "$(printf '%s' "$S" | head -1 | j type)"
chk "start names the thread"          "$(printf '%s' "$S" | tail -1 | j thread)" "$(printf '%s' "$S" | head -1 | j thread)"
chk "stream emits NDJSON deltas"      "delta"   "$(printf '%s' "$S" | sed -n 2p | j type)"
chk "the last line is typed result"   "result"  "$(printf '%s' "$S" | tail -1 | j type)"
chk "every stream line is JSON"       "ok"      "$(printf '%s' "$S" | python3 -c 'import json,sys
[json.loads(l) for l in sys.stdin if l.strip()]
print("ok")')"
chk "stream ends with the envelope"   "True"    "$(printf '%s' "$S" | tail -1 | j ok)"
chkin "streamed content is assembled" "echo=stream me" "$(printf '%s' "$S" | tail -1 | j content)"

# --- provider failure ------------------------------------------------------
curl -sf -X POST "$P/control/fail" -d '{"fail":true}' >/dev/null
chk "provider failure exits 105"      "105"     "$(code chat --agent helper --thread "$TID" --message boom)"
chk "provider failure is typed"       "provider_error" "$(err_type)"
chk "provider failure is recoverable" "True"    "$(errj error.recoverable)"
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

# The agent carries the project; the chat turn injects a second system
# message. The echoing provider reports the roles it actually received.
a agent create --name remembers --model m1 --system "sys" --memory-project demo >/dev/null
M="$(a chat --agent remembers --message "how does the seeder release its port")"
chkin "memory reaches the model"      "mem=1"   "$(printf '%s' "$M" | j content)"
chk "the turn reports memory use"     "2"       "$(printf '%s' "$M" | j memory.used)"
chk "the memory project is reported"  "demo"    "$(printf '%s' "$M" | j memory.project)"
chkin "the recall query was the message" "how does the seeder release its port" "$(printf '%s' "$M" | j content)"

# argv, not a shell line: a message full of metacharacters arrives verbatim.
a chat --agent remembers --message 'rm -rf /; $(touch /tmp/pwned) `id` && echo x' >/dev/null
chk "no shell interpretation"         "False"   "$([ -e /tmp/pwned ] && echo True || echo False)"
chk "the whole message reached memgraph" "True" "$(python3 -c '
import json,sys
want = "rm -rf /; $(touch /tmp/pwned) \x60id\x60 && echo x"
for line in open(sys.argv[1]):
    argv = json.loads(line)
    if want in argv: print("True"); break
else: print("False")' "$WORK/memgraph.argv")"

chkin "--no-memory skips recall"      "mem=0"   "$(a chat --agent remembers --no-memory --message hi | j content)"

# A memory store that is down degrades the answer; it does not withhold it.
FAKE_MEMGRAPH_FAIL=1 "$AHUB" chat --agent remembers --message "still answer me" >"$WORK/degraded" 2>/dev/null
chk "a failed recall still answers"   "True"    "$(j ok <"$WORK/degraded")"
chkin "the failure is reported"       "store on fire" "$(j memory.error <"$WORK/degraded")"
chk "nothing was injected"            "0"       "$(j memory.used <"$WORK/degraded")"
chk "--memory-required makes it fatal" "105"    "$(FAKE_MEMGRAPH_FAIL=1 code chat --agent remembers --memory-required --message x)"

# A machine without memgraph: explicit memory is an error, a configured
# agent still answers.
chk "missing memgraph exits 92"       "92"      "$(AHUB_MEMGRAPH=/nonexistent code memory status)"
chk "missing memgraph is typed"       "memgraph_missing" "$(err_type)"
chk "explicit --memory-project needs it" "92"   "$(AHUB_MEMGRAPH=/nonexistent code chat --agent helper --memory-project demo --message hi)"
chk "a configured agent degrades" "True"    "$(AHUB_MEMGRAPH=/nonexistent a chat --agent remembers --message hi | j ok)"

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

K="$(a chat --agent helper --skill demo-skill --message "how do I release a port")"
chkin "the skill reaches the model"   "skl=1"   "$(printf '%s' "$K" | j content)"
chk "the turn reports the skill"      "demo-skill" "$(printf '%s' "$K" | j skills.loaded.0)"
chkin "no skill without one named"    "skl=0"   "$(a chat --agent helper --message plain | j content)"
chkin "--no-skills wins"              "skl=0"   "$(a chat --agent helper --skill demo-skill --no-skills --message x | j content)"

# An agent can carry its skills, and a missing one degrades by default.
a agent create --name skilled --model m1 --system "sys" --skills demo-skill,ghost-skill >/dev/null
SK="$(a chat --agent skilled --message "how do I release a port")"
chkin "a configured skill is loaded"  "skl=1"   "$(printf '%s' "$SK" | j content)"
chk "the missing one is reported"     "True"    "$(printf '%s' "$SK" | python3 -c 'import json,sys;print("ghost-skill" in (json.load(sys.stdin)["skills"].get("skipped") or {}))')"
chk "--skills-required makes it fatal" "92"     "$(code chat --agent skilled --skills-required --message x)"

# A skill whose file is not markdown is refused, wherever the path came from.
# 85, not 105: this tool refused the path, memgraph did not fail.
chk "a non-markdown skill is refused" "85"      "$(FAKE_SKILL_BADPATH=1 code skill show bad-skill)"
chkin "and it says why"               "not markdown" "$(errj error.message)"

# --- tools -----------------------------------------------------------------
# The fake model asks for one tool, then answers with the result in its
# history: the same two-pass shape a real model produces.
chk "the catalog needs nothing"       "8"       "$("$AHUB" tool list | python3 -c 'import json,sys;print(len(json.load(sys.stdin)["tools"]))')"
chk "the catalog marks what writes"   "memory_save" "$("$AHUB" tool list | python3 -c 'import json,sys;print(",".join(t["name"] for t in json.load(sys.stdin)["tools"] if t["writes"]))')"
chk "an unknown tool is refused"      "85"      "$(code chat --agent helper --tools no_such_tool --message x)"
chkin "and it lists the real ones"    "memory_recall" "$(errj error.message)"
chk "a writing tool needs consent"    "85"      "$(code chat --agent helper --tools memory_save --message x)"
chkin "and it says which flag"        "--allow-writes" "$(errj error.message)"
chk "consent enables it"              "True"    "$(a chat --agent helper --tools memory_save --allow-writes --message x | j ok)"

T1="$(a chat --agent helper --tools memory_recall --message "how do I release a port")"
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
R="$(a chat --agent helper --thread "$TT" --tools memory_recall --message "and now?")"
chk "a thread with tool turns resumes" "True"   "$(printf '%s' "$R" | j ok)"
chkin "the replay kept the tool role" "roles=system,user,assistant,tool" "$(printf '%s' "$R" | j content)"

# A tool that fails answers the model instead of killing the turn.
chk "a failing tool still answers"    "True"    "$(FAKE_MEMGRAPH_FAIL=1 a chat --agent helper --tools memory_recall --message x | j ok)"
chk "and the failure is reported"     "True"    "$(FAKE_MEMGRAPH_FAIL=1 a chat --agent helper --tools memory_recall --message x | python3 -c 'import json,sys;print(bool(json.load(sys.stdin)["tool_runs"][0]["failed"]))')"

# The step cap is a budget, so hitting it is said out loud. When the model
# never produced text, that is a failure with a resumable thread, not an
# answer: ok:true with an empty string is the worst thing to hand an agent.
chk "max-steps without an answer fails" "105"   "$(code chat --agent helper --tools memory_recall --max-steps 1 --message x)"
chk "and it is typed"                 "max_steps_exhausted" "$(err_type)"
chkin "and it says how to resume"     "--max-steps 20" "$(errj error.suggestions.0)"

# A tool called with a missing required parameter is told which one, rather
# than being handed whatever error an empty string produces downstream.
tool_mode() { curl -sf -X POST "$P/control/tool" -d "$1" >/dev/null; }
reply_mode() { curl -sf -X POST "$P/control/reply" -d "$1" >/dev/null; }
tool_mode '{"args":"{\"nope\":\"x\"}"}'
MA="$(a chat --agent helper --tools memory_recall --message x)"
chkin "a missing parameter is named"  "requires the parameter" "$(printf '%s' "$MA" | j content)"
chk "and the run records it"          "missing required parameter query" "$(printf '%s' "$MA" | j tool_runs.0.failed)"

# A hallucinated tool name gets the real list back, so the model can correct.
tool_mode '{"tool":"memory_search","args":"{}"}'
HT="$(a chat --agent helper --tools memory_recall,skill_show --message x)"
chkin "an unknown tool lists the real ones" "Available tools: memory_recall, skill_show" "$(printf '%s' "$HT" | j content)"
tool_mode '{"args":"{\"query\":\"ports\"}"}'   # back to the default for later sections

# Streamed tool calls are accumulated from fragments.
ST="$(a chat --agent helper --tools memory_recall --stream --message "stream a tool")"
chk "a streamed tool call works"      "True"    "$(printf '%s' "$ST" | tail -1 | j ok)"
chk "the streamed loop ran twice"     "2"       "$(printf '%s' "$ST" | tail -1 | j steps)"
chkin "the stream shows tool activity" "start tool_call tool_result" "$(printf '%s\n' "$ST" | python3 -c 'import json,sys
print(" ".join(t for t in (json.loads(l)["type"] for l in sys.stdin) if t!="delta"))')"

# An agent can carry its tools.
a agent create --name tooled --model m1 --system sys --tools memory_recall,skill_show >/dev/null
chk "a configured tool set is used"   "2"       "$(a chat --agent tooled --message x | j steps)"
chk "--no-tools wins"                 "1"       "$(a chat --agent tooled --no-tools --message x | j steps)"
chk "a turn without tools is one step" "1"      "$(a chat --agent helper --message x | j steps)"

# --- flows -----------------------------------------------------------------
# The deterministic half. The claim being tested is not that flows work but
# that they are free: a wizard that collects, branches and shells out must
# report zero llm_calls and zero tokens.
a config set --allow-cmd echo --allow-host 127.0.0.1 --secret TOK=hunter2 >/dev/null

cat > "$WORK/wizard.json" <<'FLOW'
{
  "name": "wizard",
  "start": "who",
  "steps": {
    "who":   {"type":"collect_input","var":"who","prompt":"Your name?","required":true,"next":"greet"},
    "greet": {"type":"cmd","command":"echo","args":["hello {{who}}"],"var":"line","next":"branch"},
    "branch":{"type":"conditional","when":[{"var":"who","op":"eq","value":"vip","next":"gold"}],"else":"std"},
    "gold":  {"type":"message","text":"VIP: {{line}}","next":"done"},
    "std":   {"type":"message","text":"Standard: {{line}}","next":"done"},
    "done":  {"type":"end"}
  }
}
FLOW
chk "validate accepts a good flow"    "True"    "$(a flow validate --file "$WORK/wizard.json" | j valid)"
chk "validate reads stdin too"        "True"    "$(a flow validate < "$WORK/wizard.json" | j valid)"
chk "create stores it"                "wizard"  "$(a flow create --file "$WORK/wizard.json" | j flow.name)"
chk "it is listed"                    "wizard"  "$(a flow list | j flows.0.name)"
chk "show returns the definition"     "who"     "$(a flow show wizard | j flow.start)"

# A structural mistake is fatal; a machine-local one is a warning, because a
# definition must stay storable on a machine that cannot run it.
cat > "$WORK/broken.json" <<'FLOW'
{"name":"broken","start":"a","steps":{
  "a":{"type":"cmd","command":"nosuchtool","args":[],"var":"o","next":"ghost"},
  "b":{"type":"message","text":"{{never}}","next":"a"}}}
FLOW
chk "a broken flow is refused"        "82"      "$(code flow validate --file "$WORK/broken.json")"
chk "and it is typed"                 "invalid_flow" "$(err_type)"
chkin "the missing target is named"   "does not exist" "$(errj error.message)"
chkin "the unlisted command warns"    "not allowed on this machine" "$(errj error.message)"
chk "a broken flow cannot be stored"  "82"      "$(code flow create --file "$WORK/broken.json")"

# Running: pauses for input rather than prompting, because this tool never
# blocks on a terminal that may not exist.
chk "a run pauses for input"          "92"      "$(code flow run wizard)"
chk "and it is typed"                 "needs_input" "$(err_type)"
chkin "it says what it needs"         "needs who" "$(errj error.message)"
chkin "and how to continue"           "flow resume" "$(errj error.suggestions.0)"
RUN="$(tail -1 "$WORK/err" | python3 -c 'import json,sys;print(json.loads(json.load(sys.stdin)["error"]["suggestions"][1])["run"])')"
chk "the paused run is stored"        "paused"  "$(a flow runs --limit 1 | j runs.0.status)"
RES="$(a flow resume "$RUN" --input who=vip)"
chk "resume completes it"             "completed" "$(printf '%s' "$RES" | j status)"
chkin "the right branch was taken"    "VIP: hello vip" "$(printf '%s' "$RES" | j messages.0)"
chk "resuming a finished run is a conflict" "95" "$(code flow resume "$RUN" --input who=x)"
chkin "one run reads back with its messages" "VIP: hello vip" "$(a flow runs --run "$RUN" | j run.messages.0)"
chk "and with its status"             "completed" "$(a flow runs --run "$RUN" | j run.status)"
chk "an unknown run id is not found"  "92"      "$(code flow runs --run 01NOSUCHRUN00000000000000)"

# The whole argument for the feature, asserted rather than claimed.
DET="$(a flow run wizard --input who=ada)"
chk "a deterministic run is free"     "0"       "$(printf '%s' "$DET" | j llm_calls)"
chk "and spends no tokens"            "0"       "$(printf '%s' "$DET" | j tokens)"
chk "and still did the work"          "5"       "$(printf '%s' "$DET" | j steps)"

# argv, not a shell line: the same guarantee as the memgraph wrapper.
cat > "$WORK/shell.json" <<'FLOW'
{"name":"shelltest","start":"a","steps":{
  "a":{"type":"cmd","command":"echo","args":["{{payload}}"],"var":"out","next":"b"},
  "b":{"type":"message","text":"got {{out}}","next":"c"},
  "c":{"type":"end"}}}
FLOW
a flow create --file "$WORK/shell.json" >/dev/null
SH_OUT="$(a flow run shelltest --input 'payload=x; touch /tmp/flow-pwned; echo y')"
chkin "a command is argv, not a shell line" "x; touch /tmp/flow-pwned; echo y" "$(printf '%s' "$SH_OUT" | j data.out)"
chk "nothing was executed"            "False"   "$([ -e /tmp/flow-pwned ] && echo True || echo False)"

# A command that is not allowed here fails the run, wherever the flow came from.
a config set --deny-cmd echo >/dev/null
chk "an unlisted command fails the run" "105"   "$(code flow run shelltest --input payload=hi)"
chkin "and says how to allow it"      "--allow-cmd echo" "$(errj error.message)"
a config set --allow-cmd echo >/dev/null

# http, against the local bkn: allowlisted host, real request, JSON select.
cat > "$WORK/http.json" <<FLOW
{"name":"probe","start":"a","steps":{
  "a":{"type":"http","method":"GET","url":"$B/_health","var":"health","parse":"json","select":"ok","next":"b","on_error":"down"},
  "b":{"type":"message","text":"bkn ok={{health}} status={{health_code}}","next":"end_ok"},
  "down":{"type":"message","text":"bkn down: {{health_err}}","next":"end_bad"},
  "end_ok":{"type":"end"},
  "end_bad":{"type":"end","status":"failed"}}}
FLOW
a flow create --file "$WORK/http.json" >/dev/null
HP="$(a flow run probe)"
chk "an http step runs"               "completed" "$(printf '%s' "$HP" | j status)"
chk "and selects a field"             "true"    "$(printf '%s' "$HP" | j data.health)"
chk "and records the status code"     "200"     "$(printf '%s' "$HP" | j data.health_code)"
chk "an http run is also free"        "0"       "$(printf '%s' "$HP" | j tokens)"

cat > "$WORK/badhost.json" <<'FLOW'
{"name":"badhost","start":"a","steps":{
  "a":{"type":"http","url":"https://example.com/","var":"o","next":"b"},
  "b":{"type":"end"}}}
FLOW
a flow create --file "$WORK/badhost.json" >/dev/null
chk "an unlisted host fails the run"  "105"     "$(code flow run badhost)"
chkin "and says how to allow it"      "--allow-host example.com" "$(errj error.message)"

# A secret is referenced by name, never stored in the definition or the run.
cat > "$WORK/secret.json" <<FLOW
{"name":"secretflow","start":"a","steps":{
  "a":{"type":"http","method":"GET","url":"$B/_health","headers":{"X-Test":"Bearer {{secret.TOK}}"},"var":"o","next":"b"},
  "b":{"type":"end"}}}
FLOW
a flow create --file "$WORK/secret.json" >/dev/null
chk "a flow using a secret runs"      "completed" "$(a flow run secretflow | j status)"
chk "the secret is not in the run"    "False"   "$(a flow run secretflow | grep -c hunter2 | sed 's/^0$/False/;s/^[1-9].*/True/')"
chk "config lists secrets by name only" "TOK"   "$(a config show | j config.secrets.0)"
chk "and never their values"          "False"   "$(a config show | grep -c hunter2 | sed 's/^0$/False/;s/^[1-9].*/True/')"

# The one step that costs money, wired to a real agent and thread.
cat > "$WORK/ask.json" <<'FLOW'
{"name":"askflow","start":"a","steps":{
  "a":{"type":"ask","agent":"helper","prompt":"summarise: {{topic}}","var":"answer","next":"b"},
  "b":{"type":"message","text":"said {{answer}}","next":"c"},
  "c":{"type":"end"}}}
FLOW
a flow create --file "$WORK/ask.json" >/dev/null
AK="$(a flow run askflow --input topic=ports)"
chk "an ask step calls the model"     "1"       "$(printf '%s' "$AK" | j llm_calls)"
chk "and counts its tokens"           "True"    "$(printf '%s' "$AK" | python3 -c 'import json,sys;print(json.load(sys.stdin)["tokens"] > 0)')"
chkin "and the answer is a variable"  "said turns=" "$(printf '%s' "$AK" | j messages.0)"
chk "the LLM turn is stored as a thread" "True" "$(printf '%s' "$AK" | python3 -c 'import json,sys;d=json.load(sys.stdin);print(bool(d["data"].get("answer")))')"

# --- flows calling bkn scripts ---------------------------------------------
# The step that needed a bkn change first: a script runs with the USER's
# token, and bkn's per-script run policy decides whether it may run at all.
# No admin token appears anywhere on this path.
cat > "$WORK/tally.js" <<'JS'
function main(input) {
  return { who: bkn.caller.kind, sub: bkn.caller.sub, note: input.note || "none" };
}
JS
cat > "$WORK/boom.js" <<'JS'
function main(input) { throw new Error("script decided to fail"); }
JS
"$BKN" script create tally --file "$WORK/tally.js" --run-access user >/dev/null 2>&1
"$BKN" script create shut  --file "$WORK/tally.js" >/dev/null 2>&1
"$BKN" script create boom  --file "$WORK/boom.js" --run-access user >/dev/null 2>&1

cat > "$WORK/scriptflow.json" <<'FLOW'
{"name":"tallyflow","start":"a","steps":{
  "a":{"type":"script","script":"tally","input":"{\"note\":\"from a flow\"}","var":"t","parse":"json","select":"who","next":"b","on_error":"refused"},
  "b":{"type":"message","text":"script saw {{t}}","next":"done"},
  "refused":{"type":"message","text":"refused: {{t_err}}","next":"bad"},
  "done":{"type":"end"},
  "bad":{"type":"end","status":"failed"}}}
FLOW
a flow create --file "$WORK/scriptflow.json" >/dev/null
SF="$(a flow run tallyflow)"
chk "a flow can run a bkn script"     "completed" "$(printf '%s' "$SF" | j status)"
chk "the script ran as the user"      "user"    "$(printf '%s' "$SF" | j data.t)"
chk "and no tokens were spent"        "0"       "$(printf '%s' "$SF" | j tokens)"
# Selecting a different field proves the input arrived, rather than hoping to
# find it somewhere in the run.
python3 -c "
import json,sys
d = json.load(open(sys.argv[1])); d['name']='noteflow'; d['steps']['a']['select']='note'
json.dump(d, open(sys.argv[2],'w'))" "$WORK/scriptflow.json" "$WORK/noteflow.json"
a flow create --file "$WORK/noteflow.json" >/dev/null
chk "the input reached the script"    "from a flow" "$(a flow run noteflow | j data.t)"

# A script the operator has not opened is refused, and the flow handles it.
python3 -c "
import json,sys
d = json.load(open(sys.argv[1])); d['name']='shutflow'; d['steps']['a']['script']='shut'
json.dump(d, open(sys.argv[2],'w'))" "$WORK/scriptflow.json" "$WORK/shutflow.json"
a flow create --file "$WORK/shutflow.json" >/dev/null
chk "an unopened script fails the run" "105"    "$(code flow run shutflow)"
chkin "and names both possibilities"  "admin-only, or it does not exist" "$(tail -1 "$WORK/err" | python3 -c 'import json,sys;print(json.loads(json.load(sys.stdin)["error"]["suggestions"][1])["messages"][0])')"
chkin "and how an operator opens it"  "--run-access user" "$(tail -1 "$WORK/err" | python3 -c 'import json,sys;print(json.loads(json.load(sys.stdin)["error"]["suggestions"][1])["messages"][0])')"

# A script that ran and threw is a result, not a transport failure: the flow
# gets to branch on it.
python3 -c "
import json,sys
d = json.load(open(sys.argv[1])); d['name']='boomflow'; d['steps']['a']['script']='boom'
d['steps']['a'].pop('parse'); d['steps']['a'].pop('select')
json.dump(d, open(sys.argv[2],'w'))" "$WORK/scriptflow.json" "$WORK/boomflow.json"
a flow create --file "$WORK/boomflow.json" >/dev/null
chk "a failing script takes on_error" "105"    "$(code flow run boomflow)"
chkin "and says the script failed"    "reported failure" "$(tail -1 "$WORK/err" | python3 -c 'import json,sys;print(json.loads(json.load(sys.stdin)["error"]["suggestions"][1])["messages"][0])')"

# --- the cmd tool -----------------------------------------------------------
# Flows could always reach the world; agents could only look things up. This
# closes that, but the argv author changes from a human to a model, so the
# gate changes too: prefixes, not bare command names.
chk "the catalog lists cmd"           "True"    "$("$AHUB" tool list | python3 -c 'import json,sys;print(any(t["name"]=="cmd" for t in json.load(sys.stdin)["tools"]))')"
chk "and marks it as needing an allowlist" "True" "$("$AHUB" tool list | python3 -c 'import json,sys;print(next(t["needs_allowlist"] for t in json.load(sys.stdin)["tools"] if t["name"]=="cmd"))')"
chk "and as not needing memgraph"     "False"   "$("$AHUB" tool list | python3 -c 'import json,sys;print(next(t["needs_memgraph"] for t in json.load(sys.stdin)["tools"] if t["name"]=="cmd"))')"

# With no prefixes the tool does not exist at all -- the default is that an
# agent cannot run anything.
chk "cmd needs a permitted command"   "85"      "$(code chat --agent helper --tools cmd --message x)"
chkin "and says how to permit one"    "--allow-tool-cmd" "$(errj error.message)"

a config set --allow-tool-cmd "echo hello" --allow-tool-cmd "git log" --allow-tool-arg "echo hello *" >/dev/null
chk "config lists the prefixes"       "echo hello" "$(a config show | j config.allow_tool_cmds.0)"

tool_mode '{"tool":"cmd","args":"{\"command\":\"echo hello\",\"args\":[\"world\"]}"}'
CM="$(a chat --agent helper --tools cmd --message run)"
chkin "a permitted command runs"      "hello world" "$(printf '%s' "$CM" | j content)"
chkin "and the model sees the exit code" "exit code: 0" "$(printf '%s' "$CM" | j content)"
chk "the run is recorded"             "cmd"     "$(printf '%s' "$CM" | j tool_runs.0.name)"

# The gate: a model may append to a prefix, never change it.
tool_mode '{"tool":"cmd","args":"{\"command\":\"rm -rf\",\"args\":[\"/\"]}"}'
chkin "an unpermitted command is refused" "is not permitted here" "$(a chat --agent helper --tools cmd --message x | j tool_runs.0.failed)"
chk "and nothing ran"                 "False"   "$([ -e /tmp/cmdtool-pwned ] && echo True || echo False)"
tool_mode '{"tool":"cmd","args":"{\"command\":\"git push\",\"args\":[\"--force\"]}"}'
chkin "a sibling subcommand is refused" "\"git push\" is not permitted" "$(a chat --agent helper --tools cmd --message x | j tool_runs.0.failed)"
tool_mode '{"tool":"cmd","args":"{\"command\":\"git logger\",\"args\":[]}"}'
chkin "a longer word is not a prefix match" "is not permitted" "$(a chat --agent helper --tools cmd --message x | j tool_runs.0.failed)"

# Arguments are argv, exactly as in a flow step.
tool_mode '{"tool":"cmd","args":"{\"command\":\"echo hello\",\"args\":[\"; touch /tmp/cmdtool-pwned\"]}"}'
CS="$(a chat --agent helper --tools cmd --message x)"
chkin "arguments are argv, not a shell line" "; touch /tmp/cmdtool-pwned" "$(printf '%s' "$CS" | j content)"
chk "so nothing was executed"         "False"   "$([ -e /tmp/cmdtool-pwned ] && echo True || echo False)"

# A model that sends one string instead of an array is making an ordinary
# mistake, and one argument is a fine reading of it.
tool_mode '{"tool":"cmd","args":"{\"command\":\"echo hello\",\"args\":\"there\"}"}'
chkin "a bare string arg is accepted"  "hello there" "$(a chat --agent helper --tools cmd --message x | j content)"

# Revocation takes effect on the next call.
a config set --deny-tool-cmd "echo hello" >/dev/null
tool_mode '{"tool":"cmd","args":"{\"command\":\"echo hello\",\"args\":[\"world\"]}"}'
chkin "revoking a prefix takes effect" "is not permitted here" "$(a chat --agent helper --tools cmd --message x | j tool_runs.0.failed)"
a config set --allow-tool-cmd "echo hello" --allow-tool-arg "echo hello *" >/dev/null

# A prefix alone is not a boundary: each further argument must be listed.
tool_mode "{\"tool\":\"cmd\",\"args\":\"{\\\"command\\\":\\\"git log\\\",\\\"args\\\":[\\\"--output=$WORK/pwned\\\"]}\"}"
chkin "an unlisted flag is refused"    "is not permitted after" "$(a chat --agent helper --tools cmd --message x | j tool_runs.0.failed)"
chk "and writes nothing"              "False"   "$([ -e "$WORK/pwned" ] && echo True || echo False)"
tool_mode '{"tool":"cmd","args":"{\"command\":\"echo hello\",\"args\":[\"-n\"]}"}'
chkin "a * rule does not admit a flag" "is not permitted after" "$(a chat --agent helper --tools cmd --message x | j tool_runs.0.failed)"
chk "a rule for nothing is refused"   "85"      "$(code config set --allow-tool-arg 'rm -rf')"
chkin "config shows the rules"        "*"       "$(a config show | j 'config.allow_tool_args')"

# An agent given only cmd must run on a machine with no memgraph at all.
tool_mode '{"tool":"cmd","args":"{\"command\":\"echo hello\",\"args\":[\"nomem\"]}"}'
chkin "cmd works without memgraph"    "hello nomem" "$(AHUB_MEMGRAPH=/nonexistent a chat --agent helper --tools cmd --message x | j content)"
tool_mode '{"args":"{\"query\":\"ports\"}"}'

# --- background jobs --------------------------------------------------------
# A turn cannot wait: a test suite or a deploy outlives the provider timeout.
# A job outlives the process that started it, which is also what lets a
# schedule begin something and a later run collect it.
a config set --allow-tool-cmd "seq" --allow-tool-cmd "sleep" --allow-tool-arg "seq *" --allow-tool-arg "sleep *" >/dev/null
chk "adding a permitted command twice is fine" "0" "$(code config set --allow-tool-cmd seq)"

JB="$(a job start --command seq --args 3)"
JID="$(printf '%s' "$JB" | j job.id)"
chk "a job starts and is recorded"    "True"    "$([ -n "$JID" ] && echo True || echo False)"
chk "it is listed"                    "$JID"    "$(a job list --limit 1 | j jobs.0.id)"
JW="$(a job wait "$JID" --timeout 20)"
chk "it finishes"                     "exited"  "$(printf '%s' "$JW" | j status)"
chk "with an exit code"               "0"       "$(printf '%s' "$JW" | j exit_code)"
chkin "and its output is captured"    "3"       "$(printf '%s' "$JW" | j output)"
chk "output survives the process that started it" "True" "$(a job output "$JID" | python3 -c 'import json,sys;print("1" in json.load(sys.stdin)["output"])')"

# The same allowlist as the cmd tool: a human starting a job by hand is bound
# by what this machine permits a model to run.
chk "an unlisted job argument is refused" "85" "$(code job start --command seq --args=-w)"
chk "an unpermitted job is refused"   "85"      "$(code job start --command "rm -rf")"
chkin "and says how to permit it"     "not permitted" "$(errj error.message)"

# Waiting is bounded, and "still running" is not a failure.
SL="$(a job start --command sleep --args 30 | j job.id)"
chk "a slow job is still running"     "92"      "$(code job wait "$SL" --timeout 1)"
chk "and says so"                     "still_running" "$(err_type)"
chk "it can be stopped"               "True"    "$(a job stop "$SL" | j stopped)"
chk "and reads as stopped"            "stopped" "$(a job show "$SL" | j job.status)"

# A command starts processes of its own, and stopping the job must reach them.
alive() { pgrep -fx "sleep $1" >/dev/null && echo alive || echo gone; }
settle() { for _ in $(seq 1 50); do [ "$(alive "$1")" = "$2" ] && break; sleep 0.1; done; alive "$1"; }
a config set --allow-tool-cmd 'sh -c' --allow-tool-arg 'sh -c *' >/dev/null
TREE="$(a job start --command 'sh -c' --args 'sleep 6171 & wait' | j job.id)"
chk "a job's grandchild starts"       "alive"   "$(settle 6171 alive)"
a job stop "$TREE" >/dev/null
chk "stopping the job takes it too"   "gone"    "$(settle 6171 gone)"
a config set --deny-tool-cmd 'sh -c' >/dev/null

# The model-facing verbs.
tool_mode '{"tool":"job_start","args":"{\"command\":\"seq\",\"args\":[\"2\"]}"}'
JT="$(a chat --agent helper --tools job_start --message "start it")"
chkin "a model can start one"         "Started job" "$(printf '%s' "$JT" | j content)"
chk "and it is recorded against the thread" "True" "$(a job list --limit 1 | python3 -c 'import json,sys;print(bool(json.load(sys.stdin)["jobs"][0].get("thread")))')"
tool_mode '{"args":"{\"query\":\"ports\"}"}'

# --- context compaction -----------------------------------------------------
# Threads grow. The precise signal is already in hand -- every response reports
# prompt_tokens -- so the threshold is measured, not estimated.
ctx_mode() { curl -sf -X POST "$P/control/context" -d "$1" >/dev/null; }

ctx_mode '{"prompt_tokens":900}'
a config set --model-limit m1=1000 --compact-at 0.6 >/dev/null
chk "the limit is remembered"         "1000"    "$(a config show | python3 -c 'import json,sys;print(json.load(sys.stdin)["config"]["model_limits"]["m1"])')"
CT="$(a chat --agent helper --message "first turn" | j thread)"
chk "the measured prompt is stored"   "900"     "$(a thread show "$CT" | j thread.last_prompt_tokens)"
for i in 2 3 4 5 6; do a chat --agent helper --thread "$CT" --message "turn $i" >/dev/null; done
chk "the thread was folded"           "True"    "$(a thread show "$CT" | python3 -c 'import json,sys;print(bool(json.load(sys.stdin)["thread"].get("summary")))')"
chk "and it says how far"             "True"    "$(a thread show "$CT" | python3 -c 'import json,sys;print(json.load(sys.stdin)["thread"].get("summary_seq",0) > 0)')"
chk "nothing was deleted"             "True"    "$(a thread show "$CT" | python3 -c 'import json,sys;d=json.load(sys.stdin);print(d["count"] > d["thread"]["summary_seq"])')"

# Once folding stops helping it stops happening -- otherwise every later turn
# would pay for another summary forever.
BEFORE_SEQ="$(a thread show "$CT" | j thread.summary_seq)"
a chat --agent helper --thread "$CT" --message "another" >/dev/null
a chat --agent helper --thread "$CT" --message "and another" >/dev/null
chk "it does not fold again pointlessly" "$BEFORE_SEQ" "$(a thread show "$CT" | j thread.summary_seq)"

# --no-compact leaves a thread alone.
NC="$(a chat --agent helper --no-compact --message "solo" | j thread)"
for i in 2 3 4 5 6; do a chat --agent helper --no-compact --thread "$NC" --message "turn $i" >/dev/null; done
chk "--no-compact never folds"        "False"   "$(a thread show "$NC" | python3 -c 'import json,sys;print(bool(json.load(sys.stdin)["thread"].get("summary")))')"

# The backstop: the provider refuses, the window is learned, the thread folds,
# and the turn is retried once.
a agent create --name learner --model learn-model --system short >/dev/null
ctx_mode '{"limit":2000,"prompt_tokens":20}'
# The echoing provider would return the whole fold as its "summary", which is
# larger than what it replaced. A real summariser summarises, so this one is
# told to answer briefly for the duration of the scenario.
reply_mode '{"reply":"notes: the conversation covered several turns."}'
PAD="$(python3 -c 'print("sentence that takes up room. "*40)')"
LT="$(a chat --agent learner --message first | j thread)"
# Somewhere in here the prompt stops fitting. The turn that hits it should
# still answer, and should say the provider is why it folded -- so the loop
# keeps whichever turn reported that.
# Every turn is expected to answer: whichever one stops fitting should fold
# and retry rather than fail. Which iteration that is depends on the fake
# tokeniser, so the assertion is on the outcome, not on the turn number.
FAILED=0
for i in $(seq 2 14); do
  a chat --agent learner --thread "$LT" --message "turn $i: $PAD" >/dev/null 2>&1 || FAILED=$((FAILED+1))
done
chk "no turn was lost to the window"  "0"       "$FAILED"
chk "the thread was folded"           "True"    "$(a thread show "$LT" | python3 -c 'import json,sys;print(bool(json.load(sys.stdin)["thread"].get("summary")))')"
chk "and the fold is recorded"        "True"    "$(a thread show "$LT" | python3 -c 'import json,sys;print(json.load(sys.stdin)["thread"].get("compacted_at_prompt",0) > 0)')"
chk "the window was learned into bkn" "2000"    "$(a model list | python3 -c 'import json,sys;print(next((m["context_limit"] for m in json.load(sys.stdin)["models"] if m["model"]=="learn-model"), None))')"
reply_mode '{"reply":null}'

# A window smaller than the fixed parts cannot be rescued, and says so rather
# than inviting a retry that will fail identically.
a agent create --name toobig --model tiny-model --system "$(python3 -c 'print("padding. "*200)')" >/dev/null
ctx_mode '{"limit":120,"prompt_tokens":50}'
chk "an unfittable prompt is typed"   "85"      "$(code chat --agent toobig --message hello)"
chk "and named"                       "prompt_too_large" "$(err_type)"
chkin "with what would actually help" "system prompt" "$(errj error.suggestions.0)"
ctx_mode '{"limit":0,"prompt_tokens":0}'

# --- what belongs in bkn, and what does not ------------------------------
# A context window is a fact about a MODEL, so it is shared through bkn and no
# second machine has to pay for a refused call to learn it. An allowlist or a
# credential is a policy of THIS machine and stays local.
chk "an unknown model is not listed"  "False"   "$(a model list | python3 -c 'import json,sys;print(any(m["model"]=="never-used-model" for m in json.load(sys.stdin)["models"]))')"
chk "a window can be declared"        "declared" "$(a model set never-used-model=4096 | j source)"
chk "and it is listed"                "4096"    "$(a model list | python3 -c 'import json,sys;print(next(m["context_limit"] for m in json.load(sys.stdin)["models"] if m["model"]=="never-used-model"))')"
chk "declaring twice does not duplicate" "1"    "$(a model set never-used-model=8192 >/dev/null; a model list | python3 -c 'import json,sys;print(sum(1 for m in json.load(sys.stdin)["models"] if m["model"]=="never-used-model"))')"
chk "the newer belief wins"           "8192"    "$(a model list | python3 -c 'import json,sys;print(next(m["context_limit"] for m in json.load(sys.stdin)["models"] if m["model"]=="never-used-model"))')"
chk "it lives in bkn, not the profile" "True"   "$(python3 -c '
import json,sys
p = json.load(open(sys.argv[1]))["profiles"]["default"]
print("never-used-model" not in (p.get("model_limits") or {}))' "$AHUB_CONFIG")"

# A learned window is used without any local configuration.
ctx_mode '{"limit":1500,"prompt_tokens":20}'
reply_mode '{"reply":"notes: brief."}'
a agent create --name shared --model shared-model --system short >/dev/null
SPAD="$(python3 -c 'print("sentence that takes room. "*40)')"
ST="$(a chat --agent shared --message first | j thread)"
for i in $(seq 2 10); do a chat --agent shared --thread "$ST" --message "turn $i: $SPAD" >/dev/null 2>&1; done
chk "a refused window is recorded in bkn" "True" "$(a model list | python3 -c 'import json,sys;print(any(m["model"]=="shared-model" and m["source"]=="learned" for m in json.load(sys.stdin)["models"]))')"
reply_mode '{"reply":null}'
ctx_mode '{"limit":0,"prompt_tokens":0}'

# A job's output travels with its record, so a machine that did not run it can
# still read what happened. The local file stays authoritative where it exists.
JO="$(a job start --command seq --args 40 | j job.id)"
a job wait "$JO" --timeout 20 >/dev/null
chkin "output reads from the local file" "local file" "$(a job output "$JO" | j source)"
chk "and a tail was recorded"          "True"    "$(a job show "$JO" | python3 -c 'import json,sys;print(bool(json.load(sys.stdin)["job"].get("output_tail")))')"
OF="$(a job show "$JO" | j job.output_file)"
mv "$OF" "$OF.gone"
chkin "without the file it reads the tail" "recorded tail" "$(a job output "$JO" | j source)"
chkin "and the content is still there"  "40"     "$(a job output "$JO" | j output)"
chk "the exit code was never local"    "0"       "$(a job show "$JO" | j job.exit_code)"
mv "$OF.gone" "$OF"

# --- guards against unattended failure --------------------------------------
# Everything here is about a run nobody is watching: two timers overlapping, a
# supervisor killed, output piling up, a budget with no ceiling, a flow that
# never ends.
patch_store() { # patch_store <coll> <id> <json>
  curl -s -X PATCH -H "Authorization: Bearer $(python3 -c "
import json,sys;print(json.load(open('$AHUB_CONFIG'))['profiles']['default']['token'])")" \
    -H 'Content-Type: application/json' -d "$3" "$B/v1/store/hub/$1/$2" >/dev/null
}

a agent create --name guarded --model m1 --system short >/dev/null
a schedule create --name guardsched --agent guarded --prompt hello --every 1h >/dev/null
GS="$(a schedule show guardsched | j schedule.id)"

# 1. one run at a time per schedule
patch_store schedules "$GS" '{"lease":"otherhost:999|2099-01-01T00:00:00Z"}'
chk "a held schedule refuses a manual run" "95" "$(code schedule run guardsched)"
chk "and says who holds it"            "already_running" "$(err_type)"
patch_store schedules "$GS" '{"next_run_at":"2020-01-01T00:00:00Z"}'
TK="$(a schedule tick)"
chk "tick skips a held schedule"      "1"       "$(printf '%s' "$TK" | j skipped)"
chk "and runs nothing"                "0"       "$(printf '%s' "$TK" | j ran)"
chkin "naming the holder"             "otherhost:999" "$(printf '%s' "$TK" | j runs.0.held_by)"

# An expired lease is taken over: a process that died cannot release its own.
patch_store schedules "$GS" '{"lease":"deadhost:1|2020-01-01T00:00:00Z","next_run_at":"2020-01-01T00:00:00Z"}'
TK2="$(a schedule tick)"
chk "an expired lease is taken over"  "1"       "$(printf '%s' "$TK2" | j ran)"
chk "and released afterwards"         ""        "$(a schedule show guardsched | j schedule.lease)"

# 2. jobs whose supervisor is gone do not say "running" forever
JR="$(curl -s -X POST -H "Authorization: Bearer $(python3 -c "
import json,sys;print(json.load(open('$AHUB_CONFIG'))['profiles']['default']['token'])")" \
  -H 'Content-Type: application/json' \
  -d '{"command":"seq","status":"running","started_at":"2020-01-01T00:00:00Z","created_at":"2020-01-01T00:00:00Z"}' \
  "$B/v1/store/hub/jobs" | j record.id)"
chk "a never-started job is reaped"   "1"       "$(a job reap | j reaped)"
chk "and marked failed"               "failed"  "$(a job show "$JR" | j job.status)"
chkin "with the reason"               "never started" "$(a job show "$JR" | j job.error)"
chk "reaping again finds nothing"     "0"       "$(a job reap | j reaped)"

# 3. finished jobs and their output do not accumulate forever
chk "a dry run prunes nothing"        "True"    "$(a job prune --dry-run | j dry_run)"
patch_store jobs "$JR" '{"finished_at":"2020-01-01T00:00:00Z"}'
chk "an old job is pruned"            "1"       "$(a job prune | j pruned)"
chk "and is gone"                     "92"      "$(code job show "$JR")"

# 4. a day's spending has a ceiling, counted atomically in bkn
a config set --daily-token-budget 40 >/dev/null
SP0="$(a spend | j tokens)"
a chat --agent guarded --message "costs something" >/dev/null
chk "a turn is counted"               "True"    "$(a spend | python3 -c "import json,sys;print(json.load(sys.stdin)['tokens'] > $SP0)")"
chk "the budget is reported"          "40"      "$(a spend | j budget)"
for i in 1 2 3 4 5 6 7 8; do a chat --agent guarded --message "more" >/dev/null 2>&1; done
chk "past the ceiling a turn is refused" "95"   "$(code chat --agent guarded --message "one too many")"
chk "and is typed"                    "budget_spent" "$(err_type)"
chkin "and says how to raise it"      "--daily-token-budget" "$(errj error.suggestions.0)"
a config set --daily-token-budget 100000000 >/dev/null

# 5. a flow has a deadline of its own, and the right clock is named
a config set --allow-cmd sleep >/dev/null
printf '%s' '{"name":"slowflow","start":"a","steps":{"a":{"type":"cmd","command":"sleep","args":["5"],"var":"o","next":"b"},"b":{"type":"end"}}}' > "$WORK/slow.json"
a flow create --file "$WORK/slow.json" >/dev/null
chk "a flow deadline stops it"        "105"     "$(code flow run slowflow --timeout 1)"
chkin "naming the run's deadline"     "run's deadline passed" "$(errj error.message)"
chkin "and a step budget names itself" "timed out after 1s" "$(code flow run slowflow --step-timeout 1 --timeout 60 >/dev/null; errj error.message)"
a config set --allow-cmd 'sh -c' >/dev/null
printf '%s' '{"name":"treeflow","start":"a","steps":{"a":{"type":"cmd","command":"sh","args":["-c","sleep 6172 & wait"],"var":"o","next":"b"},"b":{"type":"end"}}}' > "$WORK/tree.json"
a flow create --file "$WORK/tree.json" >/dev/null
T0=$(date +%s)
chk "a timed out step with a grandchild fails" "105" "$(code flow run treeflow --step-timeout 1)"
chk "and returns on time"             "yes"     "$([ $(($(date +%s) - T0)) -le 5 ] && echo yes || echo no)"
chk "leaving nothing running"         "gone"    "$(settle 6172 gone)"
a config set --deny-cmd 'sh -c' >/dev/null

# --- evals -----------------------------------------------------------------
# The measurement half. Without it every other change here is an opinion.
cat > "$WORK/quality.json" <<'EVAL'
{
  "name": "quality",
  "agent": "helper",
  "cases": [
    {"name": "echoes", "message": "why does it leak",
     "expect": [{"op": "contains", "value": "echo=why does it leak"},
                {"op": "max_tokens", "value": "500"},
                {"op": "tool_not_used", "value": "memory_recall"}]},
    {"name": "fails-on-purpose", "message": "hello",
     "expect": [{"op": "contains", "value": "a string the echo will never contain",
                 "why": "proves a failing assertion is reported, not swallowed"}]},
    {"name": "parked", "message": "x", "skip": "waiting on something",
     "expect": [{"op": "contains", "value": "never checked"}]}
  ]
}
EVAL
chk "validate accepts a good eval"    "True"    "$(a eval validate --file "$WORK/quality.json" | j valid)"
chk "and counts the costly assertions" "0"      "$(a eval validate --file "$WORK/quality.json" | j costly_assertions)"

cat > "$WORK/badeval.json" <<'EVAL'
{"name":"bad","agent":"helper","cases":[
  {"name":"a","message":"m","expect":[{"op":"matches","value":"[unclosed"}]},
  {"name":"a","message":"m","expect":[{"op":"max_tokens","value":"lots"}]},
  {"name":"b","message":"m","expect":[{"op":"nonsense","value":"x"}]},
  {"name":"c","message":"m","expect":[{"op":"judge","value":"is it good?"}]}]}
EVAL
chk "a broken eval is refused"        "82"      "$(code eval validate --file "$WORK/badeval.json")"
chkin "the bad regex is named"        "error parsing regexp" "$(errj error.message)"
chkin "the unparseable bound is named" "needs a number" "$(errj error.message)"
chkin "the unknown op is named"       "unknown op" "$(errj error.message)"
chkin "a judge with no judge warns"   "names no judge" "$(errj error.message)"
chk "a broken eval cannot be stored"  "82"      "$(code eval create --file "$WORK/badeval.json")"

chk "create stores it"                "quality" "$(a eval create --file "$WORK/quality.json" | j eval.name)"
chk "it is listed"                    "quality" "$(a eval list | j evals.0.name)"

EV="$(a eval run quality)"
chk "a run scores every case"         "1"       "$(printf '%s' "$EV" | j passed)"
chk "and counts the failure"          "1"       "$(printf '%s' "$EV" | j failed)"
chk "and honours a skip"              "1"       "$(printf '%s' "$EV" | j skipped)"
chk "the pass rate ignores skips"     "0.50"    "$(printf '%s' "$EV" | j pass_rate)"
chk "tokens are totalled"             "True"    "$(printf '%s' "$EV" | python3 -c 'import json,sys;print(json.load(sys.stdin)["tokens"] > 0)')"
chk "a failing check explains itself" "not found in the answer" "$(printf '%s' "$EV" | j cases.1.checks.0.detail)"
chk "and carries its why"             "proves a failing assertion is reported, not swallowed" "$(printf '%s' "$EV" | j cases.1.checks.0.why)"
chk "a skipped case says why"         "waiting on something" "$(printf '%s' "$EV" | j cases.2.skipped)"

# A completed eval exits 0 however it scored: the code says the eval RAN.
chk "a scored run still exits 0"      "0"       "$(code eval run quality)"
# --strict is for a pipeline that should go red. Exit 1, not a spec band:
# the tool worked, the agent did not.
chk "--strict exits 1 on failures"    "1"       "$(code eval run quality --strict)"
chk "and is typed"                    "eval_failed" "$(err_type)"
chk "help-json documents that code"   "an eval run completed with failing assertions (only with --strict)" "$("$AHUB" help-json | j exit_codes.1)"

chk "runs are recorded"               "quality" "$(a eval runs | j runs.0.eval)"

# The question the whole feature exists to answer.
cat > "$WORK/guard.json" <<'EVAL'
{"name":"guard","agent":"clean","cases":[
  {"name":"stays-clean","message":"hello","expect":[{"op":"not_contains","value":"mem=1"}]}]}
EVAL
a agent create --name clean --model m1 --system sys >/dev/null
a eval create --file "$WORK/guard.json" >/dev/null
BEFORE="$(a eval run guard | j run)"
chk "before the change it passes"     "1"       "$(a eval runs --eval guard --limit 1 | j runs.0.passed)"
a agent update clean --memory-project demo >/dev/null
a eval run guard >"$WORK/after.json" 2>/dev/null
AFTER="$(j run <"$WORK/after.json")"
chk "after the change it fails"       "1"       "$(j failed <"$WORK/after.json")"
CMP="$(a eval compare "$BEFORE" "$AFTER")"
chk "compare names the regression"    "stays-clean" "$(printf '%s' "$CMP" | j regressed.0)"
chk "and summarises it"               "0 fixed, 1 regressed, 0 still failing" "$(printf '%s' "$CMP" | j summary)"
chk "compare needs two runs"          "82"      "$(code eval compare "$BEFORE")"

# A field must be clearable, or a memory project could never be taken away.
a agent update clean --memory-project "" >/dev/null
chkin "clearing a field works"        "mem=0"   "$(a chat --agent clean --message hi | j content)"

# The judge: the only assertion that spends tokens.
cat > "$WORK/judged.json" <<'EVAL'
{"name":"judged","agent":"helper","judge":"critic","cases":[
  {"name":"asks-a-judge","message":"hello","expect":[{"op":"judge","value":"Is the answer relevant?"}]}]}
EVAL
a agent create --name critic --model m1 --system "you judge" >/dev/null
a eval create --file "$WORK/judged.json" >/dev/null
chk "validate counts the costly one"  "1"       "$(a eval validate --file "$WORK/judged.json" | j costly_assertions)"
reply_mode '{"reply":"PASS\nthe answer addresses the question"}'
JR="$(a eval run judged)"
chk "a judge verdict of PASS passes"  "1"       "$(printf '%s' "$JR" | j passed)"
chk "the judge reason is kept"        "the answer addresses the question" "$(printf '%s' "$JR" | j cases.0.checks.0.detail)"
chk "judge tokens are counted apart"  "True"    "$(printf '%s' "$JR" | python3 -c 'import json,sys;print(json.load(sys.stdin)["judge_tokens"] > 0)')"
reply_mode '{"reply":"FAIL\nit ignored the question"}'
chk "a judge verdict of FAIL fails"   "1"       "$(a eval run judged | j failed)"
reply_mode '{"reply":null}'

# A case that could not run is a failure, not a skip: an outage must never
# read as an improvement.
curl -sf -X POST "$P/control/fail" -d '{"fail":true}' >/dev/null
chk "an unreachable model fails cases" "1"      "$(a eval run guard | j failed)"
chk "and records the error"           "True"    "$(a eval run guard | python3 -c 'import json,sys;print(bool(json.load(sys.stdin)["cases"][0]["error"]))')"
curl -sf -X POST "$P/control/fail" -d '{"fail":false}' >/dev/null

# --- schedules (autonomous runs) ----------------------------------------------
# An schedule is a standing instruction: run this agent on this schedule with
# nobody watching. What carries between runs is a bounded scratchpad, never
# the conversation -- replayed history would make every run cost more than the
# last and eventually stop fitting.
chk "an schedule needs a prompt"         "82"      "$(code schedule create --name a1 --agent helper)"
chkin "and says why"                  "burns tokens" "$(errj error.message)"
chk "every and at are exclusive"      "82"      "$(code schedule create --name a2 --agent helper --prompt p --every 1h --at 03:00)"
chk "a bad time of day is refused"    "85"      "$(code schedule create --name a3 --agent helper --prompt p --at 25:99)"
chk "a sub-minute interval is refused" "85"     "$(code schedule create --name a4 --agent helper --prompt p --every 5s)"

AG="$(a schedule create --name nightly --agent helper --prompt "check the port" --every 6h)"
chk "create schedules the first run"  "True"    "$(printf '%s' "$AG" | python3 -c 'import json,sys;print(bool(json.load(sys.stdin)["schedule"]["next_run_at"]))')"
chk "and it is enabled"               "True"    "$(printf '%s' "$AG" | j schedule.enabled)"
chk "it is listed"                    "True"    "$(a schedule list | python3 -c 'import json,sys;print(any(x["name"]=="nightly" for x in json.load(sys.stdin)["schedules"]))')"
chk "nothing is due yet"              "0"       "$(a schedule due | j count)"

R1="$(a schedule run nightly)"
chk "a manual run works"              "ok"      "$(printf '%s' "$R1" | j status)"
chk "it is recorded"                  "1"       "$(a schedule runs --schedule nightly | j count)"
chk "the trigger is recorded"         "manual"  "$(a schedule runs --schedule nightly | j runs.0.trigger)"
chkin "the scratchpad tool is always offered" "tools=1" "$(printf '%s' "$R1" | j answer)"
chkin "and the run starts a fresh thread" "users=1" "$(printf '%s' "$R1" | j answer)"
chkin "with no note to carry yet"     "pad=0"   "$(printf '%s' "$R1" | j answer)"

# The model writes its own note; the next run receives it.
tool_mode '{"tool":"scratchpad","args":"{\"text\":\"port released cleanly; check the backoff next\"}"}'
R2="$(a schedule run nightly)"
chk "the schedule wrote its note"        "True"    "$(printf '%s' "$R2" | j scratchpad_written)"
chkin "the note is stored"            "check the backoff next" "$(a schedule scratchpad nightly | j scratchpad)"
tool_mode '{"tool":null,"args":"{\"query\":\"ports\"}"}'
R3="$(a schedule run nightly)"
chkin "the next run carries it"       "pad=1"   "$(printf '%s' "$R3" | j answer)"
chkin "and still replays no history"  "users=1" "$(printf '%s' "$R3" | j answer)"

# A human can read and correct it: it is the one piece of an autonomous
# schedule's state that is meant to be legible.
chk "a human can set it"              "True"    "$(a schedule scratchpad nightly --set "corrected by hand" | j written)"
chk "and read it back"                "corrected by hand" "$(a schedule scratchpad nightly | j scratchpad)"
chk "and clear it"                    "True"    "$(a schedule scratchpad nightly --clear | j cleared)"
chk "clearing leaves nothing"         "0"       "$(a schedule scratchpad nightly | j chars)"

# Scheduling: tick is what a timer calls, and it is the only entry point.
a schedule create --name frequent --agent helper --prompt "tick me" --every 1m >/dev/null
a schedule scratchpad frequent --set "x" >/dev/null
python3 -c "
import json,subprocess,sys
" # (no-op: the due time is set in the past below through the API)
DUEID="$(a schedule show frequent | j schedule.id)"
curl -s -X PATCH -H "Authorization: Bearer $(python3 -c "
import json;print(json.load(open('$AHUB_CONFIG'))['profiles']['default']['token'])")"   -H 'Content-Type: application/json'   -d '{"next_run_at":"2020-01-01T00:00:00Z"}' "$B/v1/store/hub/schedules/$DUEID" >/dev/null
chk "an overdue schedule is due"         "frequent" "$(a schedule due | j due.0)"
TICK="$(a schedule tick)"
chk "tick runs it"                    "1"       "$(printf '%s' "$TICK" | j ran)"
chk "and reports no failures"         "0"       "$(printf '%s' "$TICK" | j failed)"
chk "the trigger says schedule"       "schedule" "$(a schedule runs --schedule frequent | j runs.0.trigger)"
chk "and it is no longer due"         "0"       "$(a schedule due | j count)"

# A disabled schedule never becomes due, whatever its schedule says.
a schedule disable frequent >/dev/null
curl -s -X PATCH -H "Authorization: Bearer $(python3 -c "
import json;print(json.load(open('$AHUB_CONFIG'))['profiles']['default']['token'])")"   -H 'Content-Type: application/json'   -d '{"next_run_at":"2020-01-01T00:00:00Z"}' "$B/v1/store/hub/schedules/$DUEID" >/dev/null
chk "a disabled schedule is not due"     "0"       "$(a schedule due | j count)"
chk "re-enabling re-arms it"          "True"    "$(a schedule enable frequent | j schedule.enabled)"
chk "and does not fire for missed slots" "0"    "$(a schedule due | j count)"

# A failing run is recorded and the schedule still advances: an schedule that
# stopped rescheduling when it broke would go quiet exactly when it mattered.
a schedule create --name broken --agent no-such-agent --prompt p --every 1h >/dev/null
chk "a broken schedule fails loudly"     "105"     "$(code schedule run broken)"
chk "and is typed"                    "schedule_run_failed" "$(err_type)"
chk "the failure is recorded"         "failed"  "$(a schedule runs --schedule broken | j runs.0.status)"
chk "and it is still scheduled"       "True"    "$(a schedule show broken | python3 -c 'import json,sys;print(bool(json.load(sys.stdin)["schedule"]["next_run_at"]))')"

# --- a schedule that cannot start --------------------------------------------
a schedule create --name cmdonly --agent guarded --prompt hi --every 1h --tools cmd >/dev/null
a config set --allow-tool-cmd "echo hi" --daily-token-budget 0 >/dev/null
chk "a cmd-only schedule needs no memgraph" "ok" "$(AHUB_MEMGRAPH=/nonexistent a schedule run cmdonly | j status)"
a schedule create --name needsmg --agent guarded --prompt hi --every 1h --tools memory_recall >/dev/null
patch_store schedules "$(a schedule show needsmg | j schedule.id)" '{"next_run_at":"2020-01-01T00:00:00Z"}'
chk "a schedule that cannot start fails the tick" "105" "$(AHUB_MEMGRAPH=/nonexistent code schedule tick)"
chk "and leaves a failed run"         "1"       "$(a schedule runs --schedule needsmg --status failed | j count)"
chk "and releases its lease"          ""        "$(a schedule show needsmg | j schedule.lease)"
TK3="$(AHUB_MEMGRAPH=/nonexistent a schedule tick)"
chk "and is not reported as held"     "0"       "$(printf '%s' "$TK3" | j skipped)"

# --- install / update ------------------------------------------------------
# cli-update-spec: a binary already in hand relocates itself, and updates
# itself by content hash with a verify-then-swap.
PREFIX="$WORK/bin"
chk "install copies the binary"       "$PREFIX/ahub" "$(a install --prefix "$PREFIX" | j installed)"
chk "the copy runs"                   "True"    "$("$PREFIX/ahub" version | j ok)"
chk "install is idempotent"           "$PREFIX/ahub" "$(a install --prefix "$PREFIX" | j installed)"
chk "installing over a running copy"  "True"    "$("$PREFIX/ahub" install --prefix "$PREFIX" | j ok)"
chk "install refuses an unwritable prefix" "90" "$(code install --prefix /proc/nope)"
chk "and is typed"                    "permission_denied" "$(err_type)"
chk "uninstall removes it"            "True"    "$(a uninstall --prefix "$PREFIX" | j removed)"
chk "uninstall twice is a no-op"      "False"   "$(a uninstall --prefix "$PREFIX" | j removed)"
chk "and still succeeds"              "0"       "$(code uninstall --prefix "$PREFIX")"
chk "uninstall leaves the config"     "True"    "$([ -f "$AHUB_CONFIG" ] && echo True || echo False)"

# A publisher, served over HTTP. Started WITHOUT a subshell so $! is the
# server's own pid: `( ... & )` captures the subshell instead, the trap kills
# nothing, and the orphan squats the port across runs.
PUB="$WORK/pub"; mkdir -p "$PUB"
cp "$AHUB" "$PUB/ahub"
python3 - "$PUB" <<'PYPUB'
import hashlib, json, sys, pathlib
d = pathlib.Path(sys.argv[1])
digest = hashlib.sha256((d / "ahub").read_bytes()).hexdigest()
(d / "ahub.json").write_text(json.dumps({
    "ok": True, "version": digest[:12], "download": "/ahub", "sha256": digest}))
PYPUB
PUBPORT=$((20000 + RANDOM % 20000))
python3 -m http.server "$PUBPORT" --directory "$PUB" >/dev/null 2>&1 &
PUBPID=$!
trap 'kill $PUBPID 2>/dev/null; cleanup' EXIT
for _ in $(seq 1 50); do curl -sf "http://127.0.0.1:$PUBPORT/ahub.json" >/dev/null && break; sleep 0.1; done
export AHUB_SERVER="http://127.0.0.1:$PUBPORT" AHUB_UPDATE_URL="http://127.0.0.1:$PUBPORT/ahub.json"
PUBVER="$(python3 -c "import json;print(json.load(open('$PUB/ahub.json'))['version'])")"

chk "check reports the published hash" "$PUBVER" "$(a update --check | j latest)"
chk "and sees no update needed"       "False"   "$(a update --check | j update_available)"
a install --prefix "$PREFIX" >/dev/null
chk "an up-to-date binary does nothing" "False" "$("$PREFIX/ahub" update | j updated)"

# A tampered artifact must be refused by hash, and the running binary left alone.
printf 'not a binary' > "$PUB/ahub"
chk "a mismatched artifact is refused" "100"    "$("$PREFIX/ahub" update --force >/dev/null 2>"$WORK/err"; echo $?)"
chk "and is typed"                    "verify_failed" "$(err_type)"
chk "the installed binary still works" "True"   "$("$PREFIX/ahub" version | j ok)"

# A well-formed publish of something that is not this tool: the hash matches,
# so only actually running it catches the problem.
printf '#!/bin/sh
exit 3
' > "$PUB/ahub"
python3 - "$PUB" <<'PYPUB'
import hashlib, json, sys, pathlib
d = pathlib.Path(sys.argv[1])
digest = hashlib.sha256((d / "ahub").read_bytes()).hexdigest()
(d / "ahub.json").write_text(json.dumps({
    "ok": True, "version": digest[:12], "download": "/ahub", "sha256": digest}))
PYPUB
chk "a broken publish fails its smoke test" "100" "$("$PREFIX/ahub" update >/dev/null 2>"$WORK/err"; echo $?)"
chk "and is typed"                    "smoke_test_failed" "$(err_type)"
chk "the installed binary survives"   "True"   "$("$PREFIX/ahub" version | j ok)"

kill $PUBPID 2>/dev/null; trap cleanup EXIT
unset AHUB_SERVER AHUB_UPDATE_URL

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
chk "second user sees no agents"   "0"       "$(b agent list | j count)"
chk "second user sees no threads"      "0"       "$(b thread list | j count)"
chk "second user cannot read the agent by id" "92" "$(bcode agent get "$AID")"
chk "second user cannot read the thread"          "92" "$(bcode thread show "$TID")"
chk "second user cannot chat into the thread"     "92" "$(bcode chat --agent helper --thread "$TID" --message steal)"
chk "second user cannot delete the agent"     "92" "$(bcode agent rm "$AID")"
chk "the agent is still there"     "helper"  "$(a agent get "$AID" | j agent.name)"

# --- bkn down --------------------------------------------------------------
kill "$BKN_PID" 2>/dev/null; wait "$BKN_PID" 2>/dev/null
chk "bkn down exits 100"              "100"     "$(code agent list)"
chk "bkn down is typed"               "connection_error" "$(err_type)"
chk "bkn down is recoverable"         "True"    "$(errj error.recoverable)"
BKN_PID=""

# --- spec conformance --------------------------------------------------------
CONFORMANCE="${CONFORMANCE_BIN:-$HOME/ai/cli-spec-conformance/cli-spec-conformance}"
if [ -x "$CONFORMANCE" ]; then
  chk "cli-output-spec and cli-guide-spec conform" "0" \
    "$(DO_NOT_TRACK=1 "$CONFORMANCE" check "$AHUB" --specs output,guide >/dev/null 2>&1; echo $?)"
else
  echo "  skip spec conformance: no checker at $CONFORMANCE"
fi
chk "help runs nothing, even unauthenticated" "0" "$(AHUB_TOKEN= AHUB_CONFIG="$WORK/none.json" code update --help)"
chk "an unknown flag is exit 80"      "80"      "$(code tool list --nope)"

echo
echo "[$PASS passed, $FAIL failed]"
[ "$FAIL" -eq 0 ]
