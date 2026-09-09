#!/usr/bin/env python3
"""A stand-in for the memgraph CLI.

The suite must not depend on the developer's real memories, and it must be
able to make recall fail on demand. This also records the argv it was given,
so an assertion can prove nothing in a user's message was ever interpreted as
a shell word: what arrives here is what ahub meant to send, verbatim.
"""
import json, os, sys

LOG = os.environ.get("FAKE_MEMGRAPH_LOG", "")
if LOG:
    with open(LOG, "a") as f:
        f.write(json.dumps(sys.argv[1:]) + "\n")

if os.environ.get("FAKE_MEMGRAPH_FAIL") == "1":
    sys.stderr.write('{"ok":false,"error":{"type":"memgraph_error","message":"store on fire"}}\n')
    sys.exit(105)

cmd = sys.argv[1] if len(sys.argv) > 1 else ""

def flag(name, default=""):
    a = sys.argv
    return a[a.index(name) + 1] if name in a and a.index(name) + 1 < len(a) else default

if cmd in ("recall", "search"):
    q = flag("--query")
    print(json.dumps({"count": 2, "format": flag("--format", "index"), "results": [
        {"memory_id": "111", "score": 9.5,
         "content": "[seeder-port] Closing a listening fd does not wake a blocked accept(); "
                    "use a socket timeout plus a stop channel. Query was: " + q},
        {"memory_id": "222", "score": 4.2,
         "content": "[owner-policy] bkn stamps user_id from the token on create."},
    ]}))
elif cmd == "save":
    print(json.dumps({"ok": True, "id": "333", "project": flag("--project")}))
elif cmd == "read":
    print(json.dumps({"ok": True, "id": sys.argv[2], "content": "a stored memory"}))
elif cmd in ("recommend", "query"):
    path = os.environ.get("FAKE_SKILL_PATH", "")
    skills = [{"id": "demo-skill", "name": "demo-skill", "type": "skill",
               "description": "a stand-in skill", "project": "demo",
               "file_path": path, "score": 42.0}]
    if os.environ.get("FAKE_SKILL_BADPATH") == "1":
        skills.append({"id": "bad-skill", "name": "bad-skill", "type": "skill",
                       "description": "points at a non-markdown file",
                       "file_path": "/etc/passwd", "score": 1.0})
    if cmd == "recommend":
        print(json.dumps({"task": sys.argv[2], "count": len(skills), "recommended": skills}))
    else:
        print(json.dumps({"query": sys.argv[2], "count": len(skills), "results": skills}))
elif cmd == "projects":
    print(json.dumps({"ok": True, "projects": [{"name": "demo", "memories": 2}]}))
else:
    sys.stderr.write("unknown command\n")
    sys.exit(80)
