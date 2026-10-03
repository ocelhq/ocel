import asyncio
import io
import json
import os
import re

import pytest
from fakerealtime import FIXTURE, FakeRealtimeRuntime, read_claims
from pydantic import BaseModel

from ocel import RealtimePublishError, UnprovisionedResourceError, realtime
from ocel.gen.app.resources.v1.resources_pb import (
    RealtimePublish,
    RealtimeSubscribe,
    ResourceType,
)


class OrderEvent(BaseModel):
    status: str


class ChatMessage(BaseModel):
    text: str


class Blob(BaseModel):
    data: str


OWNED = {"o-1": "u1"}


def authorize_by_header(request):
    user = request.headers.get("x-user")
    return {"id": user} if user else None


class Rules:
    def __init__(self):
        self.subscribes = []
        self.publishes = []
        self.fail_next_subscribe = False


def declare_app(rules: Rules):
    resource = realtime("app", authorize=authorize_by_header, token_ttl=30)

    @resource.channel("orders/:order_id", schema=OrderEvent)
    def orders(ctx):
        rules.subscribes.append(ctx)
        if rules.fail_next_subscribe:
            rules.fail_next_subscribe = False
            raise RuntimeError("db down")
        return OWNED.get(ctx.params["order_id"]) == ctx.auth["id"]

    @resource.channel("projects/:project_id/deploys/:deploy_id", schema=OrderEvent, wildcard=True)
    async def deploys(ctx):
        return True

    def may_post(ctx):
        rules.publishes.append(ctx)
        return ctx.auth["id"] == "u1"

    @resource.channel("rooms/:room_id", schema=ChatMessage, publish=may_post)
    def rooms(ctx):
        return True

    resource.channel("status", schema=OrderEvent, subscribe="public")
    return resource


def call_asgi(
    app, method="POST", body=b"", headers=None, path="/api/realtime", host="shop.example"
):
    headers = {"content-type": "application/json", "host": host, **(headers or {})}
    scope = {
        "type": "http",
        "method": method,
        "scheme": "https",
        "path": path,
        "root_path": "",
        "query_string": b"",
        "headers": [(k.lower().encode(), v.encode()) for k, v in headers.items()],
    }
    sent = []
    messages = [{"type": "http.request", "body": body, "more_body": False}]

    async def receive():
        return messages.pop(0) if messages else {"type": "http.disconnect"}

    async def send(message):
        sent.append(message)

    asyncio.run(app(scope, receive, send))
    start = sent[0]
    response_headers = {k.decode(): v.decode() for k, v in start["headers"]}
    content = b"".join(m.get("body", b"") for m in sent[1:])
    return start["status"], response_headers, content


def post(app, batch, headers=None, **kwargs):
    raw = batch if isinstance(batch, bytes) else json.dumps(batch).encode()
    return call_asgi(app, body=raw, headers=headers, **kwargs)


def answer(app, batch, headers=None):
    status, _, content = post(app, batch, headers)
    assert status == 200, content
    return json.loads(content)


@pytest.fixture
def appsync(monkeypatch):
    monkeypatch.setenv("OCEL_RESOURCE_REALTIME_app", json.dumps(FIXTURE))


@pytest.fixture
def runtime(monkeypatch):
    fake = FakeRealtimeRuntime()
    monkeypatch.setenv("OCEL_RESOURCE_REALTIME_app", fake.binding())
    monkeypatch.setenv("OCEL_RUNTIME_ADDRESS", fake.url)
    monkeypatch.setenv("OCEL_SESSION_TOKEN", "letmein")
    yield fake
    fake.close()


