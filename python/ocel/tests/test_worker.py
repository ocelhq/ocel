import asyncio
import json
import os

from ocel import task, worker
from ocel.gen.app.resources.v1.resources_pb import ResourceType
from ocel.worker import deliver


def test_a_declared_worker_reaches_the_dev_server_with_its_concurrency(collector):
    worker("media", concurrency=4)

    assert len(collector.declares) == 1
    _, _, declared = collector.declares[0]
    assert declared.resource.type is ResourceType.WORKER
    assert declared.resource.name == "media"
    assert declared.config.field == "worker"
    assert declared.config.value.concurrency == 4
    file, _, line = declared.source.rpartition(":")
    assert os.path.basename(file) == "test_worker.py"
    assert int(line) > 0


def test_a_worker_declared_without_a_concurrency_leaves_it_unset(collector):
    worker("media")

    assert collector.declares[0][2].config.value.concurrency == 0


def test_a_worker_outside_discovery_declares_nothing(collector, monkeypatch):
    monkeypatch.delenv("OCEL_PHASE")

    media = worker("media")

    assert media.name == "media"
    assert collector.declares == []


def test_a_worker_caps_its_runs_in_each_event_loop_it_is_delivered_on():
    running = []
    peak = []
    media = worker("media", concurrency=1)

    @task("slow", worker=media)
    async def slow(payload, ctx):
        running.append(payload)
        peak.append(len(running))
        await asyncio.sleep(0.01)
        running.remove(payload)

    def envelope(n):
        return json.dumps(
            {"v": 1, "topic": "slow", "consumer": "slow", "execution": f"run_{n}", "payload": n}
        ).encode()

    async def deliver_two():
        return await asyncio.gather(deliver("media", envelope(1)), deliver("media", envelope(2)))

    for _ in range(2):
        assert [status for status, _ in asyncio.run(deliver_two())] == [200, 200]
    assert max(peak) == 1
