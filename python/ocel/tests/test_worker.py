import os

from ocel import worker
from ocel.gen.app.resources.v1.resources_pb import ResourceType


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
