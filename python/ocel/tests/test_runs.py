import json
from datetime import datetime, timedelta, timezone

import fakeruntime
import pytest

from ocel import Run, RunStatus, UnprovisionedResourceError, runs, task
from ocel.gen.app.task.v1.task_pb import RunStatus as WireRunStatus

_AT = datetime(2023, 11, 14, 22, 13, 20, tzinfo=timezone.utc)


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


def test_a_retrieved_run_carries_its_whole_record(runtime):
    run = runs.retrieve("run_1")

    assert runtime.requests("RetrieveRun")[0].id == "run_1"
    assert run == Run(
        id="run_1",
        task="resize-image",
        status=RunStatus.COMPLETED,
        payload={"url": "a.png", "width": 100},
        output={"ok": True},
        error="",
        attempts=2,
        tags=["user:1"],
        metadata={"plan": "pro"},
        created_at=_AT,
        due_at=_AT,
        started_at=_AT,
        finished_at=_AT,
        expires_at=None,
    )
    assert runtime.authorizations == ["Bearer letmein"]


def typed(value):
    return [(key, each, type(each)) for key, each in value.items()]


def test_a_retrieved_run_keeps_integers_beyond_2_53_and_tells_2_from_2_0(runtime):
    exact = b'{"ratio":2.0,"id":9007199254740993,"count":2}'
    runtime.stored_run_json = {"payload": exact, "output": exact, "metadata": exact}

    run = runs.retrieve("run_1")

    expected = [("ratio", 2.0, float), ("id", 9007199254740993, int), ("count", 2, int)]
    assert typed(run.payload) == expected
    assert typed(run.output) == expected
    assert typed(run.metadata) == expected


def test_a_retrieved_run_without_payload_output_or_metadata_reads_them_as_none_and_empty(runtime):
    runtime.stored_run_json = {"payload": b"", "output": b"", "metadata": b""}

    run = runs.retrieve("run_1")

    assert (run.payload, run.output, run.metadata) == (None, None, {})


def test_a_run_listing_filters_by_the_declared_task_statuses_and_tags(runtime):
    @task("resize-image")
    def resize(payload, ctx):
        return None

    page = runs.list(
        task=resize,
        status=[RunStatus.FAILED, RunStatus.TIMED_OUT],
        tags=["user:1"],
        cursor="c1",
        limit=20,
    )

    [request] = runtime.requests("ListRuns")
    assert request.task == "resize-image"
    assert list(request.statuses) == [WireRunStatus.FAILED, WireRunStatus.TIMED_OUT]
    assert list(request.tags) == ["user:1"]
    assert (request.cursor, request.limit) == ("c1", 20)
    assert [run.id for run in page.runs] == ["run_1", "run_2"]
    assert page.next_cursor == "c2"


def test_a_run_listing_takes_a_task_by_its_declared_name_and_one_status(runtime):
    runs.list(task="resize-image", status=RunStatus.QUEUED)

    [request] = runtime.requests("ListRuns")
    assert request.task == "resize-image"
    assert list(request.statuses) == [WireRunStatus.QUEUED]


def test_a_run_listing_without_filters_lists_every_run(runtime):
    runs.list()

    [request] = runtime.requests("ListRuns")
    assert (request.task, list(request.statuses), list(request.tags)) == ("", [], [])


def test_a_canceled_run_answers_its_record(runtime):
    assert runs.cancel("run_1").status is RunStatus.CANCELED
    assert runtime.requests("CancelRun")[0].id == "run_1"


def test_a_replayed_run_answers_the_new_run(runtime):
    assert runs.replay("run_1").id == "run_9"
    assert runtime.requests("ReplayRun")[0].id == "run_1"


def test_a_rescheduled_run_is_due_after_the_delay(runtime):
    before = datetime.now(timezone.utc)

    run = runs.reschedule("run_1", delay=timedelta(hours=1))

    due = runtime.requests("RescheduleRun")[0].due_at.to_datetime()
    assert before + timedelta(hours=1) <= due <= datetime.now(timezone.utc) + timedelta(hours=1)
    assert run.status is RunStatus.DELAYED
    assert run.due_at == due


def test_a_run_rescheduled_to_a_moment_is_due_then(runtime):
    at = datetime(2030, 5, 6, tzinfo=timezone.utc)

    runs.reschedule("run_1", delay=at)

    assert runtime.requests("RescheduleRun")[0].due_at.to_datetime() == at


@pytest.mark.asyncio
async def test_an_async_app_reaches_runs_without_blocking_its_loop(runtime):
    assert (await runs.retrieve_async("run_1")).id == "run_1"
    assert len((await runs.list_async(task="resize-image")).runs) == 2
    assert (await runs.cancel_async("run_1")).status is RunStatus.CANCELED
    assert (await runs.replay_async("run_1")).id == "run_9"
    assert (await runs.reschedule_async("run_1", delay=60)).status is RunStatus.DELAYED


def test_runs_reached_during_discovery_say_they_are_not_provisioned_yet(collector):
    with pytest.raises(UnprovisionedResourceError) as raised:
        runs.retrieve("run_1")
    assert str(raised.value) == (
        "'runs' cannot be used during discovery: "
        "tried to access 'retrieve' before the resource was provisioned"
    )
