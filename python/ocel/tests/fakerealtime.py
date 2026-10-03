import base64
import io
import json
import threading
from pathlib import Path
from wsgiref.simple_server import make_server

from connectrpc.code import Code
from connectrpc.errors import ConnectError
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
from fakebucket import _body, _Quiet, _Threading

from ocel.gen.app.realtime.v1.realtime_connect import RealtimeServiceWSGIApplication
from ocel.gen.app.realtime.v1.realtime_pb import PublishResponse

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


class FakeRealtimeRuntime:
    def __init__(self):
        self.refusal: str | None = None
        self.published: list[dict] = []
        self.authorizations: list[str | None] = []
        self.server = make_server(
            "127.0.0.1",
            0,
            self._dispatch,
            server_class=_Threading,
            handler_class=_Quiet,
        )
        self._app = RealtimeServiceWSGIApplication(self)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def binding(self) -> str:
        realtime = {
            **FIXTURE["realtime"],
            "transport": "REALTIME_TRANSPORT_OCEL_GATEWAY",
            "url": "wss://realtime.shop.example/event/realtime",
            "host": "realtime.shop.example",
        }
        return json.dumps({"name": "realtime--app", "realtime": realtime})

    def publish(self, request, ctx):
        self.authorizations.append(ctx.request_headers.get("Authorization"))
        if self.refusal is not None:
            raise ConnectError(Code.UNAVAILABLE, self.refusal)
        self.published.append(
            {
                "realtime": request.realtime,
                "channel": request.channel,
                "envelope": json.loads(request.event),
            }
        )
        return PublishResponse()

    def _dispatch(self, environ, start_response):
        environ["wsgi.input"] = io.BytesIO(_body(environ))
        return self._app(environ, start_response)

    def close(self):
        self.server.shutdown()
        self.server.server_close()
