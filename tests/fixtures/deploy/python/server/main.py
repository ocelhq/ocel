import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from probes import PROBES

APP_NAME = "web"

DEFAULT_PORT = "3104"

OCEL_SVG = (
    '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64" '
    'role="img" aria-label="ocel"><rect width="64" height="64" rx="14" fill="#0b0f14"/>'
    '<circle cx="24" cy="27" r="5" fill="#f2b705"/><circle cx="42" cy="27" r="5" fill="#f2b705"/>'
    '<path d="M20 42c4 5 20 5 24 0" stroke="#f2b705" stroke-width="4" fill="none" '
    'stroke-linecap="round"/></svg>\n'
)


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        self.serve("GET")

    def do_POST(self):
        self.serve("POST")

    def do_PUT(self):
        self.serve("PUT")

    def do_PATCH(self):
        self.serve("PATCH")

    def do_DELETE(self):
        self.serve("DELETE")

    def serve(self, method):
        path = self.path.split("?", 1)[0]
        if method == "GET" and path == "/health":
            self.write_json(200, {"ok": True, "app": APP_NAME})
            return
        if method == "GET" and path == "/ocel.svg":
            self.write_bytes(200, "image/svg+xml", OCEL_SVG.encode())
            return
        for matches, answer in PROBES:
            if matches(method, path):
                answer(self)
                return
        self.write_json(404, {"error": "not found"})

    def write_json(self, status, body):
        self.write_bytes(status, "application/json", json.dumps(body).encode())

    def write_bytes(self, status, content_type, body, extra=None):
        self.send_response(status)
        self.send_header("content-type", content_type)
        self.send_header("content-length", str(len(body)))
        for name, value in (extra or {}).items():
            self.send_header(name, value)
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        pass


def main():
    port = int(os.environ.get("PORT") or DEFAULT_PORT)
    print(f"python listening on http://localhost:{port}", flush=True)
    ThreadingHTTPServer(("", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
