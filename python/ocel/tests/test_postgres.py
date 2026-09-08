import gzip
import json
import os
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

from ocel import UnprovisionedResourceError, postgres
from ocel.gen.app.resources.v1.resources_pb import DeclareRequest
from ocel.gen.common.links.v1.links_pb import LinkType


class Collector:
    def __init__(self):
        self.declares = []
        self.server = HTTPServer(("127.0.0.1", 0), self._handler())
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    @property
    def url(self):
        host, port = self.server.server_address
        return f"http://{host}:{port}"

    def _handler(self):
        declares = self.declares

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = self.rfile.read(int(self.headers["Content-Length"]))
                if self.headers.get("Content-Encoding") == "gzip":
                    body = gzip.decompress(body)
                kind = self.headers.get("Content-Type", "")
                declares.append(
                    (self.path, self.headers.get("Connect-Protocol-Version"), decode(kind, body))
                )
                self.send_response(200)
                self.send_header("Content-Type", kind)
                self.end_headers()
                self.wfile.write(b"{}" if kind.endswith("json") else b"")

            def log_message(self, *_args):
                pass

        return Handler

    def close(self):
        self.server.shutdown()
        self.server.server_close()


def decode(content_type: str, body: bytes) -> DeclareRequest:
    if content_type.endswith("json"):
        return DeclareRequest.from_json(body)
    return DeclareRequest.from_binary(body)


@pytest.fixture
def collector(monkeypatch):
    c = Collector()
    monkeypatch.setenv("OCEL_PHASE", "discovery")
    monkeypatch.setenv("OCEL_DEV_SERVER", c.url)
    yield c
    c.close()


def test_a_declared_database_reaches_the_dev_server_with_the_file_that_declared_it(collector):
    postgres("main")

    assert len(collector.declares) == 1
    path, protocol, declared = collector.declares[0]
    assert path == "/app.resources.v1.ResourceService/Declare"
    assert protocol == "1"
    assert declared.resource.type is LinkType.POSTGRES
    assert declared.resource.name == "main"
    assert declared.config.field == "postgres"
    assert declared.config.value.version == "17"
    file, _, line = declared.source.rpartition(":")
    assert os.path.basename(file) == "test_postgres.py"
    assert int(line) > 0


def test_a_database_reached_during_discovery_says_it_is_not_provisioned_yet(collector):
    db = postgres("main")

    assert db.name == "main"
    with pytest.raises(UnprovisionedResourceError) as raised:
        _ = db.connection_string
    assert str(raised.value) == (
        "'postgres(\"main\")' cannot be used during discovery: "
        "tried to access 'connection_string' before the resource was provisioned"
    )


def test_a_declared_version_replaces_the_one_ocel_picks(collector):
    postgres("main", version="16")

    assert collector.declares[0][2].config.value.version == "16"


def test_a_declaration_the_server_refuses_says_what_it_said(monkeypatch):
    monkeypatch.setenv("OCEL_PHASE", "discovery")
    monkeypatch.setenv("OCEL_DEV_SERVER", "http://127.0.0.1:1")

    with pytest.raises(RuntimeError) as raised:
        postgres("main")
    assert str(raised.value).startswith("ocel: declare postgres 'main': ")


def test_a_database_with_no_link_delivered_names_the_commands_that_deliver_one(monkeypatch):
    monkeypatch.delenv("OCEL_PHASE", raising=False)
    monkeypatch.delenv("OCEL_RESOURCE_POSTGRES_main", raising=False)

    with pytest.raises(RuntimeError) as raised:
        _ = postgres("main").connection_string
    assert str(raised.value) == (
        "Value for OCEL_RESOURCE_POSTGRES_main is not defined. "
        "Run `ocel dev` to resolve it locally, or `ocel deploy` to have it delivered "
        "from the resource this app links."
    )


def test_a_link_of_another_type_is_refused_for_the_type_it_carries(monkeypatch):
    monkeypatch.delenv("OCEL_PHASE", raising=False)
    monkeypatch.setenv(
        "OCEL_RESOURCE_POSTGRES_main",
        json.dumps({"name": "main", "bucket": {"bucket": "uploads"}}),
    )

    with pytest.raises(RuntimeError) as raised:
        _ = postgres("main").connection_string
    assert str(raised.value) == (
        "OCEL_RESOURCE_POSTGRES_main carries a BUCKET link, and this app reads it as a POSTGRES"
    )


