import asyncio
import json
import logging
import secrets
import time
from collections.abc import Awaitable, Callable, Iterable, Mapping
from dataclasses import dataclass, field
from datetime import timedelta
from typing import BinaryIO, Literal, cast, get_args, overload
from urllib.parse import urlsplit, urlunsplit
from wsgiref.types import StartResponse

from protobuf import Oneof
from protobuf.wkt import Duration

from ocel._binding import read_realtime_binding, refuse_unprovisioned
from ocel._callables import call_function
from ocel._declare import declare, find_caller_source, is_discovering
from ocel._error import OcelError
from ocel._payload import Codec
from ocel._realtime_wire import (
    CHANNEL_SEGMENT,
    SEGMENT_RULE,
    ChannelPattern,
    WireRefusal,
    encode_wire_channel,
)
from ocel._runtime import RuntimeConnection
from ocel.gen.app.realtime.v1.realtime_connect import (
    RealtimeServiceClient,
    RealtimeServiceClientSync,
)
from ocel.gen.app.realtime.v1.realtime_pb import (
    Grant,
    MintRequest,
    MintResponse,
    Operation,
    PublishRequest,
)
from ocel.gen.app.resources.v1.resources_pb import (
    DeclareRequest,
    RealtimeChannel,
    RealtimeConfig,
    RealtimePublish,
    RealtimeSubscribe,
    ResourceIdentifier,
    ResourceType,
)
from ocel.gen.common.bindings.v1.bindings_pb import RealtimeProperties

_logger = logging.getLogger("ocel")
_MIN_TOKEN_TTL_SECONDS = 10
_MAX_TOKEN_TTL_SECONDS = 300
_MAX_OPS = 50
_MAX_REQUEST_BYTES = 1 << 20
_MAX_EVENT_BYTES = 240 * 1024
_BATCH_KEYS = frozenset({"connect", "ops"})
_OP_KEYS = frozenset({"op", "pattern", "params", "body"})
_MISSING_BODY = object()

_Operation = Literal["subscribe", "publish"]
_OPERATIONS: tuple[_Operation, ...] = get_args(_Operation)
_PublishRefusal = WireRefusal | Literal["invalid-params", "invalid-body", "body-too-large"]
_DenialCode = (
    _PublishRefusal
    | Literal[
        "invalid-op",
        "unknown-op",
        "unknown-pattern",
        "no-publish-rule",
        "unauthenticated",
        "forbidden",
        "rule-error",
        "publish-failed",
    ]
)


@dataclass(frozen=True)
class RealtimeRequest:
    """The request the realtime handler received, as every rule and ``authorize`` sees it,
    whichever server ran the handler."""

    #: The HTTP method.
    method: str
    #: The full URL the request was made to.
    url: str
    #: The request's headers, by lowercase name.
    headers: Mapping[str, str]
    #: The request's body.
    body: bytes = b""


@dataclass(frozen=True)
class RuleContext[A]:
    """What a rule decides from."""

    #: What ``authorize`` returned for the request; never ``None`` in a rule.
    auth: A
    #: The params the caller asked for. On a ``wildcard`` channel, trailing params a
    #: subscriber left off are missing.
    params: dict[str, str]
    #: The request the realtime handler received.
    request: RealtimeRequest
    #: In a publish rule, the event, validated against the channel's schema.
    body: object = None


class RealtimePublishError(OcelError, ValueError):
    """Raised by ``publish`` when the params or the event cannot be published."""

    #: The reason, one of the codes the realtime handler denies an op with:
    #: ``invalid-params``, ``missing-param``, ``unknown-param``, ``empty-value``,
    #: ``value-too-long``, ``invalid-body`` or ``body-too-large``.
    code: _PublishRefusal

    def __init__(self, pattern: str, code: _PublishRefusal):
        super().__init__(f'realtime channel "{pattern}" refuses the publish: {code}')
        self.code = code


type Rule[A] = Callable[[RuleContext[A]], bool | Awaitable[bool]]
"""A rule deciding whether a caller may subscribe to or publish on a channel, sync or async."""

type ASGIApplication = Callable[
    [
        Mapping[str, object],
        Callable[[], Awaitable[Mapping[str, object]]],
        Callable[[dict[str, object]], Awaitable[None]],
    ],
    Awaitable[None],
]
"""An ASGI application, as :meth:`Realtime.asgi` returns it."""

