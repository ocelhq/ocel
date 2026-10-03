import base64
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey

FIXTURE = json.loads(
    (
        Path(__file__).parents[3]
        / "proto"
        / "common"
        / "bindings"
        / "v1"
        / "fixtures"
        / "realtime.json"
    ).read_text()
)
VERIFY_KEY = base64.b64decode(FIXTURE["realtime"]["verifyKey"])


def read_claims(token: str) -> dict:
    header, payload, signature = token.split(".")
    Ed25519PublicKey.from_public_bytes(VERIFY_KEY).verify(
        base64.urlsafe_b64decode(signature + "=="), f"{header}.{payload}".encode()
    )
    return json.loads(base64.urlsafe_b64decode(payload + "=="))


class FakeGateway:
    def __init__(self, status: int = 202):
        self.status = status
        self.delay_seconds = 0.0
        self.published: list[dict] = []
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), self._handler())
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    @property
    def host(self) -> str:
        host, port = self.server.server_address
        return f"{host}:{port}"

    def binding(self) -> str:
        realtime = {
            **FIXTURE["realtime"],
            "transport": "REALTIME_TRANSPORT_OCEL_GATEWAY",
            "url": f"ws://{self.host}/realtime",
            "host": self.host,
        }
        return json.dumps({"name": "realtime--app", "realtime": realtime})

    def _handler(self):
        gateway = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = self.rfile.read(int(self.headers["Content-Length"]))
                time.sleep(gateway.delay_seconds)
                gateway.published.append(
                    {
                        "path": self.path,
                        "authorization": self.headers.get("Authorization"),
                        "envelope": json.loads(body),
                    }
                )
                self.send_response(gateway.status)
                self.send_header("Content-Length", "0")
                self.end_headers()

            def log_message(self, *_args):
                pass

        return Handler

    def close(self):
        self.server.shutdown()
        self.server.server_close()


class FakeAppSync:
    def __init__(self, status: int = 200, answer: bytes = b'{"successful":[],"failed":[]}'):
        self.status = status
        self.answer = answer
        self.published: list[dict] = []
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), self._handler())
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    @property
    def host(self) -> str:
        host, port = self.server.server_address
        return f"{host}:{port}"

    def binding(self) -> str:
        return json.dumps(
            {"name": "realtime--app", "realtime": {**FIXTURE["realtime"], "host": self.host}}
        )

    def _handler(self):
        appsync = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = self.rfile.read(int(self.headers["Content-Length"]))
                appsync.published.append(
                    {
                        "path": self.path,
                        "authorization": self.headers.get("Authorization"),
                        "token": self.headers.get("X-Amz-Security-Token"),
                        "body": json.loads(body),
                    }
                )
                self.send_response(appsync.status)
                self.send_header("Content-Length", str(len(appsync.answer)))
                self.end_headers()
                self.wfile.write(appsync.answer)

            def log_message(self, *_args):
                pass

        return Handler

    def close(self):
        self.server.shutdown()