def test_a_realtime_declares_each_channels_pattern_wildcard_schema_and_access(collector):
    declare_app(Rules())

    _, _, declared = collector.declares[-1]
    assert declared.resource.type is ResourceType.REALTIME
    assert declared.resource.name == "app"
    config = declared.config.value
    assert config.token_ttl.to_seconds() == 30
    assert [(c.pattern, c.wildcard, c.subscribe, c.publish) for c in config.channels] == [
        ("orders/:order_id", False, RealtimeSubscribe.RULE, RealtimePublish.SERVER),
        (
            "projects/:project_id/deploys/:deploy_id",
            True,
            RealtimeSubscribe.RULE,
            RealtimePublish.SERVER,
        ),
        ("rooms/:room_id", False, RealtimeSubscribe.RULE, RealtimePublish.RULE),
        ("status", False, RealtimeSubscribe.PUBLIC, RealtimePublish.SERVER),
    ]
    assert json.loads(config.channels[0].schema)["required"] == ["status"]
    for channel in config.channels:
        assert os.path.basename(channel.source.rpartition(":")[0]) == "test_realtime.py"
    assert len(collector.declares) == 5


def test_a_realtime_without_a_token_ttl_declares_60_seconds(collector):
    realtime("plain")

    assert collector.declares[0][2].config.value.token_ttl.to_seconds() == 60


@pytest.mark.parametrize(
    ("declare", "reason"),
    [
        (lambda: realtime("short", token_ttl=5), "outside 10s to 300s"),
        (lambda: realtime("my_app"), "channel namespace"),
        (lambda: realtime("app").channel("a/:x/:x", subscribe="public"), "twice"),
        (lambda: realtime("app").channel("a", subscribe="private"), '"public"'),
    ],
)
def test_a_realtime_declaration_outside_the_contract_is_refused_saying_why(declare, reason):
    with pytest.raises(ValueError, match=reason):
        declare()


def test_the_handler_names_the_transport_and_mints_a_connect_token_when_asked(appsync):
    res = answer(declare_app(Rules()).asgi(), {"connect": True, "ops": []})

    assert res["transport"] == "appsync-events"
    assert res["url"] == FIXTURE["realtime"]["url"]
    assert res["host"] == FIXTURE["realtime"]["host"]
    claims = read_claims(res["connect"]["token"])
    assert claims["iss"] == "ocel:rt:app"
    assert claims["aud"] == FIXTURE["realtime"]["host"]
    assert claims["sub"] == "anonymous"
    assert claims["ocel"] == {"op": "connect", "ch": "/app", "ns": "app"}
    assert claims["exp"] == res["connect"]["expiresAt"]
    assert claims["exp"] - claims["iat"] == 30


def test_the_handler_answers_a_socket_path_on_the_origin_the_request_reached_it_at(
    monkeypatch,
):
    realtime_properties = {
        **FIXTURE["realtime"],
        "transport": "REALTIME_TRANSPORT_OCEL_GATEWAY",
        "url": "/.well-known/ocel-realtime",
        "host": "gateway:8080",
    }
    monkeypatch.setenv(
        "OCEL_RESOURCE_REALTIME_app",
        json.dumps({"name": "realtime--app", "realtime": realtime_properties}),
    )
    app = declare_app(Rules()).asgi()

    reached = answer(app, {"ops": []})
    forwarded = answer(
        app, {"ops": []}, {"x-forwarded-proto": "http", "x-forwarded-host": "web.localhost"}
    )

    assert reached["url"] == "wss://shop.example/.well-known/ocel-realtime"
    assert forwarded["url"] == "ws://web.localhost/.well-known/ocel-realtime"


def test_the_handler_grants_a_public_subscribe_to_anyone(appsync):
    res = answer(
        declare_app(Rules()).asgi(),
        {"ops": [{"op": "subscribe", "pattern": "status", "params": {}}]},
    )

    assert "connect" not in res
    assert res["denied"] == []
    assert [(g["i"], g["wire"]) for g in res["grants"]] == [(0, "/app/status")]
    claims = read_claims(res["grants"][0]["token"])
    assert claims["sub"] == "anonymous"
    assert claims["ocel"] == {"op": "subscribe", "ch": "/app/status", "ns": "app"}


