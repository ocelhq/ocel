import json
import os
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

from ocel import UnprovisionedResourceError, postgres


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
                length = int(self.headers["Content-Length"])
                declares.append((self.path, json.loads(self.rfile.read(length))))
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(b"{}")

            def log_message(self, *_args):
                pass

        return Handler

    def close(self):
        self.server.shutdown()
        self.server.server_close()


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
    path, body = collector.declares[0]
    assert path == "/app.resources.v1.ResourceService/Declare"
    assert body["resource"] == {"type": "LINK_TYPE_POSTGRES", "name": "main"}
    assert body["postgres"] == {"version": "17"}
    file, _, line = body["source"].rpartition(":")
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

    assert collector.declares[0][1]["postgres"] == {"version": "16"}


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
        json.dumps({"name": "main", "bucket": {"name": "b"}}),
    )

    with pytest.raises(RuntimeError) as raised:
        _ = postgres("main").connection_string
    assert str(raised.value) == (
        "OCEL_RESOURCE_POSTGRES_main carries a BUCKET link, and this app reads it as a POSTGRES"
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
