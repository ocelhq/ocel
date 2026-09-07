import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from probes import MAX_BODY, PROBES

APP_NAME = "web"

DEFAULT_PORT = "3104"

LINE_LIMIT = 65536

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

    def do_HEAD(self):
        self.serve("HEAD")

    def do_POST(self):
        self.serve("POST")

    def do_PUT(self):
        self.serve("PUT")

    def do_PATCH(self):
        self.serve("PATCH")

    def do_DELETE(self):
        self.serve("DELETE")

    def serve(self, method):
        self.headless = method == "HEAD"
        self.body_taken = False
        self.answer("GET" if self.headless else method, self.path.split("?", 1)[0])
        if not self.body_taken:
            self.read_body()

    def answer(self, method, path):
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

    def read_body(self):
        self.body_taken = True
        try:
            if "chunked" in (self.headers.get("transfer-encoding") or "").lower():
                return self.read_chunks()
            length = int(self.headers.get("content-length") or 0)
            if length <= 0:
                return b""
            if length > MAX_BODY:
                return self.unreadable()
            return self.rfile.read(length)
        except (OSError, ValueError):
            return self.unreadable()

    def read_chunks(self):
        read = bytearray()
        while True:
            size = int(self.rfile.readline(LINE_LIMIT).split(b";", 1)[0], 16)
            if size == 0:
                self.rfile.readline(LINE_LIMIT)
                return bytes(read)
            if len(read) + size > MAX_BODY:
                return self.unreadable()
            read += self.rfile.read(size)
            self.rfile.readline(LINE_LIMIT)

    def unreadable(self):
        self.close_connection = True
        return None

    def write_json(self, status, body):
        self.write_bytes(status, "application/json", json.dumps(body).encode())

    def write_bytes(self, status, content_type, body, extra=None):
        headers = {"content-type": content_type, "content-length": str(len(body))}
        headers.update(extra or {})
        self.write_status(status, headers)
        if not self.headless:
            self.wfile.write(body)

    def write_status(self, status, extra=None):
        self.send_response(status)
        for name, value in (extra or {}).items():
            self.send_header(name, value)
        self.end_headers()

    def start_chunks(self, extra=None):
        headers = dict(extra or {})
        if not self.headless:
            headers["transfer-encoding"] = "chunked"
        self.write_status(200, headers)

    def write_chunk(self, chunk):
        if self.headless:
            return
        self.wfile.write(b"%X\r\n%s\r\n" % (len(chunk), chunk))
        self.wfile.flush()

    def end_chunks(self):
        if self.headless:
            return
        self.wfile.write(b"0\r\n\r\n")
        self.wfile.flush()

    def log_message(self, fmt, *args):
        pass


def main():
    port = int(os.environ.get("PORT") or DEFAULT_PORT)
    print(f"python listening on http://localhost:{port}", flush=True)
    ThreadingHTTPServer(("", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