def test_the_handler_runs_the_subscribe_rule_with_the_auth_params_and_request(appsync):
    rules = Rules()
    res = answer(
        declare_app(rules).asgi(),
        {
            "connect": True,
            "ops": [
                {"op": "subscribe", "pattern": "orders/:order_id", "params": {"order_id": "o-1"}},
                {"op": "subscribe", "pattern": "orders/:order_id", "params": {"order_id": "o-2"}},
            ],
        },
        {"x-user": "u1"},
    )

    assert [(g["i"], g["wire"]) for g in res["grants"]] == [(0, "/app/orders/o-1")]
    assert res["denied"] == [{"i": 1, "code": "forbidden"}]
    assert read_claims(res["connect"]["token"])["sub"] == "u1"
    assert read_claims(res["grants"][0]["token"])["sub"] == "u1"
    ctx = rules.subscribes[0]
    assert ctx.auth == {"id": "u1"}
    assert ctx.params == {"order_id": "o-1"}
    assert ctx.request.headers["x-user"] == "u1"
    assert ctx.request.url == "https://shop.example/api/realtime"


def test_the_handler_denies_a_ruled_op_to_nobody_without_running_the_rule(appsync):
    rules = Rules()
    res = answer(
        declare_app(rules).asgi(),
        {
            "ops": [
                {"op": "subscribe", "pattern": "orders/:order_id", "params": {"order_id": "o-1"}}
            ]
        },
    )

    assert res["denied"] == [{"i": 0, "code": "unauthenticated"}]
    assert rules.subscribes == []


def test_the_handler_grants_a_wildcard_subscribe_as_the_prefix_and_star(appsync):
    res = answer(
        declare_app(Rules()).asgi(),
        {
            "ops": [
                {
                    "op": "subscribe",
                    "pattern": "projects/:project_id/deploys/:deploy_id",
                    "params": {"project_id": "p_1"},
                }
            ]
        },
        {"x-user": "u1"},
    )

    assert [g["wire"] for g in res["grants"]] == ["/app/projects/0zobptc/deploys/*"]


def test_the_handler_denies_each_op_it_cannot_serve_with_its_code(appsync):
    res = answer(
        declare_app(Rules()).asgi(),
        {
            "ops": [
                {"op": "subscribe", "pattern": "nope", "params": {}},
                {"op": "subscribe", "pattern": "orders/:order_id", "params": {}},
                {"op": "subscribe", "pattern": "orders/:order_id", "params": {"order_id": 7}},
                {
                    "op": "subscribe",
                    "pattern": "orders/:order_id",
                    "params": {"order_id": "x" * 31},
                },
                {"op": "unsubscribe", "pattern": "status", "params": {}},
                {"op": "publish", "pattern": "status", "params": {}, "body": {"status": "up"}},
            ]
        },
        {"x-user": "u1"},
    )

    assert res["grants"] == []
    assert res["denied"] == [
        {"i": 0, "code": "unknown-pattern"},
        {"i": 1, "code": "missing-param"},
        {"i": 2, "code": "invalid-params"},
        {"i": 3, "code": "value-too-long"},
        {"i": 4, "code": "unknown-op"},
        {"i": 5, "code": "no-publish-rule"},
    ]


def test_the_handler_denies_an_op_whose_rule_raises_and_serves_the_rest(appsync):
    rules = Rules()
    rules.fail_next_subscribe = True
    res = answer(
        declare_app(rules).asgi(),
        {
            "ops": [
                {"op": "subscribe", "pattern": "orders/:order_id", "params": {"order_id": "o-1"}},
                {"op": "subscribe", "pattern": "status", "params": {}},
            ]
        },
        {"x-user": "u1"},
    )

    assert res["denied"] == [{"i": 0, "code": "rule-error"}]
    assert [g["i"] for g in res["grants"]] == [1]


def test_the_handler_answers_uncacheable_and_sets_no_cookie(appsync):
    _, headers, _ = post(declare_app(Rules()).asgi(), {"connect": True, "ops": []})

    assert headers["cache-control"] == "no-store"
    assert "set-cookie" not in headers


