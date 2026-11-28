#!/usr/bin/env python3
"""A stand-in sign-up API on 127.0.0.1, for signup-flow.sh. It keeps what the
last request really contained in the file named by the second argument, so the
script can check what arrived. A real service must not do that."""
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        with open(sys.argv[2], "wb") as seen:
            seen.write(raw)
        body = json.loads(raw)
        if len(body.get("password", "")) < 8:
            # Careless on purpose: many APIs repeat the request in an error.
            return self.reply(422, {"error": "password too short", "got": body})
        self.reply(201, {"id": "acct_1"})

    def reply(self, code, body):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


HTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
