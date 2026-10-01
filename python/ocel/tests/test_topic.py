import json
import os
from datetime import datetime, timedelta, timezone

import fakeruntime
import pytest
from pydantic import BaseModel

from ocel import Lane, Retry, UnprovisionedResourceError, topic, worker
from ocel.gen.app.resources.v1.resources_pb import ResourceType
from ocel.gen.app.topic.v1.topic_pb import Lane as WireLane


class Order(BaseModel):
    id: int


def test_a_declared_topic_reaches_the_dev_server_with_its_options(collector):
    topic("orders", schema=Order, ordered=True, retry=Retry(max_attempts=4))

    _, _, declared = collector.declares[0]
    assert declared.resource.type is ResourceType.TOPIC
    assert declared.resource.name == "orders"
    assert declared.config.field == "topic"
    config = declared.config.value
    assert json.loads(config.schema)["required"] == ["id"]
    assert config.ordered is True
    assert config.retry.max_attempts == 4
    file, _, line = declared.source.rpartition(":")
    assert os.path.basename(file) == "test_topic.py"
    assert int(line) > 0


def test_a_topic_declared_without_options_leaves_them_unset(collector):
    topic("orders")

    config = collector.declares[0][2].config.value
    assert config.schema == ""
    assert config.ordered is False
    assert config.retry is None


def test_a_consumer_declares_the_topic_it_reads_and_its_options(collector):
    orders = topic("orders")
    media = worker("media")

    @orders.consumer(
        "ship",
        retry=Retry(max_attempts=2),
        concurrency=5,
        lanes=[Lane.HIGH, "low"],
        max_duration=timedelta(seconds=90),
        worker=media,
    )
    def ship(order, ctx):
        return None

    _, _, declared = collector.declares[2]
    assert declared.resource.type is ResourceType.CONSUMER
    assert declared.resource.name == "ship"
    assert declared.config.field == "consumer"
    config = declared.config.value
    assert config.topic == "orders"
    assert config.worker == "media"
    assert config.retry.max_attempts == 2
    assert config.concurrency == 5
    assert list(config.lanes) == [WireLane.HIGH, WireLane.LOW]
    assert config.max_duration.to_seconds() == 90
    assert config.batch is None
    assert os.path.basename(declared.source.rpartition(":")[0]) == "test_topic.py"


def test_a_batch_consumer_declares_its_batch_policy(collector):
    orders = topic("orders")

    @orders.batch_consumer("index", batch_size=50, batch_timeout=2)
    def index(orders, ctx):
        return None

    config = collector.declares[1][2].config.value
    assert config.batch.size == 50
    assert config.batch.timeout.to_seconds() == 2
    assert config.worker == ""


def test_a_batch_consumer_without_a_batch_size_is_refused():
    with pytest.raises(TypeError):
        topic("orders").batch_consumer("index")


def test_a_consumer_given_a_worker_by_name_instead_of_its_handle_is_refused(collector):
    orders = topic("orders")

    with pytest.raises(TypeError, match="media"):

        @orders.consumer("ship", worker="media")
        def ship(order, ctx):
            return None

    assert len(collector.declares) == 1


def test_a_decorated_consumer_still_runs_as_the_function_it_decorated():
    orders = topic("orders")

    @orders.consumer("ship")
    def ship(order, ctx=None):
        return order["id"]

    assert ship({"id": 3}) == 3
    assert ship.name == "ship"
    assert ship.topic is orders


@pytest.fixture
def runtime(monkeypatch):
    fake = fakeruntime.Runtime()
    monkeypatch.setenv(
        "OCEL_RESOURCE_TOPIC_orders",
        json.dumps({"name": "orders", "topic": {}}),
    )
    monkeypatch.setenv("OCEL_RUNTIME_ADDRESS", fake.url)
    monkeypatch.setenv("OCEL_SESSION_TOKEN", fakeruntime.TOKEN)
    yield fake
    fake.close()


def test_a_send_publishes_the_payload_as_json_to_the_topic_by_its_declared_name(runtime):
    message_id = topic("orders", schema=Order).send(Order(id=7))

    assert message_id == "01HZY3V0J9Q8C7B6A5Z4Y3X2W1"
    [request] = runtime.requests("Send")
    assert request.topic == "orders"
    assert json.loads(request.payload) == {"id": 7}
    assert request.due_at is None
    assert request.lane is WireLane.UNSPECIFIED
    assert runtime.authorizations == ["Bearer letmein"]


def test_a_send_carries_every_option_it_was_given(runtime):
    at = datetime(2030, 1, 2, tzinfo=timezone.utc)

    topic("orders").send({"id": 7}, delay=at, idempotency_key="o-7", key="user-1", lane="high")

    [request] = runtime.requests("Send")
    assert request.due_at.to_datetime() == at
    assert request.idempotency_key == "o-7"
    assert request.key == "user-1"
    assert request.lane is WireLane.HIGH


def test_a_send_over_256_kib_is_refused_before_it_is_sent(runtime):
    with pytest.raises(ValueError):
        topic("orders").send("x" * 262144)

    assert runtime.calls == []


@pytest.mark.asyncio
async def test_an_async_app_sends_without_blocking_its_loop(runtime):
    message_id = await topic("orders").send_async({"id": 1}, delay=10)

    assert message_id == "01HZY3V0J9Q8C7B6A5Z4Y3X2W1"
    assert runtime.requests("Send")[0].due_at is not None


def test_a_dead_letter_listing_reads_one_page_of_a_consumer(runtime):
    orders = topic("orders")

    @orders.consumer("ship")
    def ship(order, ctx):
        return None

    page = orders.dead_letter(ship).list(cursor="c1", limit=10)

    [request] = runtime.requests("ListDeadLetters")
    assert (request.topic, request.consumer, request.cursor, request.limit) == (
        "orders",
        "ship",
        "c1",
        10,
    )
    assert page.next_cursor == "next"
    [letter] = page.dead_letters
    assert letter.execution == "01HZY3V0J9Q8C7B6A5Z4Y3X2W1-ship"
    assert letter.message_id == "01HZY3V0J9Q8C7B6A5Z4Y3X2W1"
    assert letter.payload == {"order": 7}
    assert letter.attempts == 3
    assert letter.error == "carrier down"
    assert letter.failed_at == datetime(2023, 11, 14, 22, 13, 20, tzinfo=timezone.utc)


def test_dead_letters_are_redriven_purged_and_counted_by_consumer_name(runtime):
    letters = topic("orders").dead_letter("ship")

    assert letters.redrive() == 5
    assert letters.redrive(["a", "b"]) == 2
    assert letters.purge() == 4
    assert letters.count() == 3

    assert list(runtime.requests("RedriveDeadLetters")[1].executions) == ["a", "b"]
    assert {r.consumer for _, r in runtime.calls} == {"ship"}


@pytest.mark.asyncio
async def test_an_async_app_reaches_dead_letters_without_blocking_its_loop(runtime):
    letters = topic("orders").dead_letter("ship")

    assert len((await letters.list_async()).dead_letters) == 1
    assert await letters.redrive_async() == 5
    assert await letters.purge_async(["x"]) == 1
    assert await letters.count_async() == 3


def test_a_topic_used_during_discovery_says_it_is_not_provisioned_yet(collector):
    orders = topic("orders")

    with pytest.raises(UnprovisionedResourceError) as raised:
        orders.send({"id": 1})
    assert str(raised.value) == (
        "'topic(\"orders\")' cannot be used during discovery: "
        "tried to access 'send' before the resource was provisioned"
    )
    with pytest.raises(UnprovisionedResourceError):
        orders.dead_letter("ship").count()