def test_the_handler_refuses_what_is_no_batch_of_json(appsync):
    app = declare_app(Rules()).asgi()

    status, headers, _ = call_asgi(app, method="GET")
    assert (status, headers["allow"], headers["cache-control"]) == (405, "POST", "no-store")
    assert call_asgi(app, body=b"{}", headers={"content-type": "text/plain"})[0] == 415
    for body in [b"{", b'{"ops":"x"}', b"{}"]:
        assert post(app, body)[0] == 400
    ops = [{"op": "subscribe", "pattern": "status"}] * 51
    assert post(app, {"ops": ops})[0] == 400


def test_the_handler_serves_its_own_origin_and_refuses_another_without_cors(appsync):
    app = declare_app(Rules()).asgi()

    same = post(app, {"ops": []}, {"origin": "https://shop.example"})
    other = post(app, {"ops": []}, {"origin": "https://evil.example"})
    proxied = post(
        app,
        {"ops": []},
        {"origin": "https://shop.example", "x-forwarded-host": "shop.example"},
        host="10.0.0.5:3000",
    )

    assert same[0] == 200 and "access-control-allow-origin" not in same[1]
    assert other[0] == 403 and "access-control-allow-origin" not in other[1]
    assert proxied[0] == 200


def test_the_handler_serves_an_allowed_origin_with_cors_and_answers_its_preflight(appsync):
    resource = declare_app(Rules())
    cors = resource.asgi(allowed_origins=["https://app.example"])

    allowed = post(cors, {"ops": []}, {"origin": "https://app.example"})
    preflight = call_asgi(cors, method="OPTIONS", headers={"origin": "https://app.example"})
    refused = post(cors, {"ops": []}, {"origin": "https://evil.example"})
    plain = call_asgi(resource.asgi(), method="OPTIONS", headers={"origin": "https://app.example"})

    assert allowed[0] == 200
    assert allowed[1]["access-control-allow-origin"] == "https://app.example"
    assert "Origin" in allowed[1]["vary"]
    assert preflight[0] == 204
    assert preflight[1]["access-control-allow-methods"] == "POST"
    assert preflight[1]["access-control-allow-headers"] == "authorization, content-type"
    assert refused[0] == 403
    assert plain[0] == 405 and "access-control-allow-origin" not in plain[1]


def test_the_handler_answers_500_without_the_cause_when_authorize_raises(appsync):
    def failing(request):
        raise RuntimeError("secret detail")

    status, _, content = post(realtime("app", authorize=failing).asgi(), {"ops": []})

    assert status == 500
    assert b"secret detail" not in content


def test_the_wsgi_handler_serves_the_same_batch(appsync):
    app = declare_app(Rules()).wsgi()
    body = json.dumps(
        {"ops": [{"op": "subscribe", "pattern": "orders/:order_id", "params": {"order_id": "o-1"}}]}
    ).encode()
    started = []
    environ = {
        "REQUEST_METHOD": "POST",
        "wsgi.url_scheme": "https",
        "HTTP_HOST": "shop.example",
        "PATH_INFO": "/api/realtime",
        "SCRIPT_NAME": "",
        "QUERY_STRING": "",
        "CONTENT_TYPE": "application/json",
        "CONTENT_LENGTH": str(len(body)),
        "HTTP_X_USER": "u1",
        "wsgi.input": io.BytesIO(body),
    }

    content = b"".join(app(environ, lambda status, headers: started.append((status, headers))))

    assert started[0][0] == "200 OK"
    assert ("Cache-Control", "no-store") in started[0][1]
    res = json.loads(content)
    assert read_claims(res["grants"][0]["token"])["sub"] == "u1"


