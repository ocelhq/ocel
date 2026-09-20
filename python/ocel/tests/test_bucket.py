import os

import pytest

from ocel import UnprovisionedResourceError, bucket
from ocel.gen.app.resources.v1.resources_pb import ResourceType


def test_a_declared_bucket_reaches_the_dev_server_with_the_file_that_declared_it(collector):
    bucket("uploads")

    assert len(collector.declares) == 1
    path, protocol, declared = collector.declares[0]
    assert path == "/app.resources.v1.ResourceService/Declare"
    assert protocol == "1"
    assert declared.resource.type is ResourceType.BUCKET
    assert declared.resource.name == "uploads"
    assert declared.config.field == "bucket"
    assert declared.config.value.public is False
    assert list(declared.config.value.allowed_origins) == []
    file, _, line = declared.source.rpartition(":")
    assert os.path.basename(file) == "test_bucket.py"
    assert int(line) > 0


def test_a_bucket_declared_public_carries_the_origins_it_allows(collector):
    bucket("uploads", public=True, allowed_origins=("https://app.example", "https://www.example"))

    declared = collector.declares[0][2].config.value
    assert declared.public is True
    assert list(declared.allowed_origins) == ["https://app.example", "https://www.example"]


def test_a_bucket_reached_during_discovery_says_it_is_not_provisioned_yet(collector):
    uploads = bucket("uploads")

    assert uploads.name == "uploads"
    with pytest.raises(UnprovisionedResourceError) as raised:
        uploads.head("a.txt")
    assert str(raised.value) == (
        "'bucket(\"uploads\")' cannot be used during discovery: "
        "tried to access 'head' before the resource was provisioned"
    )


def test_a_declaration_the_server_refuses_says_what_it_said(monkeypatch):
    monkeypatch.setenv("OCEL_PHASE", "discovery")
    monkeypatch.setenv("OCEL_DEV_SERVER", "http://127.0.0.1:1")

    with pytest.raises(RuntimeError) as raised:
        bucket("uploads")
    assert str(raised.value).startswith("ocel: declare bucket 'uploads': ")
