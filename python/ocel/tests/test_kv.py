import json
import os
import ssl
import sys
from itertools import count

import fakeredis
import pytest
import redis
import redis.asyncio
from pydantic import BaseModel

from ocel import InvalidKVValueError, UnprovisionedResourceError, kv
from ocel.gen.app.resources.v1.resources_pb import KvShape, ResourceType

_ports = count(7000)


class Session(BaseModel):
    user: str
    visits: int


@pytest.fixture
def valkey(monkeypatch):
    monkeypatch.setattr(redis, "Redis", fakeredis.FakeRedis)
    monkeypatch.setattr(redis.asyncio, "Redis", fakeredis.FakeAsyncRedis)

    def deliver(name, **properties):
        port = next(_ports)
        binding = {"host": "127.0.0.1", "port": port, "password": "pw", **properties}
        monkeypatch.setenv(
            f"OCEL_RESOURCE_KV_{name}", json.dumps({"name": f"kv--{name}", "kv": binding})
        )
        return fakeredis.FakeRedis(host="127.0.0.1", port=port, decode_responses=True)

    return deliver


def _line_of(source: str) -> int:
    file, _, line = source.rpartition(":")
    assert os.path.basename(file) == "test_kv.py"
    return int(line)


def test_a_store_declares_itself_and_every_entry_with_the_line_each_was_declared_on(collector):
    cache = kv("cache", version="8", eviction="allkeys-lru", memory="256mb")
    line = sys._getframe().f_lineno
    cache.text("greeting", "greeting")
    cache.counter("requests", "requests/:user_id", ttl="10s")
    cache.json("session", "session/:id", model=Session, ttl="30d")
    cache.list("recent", "recent/:user_id")
    cache.set("online", "online/:room")

    declared = [body for _, _, body in collector.declares]
    assert len(declared) == 6
    last = declared[-1]
    assert last.resource.type is ResourceType.KV
    assert last.resource.name == "cache"
    assert {_line_of(body.source) for body in declared} == {line - 1}
    config = last.config.value
    assert (config.version, config.eviction, config.memory) == ("8", "allkeys-lru", "256mb")
    assert [(e.name, e.pattern, e.shape, _line_of(e.source)) for e in config.entries] == [
        ("greeting", "greeting", KvShape.TEXT, line + 1),
        ("requests", "requests/:user_id", KvShape.COUNTER, line + 2),
        ("session", "session/:id", KvShape.JSON, line + 3),
        ("recent", "recent/:user_id", KvShape.LIST, line + 4),
        ("online", "online/:room", KvShape.SET, line + 5),
    ]


def test_a_store_leaves_version_eviction_and_memory_to_the_provider(collector):
    kv("plain")

    config = collector.declares[0][2].config.value
    assert (config.version, config.eviction, config.memory, list(config.entries)) == (
        "",
        "",
        "",
        [],
    )


def test_two_overlapping_entries_are_refused_naming_both_lines(collector):
    cache = kv("overlap")
    line = sys._getframe().f_lineno
    cache.text("session", "session/:id")
    with pytest.raises(ValueError) as raised:
        cache.text("current", "session/current")

    message = str(raised.value)
    for said in ['"current"', '"session/current"', "overlaps", '"session/:id"', '"session"']:
        assert said in message
    assert f"test_kv.py:{line + 1}" in message
    assert f"test_kv.py:{line + 3}" in message


@pytest.mark.parametrize(
    ("name", "pattern", "says"),
    [
        ("session", "session/{id}", "hash tag"),
        ("pair", "a/:id/:id", "twice"),
        ("trailing", "a/", "empty segment"),
        ("client", "clients/:id", "reserved"),
        ("expiring", "expiring/:ttl", "ttl"),
        ("newline", "session\n", "no literal"),
        ("newline", "session/:id\n", "no parameter"),
    ],
)
def test_a_malformed_entry_is_refused_saying_what_is_wrong(name, pattern, says):
    with pytest.raises(ValueError, match=says):
        kv("malformed").text(name, pattern)