def test_a_relayed_publish_runs_the_publish_rule_then_publishes_from_the_server(runtime):
    rules = Rules()
    res = answer(
        declare_app(rules).asgi(),
        {
            "ops": [
                {
                    "op": "publish",
                    "pattern": "rooms/:room_id",
                    "params": {"room_id": "r1"},
                    "body": {"text": "hi"},
                }
            ]
        },
        {"x-user": "u1"},
    )

    assert res["transport"] == "ocel-gateway"
    assert "host" not in res
    assert res["grants"] == [{"i": 0, "wire": "/app/rooms/r1"}]
    ctx = rules.publishes[0]
    assert (ctx.auth, ctx.params, ctx.body) == (
        {"id": "u1"},
        {"room_id": "r1"},
        ChatMessage(text="hi"),
    )
    [event] = runtime.published
    assert event["envelope"]["ch"] == "/app/rooms/r1"
    assert event["envelope"]["kind"] == "live"
    assert event["envelope"]["data"] == {"text": "hi"}


def test_a_relayed_publish_is_denied_when_its_rule_or_its_body_refuses_it(runtime):
    app = declare_app(Rules()).asgi()

    def publish(text, user):
        return answer(
            app,
            {
                "ops": [
                    {
                        "op": "publish",
                        "pattern": "rooms/:room_id",
                        "params": {"room_id": "r1"},
                        "body": {"text": text},
                    }
                ]
            },
            {"x-user": user},
        )

    assert publish(1, "u1")["denied"] == [{"i": 0, "code": "invalid-body"}]
    assert publish("x", "u2")["denied"] == [{"i": 0, "code": "forbidden"}]
    assert runtime.published == []


def test_a_relayed_publish_the_runtime_refuses_is_denied(runtime):
    runtime.refusal = "the gateway refused it with status 401"
    res = answer(
        declare_app(Rules()).asgi(),
        {
            "ops": [
                {
                    "op": "publish",
                    "pattern": "rooms/:room_id",
                    "params": {"room_id": "r1"},
                    "body": {"text": "hi"},
                }
            ]
        },
        {"x-user": "u1"},
    )

    assert res["denied"] == [{"i": 0, "code": "publish-failed"}]


def test_publish_hands_the_runtime_the_envelope_on_its_wire_channel(runtime):
    orders = realtime("app").channel("orders/:order_id", schema=OrderEvent, subscribe="public")

    orders.publish(OrderEvent(status="shipped"), order_id="o_1")
    asyncio.run(orders.publish_async({"status": "paid"}, order_id="o_2"))

    first, second = runtime.published
    assert (first["realtime"], first["channel"]) == ("app", "/app/orders/0zn5ptc")
    envelope = first["envelope"]
    assert (envelope["v"], envelope["ch"], envelope["kind"]) == (1, "/app/orders/0zn5ptc", "live")
    assert envelope["data"] == {"status": "shipped"} and envelope["id"] and envelope["ts"]
    assert second["envelope"]["data"] == {"status": "paid"}
    assert runtime.authorizations == ["Bearer letmein", "Bearer letmein"]


def test_publish_refuses_params_or_an_event_it_cannot_send(runtime):
    resource = realtime("app")
    orders = resource.channel("orders/:order_id", schema=OrderEvent, subscribe="public")
    blobs = resource.channel("blobs", schema=Blob, subscribe="public")

    for publish, code in [
        (lambda: orders.publish({"status": "x"}), "missing-param"),
        (lambda: orders.publish({"status": 1}, order_id="o1"), "invalid-body"),
        (lambda: blobs.publish({"data": "x" * 240 * 1024}), "body-too-large"),
    ]:
        with pytest.raises(RealtimePublishError) as refused:
            publish()
        assert refused.value.code == code
    assert runtime.published == []


def test_publish_fails_when_the_runtime_refuses_it(runtime):
    runtime.refusal = "the gateway refused it with status 401"
    orders = realtime("app").channel("orders/:order_id", schema=OrderEvent, subscribe="public")

    with pytest.raises(RuntimeError, match="status 401"):
        orders.publish({"status": "paid"}, order_id="o1")


