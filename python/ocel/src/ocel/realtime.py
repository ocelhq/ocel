import asyncio
import inspect
import json
import secrets
import time
from collections.abc import Awaitable, Callable, Iterable, Mapping
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from typing import Any, Literal, get_args, overload
from urllib.parse import urlsplit, urlunsplit

from protobuf import Oneof
from protobuf.wkt import Duration

from ocel._aws_signature import find_appsync_region, read_aws_credentials, sign_appsync_publish
from ocel._binding import read_realtime_binding, refuse_unprovisioned
from ocel._declare import declare, find_caller_source, is_discovering
from ocel._payload import Codec
from ocel._realtime_token import TokenOperation, mint_token
from ocel._realtime_wire import (
    CHANNEL_SEGMENT,
    SEGMENT_RULE,
    ChannelPattern,
    WireRefusal,
    encode_wire_channel,
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
from ocel.gen.common.bindings.v1.bindings_pb import RealtimeProperties, RealtimeTransport

_MIN_TOKEN_TTL_SECONDS = 10
_MAX_TOKEN_TTL_SECONDS = 300
_MAX_OPS = 50
_MAX_REQUEST_BYTES = 1 << 20
_MAX_EVENT_BYTES = 240 * 1024
_PUBLISH_TIMEOUT_SECONDS = 10.0
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
class RuleContext:
    """What a rule decides from."""

    #: What ``authorize`` returned for the request; never ``None`` in a rule.
    auth: Any
    #: The params the caller asked for. On a ``wildcard`` channel, trailing params a
    #: subscriber left off are missing.
    params: dict[str, str]
    #: The request the realtime handler received.
    request: RealtimeRequest
    #: In a publish rule, the event, validated against the channel's schema.
    body: Any = None


class RealtimePublishError(ValueError):
    """Raised by ``publish`` when the params or the event cannot be published."""

    #: The reason, one of the codes the realtime handler denies an op with:
    #: ``invalid-params``, ``missing-param``, ``unknown-param``, ``empty-value``,
    #: ``value-too-long``, ``invalid-body`` or ``body-too-large``.
    code: _PublishRefusal

    def __init__(self, pattern: str, code: _PublishRefusal):
        super().__init__(f'realtime channel "{pattern}" refuses the publish: {code}')
        self.code = code


Rule = Callable[[RuleContext], bool | Awaitable[bool]]


def _raise_unless_taken(
    resource: str, transport: str, channel: str, status: int, answer: bytes
) -> None:
    if status // 100 != 2:
        raise RuntimeError(
            f'realtime "{resource}": {transport} refused a publish on {channel} '
            f"with status {status}: {answer.decode(errors='replace')}"
        )


@dataclass(frozen=True)
class _OcelGateway:
    resource: str
    publish_url: str
    mint_publish_token: Callable[[str], str]
    name: Literal["ocel-gateway"] = "ocel-gateway"
    answered_host: None = None

    def read_credentials_and_sign(
        self, channel: str, envelope: bytes
    ) -> tuple[str, dict[str, str], bytes]:
        token = self.mint_publish_token(channel)
        headers = {"Authorization": f"Bearer {token}", "Content-Type": "application/json"}
        return self.publish_url, headers, envelope

    def raise_unless_taken(self, channel: str, status: int, answer: bytes) -> None:
        _raise_unless_taken(self.resource, self.name, channel, status, answer)


@dataclass(frozen=True)
class _AppSyncEvents:
    resource: str
    answered_host: str
    name: Literal["appsync-events"] = "appsync-events"

    def read_credentials_and_sign(
        self, channel: str, envelope: bytes
    ) -> tuple[str, dict[str, str], bytes]:
        body = json.dumps(
            {"channel": channel, "events": [envelope.decode()]},
            separators=(",", ":"),
            ensure_ascii=False,
        ).encode()
        headers = sign_appsync_publish(
            self.answered_host,
            body,
            read_aws_credentials(),
            find_appsync_region(self.answered_host),
            datetime.now(timezone.utc),
        )
        return _build_appsync_publish_url(self.answered_host), headers, body

    def raise_unless_taken(self, channel: str, status: int, answer: bytes) -> None:
        _raise_unless_taken(self.resource, self.name, channel, status, answer)
        if _has_failed_events(answer):
            raise RuntimeError(
                f'realtime "{self.resource}": AppSync failed the event published on {channel}: '
                f"{answer.decode(errors='replace')}"
            )


_Transport = _OcelGateway | _AppSyncEvents


def _build_appsync_publish_url(host: str) -> str:
    return f"https://{host}/event"


def _build_gateway_publish_url(socket_url: str) -> str:
    parts = urlsplit(socket_url)
    return urlunsplit(
        ("https" if parts.scheme == "wss" else "http", parts.netloc, "/publish", "", "")
    )


def _has_failed_events(answer: bytes) -> bool:
    try:
        failed = json.loads(answer).get("failed")
    except (ValueError, AttributeError):
        return False
    return isinstance(failed, list) and len(failed) > 0


async def _await_if_awaitable(value: Any) -> Any:
    return await value if inspect.isawaitable(value) else value


def _read_subject(auth: Any) -> str:
    if auth is None:
        return "anonymous"
    identity = auth.get("id") if isinstance(auth, Mapping) else getattr(auth, "id", None)
    if isinstance(identity, str) or (isinstance(identity, int) and not isinstance(identity, bool)):
        return str(identity)
    return "anonymous"


def _deny(code: _DenialCode) -> dict[str, Any]:
    return {"code": code}


class Channel:
    """One channel pattern of a realtime resource, and the handle its events are published
    through."""

    #: The pattern the channel was declared under.
    pattern: str

    def __init__(
        self,
        resource: "Realtime",
        pattern: ChannelPattern,
        codec: Codec,
        wildcard: bool,
        subscribe: Rule | None,
        publish: Rule | None,
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

    def _encode_event(self, params: Mapping[str, Any], body: Any) -> tuple[str, Any, bytes]:
        if not all(isinstance(value, str) for value in params.values()):
            raise RealtimePublishError(self.pattern, "invalid-params")
        channel, refused = encode_wire_channel(self._resource.name, self._parsed, params, False)
        if refused is not None:
            raise RealtimePublishError(self.pattern, refused)
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

    def publish(self, body: Any, /, **params: str) -> None:
        """Publish ``body`` to every subscriber of the channel ``params`` fill in the
        pattern, after validating it against the channel's schema. Every param is required.
        Raises :class:`RealtimePublishError` for params or a body that cannot be published,
        and ``TimeoutError`` when the transport does not take the event within 10 seconds."""
        properties = self._resource._read_properties("publish")
        channel, _, envelope = self._encode_event(params, body)
        self._resource._publish_sync(properties, channel, envelope)

    async def publish_async(self, body: Any, /, **params: str) -> None:
        """Publish ``body`` as :meth:`publish` does, without blocking the event loop."""
        properties = self._resource._read_properties("publish_async")
        channel, _, envelope = self._encode_event(params, body)
        await self._resource._publish_async(properties, channel, envelope)


@dataclass
class _Response:
    status: int
    body: Any = None
    headers: dict[str, str] = field(default_factory=dict)

    def encode(self) -> tuple[int, list[tuple[str, str]], bytes]:
        headers = {"Cache-Control": "no-store", **self.headers}
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


def _parse_batch(body: bytes) -> tuple[bool, list[Any]] | None:
    try:
        batch = json.loads(body)
    except (ValueError, RecursionError):
        return None
    if not isinstance(batch, dict) or not batch.keys() <= _BATCH_KEYS:
        return None
    ops = batch.get("ops")
    connect = batch.get("connect", False)
    if not isinstance(ops, list) or not isinstance(connect, bool):
        return None
    return connect, ops


def _read_first_header_value(request: RealtimeRequest, name: str) -> str:
    return request.headers.get(name, "").split(",")[0].strip()


def _is_own_origin(request: RealtimeRequest, origin: str) -> bool:
    try:
        parts = urlsplit(origin)
    except ValueError:
        return False
    if parts.netloc == "":
        return False
    own_url = urlsplit(request.url)
    host = (
        _read_first_header_value(request, "x-forwarded-host")
        or request.headers.get("host")
        or own_url.netloc
    )
    scheme = _read_first_header_value(request, "x-forwarded-proto").lower() or own_url.scheme
    return parts.scheme == scheme and parts.netloc == host


class Realtime:
    """A realtime resource an app declares: typed channels that browsers subscribe to
    through its :meth:`asgi` or :meth:`wsgi` handler, and the server publishes on."""

    #: The name the resource was declared under, which begins every channel.
    name: str

    def __init__(
        self,
        name: str,
        authorize: Callable[[RealtimeRequest], Any] | None,
        ttl_seconds: int,
        source: str,
    ):
        """Take the handle for the realtime resource named ``name``. Prefer :func:`realtime`,
        which declares the resource as well as handing back its handle."""
        self.name = name
        self._authorize = authorize
        self._ttl_seconds = ttl_seconds
        self._source = source
        self._channels: dict[str, Channel] = {}

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
        schema: Any = None,
        wildcard: bool = False,
        subscribe: Literal["public"],
        publish: Rule | None = None,
    ) -> Channel: ...
    @overload
    def channel(
        self,
        pattern: str,
        *,
        schema: Any = None,
        wildcard: bool = False,
        subscribe: None = None,
        publish: Rule | None = None,
    ) -> Callable[[Rule], Channel]: ...
    def channel(
        self,
        pattern: str,
        *,
        schema: Any = None,
        wildcard: bool = False,
        subscribe: Literal["public"] | None = None,
        publish: Rule | None = None,
    ) -> Channel | Callable[[Rule], Channel]:
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

        def attach(rule: Rule | None) -> Channel:
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

    def _mint_token(
        self,
        properties: RealtimeProperties,
        subject: str,
        operation: TokenOperation,
        channel: str,
    ) -> dict[str, Any]:
        return mint_token(
            properties.signing_key,
            namespace=self.name,
            audience=properties.host,
            subject=subject,
            operation=operation,
            channel=channel,
            ttl_seconds=self._ttl_seconds,
        )

    def _resolve_transport(self, properties: RealtimeProperties) -> _Transport:
        if properties.transport == RealtimeTransport.APPSYNC_EVENTS:
            return _AppSyncEvents(self.name, properties.host)
        if properties.transport == RealtimeTransport.OCEL_GATEWAY:
            return _OcelGateway(
                self.name,
                _build_gateway_publish_url(properties.url),
                lambda channel: self._mint_token(properties, "server", "publish", channel)["token"],
            )
        raise RuntimeError(
            f'realtime "{self.name}": the binding names transport {properties.transport}, '
            "which this SDK does not speak"
        )

    def _publish_sync(self, properties: RealtimeProperties, channel: str, envelope: bytes) -> None:
        from pyqwest import SyncClient

        transport = self._resolve_transport(properties)
        url, headers, body = transport.read_credentials_and_sign(channel, envelope)
        response = SyncClient().post(url, headers, body, timeout=_PUBLISH_TIMEOUT_SECONDS)
        transport.raise_unless_taken(channel, response.status, response.content)

    async def _publish_async(
        self, properties: RealtimeProperties, channel: str, envelope: bytes
    ) -> None:
        await self._post_event(self._resolve_transport(properties), channel, envelope)

    async def _post_event(self, transport: _Transport, channel: str, envelope: bytes) -> None:
        from pyqwest import Client

        url, headers, body = await asyncio.to_thread(
            transport.read_credentials_and_sign, channel, envelope
        )
        response = await asyncio.wait_for(
            Client().post(url, headers, body), _PUBLISH_TIMEOUT_SECONDS
        )
        transport.raise_unless_taken(channel, response.status, response.content)

    async def _serve_op(
        self,
        properties: RealtimeProperties,
        transport: _Transport,
        request: RealtimeRequest,
        auth: Any,
        op: Any,
    ) -> dict[str, Any]:
        if (
            not isinstance(op, dict)
            or not op.keys() <= _OP_KEYS
            or (op.get("op") == "subscribe" and "body" in op)
        ):
            return _deny("invalid-op")
        operation = op.get("op")
        if not isinstance(operation, str) or operation not in _OPERATIONS:
            return _deny("unknown-op")
        pattern = op.get("pattern")
        channel = self._channels.get(pattern) if isinstance(pattern, str) else None
        if channel is None:
            return _deny("unknown-pattern")
        if operation == "publish" and channel._publish is None:
            return _deny("no-publish-rule")
        params = op.get("params")
        if params is None:
            params = {}
        elif not isinstance(params, dict) or not all(
            isinstance(value, str) for value in params.values()
        ):
            return _deny("invalid-params")
        if operation == "subscribe":
            return await self._serve_subscribe(properties, request, auth, channel, params)
        return await self._serve_publish(
            properties, transport, request, auth, channel, params, op.get("body", _MISSING_BODY)
        )

    async def _serve_subscribe(
        self,
        properties: RealtimeProperties,
        request: RealtimeRequest,
        auth: Any,
        channel: Channel,
        params: dict[str, str],
    ) -> dict[str, Any]:
        wire, refused = encode_wire_channel(self.name, channel._parsed, params, channel._wildcard)
        if refused is not None:
            return _deny(refused)
        if channel._subscribe is not None:
            if auth is None:
                return _deny("unauthenticated")
            denied = await _run_rule(channel._subscribe, RuleContext(auth, dict(params), request))
            if denied is not None:
                return _deny(denied)
        token = self._mint_token(properties, _read_subject(auth), "subscribe", wire)["token"]
        return {"wire": wire, "token": token}

    async def _serve_publish(
        self,
        properties: RealtimeProperties,
        transport: _Transport,
        request: RealtimeRequest,
        auth: Any,
        channel: Channel,
        params: dict[str, str],
        body: Any,
    ) -> dict[str, Any]:
        try:
            wire, validated, envelope = channel._encode_event(params, body)
        except RealtimePublishError as refused:
            return _deny(refused.code)
        if auth is None:
            return _deny("unauthenticated")
        context = RuleContext(auth, dict(params), request, validated)
        denied = await _run_rule(channel._publish, context)
        if denied is not None:
            return _deny(denied)
        try:
            await self._post_event(transport, wire, envelope)
        except Exception:
            return _deny("publish-failed")
        return {"wire": wire}

    async def _answer_batch(
        self, request: RealtimeRequest, connect: bool, ops: list[Any]
    ) -> dict[str, Any]:
        properties = self._read_properties("handler")
        transport = self._resolve_transport(properties)
        auth = None
        if self._authorize is not None:
            auth = await _await_if_awaitable(self._authorize(request))

        async def serve(i: int, op: Any) -> tuple[int, dict[str, Any]]:
            return i, await self._serve_op(properties, transport, request, auth, op)

        publishes = [
            (i, op)
            for i, op in enumerate(ops)
            if isinstance(op, dict) and op.get("op") == "publish"
        ]
        published = {i for i, _ in publishes}

        async def serve_publishes_in_order() -> list[tuple[int, dict[str, Any]]]:
            return [await serve(i, op) for i, op in publishes]

        async with asyncio.TaskGroup() as group:
            others = [
                group.create_task(serve(i, op)) for i, op in enumerate(ops) if i not in published
            ]
            chain = group.create_task(serve_publishes_in_order())
        outcomes = sorted(
            [*(task.result() for task in others), *chain.result()], key=lambda pair: pair[0]
        )

        answer: dict[str, Any] = {"transport": transport.name, "url": properties.url}
        if transport.answered_host is not None:
            answer["host"] = transport.answered_host
        if connect:
            answer["connect"] = self._mint_token(
                properties, _read_subject(auth), "connect", f"/{self.name}"
            )
        answer["grants"] = [{"i": i, **outcome} for i, outcome in outcomes if "wire" in outcome]
        answer["denied"] = [{"i": i, **outcome} for i, outcome in outcomes if "code" in outcome]
        return answer

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
            return _build_error_response(500, "the realtime handler failed", cors)

    def asgi(self, *, allowed_origins: Iterable[str] = ()) -> Callable[..., Awaitable[None]]:
        """An ASGI application serving the resource to browsers, mounted at any path: one
        batched POST of ``{ connect?, ops: [{ op, pattern, params?, body? }] }``, at most 50
        ops, answered with the transport, its url, a connect token when asked, a grant with a
        token for each op its rule allows and a denial with a code for each it does not. It
        takes POST with application/json alone, serves its own origin and those in
        ``allowed_origins`` (the only ones it sends CORS headers to), answers
        ``Cache-Control: no-store``, and never sets a cookie."""
        origins = tuple(allowed_origins)

        async def app(scope, receive, send):
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
                body += message.get("body", b"")
                more = message.get("more_body", False)
                if len(body) > _MAX_REQUEST_BYTES:
                    break
            headers: dict[str, str] = {}
            for name, value in scope["headers"]:
                headers.setdefault(name.decode("latin-1").lower(), value.decode("latin-1"))
            host = headers.get("host", "")
            path = scope.get("root_path", "") + scope["path"]
            query = scope.get("query_string", b"").decode("latin-1")
            url = urlunsplit((scope.get("scheme", "http"), host, path, query, ""))
            request = RealtimeRequest(scope["method"], url, headers, body)
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

    def wsgi(self, *, allowed_origins: Iterable[str] = ()) -> Callable[..., Iterable[bytes]]:
        """A WSGI application serving the resource as :meth:`asgi` does."""
        origins = tuple(allowed_origins)

        def app(environ, start_response):
            headers = {
                key[5:].replace("_", "-").lower(): value
                for key, value in environ.items()
                if key.startswith("HTTP_")
            }
            if environ.get("CONTENT_TYPE"):
                headers["content-type"] = environ["CONTENT_TYPE"]
            try:
                length = int(environ.get("CONTENT_LENGTH") or 0)
            except ValueError:
                length = 0
            body = (
                environ["wsgi.input"].read(min(length, _MAX_REQUEST_BYTES + 1)) if length else b""
            )
            host = headers.get("host") or environ.get("SERVER_NAME", "")
            path = environ.get("SCRIPT_NAME", "") + environ.get("PATH_INFO", "")
            url = urlunsplit(
                (
                    environ.get("wsgi.url_scheme", "http"),
                    host,
                    path,
                    environ.get("QUERY_STRING", ""),
                    "",
                )
            )
            request = RealtimeRequest(environ["REQUEST_METHOD"], url, headers, body)
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


async def _run_rule(rule: Rule, context: RuleContext) -> Literal["forbidden", "rule-error"] | None:
    try:
        allowed = await _await_if_awaitable(rule(context))
    except Exception:
        return "rule-error"
    return None if allowed else "forbidden"


def realtime(
    name: str,
    *,
    authorize: Callable[[RealtimeRequest], Any] | None = None,
    token_ttl: int | timedelta = 60,
) -> Realtime:
    """Declare a realtime resource named ``name`` and return the handle its channels are
    declared, served and published through. ``authorize`` identifies the caller of the
    handler from its :class:`RealtimeRequest`, with whatever auth the app already uses, sync
    or async, and returns ``None`` for nobody; its result is ``ctx.auth`` in every rule, and
    its ``id`` key or attribute, when a string or integer, is the ``sub`` of every token
    minted for the caller. A resource with a subscribe or publish rule needs ``authorize``.
    ``token_ttl`` is how long each token lives, 10 to 300 seconds.

    Minting tokens needs the ``ocel[realtime]`` extra."""
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
