import json
import os
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone

import fakeruntime
import pytest
from pydantic import BaseModel

import ocel
from ocel import (
    Batch,
    Debounce,
    Lane,
    Retry,
    RunContext,
    Trigger,
    UnprovisionedResourceError,
    task,
    worker,
)
from ocel.gen.app.resources.v1.resources_pb import ResourceType
from ocel.gen.app.topic.v1.topic_pb import Lane as WireLane


def test_a_declared_task_reaches_the_dev_server_with_the_file_that_declared_it(collector):
    @task("resize-image")
    def resize(payload, ctx):
        return None

    assert len(collector.declares) == 1
    _, _, declared = collector.declares[0]
    assert declared.resource.type is ResourceType.TASK
    assert declared.resource.name == "resize-image"
    assert declared.config.field == "task"
    config = declared.config.value
    assert config.schema == ""
    assert config.ordered is False
    assert config.retry is None
    assert config.concurrency == 0
    assert config.max_duration is None
    assert config.ttl is None
    assert config.batch is None
    assert config.worker == ""
    assert config.cron == ""
    file, _, line = declared.source.rpartition(":")
    assert os.path.basename(file) == "test_task.py"
    assert int(line) > 0


def test_a_task_declares_every_option_it_was_given(collector):
    media = worker("media")

    @task(
        "resize-image",
        retry=Retry(max_attempts=5, min_delay=2, max_delay=timedelta(minutes=1)),
        concurrency=10,
        max_duration=timedelta(minutes=5),
        ttl=3600,
        ordered=True,
        worker=media,
        cron="0 * * * *",
    )
    async def resize(payload, ctx):
        return None

    config = collector.declares[1][2].config.value
    assert config.ordered is True
    assert config.retry.max_attempts == 5
    assert config.retry.min_delay.to_seconds() == 2
    assert config.retry.max_delay.to_seconds() == 60
    assert config.concurrency == 10
    assert config.max_duration.to_seconds() == 300
    assert config.ttl.to_seconds() == 3600
    assert config.worker == "media"
    assert config.cron == "0 * * * *"


def test_a_batched_task_declares_its_batch_policy(collector):
    @task("index", batch=Batch(size=100, timeout=0.5))
    def index(payloads, ctx):
        return None

    batch = collector.declares[0][2].config.value.batch
    assert batch.size == 100
    assert batch.timeout.to_seconds() == 0.5


def test_a_task_given_a_worker_by_name_instead_of_its_handle_is_refused(collector):
    with pytest.raises(TypeError, match="media"):

        @task("resize-image", worker="media")
        def resize(payload, ctx):
            return None

    assert collector.declares == []


def test_a_batch_without_a_size_is_refused():
    with pytest.raises(TypeError):
        Batch()


class Image(BaseModel):
    url: str
    width: int


def test_a_task_whose_payload_is_a_model_declares_its_json_schema(collector):
    @task("resize-image")
    def resize(payload: Image, ctx: RunContext):
        return None

    schema = json.loads(collector.declares[0][2].config.value.schema)
    assert schema["properties"]["url"] == {"title": "Url", "type": "string"}
    assert schema["required"] == ["url", "width"]


@dataclass
class Thumbnail:
    url: str


def test_a_task_given_a_schema_declares_it_over_the_annotation(collector):
    @task("resize-image", schema=Thumbnail)
    def resize(payload: Image, ctx):
        return None

    schema = json.loads(collector.declares[0][2].config.value.schema)
    assert schema["required"] == ["url"]


def test_a_batched_task_declares_the_schema_of_one_payload(collector):
    @task("index", batch=Batch(size=10))
    def index(payloads: list[Image], ctx):
        return None

    schema = json.loads(collector.declares[0][2].config.value.schema)
    assert schema["required"] == ["url", "width"]


def test_a_task_whose_payload_is_plain_json_declares_no_schema(collector):
    @task("resize-image")
    def resize(payload: dict[str, int], ctx):
        return None

    assert collector.declares[0][2].config.value.schema == ""


def test_a_decorated_task_still_runs_as_the_function_it_decorated():
    @task("double")
    def double(payload: int, ctx=None):
        return payload * 2

    assert double(4) == 8
    assert double.name == "double"
    assert isinstance(double, ocel.Task)


def test_a_task_declaration_the_server_refuses_says_what_it_said(monkeypatch):
    monkeypatch.setenv("OCEL_PHASE", "discovery")
    monkeypatch.setenv("OCEL_DEV_SERVER", "http://127.0.0.1:1")

    with pytest.raises(RuntimeError) as raised:

        @task("resize-image")
        def resize(payload, ctx):
            return None

    assert str(raised.value).startswith("ocel: declare task 'resize-image': ")


