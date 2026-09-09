#!/usr/bin/env python3
"""A minimal OpenAI-compatible /chat/completions server for the live suite.

It echoes back what it was told, so an assertion can prove the whole prompt --
system message, replayed history, new turn -- actually reached the model. A
mock that returned a constant would pass even if history replay were broken.
"""
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

MODE = {"fail": False}

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
        if not self.path.endswith("/chat/completions"):
            return self._json(404, {"error": {"message": "no route"}})
        if MODE["fail"]:
            return self._json(500, {"error": {"message": "model on fire"}})

        msgs = req.get("messages", [])
        roles = ",".join(m["role"] for m in msgs)
        last = msgs[-1]["content"] if msgs else ""
        text = f"turns={len(msgs)} roles={roles} echo={last}"
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