def test_a_link_carrying_nothing_at_all_is_refused_for_the_type_it_carries(monkeypatch):
    monkeypatch.delenv("OCEL_PHASE", raising=False)
    monkeypatch.setenv("OCEL_RESOURCE_POSTGRES_main", json.dumps({"name": "main"}))

    with pytest.raises(RuntimeError) as raised:
        _ = postgres("main").connection_string
    assert str(raised.value) == (
        "OCEL_RESOURCE_POSTGRES_main carries a UNSPECIFIED link, "
        "and this app reads it as a POSTGRES"
    )


def test_a_value_that_is_not_a_link_record_is_reported_without_quoting_what_it_held(monkeypatch):
    monkeypatch.delenv("OCEL_PHASE", raising=False)
    monkeypatch.setenv("OCEL_RESOURCE_POSTGRES_main", "s3cret-not-json")

    with pytest.raises(RuntimeError) as raised:
        _ = postgres("main").connection_string
    assert "s3cret" not in str(raised.value)
    assert str(raised.value) == (
        "OCEL_RESOURCE_POSTGRES_main does not carry a link record, "
        "so this app cannot read it as a POSTGRES"
    )


def test_a_link_the_deploy_delivers_is_read_past_the_fields_this_app_uses(monkeypatch):
    monkeypatch.delenv("OCEL_PHASE", raising=False)
    fixtures = os.path.join(
        os.path.dirname(__file__), "..", "..", "..", "proto", "common", "links", "v1", "fixtures"
    )
    with open(os.path.join(fixtures, "postgres.json")) as delivered:
        monkeypatch.setenv("OCEL_RESOURCE_POSTGRES_main", delivered.read())

    assert postgres("main").connection_string == (
        "postgres://fixture_operator:fixture-password-not-a-secret"
        "@shop-prod-main-r1a2b3c4.cluster-cxyz.us-east-1.rds.amazonaws.com:5433/fixture_catalog"
    )


def test_the_connection_string_carries_credentials_no_url_could_hold_unescaped(monkeypatch):
    monkeypatch.delenv("OCEL_PHASE", raising=False)
    monkeypatch.setenv(
        "OCEL_RESOURCE_POSTGRES_main",
        json.dumps(
            {
                "name": "main",
                "postgres": {
                    "host": "db.internal",
                    "port": 5432,
                    "database": "app",
                    "username": "user name",
                    "password": "p@ss:word/with#odd?chars",
                },
            }
        ),
    )

    assert postgres("main").connection_string == (
        "postgres://user%20name:p%40ss%3Aword%2Fwith%23odd%3Fchars@db.internal:5432/app"
    )


@pytest.mark.asyncio
async def test_the_pool_is_opened_once_however_often_it_is_asked_for(monkeypatch):
    monkeypatch.delenv("OCEL_PHASE", raising=False)
    monkeypatch.setenv(
        "OCEL_RESOURCE_POSTGRES_main",
        json.dumps(
            {
                "name": "main",
                "postgres": {
                    "host": "db.internal",
                    "port": 5432,
                    "database": "app",
                    "username": "u",
                    "password": "p",
                },
            }
        ),
    )
    import asyncpg

    opened = []

    async def create_pool(dsn):
        opened.append(dsn)
        return object()

    monkeypatch.setattr(asyncpg, "create_pool", create_pool)

    db = postgres("main")
    assert await db.pool() is await db.pool()
    assert opened == ["postgres://u:p@db.internal:5432/app"]


@pytest.mark.asyncio
async def test_two_calls_racing_for_the_pool_open_one_between_them(monkeypatch):
    monkeypatch.delenv("OCEL_PHASE", raising=False)
    monkeypatch.setenv(
        "OCEL_RESOURCE_POSTGRES_main",
        json.dumps(
            {
                "name": "main",
                "postgres": {
                    "host": "db.internal",
                    "port": 5432,
                    "database": "app",
                    "username": "u",
                    "password": "p",
                },
            }
        ),
    )
    import asyncio

    import asyncpg

    opened = []

    async def create_pool(dsn):
        opened.append(dsn)
        await asyncio.sleep(0)
        return object()

    monkeypatch.setattr(asyncpg, "create_pool", create_pool)

    db = postgres("main")
    first, second = await asyncio.gather(db.pool(), db.pool())

    assert len(opened) == 1
    assert first is second


def test_an_unprovisioned_database_reprs_like_any_other_object(collector):
    db = postgres("main")

    assert repr(db)
    assert db.__class__ is not None
