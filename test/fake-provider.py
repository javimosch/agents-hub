#!/usr/bin/env python3
"""A minimal OpenAI-compatible /chat/completions server for the live suite.

It echoes back what it was told, so an assertion can prove the whole prompt --
system message, replayed history, new turn -- actually reached the model. A
mock that returned a constant would pass even if history replay were broken.
"""
import json, os, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

# Set through /control/*, not the environment: the server is started once and
# the suite has to be able to change its behaviour per assertion.
MODE = {"fail": False, "tool": None, "args": '{"query":"ports"}', "reply": None}

class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass

    def _json(self, code, body):
        b = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        req = json.loads(self.rfile.read(n) or b"{}")
        if self.path == "/control/fail":
            MODE["fail"] = bool(req.get("fail"))
            return self._json(200, {"fail": MODE["fail"]})
        if self.path == "/control/reply":
            # Lets a test make the model answer a fixed string -- needed to
            # play the judge in an eval, whose verdict must be parseable.
            MODE["reply"] = req.get("reply")
            return self._json(200, {"reply": MODE["reply"]})
        if self.path == "/control/tool":
            MODE["tool"] = req.get("tool")
            MODE["args"] = req.get("args", '{"query":"ports"}')
            return self._json(200, {"tool": MODE["tool"], "args": MODE["args"]})
        if not self.path.endswith("/chat/completions"):
            return self._json(404, {"error": {"message": "no route"}})
        if MODE["fail"]:
            return self._json(500, {"error": {"message": "model on fire"}})

        # Tool loop: when tools are offered and no result has come back yet,
        # ask for one. On the next pass the tool turns are in the history, so
        # answer for real. This is the shape a real model produces, which is
        # what makes the assertions about replay meaningful.
        tools = req.get("tools") or []
        msgs_in = req.get("messages", [])
        results = [m for m in msgs_in if m.get("role") == "tool"]
        if tools and not results:
            want = MODE["tool"] or tools[0]["function"]["name"]
            args = MODE["args"]
            call = {"id": "call_1", "type": "function",
                    "function": {"name": want, "arguments": args}}
            usage = {"prompt_tokens": len(msgs_in), "completion_tokens": 3,
                     "total_tokens": len(msgs_in) + 3}
            if req.get("stream"):
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                frag = dict(call); frag["index"] = 0
                self.wfile.write(b"data: " + json.dumps(
                    {"choices": [{"delta": {"tool_calls": [frag]}}]}).encode() + b"\n\n")
                self.wfile.write(b"data: " + json.dumps(
                    {"choices": [{"delta": {}, "finish_reason": "tool_calls"}],
                     "usage": usage}).encode() + b"\n\n")
                self.wfile.write(b"data: [DONE]\n\n")
                self.wfile.flush()
                return
            return self._json(200, {"model": req.get("model"), "usage": usage,
                "choices": [{"message": {"content": "", "tool_calls": [call]},
                             "finish_reason": "tool_calls"}]})

        msgs = req.get("messages", [])
        roles = ",".join(m["role"] for m in msgs)
        last = msgs[-1]["content"] if msgs else ""
        # Whether a recalled-memory block actually arrived. The client merges
        # leading system messages, so counting roles cannot answer this.
        mem = int(any("Recalled memories" in m.get("content", "")
                      for m in msgs if m["role"] == "system"))
        skl = int(any("## Skills" in m.get("content", "")
                      for m in msgs if m["role"] == "system"))
        # Whether an agent's carried note arrived.
        pad = int(any("Your notes from previous runs" in m.get("content", "")
                      for m in msgs if m["role"] == "system"))
        users = sum(1 for m in msgs if m["role"] == "user")
        if MODE["reply"] is not None:
            usage = {"prompt_tokens": len(msgs), "completion_tokens": 5,
                     "total_tokens": len(msgs) + 5}
            return self._json(200, {"model": req.get("model"), "usage": usage,
                "choices": [{"message": {"content": MODE["reply"]}, "finish_reason": "stop"}]})

        tool_out = " | ".join(m.get("content", "")[:40] for m in msgs if m["role"] == "tool")
        text = (f"turns={len(msgs)} roles={roles} mem={mem} skl={skl} pad={pad} "
                f"users={users} tools={len(req.get('tools') or [])} "
                f"toolout={tool_out} echo={last}")
        usage = {"prompt_tokens": len(msgs), "completion_tokens": 7, "total_tokens": len(msgs) + 7}

        if req.get("stream"):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            for piece in text.split(" "):
                chunk = {"model": req.get("model"), "choices": [{"delta": {"content": piece + " "}}]}
                self.wfile.write(b"data: " + json.dumps(chunk).encode() + b"\n\n")
            done = {"model": req.get("model"),
                    "choices": [{"delta": {}, "finish_reason": "stop"}], "usage": usage}
            self.wfile.write(b"data: " + json.dumps(done).encode() + b"\n\n")
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
            return

        self._json(200, {"model": req.get("model"),
                         "choices": [{"message": {"content": text}, "finish_reason": "stop"}],
                         "usage": usage})

if __name__ == "__main__":
    HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