type WSGIApplication = Callable[[dict[str, object], StartResponse], Iterable[bytes]]
"""A WSGI application, as :meth:`Realtime.wsgi` returns it."""


def _read_subject(auth: object) -> str:
    if auth is None:
        return "anonymous"
    if isinstance(auth, Mapping):
        identity = cast(Mapping[str, object], auth).get("id")
    else:
        identity = cast(object, getattr(auth, "id", None))
    if isinstance(identity, str) or (isinstance(identity, int) and not isinstance(identity, bool)):
        return str(identity)
    return "anonymous"


def _deny(code: _DenialCode) -> dict[str, object]:
    return {"code": code}


class Channel[A]:
    """One channel pattern of a realtime resource, and the handle its events are published
    through."""

    #: The pattern the channel was declared under.
    pattern: str

    def __init__(
        self,
        resource: "Realtime[A]",
        pattern: ChannelPattern,
        codec: Codec,
        wildcard: bool,
        subscribe: Rule[A] | None,
        publish: Rule[A] | None,
        source: str,
    ):
        self.pattern = pattern.written
        self._resource = resource
        self._parsed = pattern
        self._codec = codec
        self._wildcard = wildcard
        self._subscribe = subscribe
        self._publish = publish
        self._source = source

    def _build_manifest(self) -> RealtimeChannel:
        return RealtimeChannel(
            pattern=self.pattern,
            wildcard=self._wildcard,
            schema=self._codec.schema,
            subscribe=RealtimeSubscribe.PUBLIC
            if self._subscribe is None
            else RealtimeSubscribe.RULE,
            publish=RealtimePublish.SERVER if self._publish is None else RealtimePublish.RULE,
            source=self._source,
        )

    def _encode_event(
        self, params: Mapping[str, object], body: object
    ) -> tuple[str, object, bytes]:
        if not all(isinstance(value, str) for value in params.values()):
            raise RealtimePublishError(self.pattern, "invalid-params")
        named = cast(Mapping[str, str], params)
        encoded = encode_wire_channel(self._resource.name, self._parsed, named, False)
        if encoded[1] is not None:
            raise RealtimePublishError(self.pattern, encoded[1])
        channel = encoded[0]
        if body is _MISSING_BODY:
            raise RealtimePublishError(self.pattern, "invalid-body")
        try:
            validated = self._codec.decode(body)
            data = self._codec.to_jsonable(validated)
        except (ValueError, TypeError):
            raise RealtimePublishError(self.pattern, "invalid-body") from None
        envelope = json.dumps(
            {
                "v": 1,
                "id": secrets.token_hex(16),
                "ch": channel,
                "ts": int(time.time() * 1000),
                "kind": "live",
                "data": data,
            },
            separators=(",", ":"),
            ensure_ascii=False,
        ).encode()
        if len(envelope) > _MAX_EVENT_BYTES:
            raise RealtimePublishError(self.pattern, "body-too-large")
        return channel, validated, envelope

    def publish(self, body: object, /, **params: str) -> None:
        """Publish ``body`` to every subscriber of the channel ``params`` fill in the
        pattern, after validating it against the channel's schema. Every param is required.
        Raises :class:`RealtimePublishError` for params or a body that cannot be published,
        and :class:`ocel.RuntimeRefusedError` when the ocel runtime does not publish the event."""
        self._resource._runtime.open("publish", True)
        channel, _, envelope = self._encode_event(params, body)
        self._resource._publish("publish", channel, envelope)

    async def publish_async(self, body: object, /, **params: str) -> None:
        """Publish ``body`` as :meth:`publish` does, without blocking the event loop."""
        channel, _, envelope = self._encode_event(params, body)
        await self._resource._publish_async("publish_async", channel, envelope)


@dataclass
class _Response:
    status: int
    body: object = None
    headers: dict[str, str] = field(default_factory=dict[str, str])

    def encode(self) -> tuple[int, list[tuple[str, str]], bytes]:
        headers: dict[str, str] = {"Cache-Control": "no-store", **self.headers}
        content = b""
        if self.body is not None:
            content = json.dumps(self.body, separators=(",", ":")).encode()
            headers["Content-Type"] = "application/json"
        headers["Content-Length"] = str(len(content))
        return self.status, list(headers.items()), content


