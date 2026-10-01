import asyncio
import json
import threading
from datetime import datetime, timezone

import pytest
from pydantic import BaseModel

from ocel import (
    AbortTaskRunError,
    Batch,
    CatchErrorResult,
    RunAttempt,
    RunContext,
    RunMessage,
    RunResult,
    task,
    topic,
    worker,
)
from ocel.worker import deliver

pytestmark = pytest.mark.asyncio

_MESSAGE = "01HZY3V0J9Q8C7B6A5Z4Y3X2W1"
_AT = "2023-11-14T22:13:20Z"


def envelope(topic, consumer, payload=None, *, number=1, of=3, execution="run_1", messages=None):
    body = {
        "v": 1,
        "topic": topic,
        "consumer": consumer,
        "execution": execution,
        "message": {"id": _MESSAGE, "publishedAt": _AT},
        "attempt": {"number": number, "of": of, "firstAttemptedAt": _AT},
    }
    if messages is None:
        body["payload"] = payload
    else:
        body["messages"] = [
            {
                "execution": f"{execution}-{n}",
                "message": {"id": _MESSAGE, "publishedAt": _AT},
                "attempt": {"number": number, "of": of, "firstAttemptedAt": _AT},
                "payload": each,
            }
            for n, each in enumerate(messages)
        ]
    return json.dumps(body).encode()


async def test_a_body_that_is_not_an_envelope_is_answered_400():
    status, body = await deliver("worker", b"{not json")

    assert status == 400
    assert body


async def test_a_delivery_for_a_consumer_this_process_never_registered_is_answered_404():
    status, body = await deliver("worker", envelope("orders", "ship", {}))

    assert status == 404
    assert b'"ship"' in body
    assert b'"orders"' in body


async def test_a_delivery_for_a_task_on_another_worker_is_answered_404():
    @task("resize-image", worker=worker("media"))
    def resize(payload, ctx):
        return None

    status, body = await deliver("worker", envelope("resize-image", "resize-image", {}))

    assert status == 404
    assert b'"media"' in body
    assert b'"worker"' in body


async def test_a_task_that_returns_is_answered_200_with_its_output_as_json():
    seen = []

    @task("double")
    def double(payload, ctx):
        seen.append((payload, ctx, threading.current_thread() is threading.main_thread()))
        return {"doubled": payload["n"] * 2}

    status, body = await deliver("worker", envelope("double", "double", {"n": 21}, number=2))

    assert (status, json.loads(body)) == (200, {"doubled": 42})
    [(payload, ctx, on_main_thread)] = seen
    assert payload == {"n": 21}
    assert on_main_thread is False
    at = datetime(2023, 11, 14, 22, 13, 20, tzinfo=timezone.utc)
    assert (ctx.kind, ctx.name, ctx.topic, ctx.id) == ("task", "double", "double", "run_1")
    assert ctx.attempt == RunAttempt(number=2, of=3, first_attempted_at=at)
    assert ctx.message == RunMessage(id=_MESSAGE, published_at=at)
    assert ctx.canceled is False


async def test_an_async_task_that_returns_nothing_is_answered_200_with_null():
    @task("noop")
    async def noop(payload, ctx):
        return None

    assert await deliver("worker", envelope("noop", "noop", 1)) == (200, b"null")


class Image(BaseModel):
    url: str
    width: int


