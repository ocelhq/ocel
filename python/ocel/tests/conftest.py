import gzip
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

import ocel.env
from ocel.gen.app.resources.v1.resources_pb import DeclareRequest, DeclareResponse
from ocel.gen.app.resources.v1.variables_pb import (
    DeclareEnvRequest,
    DeclareEnvResponse,
    ReportEnvProblemsRequest,
    ReportEnvProblemsResponse,
)

DECLARE = "/app.resources.v1.ResourceService/Declare"
DECLARE_ENV = "/app.resources.v1.ResourceService/DeclareEnv"
REPORT_ENV_PROBLEMS = "/app.resources.v1.ResourceService/ReportEnvProblems"

_REQUESTS = {
    DECLARE: DeclareRequest,
    DECLARE_ENV: DeclareEnvRequest,
    REPORT_ENV_PROBLEMS: ReportEnvProblemsRequest,
}


class Collector:
    def __init__(self):
        self.declares = []
        self.cells = []
        self.server = HTTPServer(("127.0.0.1", 0), self._handler())
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    @property
    def url(self):
        host, port = self.server.server_address
        return f"http://{host}:{port}"

    def bodies(self, path):
        return [body for seen, _, body in self.declares if seen == path]

    def declared_env(self):
        declared = self.bodies(DECLARE_ENV)
        return declared[0] if declared else None

    def reported(self):
        reported = self.bodies(REPORT_ENV_PROBLEMS)
        return reported[0].problems if reported else []

    def _response(self, path):
        if path == DECLARE_ENV:
            return DeclareEnvResponse(cells=list(self.cells))
        if path == REPORT_ENV_PROBLEMS:
            return ReportEnvProblemsResponse()
        return DeclareResponse()

    def _handler(self):
        collector = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = self.rfile.read(int(self.headers["Content-Length"]))
                if self.headers.get("Content-Encoding") == "gzip":
                    body = gzip.decompress(body)
                kind = self.headers.get("Content-Type", "")
                collector.declares.append(
                    (
                        self.path,
                        self.headers.get("Connect-Protocol-Version"),
                        decode(self.path, kind, body),
                    )
                )
                response = collector._response(self.path)
                encoded = (
                    response.to_json().encode() if kind.endswith("json") else response.to_binary()
                )
                self.send_response(200)
                self.send_header("Content-Type", kind)
                self.send_header("Content-Length", str(len(encoded)))
                self.end_headers()
                self.wfile.write(encoded)

            def log_message(self, *_args):
                pass

        return Handler

    def close(self):
        self.server.shutdown()
        self.server.server_close()


def decode(path: str, content_type: str, body: bytes):
    message = _REQUESTS[path]
    if content_type.endswith("json"):
        return message.from_json(body)
    return message.from_binary(body)


@pytest.fixture
def collector(monkeypatch):
    c = Collector()
    monkeypatch.setenv("OCEL_PHASE", "discovery")
    monkeypatch.setenv("OCEL_DEV_SERVER", c.url)
    yield c
    c.close()


@pytest.fixture(autouse=True)
def owners(monkeypatch):
    monkeypatch.delenv("OCEL_PHASE", raising=False)
    monkeypatch.delenv("OCEL_APP_FOLDER", raising=False)
    ocel.env._owner.clear()
    yield
    ocel.env._owner.clear()