@pytest.mark.parametrize(
    "name", ["", "_session", "1session", "user-sessions", "sessión", "session\n", "a" * 64]
)
def test_an_entry_name_no_sdk_can_hold_is_refused(name):
    with pytest.raises(ValueError, match="is no name every SDK can hold"):
        kv("names").text(name, "session")


def test_an_entry_name_of_63_letters_digits_and_underscores_is_held():
    kv("long").text("S" + "a_9" * 20 + "zz", "session")


def test_an_entry_name_declared_twice_is_refused():
    cache = kv("twice")
    cache.text("session", "session/:id")

    with pytest.raises(ValueError, match='"session" is declared already'):
        cache.text("session", "sessions/:id")


@pytest.mark.parametrize("ttl", [10, "10", "10s\n", "١٠s"])
def test_a_ttl_is_a_duration_written_with_its_unit(ttl):
    with pytest.raises(ValueError, match="duration"):
        kv("ttl").text("a", "a", ttl=ttl)


def test_a_json_entry_takes_a_model():
    with pytest.raises(TypeError):
        kv("json").json("session", "session/:id")


def test_every_accessor_is_refused_during_discovery(collector):
    cache = kv("cache")
    requests = cache.counter("requests", "requests/:user_id")

    for access in (cache.client, cache.sync_client, lambda: cache.connection_string):
        with pytest.raises(UnprovisionedResourceError):
            access()
    with pytest.raises(UnprovisionedResourceError):
        requests.increment(user_id="u")


def test_one_client_of_each_kind_is_opened_per_store_and_shared(valkey):
    valkey("conn")
    cache = kv("conn")

    assert cache.client() is cache.client()
    assert cache.sync_client() is cache.sync_client()
    cache.sync_client().set("hello", "world")
    assert cache.sync_client().get("hello") == b"world"


def test_the_connection_string_names_the_scheme_the_bindings_tls_asks(monkeypatch):
    monkeypatch.setenv(
        "OCEL_RESOURCE_KV_tls",
        '{"name":"kv--tls","kv":{"host":"cache.internal","port":6380,"username":"app","password":"p@ss/word","tls":true}}',
    )
    monkeypatch.setenv(
        "OCEL_RESOURCE_KV_plain",
        '{"name":"kv--plain","kv":{"host":"127.0.0.1","port":6379,"password":"pw"}}',
    )

    assert kv("tls").connection_string == "rediss://app:p%40ss%2Fword@cache.internal:6380"
    assert kv("plain").connection_string == "redis://:pw@127.0.0.1:6379"


def test_a_tls_store_is_reached_by_clients_that_check_its_hostname(monkeypatch):
    monkeypatch.setenv(
        "OCEL_RESOURCE_KV_secure",
        '{"name":"kv--secure","kv":{"host":"cache.internal","port":6380,"password":"pw","tls":true}}',
    )
    cache = kv("secure")

    for client in (cache.sync_client(), cache.client()):
        options = client.connection_pool.connection_kwargs
        assert options["ssl_check_hostname"] is True
        assert options["ssl_cert_reqs"] == "required"


@pytest.mark.parametrize(
    "ca_pem",
    [
        "not a certificate",
        "-----BEGIN CERTIFICATE-----\nfixture\n-----END CERTIFICATE-----\n",
        "-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----\n",
    ],
)
def test_a_client_is_refused_when_the_delivered_authority_holds_no_certificate(monkeypatch, ca_pem):
    monkeypatch.setenv(
        "OCEL_RESOURCE_KV_garbled",
        json.dumps(
            {
                "name": "kv--garbled",
                "kv": {
                    "host": "10.240.0.5",
                    "port": 6378,
                    "password": "pw",
                    "tls": True,
                    "caPem": ca_pem,
                },
            }
        ),
    )
    cache = kv("garbled")

    for open_client in (cache.sync_client, cache.client):
        with pytest.raises(
            ValueError,
            match="OCEL_RESOURCE_KV_garbled delivers a caPem .* holds no PEM certificate",
        ):
            open_client()


