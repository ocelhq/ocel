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
    MintRequest,
    MintResponse,
    PublishResponse,
    SubscribeToken,
)

MINTED_EXPIRES_AT = 1790000030


def fake_connect_token(realtime: str) -> str:
    return f"token connect /{realtime}"


def fake_subscribe_token(channel: str) -> str:
    return f"token subscribe {channel}"


class FakeRealtimeRuntime:
    def __init__(self):
        self.refusal: str | None = None
        self.mint_refusal: str | None = None
        self.mint_answer: MintResponse | None = None
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
        if self.mint_answer is not None:
            return self.mint_answer
        return MintResponse(
            connect_token=fake_connect_token(request.realtime) if request.connect else "",
            subscribe_tokens=[
                SubscribeToken(
                    token=fake_subscribe_token(channel), channel=self.map_channel(channel)
                )
                for channel in request.subscribe_channels
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
