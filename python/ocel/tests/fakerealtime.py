import io
import json
import threading
from collections.abc import Callable
from wsgiref.simple_server import make_server

from connectrpc.code import Code
from connectrpc.errors import ConnectError
from fakebucket import _body, _Quiet, _Threading
from protobuf.wkt import Timestamp

from ocel.gen.app.realtime.v1.realtime_connect import RealtimeServiceWSGIApplication
from ocel.gen.app.realtime.v1.realtime_pb import (
    MintedToken,
    MintRequest,
    MintResponse,
    Operation,
    PublishResponse,
)

MINTED_EXPIRES_AT = 1790000030


def fake_token(operation: Operation, channel: str) -> str:
    return f"token {operation.name} {channel}"


class FakeRealtimeRuntime:
    def __init__(self):
        self.refusal: str | None = None
        self.mint_refusal: str | None = None
        self.map_channel: Callable[[str], str] = lambda channel: channel
        self.published: list[dict] = []
        self.mints: list[MintRequest] = []
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
            "url": "wss://realtime.shop.example/event/realtime",
            "host": "realtime.shop.example",
        }
        return json.dumps({"name": "realtime--app", "realtime": realtime})

    def mint(self, request, ctx):
        self.mints.append(request)
        if self.mint_refusal is not None:
            raise ConnectError(Code.FAILED_PRECONDITION, self.mint_refusal)
        return MintResponse(
            tokens=[
                MintedToken(
                    token=fake_token(grant.operation, grant.channel),
                    channel=self.map_channel(grant.channel),
                )
                for grant in request.grants
            ],
            expires_at=Timestamp(seconds=MINTED_EXPIRES_AT),
        )

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