def _build_error_response(
    status: int, message: str, headers: dict[str, str] | None = None
) -> _Response:
    return _Response(status, {"error": message}, headers or {})


def _parse_batch(body: bytes) -> tuple[bool, list[object]] | None:
    try:
        batch: object = json.loads(body)
    except (ValueError, RecursionError):
        return None
    fields = _read_object(batch)
    if fields is None or not fields.keys() <= _BATCH_KEYS:
        return None
    ops = fields.get("ops")
    connect = fields.get("connect", False)
    if not isinstance(ops, list) or not isinstance(connect, bool):
        return None
    return connect, cast(list[object], ops)


def _read_object(value: object) -> dict[str, object] | None:
    return cast(dict[str, object], value) if isinstance(value, dict) else None


def _read_first_header_value(request: RealtimeRequest, name: str) -> str:
    return request.headers.get(name, "").split(",")[0].strip()


def _read_own_origin(request: RealtimeRequest) -> tuple[str, str]:
    own_url = urlsplit(request.url)
    host = (
        _read_first_header_value(request, "x-forwarded-host")
        or request.headers.get("host")
        or own_url.netloc
    )
    scheme = _read_first_header_value(request, "x-forwarded-proto").lower() or own_url.scheme
    return scheme, host


def _is_own_origin(request: RealtimeRequest, origin: str) -> bool:
    try:
        parts = urlsplit(origin)
    except ValueError:
        return False
    if parts.netloc == "":
        return False
    return (parts.scheme, parts.netloc) == _read_own_origin(request)


def _resolve_socket_url(url: str, request: RealtimeRequest) -> str:
    if not url.startswith("/"):
        return url
    scheme, host = _read_own_origin(request)
    return f"{'wss' if scheme == 'https' else 'ws'}://{host}{url}"


