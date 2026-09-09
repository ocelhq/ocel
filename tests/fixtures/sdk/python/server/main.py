import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from infra import Env, db

DEFAULT_PORT = "3104"


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            body = json.dumps(
                {"ok": True, "database": db.name, "greeting": Env().greeting}
            ).encode()
            self.send_response(200)
            self.send_header("content-type", "application/json")
            self.send_header("content-length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.send_response(404)
        self.end_headers()

    def log_message(self, fmt, *args):
        pass


def main():
    port = int(os.environ.get("PORT") or DEFAULT_PORT)
    print(f"python listening on http://localhost:{port}", flush=True)
    ThreadingHTTPServer(("", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