@pytest.fixture
def runtime(monkeypatch):
    fake = fakeruntime.Runtime()
    monkeypatch.setenv(
        "OCEL_RESOURCE_TASK_resize-image",
        json.dumps({"name": "resize-image", "task": {}}),
    )
    monkeypatch.setenv("OCEL_RUNTIME_ADDRESS", fake.url)
    monkeypatch.setenv("OCEL_SESSION_TOKEN", fakeruntime.TOKEN)
    yield fake
    fake.close()


def _resize():
    @task("resize-image")
    def resize(payload: Image, ctx):
        return None

    return resize


def test_a_trigger_sends_the_payload_as_json_to_the_task_by_its_declared_name(runtime):
    run = _resize().trigger(Image(url="a.png", width=100))

    assert run.id == "run_1"
    [request] = runtime.requests("Trigger")
    assert request.task == "resize-image"
    assert json.loads(request.payload) == {"url": "a.png", "width": 100}
    assert request.options is None
    assert runtime.authorizations == ["Bearer letmein"]


def test_a_trigger_sends_every_option_it_was_given(runtime):
    before = datetime.now(timezone.utc)

    _resize().trigger(
        Image(url="a.png", width=100),
        delay=timedelta(minutes=5),
        ttl=3600,
        idempotency_key="order-7",
        idempotency_key_ttl=timedelta(days=1),
        debounce=Debounce(key="user-1", delay=30),
        key="user-1",
        lane=Lane.HIGH,
        max_attempts=2,
        tags=["user:1", "plan:pro"],
        metadata={"plan": "pro", "seats": 3},
    )

    options = runtime.requests("Trigger")[0].options
    due = options.due_at.to_datetime()
    assert before + timedelta(minutes=5) <= due
    assert due <= datetime.now(timezone.utc) + timedelta(minutes=5)
    assert options.ttl.to_seconds() == 3600
    assert options.idempotency_key == "order-7"
    assert options.idempotency_key_ttl.to_seconds() == 86400
    assert options.debounce.key == "user-1"
    assert options.debounce.delay.to_seconds() == 30
    assert options.key == "user-1"
    assert options.lane is WireLane.HIGH
    assert options.max_attempts == 2
    assert list(options.tags) == ["user:1", "plan:pro"]
    assert options.metadata == b'{"plan":"pro","seats":3}'


def test_a_trigger_delayed_until_a_moment_is_due_then(runtime):
    at = datetime(2030, 1, 2, 3, 4, 5, tzinfo=timezone.utc)

    _resize().trigger(Image(url="a.png", width=1), delay=at, lane="low")

    options = runtime.requests("Trigger")[0].options
    assert options.due_at.to_datetime() == at
    assert options.lane is WireLane.LOW


def test_a_payload_over_256_kib_is_refused_before_it_is_sent(runtime):
    with pytest.raises(ValueError) as raised:
        _resize().trigger(Image(url="x" * 262144, width=1))

    assert "262144 bytes" in str(raised.value)
    assert runtime.calls == []


def test_a_batch_trigger_sends_every_payload_with_its_own_options_and_answers_ids_in_order(
    runtime,
):
    runs = _resize().batch_trigger(
        [Image(url="a.png", width=1), Trigger(Image(url="b.png", width=2), key="user-2")]
    )

    assert [run.id for run in runs] == ["run_1", "run_2"]
    [request] = runtime.requests("BatchTrigger")
    assert request.task == "resize-image"
    assert [json.loads(item.payload)["url"] for item in request.items] == ["a.png", "b.png"]
    assert request.items[0].options is None
    assert request.items[1].options.key == "user-2"


@pytest.mark.asyncio
async def test_an_async_app_triggers_without_blocking_its_loop(runtime):
    resize = _resize()

    run = await resize.trigger_async(Image(url="a.png", width=1), key="k")
    runs = await resize.batch_trigger_async([Image(url="b.png", width=2)])

    assert run.id == "run_1"
    assert [r.id for r in runs] == ["run_1"]
    assert runtime.requests("Trigger")[0].options.key == "k"


def test_a_task_triggered_during_discovery_says_it_is_not_provisioned_yet(collector):
    resize = _resize()

    with pytest.raises(UnprovisionedResourceError) as raised:
        resize.trigger(Image(url="a.png", width=1))
    assert str(raised.value) == (
        "'task(\"resize-image\")' cannot be used during discovery: "
        "tried to access 'trigger' before the resource was provisioned"
    )