def test_the_kv_binding_fixture_decodes_as_the_other_sdks_decode_it(monkeypatch):
    fixtures = os.path.join(
        os.path.dirname(__file__), "..", "..", "..", "proto", "common", "bindings", "v1", "fixtures"
    )
    with open(os.path.join(fixtures, "kv.json")) as delivered:
        body = delivered.read()
    monkeypatch.setenv("OCEL_RESOURCE_KV_cache", body)
    cache = kv("cache")

    assert cache.connection_string == (
        "rediss://fixture_operator:fixture-password-not-a-secret@"
        "shop-prod-cache-h4j5k6l7.ab12cd.ng.0001.use1.cache.amazonaws.com:6380"
    )
    authority = cache.sync_client().connection_pool.connection_kwargs["ssl_authority"]
    assert authority.get_ca_certs(binary_form=True) == [
        ssl.PEM_cert_to_DER_cert(json.loads(body)["kv"]["caPem"])
    ]


def test_a_text_entry_reads_what_was_written_and_misses_as_none(valkey):
    server = valkey("text")
    cache = kv("text")
    greeting = cache.text("greeting", "greeting")
    notes = cache.text("notes", "notes/:id")

    assert greeting.get() is None
    greeting.set("hello")
    notes.set("first", id="a")
    assert greeting.get() == "hello"
    assert server.get("greeting") == "hello"
    assert notes.get_many([{"id": "a"}, {"id": "b"}]) == ["first", None]
    assert notes.delete(id="a") is True
    assert notes.delete(id="a") is False


@pytest.mark.asyncio
async def test_every_operation_has_an_async_twin(valkey):
    valkey("aio")
    cache = kv("aio")
    notes = cache.text("notes", "notes/:id")
    hits = cache.counter("hits", "hits/:id")
    recent = cache.list("recent", "recent/:id")
    online = cache.set("online", "online/:id")

    await notes.set_async("x", id="a")
    assert await notes.get_async(id="a") == "x"
    assert await notes.get_many_async([{"id": "a"}, {"id": "z"}]) == ["x", None]
    assert await hits.increment_async(2, id="a") == 2
    assert await hits.decrement_async(id="a") == 1
    assert await recent.append_async("a", "b", id="a") == 2
    assert await recent.slice_async(id="a") == ["a", "b"]
    assert await online.add_async("ada", id="a") == 1
    assert await online.members_async(id="a") == {"ada"}
    assert await notes.delete_async(id="a") is True


def test_a_counter_counts_atomically_from_zero(valkey):
    valkey("counter")
    hits = kv("counter").counter("hits", "hits/:page")

    assert hits.get(page="home") is None
    assert hits.increment(page="home") == 1
    assert hits.increment(5, page="home") == 6
    assert hits.decrement(2, page="home") == 4
    assert hits.decrement(page="other") == -1
    hits.set(10, page="set")
    assert hits.get_many([{"page": "home"}, {"page": "set"}, {"page": "none"}]) == [4, 10, None]


def test_a_key_parameter_may_share_a_name_with_an_operations_own_argument(valkey):
    server = valkey("by")
    hits = kv("by").counter("hits", "hits/:by")

    assert hits.increment(3, by="ada") == 3
    assert server.get("hits/ada") == "3"


def test_a_counter_holding_no_integer_is_invalid(valkey):
    server = valkey("badcounter")
    hits = kv("badcounter").counter("hits", "hits")
    server.set("hits", "lots")

    with pytest.raises(InvalidKVValueError):
        hits.get()