class Realtime[A]:
    """A realtime resource an app declares: typed channels that browsers subscribe to
    through its :meth:`asgi` or :meth:`wsgi` handler, and the server publishes on."""

    #: The name the resource was declared under, which begins every channel.
    name: str

    def __init__(
        self,
        name: str,
        authorize: Callable[[RealtimeRequest], A | None | Awaitable[A | None]] | None,
        ttl_seconds: int,
        source: str,
    ):
        """Take the handle for the realtime resource named ``name``. Prefer :func:`realtime`,
        which declares the resource as well as handing back its handle."""
        self.name = name
        self._authorize = authorize
        self._ttl_seconds = ttl_seconds
        self._source = source
        self._channels: dict[str, Channel[A]] = {}
        self._runtime = RuntimeConnection(
            f'realtime("{name}")',
            lambda address: RealtimeServiceClientSync(address, send_compression=None),
            lambda address: RealtimeServiceClient(address, send_compression=None),
            lambda: read_realtime_binding(name),
        )

    def _declare(self) -> None:
        if not is_discovering():
            return
        declare(
            DeclareRequest(
                resource=ResourceIdentifier(type=ResourceType.REALTIME, name=self.name),
                config=Oneof(
                    "realtime",
                    RealtimeConfig(
                        channels=[channel._build_manifest() for channel in self._channels.values()],
                        token_ttl=Duration.from_seconds(self._ttl_seconds),
                    ),
                ),
                source=self._source,
            )
        )

    @overload
    def channel(
        self,
        pattern: str,
        *,
        schema: object = None,
        wildcard: bool = False,
        subscribe: Literal["public"],
        publish: Rule[A] | None = None,
    ) -> Channel[A]: ...
    @overload
    def channel(
        self,
        pattern: str,
        *,
        schema: object = None,
        wildcard: bool = False,
        subscribe: None = None,
        publish: Rule[A] | None = None,
    ) -> Callable[[Rule[A]], Channel[A]]: ...
    def channel(
        self,
        pattern: str,
        *,
        schema: object = None,
        wildcard: bool = False,
        subscribe: Literal["public"] | None = None,
        publish: Rule[A] | None = None,
    ) -> Channel[A] | Callable[[Rule[A]], Channel[A]]:
        """Declare the channel ``pattern``: at most 4 segments joined by /, each a literal or
        a ``:param``. Decorate the rule deciding whether a caller may subscribe, a function
        of a :class:`RuleContext` answering a bool, sync or async; or pass
        ``subscribe="public"`` to let anyone subscribe, which returns the channel at once.
        ``schema`` is the event's type, validated with pydantic beyond plain JSON;
        ``wildcard`` lets a subscriber leave off trailing params; ``publish`` is the rule
        deciding whether a caller may publish from a browser, and without it only the
        server publishes. A channel with either rule needs the resource's ``authorize``,
        and declaring one without it raises ``ValueError`` here."""
        source = find_caller_source()
        if subscribe not in (None, "public"):
            raise ValueError(
                f'realtime "{self.name}": channel "{pattern}" takes subscribe="public" or a '
                f"decorated rule, not {subscribe!r}"
            )
        if self._authorize is None and (subscribe is None or publish is not None):
            raise ValueError(
                f'realtime "{self.name}": channel "{pattern}" has a rule, and a rule decides '
                f'from what authorize returns: pass authorize to realtime("{self.name}")'
            )
        try:
            parsed = ChannelPattern(pattern)
        except ValueError as error:
            raise ValueError(f'realtime "{self.name}": {error}') from None
        if pattern in self._channels:
            raise ValueError(
                f'realtime "{self.name}": channel "{pattern}" is declared already at '
                f"{self._channels[pattern]._source}, and a resource declares each pattern once"
            )
        codec = Codec(schema)

        def attach(rule: Rule[A] | None) -> Channel[A]:
            channel = Channel(self, parsed, codec, wildcard, rule, publish, source)
            self._channels[pattern] = channel
            self._declare()
            return channel

        if subscribe == "public":
            return attach(None)
        return attach

    def _read_properties(self, access: str) -> RealtimeProperties:
        if is_discovering():
            raise refuse_unprovisioned(f'realtime("{self.name}")', access)
        return read_realtime_binding(self.name)

    async def _mint_tokens_async(self, subject: str, grants: list[Grant]) -> MintResponse:
        request = MintRequest(
            realtime=self.name,
            subject=subject,
            ttl=Duration.from_seconds(self._ttl_seconds),
            grants=grants,
        )
        minted: MintResponse = await self._runtime.call_async(
            "handler", lambda client: client.mint(request)
        )
        if len(minted.tokens) != len(grants):
            raise RuntimeError(
                f'realtime "{self.name}": the runtime minted {len(minted.tokens)} tokens '
                f"for {len(grants)} grants"
            )
        return minted

    def _build_request(self, channel: str, envelope: bytes) -> PublishRequest:
        return PublishRequest(realtime=self.name, channel=channel, event=envelope.decode())

    def _publish(self, access: str, channel: str, envelope: bytes) -> None:
        request = self._build_request(channel, envelope)
        self._runtime.call(access, lambda client: client.publish(request))

    async def _publish_async(self, access: str, channel: str, envelope: bytes) -> None:
        request = self._build_request(channel, envelope)
        await self._runtime.call_async(access, lambda client: client.publish(request))

    async def _serve_op(
        self,
        request: RealtimeRequest,
        auth: A | None,
        op: object,
    ) -> dict[str, object]:
        fields = _read_object(op)
        if (
            fields is None
            or not fields.keys() <= _OP_KEYS
            or (fields.get("op") == "subscribe" and "body" in fields)
        ):
            return _deny("invalid-op")
        operation = fields.get("op")
        if not isinstance(operation, str) or operation not in _OPERATIONS:
            return _deny("unknown-op")
        pattern = fields.get("pattern")
        channel = self._channels.get(pattern) if isinstance(pattern, str) else None
        if channel is None:
            return _deny("unknown-pattern")
        rule = channel._publish
        if operation == "publish" and rule is None:
            return _deny("no-publish-rule")
        given = fields.get("params")
        asked = {} if given is None else _read_object(given)
        if asked is None or not all(isinstance(value, str) for value in asked.values()):
            return _deny("invalid-params")
        params = cast(dict[str, str], asked)
        if operation == "subscribe" or rule is None:
            return await self._serve_subscribe(request, auth, channel, params)
        body = fields.get("body", _MISSING_BODY)
        return await self._serve_publish(request, auth, channel, rule, params, body)

    async def _serve_subscribe(
        self,
        request: RealtimeRequest,
        auth: A | None,
        channel: Channel[A],
        params: dict[str, str],
    ) -> dict[str, object]:
        encoded = encode_wire_channel(self.name, channel._parsed, params, channel._wildcard)
        if encoded[1] is not None:
            return _deny(encoded[1])
        wire = encoded[0]
        if channel._subscribe is not None:
            if auth is None:
                return _deny("unauthenticated")
            denied = await _run_rule(channel._subscribe, RuleContext(auth, dict(params), request))
            if denied is not None:
                return _deny(denied)
        return {"wire": wire, "token": None}

    async def _serve_publish(
        self,
        request: RealtimeRequest,
        auth: A | None,
        channel: Channel[A],
        rule: Rule[A],
        params: dict[str, str],
        body: object,
    ) -> dict[str, object]:
        try:
            wire, validated, envelope = channel._encode_event(params, body)
        except RealtimePublishError as refused:
            return _deny(refused.code)
        if auth is None:
            return _deny("unauthenticated")
        context = RuleContext(auth, dict(params), request, validated)
        denied = await _run_rule(rule, context)
        if denied is not None:
            return _deny(denied)
        try:
            await self._publish_async("handler", wire, envelope)
        except Exception:
            _logger.exception('realtime "%s" failed to publish a relayed event', self.name)
            return _deny("publish-failed")
        return {"wire": wire}

    async def _answer_batch(
        self, request: RealtimeRequest, connect: bool, ops: list[object]
    ) -> dict[str, object]:
        properties = self._read_properties("handler")
        auth: A | None = None
        if self._authorize is not None:
            auth = await call_function(self._authorize, request)

        async def serve(i: int, op: object) -> tuple[int, dict[str, object]]:
            return i, await self._serve_op(request, auth, op)

        publishes = [(i, op) for i, op in enumerate(ops) if _is_publish(op)]
        published = {i for i, _ in publishes}

        async def serve_publishes_in_order() -> list[tuple[int, dict[str, object]]]:
            return [await serve(i, op) for i, op in publishes]

        async with asyncio.TaskGroup() as group:
            others = [
                group.create_task(serve(i, op)) for i, op in enumerate(ops) if i not in published
            ]
            chain = group.create_task(serve_publishes_in_order())
        outcomes = sorted(
            [*(task.result() for task in others), *chain.result()], key=lambda pair: pair[0]
        )

        answer: dict[str, object] = {
            "url": _resolve_socket_url(properties.url, request),
            "host": properties.host,
        }
        await self._mint_answer_tokens(answer, _read_subject(auth), connect, outcomes)
        answer["grants"] = [{"i": i, **outcome} for i, outcome in outcomes if "wire" in outcome]
        answer["denied"] = [{"i": i, **outcome} for i, outcome in outcomes if "code" in outcome]
        return answer

    async def _mint_answer_tokens(
        self,
        answer: dict[str, object],
        subject: str,
        connect: bool,
        outcomes: list[tuple[int, dict[str, object]]],
    ) -> None:
        grants: list[Grant] = []
        if connect:
            grants.append(Grant(operation=Operation.CONNECT, channel=f"/{self.name}"))
        subscribes = [outcome for _, outcome in outcomes if "token" in outcome]
        grants.extend(
            Grant(operation=Operation.SUBSCRIBE, channel=cast(str, outcome["wire"]))
            for outcome in subscribes
        )
        if not grants:
            return
        minted = await self._mint_tokens_async(subject, grants)
        tokens = list(minted.tokens)
        if connect:
            expires_at = minted.expires_at.seconds if minted.expires_at is not None else 0
            answer["connect"] = {"token": tokens.pop(0).token, "expiresAt": expires_at}
        for outcome, token in zip(subscribes, tokens, strict=True):
            outcome["wire"], outcome["token"] = token.channel, token.token

    async def _serve(self, request: RealtimeRequest, allowed_origins: Iterable[str]) -> _Response:
        allowed = list(allowed_origins)
        origin = request.headers.get("origin")
        if request.method == "OPTIONS" and origin in allowed:
            return _Response(
                204,
                headers={
                    "Access-Control-Allow-Origin": origin,
                    "Access-Control-Allow-Methods": "POST",
                    "Access-Control-Allow-Headers": "authorization, content-type",
                    "Access-Control-Max-Age": "600",
                    "Vary": "Origin",
                },
            )
        if request.method != "POST":
            return _build_error_response(405, "the realtime handler takes POST", {"Allow": "POST"})
        cors: dict[str, str] = {}
        if origin is not None:
            if origin in allowed:
                cors = {"Access-Control-Allow-Origin": origin, "Vary": "Origin"}
            elif not _is_own_origin(request, origin):
                return _build_error_response(403, "this origin may not call the realtime handler")
        media_type = request.headers.get("content-type", "").split(";")[0].strip().lower()
        if media_type != "application/json":
            return _build_error_response(415, "the realtime handler takes application/json", cors)
        if len(request.body) > _MAX_REQUEST_BYTES:
            return _build_error_response(
                413, f"a request is at most {_MAX_REQUEST_BYTES} bytes", cors
            )
        batch = _parse_batch(request.body)
        if batch is None:
            return _build_error_response(
                400,
                "the body is { connect?: boolean, ops: [{ op, pattern, params?, body? }] }",
                cors,
            )
        connect, ops = batch
        if len(ops) > _MAX_OPS:
            return _build_error_response(400, f"a request holds at most {_MAX_OPS} ops", cors)
        try:
            return _Response(200, await self._answer_batch(request, connect, ops), cors)
        except Exception:
            _logger.exception('realtime "%s" failed to answer a request', self.name)
            return _build_error_response(500, "the realtime handler failed", cors)

    def asgi(self, *, allowed_origins: Iterable[str] = ()) -> ASGIApplication:
        """An ASGI application serving the resource to browsers, mounted at any path: one
        batched POST of ``{ connect?, ops: [{ op, pattern, params?, body? }] }``, at most 50
        ops, answered with the socket url and the host its authorization names, a connect token
        when asked, a grant with a token for each op its rule allows and a denial with a code
        for each it does not. The runtime the app runs on mints every token, in one call per
        request, and names the channel each subscribe grant is for, which the browser
        subscribes to as given. It takes POST with application/json alone, serves its own
        origin and those in ``allowed_origins`` (the only ones it sends CORS headers to), answers
        ``Cache-Control: no-store``, and never sets a cookie."""
        origins = tuple(allowed_origins)

        async def app(
            scope: Mapping[str, object],
            receive: Callable[[], Awaitable[Mapping[str, object]]],
            send: Callable[[dict[str, object]], Awaitable[None]],
        ) -> None:
            if scope["type"] == "lifespan":
                while True:
                    message = await receive()
                    if message["type"] == "lifespan.startup":
                        await send({"type": "lifespan.startup.complete"})
                    elif message["type"] == "lifespan.shutdown":
                        await send({"type": "lifespan.shutdown.complete"})
                        return
            body = b""
            more = True
            while more:
                message = await receive()
                if message["type"] == "http.disconnect":
                    return
                chunk = message.get("body", b"")
                body += chunk if isinstance(chunk, bytes) else b""
                more = message.get("more_body", False) is True
                if len(body) > _MAX_REQUEST_BYTES:
                    break
            headers: dict[str, str] = {}
            for name, value in cast(Iterable[tuple[bytes, bytes]], scope["headers"]):
                headers.setdefault(name.decode("latin-1").lower(), value.decode("latin-1"))
            host = headers.get("host", "")
            path = str(scope.get("root_path", "")) + str(scope["path"])
            query = cast(bytes, scope.get("query_string", b"")).decode("latin-1")
            url = urlunsplit((str(scope.get("scheme", "http")), host, path, query, ""))
            request = RealtimeRequest(str(scope["method"]), url, headers, body)
            status, response_headers, content = (await self._serve(request, origins)).encode()
            await send(
                {
                    "type": "http.response.start",
                    "status": status,
                    "headers": [
                        (name.lower().encode(), value.encode()) for name, value in response_headers
                    ],
                }
            )
            await send({"type": "http.response.body", "body": content})

        return app

    def wsgi(self, *, allowed_origins: Iterable[str] = ()) -> WSGIApplication:
        """A WSGI application serving the resource as :meth:`asgi` does."""
        origins = tuple(allowed_origins)

        def app(environ: dict[str, object], start_response: StartResponse) -> list[bytes]:
            def read(key: str) -> str:
                return str(environ.get(key) or "")

            headers = {
                key[5:].replace("_", "-").lower(): str(value)
                for key, value in environ.items()
                if key.startswith("HTTP_")
            }
            if read("CONTENT_TYPE"):
                headers["content-type"] = read("CONTENT_TYPE")
            try:
                length = int(read("CONTENT_LENGTH") or 0)
            except ValueError:
                length = 0
            source = cast(BinaryIO, environ["wsgi.input"])
            body = source.read(min(length, _MAX_REQUEST_BYTES + 1)) if length else b""
            host = headers.get("host") or read("SERVER_NAME")
            path = read("SCRIPT_NAME") + read("PATH_INFO")
            scheme = read("wsgi.url_scheme") or "http"
            url = urlunsplit((scheme, host, path, read("QUERY_STRING"), ""))
            request = RealtimeRequest(read("REQUEST_METHOD"), url, headers, body)
            status, response_headers, content = asyncio.run(self._serve(request, origins)).encode()
            start_response(f"{status} {_STATUS_PHRASES.get(status, '')}".strip(), response_headers)
            return [content]

        return app


