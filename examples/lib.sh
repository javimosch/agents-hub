# Shared by the example scripts. Source it; do not run it.
set -euo pipefail

AHUB="${AHUB:-ahub}"
MODEL="${AHUB_MODEL:-}"   # new_agent needs it; a flow without ask does not
command -v jq >/dev/null || { echo "these examples need jq" >&2; exit 1; }
"$AHUB" whoami >/dev/null 2>&1 || { echo "log in first: ahub setup, then ahub login" >&2; exit 1; }

RUN="example-$$"
WORK="$(mktemp -d)"
UNDO=()
cleanup() {
  local i
  for ((i = ${#UNDO[@]} - 1; i >= 0; i--)); do eval "${UNDO[i]}" >/dev/null 2>&1 || true; done
  rm -rf "$WORK"
}
trap cleanup EXIT

say()  { printf '\n== %s\n' "$*"; }
pass() { printf '   ok: %s\n' "$*"; }
die()  { printf '   FAILED: %s\n' "$*" >&2; exit 1; }

listed() { "$AHUB" config show | jq -e --arg k "$1" --arg v "$2" '(.config[$k] // []) | index($v)' >/dev/null; }

# Grants are added for the run and taken back after it. One that is already
# there stops the script: taking it back would remove what you had permitted.
permit_cmd() {
  if listed allow_tool_cmds "$1"; then die "'$1' is already permitted on this profile; use another with --profile or AHUB_PROFILE"; fi
  "$AHUB" config set --allow-tool-cmd "$1" >/dev/null
  UNDO+=("$(printf '%q config set --deny-tool-cmd %q' "$AHUB" "$1")")
}

new_agent() { # new_agent <name> <system prompt>
  [ -n "$MODEL" ] || die "set AHUB_MODEL to a model that supports tool calls"
  "$AHUB" agent create --name "$1" --model "$MODEL" --system "$2" >/dev/null
  UNDO+=("$(printf '%q agent rm %q' "$AHUB" "$1")")
}

# The commands a thread asked for, one JSON object per line, with each result.
calls_of() {
  "$AHUB" thread show "$1" | jq -c '.messages as $m | $m[] | select(.role=="assistant" and .tool_calls)
    | .tool_calls | fromjson | .[] | . as $c
    | {command: (.arguments|fromjson.command), args: (.arguments|fromjson.args // []),
       result: ([$m[] | select(.tool_call_id==$c.id) | .content] | first)}'
}