def test_a_json_entry_checks_its_model_on_write_and_on_read(valkey):
    server = valkey("json")
    cache = kv("json")
    sessions = cache.json("session", "session/:id", model=Session)
    lenient = cache.json("lenient", "lenient/:id", model=Session, on_invalid="miss")

    sessions.set(Session(user="ada", visits=2), id="s1")
    assert sessions.get(id="s1") == Session(user="ada", visits=2)
    assert sessions.get(id="none") is None
    with pytest.raises(InvalidKVValueError):
        sessions.set({"user": "ada", "visits": "many"}, id="s2")
    assert server.exists("session/s2") == 0

    for key, stored in {"s3": '{"user":7}', "s4": "not json"}.items():
        server.set(f"session/{key}", stored)
        server.set(f"lenient/{key}", stored)
        with pytest.raises(InvalidKVValueError):
            sessions.get(id=key)
        assert lenient.get(id=key) is None
    assert lenient.get_many([{"id": "s3"}, {"id": "s4"}]) == [None, None]


def test_a_list_keeps_its_values_in_order(valkey):
    valkey("list")
    recent = kv("list").list("recent", "recent/:user")

    assert recent.append("b", "c", user="ada") == 2
    assert recent.appendleft("z", "a", user="ada") == 4
    assert recent.slice(user="ada") == ["z", "a", "b", "c"]
    assert recent.slice(1, 3, user="ada") == ["a", "b"]
    assert recent.slice(-2, user="ada") == ["b", "c"]
    assert recent.slice(0, -1, user="ada") == ["z", "a", "b"]
    assert recent.slice(0, 0, user="ada") == []
    assert recent.get(-1, user="ada") == "c"
    assert recent.get(9, user="ada") is None
    assert recent.pop(user="ada") == "c"
    assert recent.popleft(user="ada") == "z"
    assert recent.length(user="ada") == 2
    assert recent.delete(user="ada") is True
    assert recent.pop(user="ada") is None


def test_a_set_keeps_distinct_members(valkey):
    valkey("set")
    online = kv("set").set("online", "online/:room")

    assert online.add("ada", "bob", room="lobby") == 2
    assert online.add("ada", room="lobby") == 0
    assert online.contains("ada", room="lobby") is True
    assert online.size(room="lobby") == 2
    assert online.members(room="lobby") == {"ada", "bob"}
    assert online.discard("ada", room="lobby") == 1
    assert online.delete(room="lobby") is True


def test_a_declared_ttl_is_applied_with_every_write(valkey):
    server = valkey("ttl")
    cache = kv("ttl")
    cache.text("session", "session/:id", ttl="30d").set("x", id="a")
    cache.counter("hits", "hits/:id", ttl="10s").increment(id="a")
    cache.list("recent", "recent/:id", ttl="1h").append("x", id="a")
    cache.set("online", "online/:id", ttl="5m").add("x", id="a")

    assert server.ttl("session/a") == 30 * 86400
    assert server.ttl("hits/a") == 10
    assert server.ttl("recent/a") == 3600
    assert server.ttl("online/a") == 300


def test_a_write_overrides_keeps_or_clears_the_ttl(valkey):
    server = valkey("ttlwrite")
    cache = kv("ttlwrite")
    sessions = cache.text("session", "session/:id", ttl="30d")
    hits = cache.counter("hits", "hits/:id", ttl="10s")
    forever = cache.text("forever", "forever/:id")

    sessions.set("x", id="o", ttl="5s")
    assert server.ttl("session/o") == 5
    sessions.set("y", id="o", ttl="keep")
    assert server.ttl("session/o") == 5
    sessions.set("z", id="o", ttl=None)
    assert server.ttl("session/o") == -1

    hits.increment(id="o", ttl="1m")
    hits.increment(id="o", ttl="keep")
    assert server.ttl("hits/o") == 60
    hits.decrement(id="o", ttl=None)
    assert server.ttl("hits/o") == -1

    server.set("forever/a", "x", ex=1)
    forever.set("y", id="a")
    assert server.ttl("forever/a") == -1
