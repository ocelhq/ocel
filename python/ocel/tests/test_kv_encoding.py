import json
import os
from itertools import count

import fakeredis
import pytest
import redis
from pydantic import BaseModel

from ocel import kv

_FIXTURE = os.path.join(
    os.path.dirname(__file__),
    "..",
    "..",
    "..",
    "proto",
    "app",
    "resources",
    "v1",
    "fixtures",
    "kv.json",
)

with open(_FIXTURE) as _file:
    fixture = json.load(_file)

_ports = count(9000)


class Session(BaseModel):
    user: str
    roles: list[str]
    visits: int
    note: str


@pytest.fixture
def server(monkeypatch):
    monkeypatch.setattr(redis, "Redis", fakeredis.FakeRedis)
    port = next(_ports)
    monkeypatch.setenv(
        "OCEL_RESOURCE_KV_encoding",
        json.dumps({"name": "kv--encoding", "kv": {"host": "127.0.0.1", "port": port}}),
    )
    return fakeredis.FakeRedis(host="127.0.0.1", port=port, decode_responses=True)


@pytest.mark.parametrize("case", fixture["keys"], ids=[case["pattern"] for case in fixture["keys"]])
def test_the_fixture_keys_are_built_as_every_sdk_builds_them(server, case):
    entry = kv("encoding").text("entry", case["pattern"])

    entry.set("written", **case["params"])
    assert server.keys("*") == [case["key"]]

    server.set(case["key"], "seeded")
    assert entry.get(**case["params"]) == "seeded"


def test_the_fixture_values_are_stored_as_every_sdk_stores_them(server):
    cache = kv("encoding")
    text = cache.text("text", "text")
    counter = cache.counter("counter", "counter")
    sessions = cache.json("json", "json", model=Session)
    values = cache.list("list", "list")
    members = cache.set("set", "set")

    for case in fixture["values"]["text"]:
        text.set(case["value"])
        assert server.get("text") == case["stored"]
        server.set("text", case["stored"])
        assert text.get() == case["value"]
    for case in fixture["values"]["counter"]:
        counter.set(case["value"])
        assert server.get("counter") == case["stored"]
        server.set("counter", case["stored"])
        assert counter.get() == case["value"]
    for case in fixture["values"]["json"]:
        sessions.set(Session(**case["value"]))
        assert server.get("json") == case["stored"]
        server.set("json", case["stored"])
        assert sessions.get() == Session(**case["value"])
    for case in fixture["values"]["list"]:
        values.append(*case["value"])
        assert server.lrange("list", 0, -1) == case["stored"]
        server.delete("list")
        server.rpush("list", *case["stored"])
        assert values.slice() == case["value"]
    for case in fixture["values"]["set"]:
        members.add(*case["value"])
        assert sorted(server.smembers("set")) == case["stored"]
        server.delete("set")
        server.sadd("set", *case["stored"])
        assert members.members() == set(case["value"])