def test_publish_outside_a_provisioned_run_says_why(collector, monkeypatch):
    status = realtime("app").channel("status", schema=OrderEvent, subscribe="public")
    with pytest.raises(UnprovisionedResourceError):
        status.publish({"status": "up"})

    monkeypatch.setenv("OCEL_PHASE", "")
    with pytest.raises(RuntimeError, match="OCEL_RESOURCE_REALTIME_app"):
        status.publish({"status": "up"})


@pytest.mark.parametrize(
    "declare",
    [
        lambda: realtime("app").channel("orders/:order_id"),
        lambda: realtime("app").channel("rooms", subscribe="public", publish=lambda ctx: True),
    ],
    ids=["subscribe rule", "publish rule"],
)
def test_a_channel_with_a_rule_on_a_realtime_without_authorize_is_refused_when_declared(declare):
    with pytest.raises(ValueError, match="authorize"):
        declare()


def with_binding(monkeypatch, **realtime_fields):
    binding = {**FIXTURE, "realtime": {**FIXTURE["realtime"], **realtime_fields}}
    monkeypatch.setenv("OCEL_RESOURCE_REALTIME_app", json.dumps(binding))


def assert_server_failure(response):
    status, headers, content = response
    assert status == 500
    assert headers["cache-control"] == "no-store"
    assert "error" in json.loads(content)


def test_the_handler_answers_500_when_a_token_cannot_be_minted(monkeypatch):
    with_binding(monkeypatch, signingKey="AAAAAAA=")
    app = declare_app(Rules()).asgi()

    assert_server_failure(post(app, {"connect": True, "ops": []}))
    assert_server_failure(post(app, {"ops": [{"op": "subscribe", "pattern": "status"}]}))


def test_the_handler_answers_500_when_the_resource_has_no_binding():
    assert_server_failure(post(declare_app(Rules()).asgi(), {"ops": []}))


def test_the_handler_answers_500_for_a_transport_it_does_not_speak(monkeypatch):
    with_binding(monkeypatch, transport="REALTIME_TRANSPORT_UNSPECIFIED")

    assert_server_failure(post(declare_app(Rules()).asgi(), {"ops": []}))


def test_the_handler_answers_500_with_cors_to_an_allowed_origin():
    app = declare_app(Rules()).asgi(allowed_origins=["https://app.example"])

    status, headers, _ = post(app, {"ops": []}, {"origin": "https://app.example"})

    assert status == 500
    assert headers["access-control-allow-origin"] == "https://app.example"


@pytest.mark.parametrize(
    "body",
    [
        {"ops": [], "extra": 1},
        {"Ops": []},
        {"ops": [], "connect": None},
        {"ops": [], "connect": "yes"},
        [],
    ],
)
def test_the_handler_refuses_a_batch_outside_its_exact_shape(appsync, body):
    status, headers, _ = post(declare_app(Rules()).asgi(), body)

    assert (status, headers["cache-control"]) == (400, "no-store")


def test_the_handler_denies_each_malformed_op_with_the_first_code_that_applies(appsync):
    status = {"op": "subscribe", "pattern": "status"}
    room = {"op": "publish", "pattern": "rooms/:room_id"}
    res = answer(
        declare_app(Rules()).asgi(),
        {
            "ops": [
                "subscribe",
                {**status, "extra": 1},
                {**status, "OP": "subscribe"},
                {**status, "body": {}},
                {**status, "op": 7},
                {"pattern": "status"},
                {**status, "pattern": 7},
                {**status, "params": []},
                {**status, "params": ""},
                {**room, "params": {}, "body": {"text": 1}},
                {**status, "params": None},
                {**status, "op": "Subscribe"},
            ]
        },
        {"x-user": "u1"},
    )

    assert res["denied"] == [
        {"i": 0, "code": "invalid-op"},
        {"i": 1, "code": "invalid-op"},
        {"i": 2, "code": "invalid-op"},
        {"i": 3, "code": "invalid-op"},
        {"i": 4, "code": "unknown-op"},
        {"i": 5, "code": "unknown-op"},
        {"i": 6, "code": "unknown-pattern"},
        {"i": 7, "code": "invalid-params"},
        {"i": 8, "code": "invalid-params"},
        {"i": 9, "code": "missing-param"},
        {"i": 11, "code": "unknown-op"},
    ]
    assert [g["i"] for g in res["grants"]] == [10]