async def test_a_typed_task_receives_its_payload_validated_and_answers_its_output_as_json():
    received = []

    @task("resize-image")
    async def resize(payload: Image, ctx: RunContext) -> Image:
        received.append(payload)
        return Image(url=payload.url, width=payload.width // 2)

    status, body = await deliver(
        "worker", envelope("resize-image", "resize-image", {"url": "a.png", "width": 100})
    )

    assert received == [Image(url="a.png", width=100)]
    assert (status, json.loads(body)) == (200, {"url": "a.png", "width": 50})


async def test_a_payload_that_fails_its_schema_aborts_the_run():
    @task("resize-image")
    async def resize(payload: Image, ctx):
        raise AssertionError("never runs")

    status, body = await deliver(
        "worker", envelope("resize-image", "resize-image", {"url": "a.png"})
    )

    assert status == 422
    assert "width" in json.loads(body)["abort"]["reason"]


async def test_a_task_that_raises_the_abort_error_is_answered_422_with_its_reason():
    @task("charge")
    def charge(payload, ctx):
        raise AbortTaskRunError("card declined")

    status, body = await deliver("worker", envelope("charge", "charge", {}))

    assert status == 422
    assert body == b'{"abort":{"reason":"card declined"}}'


async def test_a_task_that_raises_any_other_error_is_answered_500_with_its_message():
    @task("charge")
    async def charge(payload, ctx):
        raise ConnectionError("gateway unreachable")

    assert await deliver("worker", envelope("charge", "charge", {})) == (
        500,
        b"gateway unreachable",
    )


async def test_catch_error_that_skips_retrying_aborts_the_run():
    caught = []

    def catch(payload, error, ctx):
        caught.append((payload, str(error), ctx.name))
        return CatchErrorResult(skip_retrying=True)

    @task("charge", catch_error=catch)
    def charge(payload, ctx):
        raise ValueError("bad card")

    status, body = await deliver("worker", envelope("charge", "charge", {"c": 1}))

    assert (status, body) == (422, b'{"abort":{"reason":"bad card"}}')
    assert caught == [({"c": 1}, "bad card", "charge")]


async def test_catch_error_that_answers_nothing_leaves_the_run_to_retry():
    @task("charge", catch_error=lambda payload, error, ctx: None)
    def charge(payload, ctx):
        raise ValueError("bad card")

    assert (await deliver("worker", envelope("charge", "charge", {})))[0] == 500


def _lifecycle(events):
    async def on_success(payload, output, ctx):
        events.append(("success", payload, output))

    def on_failure(payload, error, ctx):
        events.append(("failure", payload, str(error)))

    async def on_complete(payload, result, ctx):
        events.append(("complete", payload, result))

    return {"on_success": on_success, "on_failure": on_failure, "on_complete": on_complete}


async def test_a_run_that_succeeds_runs_on_success_then_on_complete():
    events = []

    @task("double", **_lifecycle(events))
    def double(payload, ctx):
        return payload * 2

    await deliver("worker", envelope("double", "double", 2))

    assert events == [
        ("success", 2, 4),
        ("complete", 2, RunResult(ok=True, output=4)),
    ]


async def test_a_failed_attempt_with_attempts_left_runs_no_lifecycle_hook():
    events = []

    @task("charge", **_lifecycle(events))
    def charge(payload, ctx):
        raise ValueError("bad card")

    await deliver("worker", envelope("charge", "charge", 1, number=2, of=3))

    assert events == []


async def test_the_last_failed_attempt_runs_on_failure_then_on_complete():
    events = []
    raised = ValueError("bad card")

    @task("charge", **_lifecycle(events))
    def charge(payload, ctx):
        raise raised

    status, _ = await deliver("worker", envelope("charge", "charge", 1, number=3, of=3))

    assert status == 500
    assert events == [
        ("failure", 1, "bad card"),
        ("complete", 1, RunResult(ok=False, error=raised)),
    ]


async def test_an_aborted_run_is_over_whatever_attempts_remain():
    events = []

    @task("charge", **_lifecycle(events))
    def charge(payload, ctx):
        raise AbortTaskRunError("card declined")

    await deliver("worker", envelope("charge", "charge", 1, number=1, of=3))

    assert [event[0] for event in events] == ["failure", "complete"]


async def test_a_lifecycle_hook_that_raises_is_reported_and_leaves_the_answer(capsys):
    def on_success(payload, output, ctx):
        raise RuntimeError("metrics down")

    @task("double", on_success=on_success)
    def double(payload, ctx):
        return payload * 2

    assert await deliver("worker", envelope("double", "double", 2)) == (200, b"4")
    assert "metrics down" in capsys.readouterr().err


async def test_an_attempt_runs_worker_middleware_then_on_start_attempt_then_task_middleware():
    order = []

    async def outer(ctx, next):
        order.append(("worker", ctx.kind))
        output = await next()
        order.append("worker after")
        return output

    def inner(payload, ctx, next):
        order.append("task")
        return next() + 1

    async def on_start_attempt(payload, ctx):
        order.append("start attempt")

    media = worker("media", middleware=outer)

    @task("double", worker=media, middleware=inner, on_start_attempt=on_start_attempt)
    async def double(payload, ctx):
        order.append("run")
        return payload * 2

    assert await deliver("media", envelope("double", "double", 2)) == (200, b"5")
    assert order == [("worker", "task"), "start attempt", "task", "run", "worker after"]


async def test_an_error_from_task_middleware_fails_the_attempt_like_the_handler():
    def inner(payload, ctx, next):
        raise ValueError("refused by middleware")

    @task("double", middleware=inner)
    def double(payload, ctx):
        return payload

    assert await deliver("worker", envelope("double", "double", 2)) == (
        500,
        b"refused by middleware",
    )


async def test_a_worker_starts_once_before_its_first_delivery_even_when_they_arrive_together():
    starts = []

    async def on_start():
        starts.append("start")
        await asyncio.sleep(0.01)

    media = worker("media", on_start=on_start)

    @task("double", worker=media)
    async def double(payload, ctx):
        return starts[:]

    answers = await asyncio.gather(
        *[deliver("media", envelope("double", "double", n)) for n in range(3)]
    )

    assert starts == ["start"]
    assert answers == [(200, b'["start"]')] * 3


async def test_a_worker_whose_start_fails_fails_the_delivery_and_starts_again_on_the_next():
    attempts = []

    def on_start():
        attempts.append("start")
        if len(attempts) == 1:
            raise RuntimeError("database unreachable")

    media = worker("media", on_start=on_start)

    @task("double", worker=media)
    def double(payload, ctx):
        return payload * 2

    first = await deliver("media", envelope("double", "double", 2))
    second = await deliver("media", envelope("double", "double", 2))

    assert first[0] == 500
    assert b"database unreachable" in first[1]
    assert second == (200, b"4")
    assert attempts == ["start", "start"]


async def test_a_worker_runs_no_more_deliveries_at_once_than_its_concurrency():
    running = []
    peak = []

    media = worker("media", concurrency=2)

    @task("slow", worker=media)
    async def slow(payload, ctx):
        running.append(payload)
        peak.append(len(running))
        await asyncio.sleep(0.01)
        running.remove(payload)

    await asyncio.gather(*[deliver("media", envelope("slow", "slow", n)) for n in range(5)])

    assert max(peak) == 2


async def test_a_consumer_receives_the_message_with_a_consumer_context():
    seen = []
    orders = topic("orders")

    @orders.consumer("ship")
    async def ship(order, ctx):
        seen.append((order, ctx.kind, ctx.name, ctx.topic, ctx.id))

    status, _ = await deliver(
        "worker", envelope("orders", "ship", {"id": 7}, execution=f"{_MESSAGE}-ship")
    )

    assert status == 200
    assert seen == [({"id": 7}, "consumer", "ship", "orders", f"{_MESSAGE}-ship")]


async def test_a_batch_consumer_receives_every_payload_and_the_first_messages_context():
    seen = []
    orders = topic("orders")

    @orders.batch_consumer("index", batch_size=10)
    def index(batch, ctx):
        seen.append((batch, ctx.id))

    status, _ = await deliver(
        "worker", envelope("orders", "index", execution="m", messages=[{"id": 1}, {"id": 2}])
    )

    assert status == 200
    assert seen == [([{"id": 1}, {"id": 2}], "m-0")]


async def test_a_batched_task_receives_its_payloads_validated():
    seen = []

    @task("index", batch=Batch(size=10))
    async def index(images: list[Image], ctx):
        seen.extend(images)

    await deliver(
        "worker",
        envelope("index", "index", messages=[{"url": "a", "width": 1}, {"url": "b", "width": 2}]),
    )

    assert seen == [Image(url="a", width=1), Image(url="b", width=2)]


async def test_a_consumer_that_raises_the_abort_error_is_answered_422():
    orders = topic("orders")

    @orders.consumer("ship")
    def ship(order, ctx):
        raise AbortTaskRunError("no such address")

    status, body = await deliver("worker", envelope("orders", "ship", {}))

    assert (status, body) == (422, b'{"abort":{"reason":"no such address"}}')


async def test_a_delivery_cancelled_while_running_runs_on_cancel_and_stays_cancelled():
    started = asyncio.Event()
    canceled = []

    async def on_cancel(payload, ctx):
        canceled.append((payload, ctx.canceled))

    @task("slow", on_cancel=on_cancel)
    async def slow(payload, ctx):
        started.set()
        await asyncio.sleep(10)

    delivery = asyncio.ensure_future(deliver("worker", envelope("slow", "slow", 5)))
    await started.wait()
    delivery.cancel()

    with pytest.raises(asyncio.CancelledError):
        await delivery
    assert canceled == [(5, True)]


async def test_a_task_that_takes_only_its_payload_runs_without_a_context():
    @task("double")
    def double(payload):
        return payload * 2

    assert await deliver("worker", envelope("double", "double", 3)) == (200, b"6")


async def test_a_payload_that_fails_its_schema_runs_no_hook():
    events = []

    async def on_start_attempt(payload, ctx):
        events.append("start attempt")

    async def on_cancel(payload, ctx):
        events.append("cancel")

    @task(
        "resize-image",
        on_start_attempt=on_start_attempt,
        on_cancel=on_cancel,
        **_lifecycle(events),
    )
    async def resize(payload: Image, ctx):
        raise AssertionError("never runs")

    status, _ = await deliver("worker", envelope("resize-image", "resize-image", {"url": "a.png"}))

    assert status == 422
    assert events == []


async def test_an_output_that_cannot_be_encoded_as_json_aborts_the_run_and_ends_it_failed():
    events = []

    @task("leak", **_lifecycle(events))
    def leak(payload, ctx):
        return object()

    status, body = await deliver("worker", envelope("leak", "leak", 1, number=1, of=3))

    assert status == 422
    assert "object" in json.loads(body)["abort"]["reason"]
    assert [event[0] for event in events] == ["failure", "complete"]
    assert events[0][1] == 1
    assert "object" in events[0][2]


async def test_a_batch_consumer_given_a_single_payload_receives_a_batch_of_one():
    seen = []
    orders = topic("orders")

    @orders.batch_consumer("index", batch_size=10)
    def index(batch, ctx):
        seen.append(batch)

    status, _ = await deliver("worker", envelope("orders", "index", {"id": 1}))

    assert status == 200
    assert seen == [[{"id": 1}]]


async def test_a_task_that_is_not_batched_given_a_batch_is_answered_400_and_runs_nothing():
    ran = []

    @task("double")
    def double(payload, ctx):
        ran.append(payload)

    status, body = await deliver("worker", envelope("double", "double", messages=[1, 2]))

    assert status == 400
    assert b'"double"' in body
    assert ran == []


async def test_a_run_that_returns_once_cancelled_runs_only_on_cancel_and_answers_its_output():
    started = asyncio.Event()
    events = []

    async def on_cancel(payload, ctx):
        events.append(("cancel", ctx.canceled))

    @task("slow", on_cancel=on_cancel, **_lifecycle(events))
    async def slow(payload, ctx):
        started.set()
        try:
            await asyncio.sleep(10)
        except asyncio.CancelledError:
            return "partial"

    delivery = asyncio.ensure_future(deliver("worker", envelope("slow", "slow", 5)))
    await started.wait()
    delivery.cancel()

    assert await delivery == (200, b'"partial"')
    assert events == [("cancel", True)]


async def test_an_envelope_without_an_attempt_runs_attempt_1_of_1():
    seen = []

    @task("double")
    def double(payload, ctx):
        seen.append(ctx.attempt)

    body = json.loads(envelope("double", "double", 2))
    del body["attempt"]

    assert (await deliver("worker", json.dumps(body).encode()))[0] == 200
    assert seen == [RunAttempt(number=1, of=1)]


_EXACT = '{"ratio":2.0,"id":9007199254740993,"count":2}'


def typed(payload):
    return [(key, value, type(value)) for key, value in payload.items()]


async def test_a_delivered_payload_keeps_integers_beyond_2_53_and_tells_2_from_2_0():
    seen = []

    @task("tally")
    def tally(payload, ctx):
        seen.append(payload)

    status, _ = await deliver("worker", envelope("tally", "tally", json.loads(_EXACT)))

    assert status == 200
    assert [typed(each) for each in seen] == [
        [("ratio", 2.0, float), ("id", 9007199254740993, int), ("count", 2, int)]
    ]


async def test_a_delivered_batch_keeps_integers_beyond_2_53_and_tells_2_from_2_0():
    seen = []

    @topic("orders").batch_consumer("index", batch_size=10)
    def index(batch, ctx):
        seen.extend(batch)

    status, _ = await deliver(
        "worker", envelope("orders", "index", messages=[json.loads(_EXACT), {"n": 3.0}])
    )

    assert status == 200
    assert [typed(each) for each in seen] == [
        [("ratio", 2.0, float), ("id", 9007199254740993, int), ("count", 2, int)],
        [("n", 3.0, float)],
    ]


async def test_an_envelope_without_a_payload_delivers_none():
    seen = []

    @task("noop")
    def noop(payload, ctx):
        seen.append(payload)

    body = json.loads(envelope("noop", "noop"))
    del body["payload"]

    assert (await deliver("worker", json.dumps(body).encode()))[0] == 200
    assert seen == [None]