_STATUS_PHRASES = {
    200: "OK",
    204: "No Content",
    400: "Bad Request",
    403: "Forbidden",
    405: "Method Not Allowed",
    413: "Content Too Large",
    415: "Unsupported Media Type",
    500: "Internal Server Error",
}


def _is_publish(op: object) -> bool:
    fields = _read_object(op)
    return fields is not None and fields.get("op") == "publish"


async def _run_rule[A](
    rule: Rule[A], context: RuleContext[A]
) -> Literal["forbidden", "rule-error"] | None:
    try:
        allowed = await call_function(rule, context)
    except Exception:
        _logger.exception("a realtime rule raised")
        return "rule-error"
    return None if allowed else "forbidden"


@overload
def realtime(
    name: str, *, authorize: None = None, token_ttl: int | timedelta = 60
) -> Realtime[None]: ...
@overload
def realtime[A](
    name: str,
    *,
    authorize: Callable[[RealtimeRequest], A | None | Awaitable[A | None]],
    token_ttl: int | timedelta = 60,
) -> Realtime[A]: ...
def realtime[A](
    name: str,
    *,
    authorize: Callable[[RealtimeRequest], A | None | Awaitable[A | None]] | None = None,
    token_ttl: int | timedelta = 60,
) -> Realtime[A] | Realtime[None]:
    """Declare a realtime resource named ``name`` and return the handle its channels are
    declared, served and published through. ``authorize`` identifies the caller of the
    handler from its :class:`RealtimeRequest`, with whatever auth the app already uses, sync
    or async, and returns ``None`` for nobody; its result is ``ctx.auth`` in every rule, and
    its ``id`` key or attribute, when a string or integer, is the ``sub`` of every token
    minted for the caller. A resource with a subscribe or publish rule needs ``authorize``.
    ``token_ttl`` is how long each token lives, 10 to 300 seconds."""
    seconds = token_ttl.total_seconds() if isinstance(token_ttl, timedelta) else token_ttl
    if not CHANNEL_SEGMENT.fullmatch(name):
        raise ValueError(
            f'realtime "{name}": the name begins every channel, so it is a channel namespace: '
            f"{SEGMENT_RULE}"
        )
    if seconds != int(seconds) or not _MIN_TOKEN_TTL_SECONDS <= seconds <= _MAX_TOKEN_TTL_SECONDS:
        raise ValueError(
            f'realtime "{name}": token ttl {seconds}s is outside {_MIN_TOKEN_TTL_SECONDS}s to '
            f"{_MAX_TOKEN_TTL_SECONDS}s in whole seconds: a token lives long enough to open a "
            "socket and no longer"
        )
    resource = Realtime(name, authorize, int(seconds), find_caller_source())
    resource._declare()
    return resource