@pytest.mark.parametrize(
    ("origin", "headers", "status"),
    [
        ("http://shop.example", {}, 403),
        ("null", {}, 403),
        ("https://", {}, 403),
        ("shop.example", {}, 403),
        ("http://shop.example", {"x-forwarded-proto": "http, https"}, 200),
        ("https://shop.example", {"x-forwarded-proto": "HTTP"}, 403),
    ],
)
def test_the_handler_serves_an_origin_only_when_its_scheme_and_host_are_its_own(
    appsync, origin, headers, status
):
    response = post(declare_app(Rules()).asgi(), {"ops": []}, {"origin": origin, **headers})

    assert response[0] == status


def call_wsgi(app, body, headers=None, scheme="https"):
    started = []
    environ = {
        "REQUEST_METHOD": "POST",
        "wsgi.url_scheme": scheme,
        "HTTP_HOST": "shop.example",
        "PATH_INFO": "/api/realtime",
        "SCRIPT_NAME": "",
        "QUERY_STRING": "",
        "CONTENT_TYPE": "application/json",
        "CONTENT_LENGTH": str(len(body)),
        "wsgi.input": io.BytesIO(body),
    }
    for name, value in (headers or {}).items():
        environ["HTTP_" + name.upper().replace("-", "_")] = value
    content = b"".join(app(environ, lambda status, headers: started.append((status, headers))))
    return started[0][0], dict(started[0][1]), content


def test_the_wsgi_handler_compares_the_origin_with_its_own_scheme(appsync):
    app = declare_app(Rules()).wsgi()
    body = json.dumps({"ops": []}).encode()

    assert call_wsgi(app, body, {"origin": "https://shop.example"}, "http")[0] == "403 Forbidden"
    assert call_wsgi(app, body, {"origin": "http://shop.example"}, "http")[0] == "200 OK"


def test_the_wsgi_handler_answers_500_when_the_resource_has_no_binding():
    status, headers, _ = call_wsgi(declare_app(Rules()).wsgi(), json.dumps({"ops": []}).encode())

    assert (status, headers["Cache-Control"]) == ("500 Internal Server Error", "no-store")


def test_publish_takes_a_param_named_body(runtime):
    posts = realtime("app").channel("posts/:body", schema=OrderEvent, subscribe="public")

    posts.publish({"status": "up"}, body="b1")
    asyncio.run(posts.publish_async({"status": "down"}, body="b2"))

    assert [event["envelope"]["ch"] for event in runtime.published] == [
        "/app/posts/b1",
        "/app/posts/b2",
    ]


def test_publish_stamps_each_envelope_with_32_lowercase_hex_characters(runtime):
    status = realtime("app").channel("status", schema=OrderEvent, subscribe="public")

    status.publish({"status": "up"})

    assert re.fullmatch(r"[0-9a-f]{32}", runtime.published[0]["envelope"]["id"])


def test_relayed_publishes_reach_the_runtime_in_batch_order(runtime):
    async def may_post_slowly_first(ctx):
        await asyncio.sleep(0.3 if ctx.body.text == "first" else 0)
        return True

    resource = realtime("app", authorize=authorize_by_header)
    resource.channel(
        "rooms/:room_id", schema=ChatMessage, subscribe="public", publish=may_post_slowly_first
    )
    room = {"op": "publish", "pattern": "rooms/:room_id", "params": {"room_id": "r1"}}

    res = answer(
        resource.asgi(),
        {"ops": [{**room, "body": {"text": text}} for text in ["first", "second", "third"]]},
        {"x-user": "u1"},
    )

    assert [g["i"] for g in res["grants"]] == [0, 1, 2]
    assert [event["envelope"]["data"]["text"] for event in runtime.published] == [
        "first",
        "second",
        "third",
    ]
