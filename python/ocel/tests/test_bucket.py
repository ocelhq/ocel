import asyncio
import io
import json
import os
import threading
from datetime import datetime, timedelta, timezone

import fakebucket
import pytest
from connectrpc.code import Code
from connectrpc.errors import ConnectError

from ocel import (
    ObjectInfo,
    ObjectNotFound,
    OcelError,
    PreconditionFailed,
    RuntimeRefusedError,
    UnprovisionedResourceError,
    bucket,
)
from ocel.gen.app.bucket.v1.bucket_pb import SignedAudience, SignedOperation
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


def test_a_bucket_declared_public_declares_the_origins_it_allows(collector):
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

    with pytest.raises(OcelError) as raised:
        bucket("uploads")
    assert str(raised.value).startswith("ocel: declare bucket 'uploads': ")


@pytest.fixture
def uploads(monkeypatch):
    fake = fakebucket.Bucket()
    monkeypatch.delenv("OCEL_PHASE", raising=False)
    monkeypatch.setenv(
        "OCEL_RESOURCE_BUCKET_uploads",
        json.dumps(
            {
                "name": "uploads",
                "bucket": {
                    "bucket": "shop-prod-uploads",
                    "publicBaseUrl": "https://storage.example/uploads/",
                },
            }
        ),
    )
    monkeypatch.setenv("OCEL_RUNTIME_ADDRESS", fake.url)
    monkeypatch.setenv("OCEL_SESSION_TOKEN", fakebucket.TOKEN)
    yield fake
    fake.close()


def test_what_the_bucket_knows_about_one_of_its_objects(uploads):
    uploads.store.objects["a.txt"] = fakebucket.Stored(b"hello", "text/plain", {"by": "me"})

    info = bucket("uploads").head("a.txt")

    assert info is not None
    assert info.key == "a.txt"
    assert info.size == 5
    assert info.content_type == "text/plain"
    assert info.metadata == {"by": "me"}
    assert info.etag
    assert info.uploaded_at == datetime(2023, 11, 14, 22, 13, 20, tzinfo=timezone.utc)


def test_a_key_with_no_object_under_it_is_known_as_nothing(uploads):
    assert bucket("uploads").head("gone.txt") is None
    assert bucket("uploads").exists("gone.txt") is False


def test_every_call_sends_the_runtime_session_token(uploads):
    bucket("uploads").head("a.txt")

    assert uploads.store.authorizations == ["Bearer letmein"]


def test_deleting_a_key_with_no_object_under_it_is_not_an_error(uploads):
    uploads.store.objects["a.txt"] = fakebucket.Stored(b"hello", "text/plain", {})

    bucket("uploads").delete("a.txt", "gone.txt")

    assert uploads.store.objects == {}


def test_copying_an_object_the_bucket_does_not_have_names_what_was_missing(uploads):
    with pytest.raises(ObjectNotFound) as raised:
        bucket("uploads").copy("gone.txt", "kept.txt")
    assert str(raised.value) == 'the bucket has no object under "gone.txt"'
    assert isinstance(raised.value, FileNotFoundError)


def test_a_copy_lands_under_the_destination_key(uploads):
    uploads.store.objects["a.txt"] = fakebucket.Stored(b"hello", "text/plain", {})

    copied = bucket("uploads").copy("a.txt", "b.txt")

    assert copied.key == "b.txt"
    assert uploads.store.objects["b.txt"].data == b"hello"


def test_a_listing_walks_every_page_of_the_prefix_it_was_given(uploads):
    for key in ["kept/a", "kept/b", "kept/c", "other/d"]:
        uploads.store.objects[key] = fakebucket.Stored(b"x", "", {})

    listed = list(bucket("uploads").list(prefix="kept/"))

    assert [info.key for info in listed] == ["kept/a", "kept/b", "kept/c"]
    assert [asked[2] for asked in uploads.store.listed] == ["", "kept/c"]


def test_bytes_written_under_a_key_come_back_as_they_went_in(uploads):
    landed = bucket("uploads").put("a.txt", b"hello")

    assert landed.key == "a.txt"
    assert landed.size == 5
    assert bucket("uploads").get("a.txt").bytes() == b"hello"


def test_text_written_under_a_key_comes_back_decoded(uploads):
    bucket("uploads").put("a.txt", "héllo")

    assert bucket("uploads").get("a.txt").text() == "héllo"


def test_a_file_written_by_path_writes_its_bytes(uploads, tmp_path):
    source = tmp_path / "a.txt"
    source.write_bytes(b"from a file")

    bucket("uploads").put("a.txt", source)

    assert uploads.store.objects["a.txt"].data == b"from a file"


def test_an_open_file_is_read_to_its_end(uploads, tmp_path):
    source = tmp_path / "a.txt"
    source.write_bytes(b"from a handle")

    with open(source, "rb") as handle:
        bucket("uploads").put("a.txt", handle)

    assert uploads.store.objects["a.txt"].data == b"from a handle"


def test_a_write_sends_the_media_type_and_metadata_it_was_given(uploads):
    bucket("uploads").put(
        "a.txt",
        b"hello",
        content_type="text/plain",
        cache_control="public, max-age=60",
        metadata={"by": "me"},
    )

    stored = uploads.store.objects["a.txt"]
    assert stored.content_type == "text/plain"
    assert stored.cache_control == "public, max-age=60"
    assert stored.metadata == {"by": "me"}


def test_a_write_that_must_not_replace_refuses_a_key_the_bucket_has(uploads):
    bucket("uploads").put("a.txt", b"first")

    with pytest.raises(PreconditionFailed) as raised:
        bucket("uploads").put("a.txt", b"second", if_not_exists=True)
    assert str(raised.value) == (
        'the object under "a.txt" did not meet the condition this write set'
    )
    assert uploads.store.objects["a.txt"].data == b"first"


def test_a_write_conditioned_on_a_version_refuses_another(uploads):
    bucket("uploads").put("a.txt", b"first")

    with pytest.raises(PreconditionFailed):
        bucket("uploads").put("a.txt", b"second", if_match='"not-the-etag"')


def test_a_write_conditioned_on_the_current_version_goes_through(uploads):
    first = bucket("uploads").put("a.txt", b"first")

    bucket("uploads").put("a.txt", b"second", if_match=first.etag)

    assert uploads.store.objects["a.txt"].data == b"second"


def test_reading_a_key_the_bucket_does_not_have_names_what_was_missing(uploads):
    with pytest.raises(ObjectNotFound) as raised:
        bucket("uploads").get("gone.txt")
    assert str(raised.value) == 'the bucket has no object under "gone.txt"'


def test_a_range_reads_the_bytes_it_names(uploads):
    bucket("uploads").put("a.txt", b"hello world")

    assert bucket("uploads").get("a.txt", range=(6, 5)).bytes() == b"world"
    assert bucket("uploads").get("a.txt", range=(6, None)).bytes() == b"world"


def test_an_object_is_read_a_chunk_at_a_time(uploads):
    bucket("uploads").put("a.txt", b"hello world")

    read = bucket("uploads").get("a.txt")

    assert b"".join(read) == b"hello world"
    assert read.info.size == 11


def test_a_body_read_whole_twice_answers_the_same_bytes_both_times(uploads):
    bucket("uploads").put("a.txt", b"hello world")

    read = bucket("uploads").get("a.txt")

    assert read.bytes() == b"hello world"
    assert read.bytes() == b"hello world"
    assert read.text() == "hello world"


def test_a_body_already_walked_chunk_by_chunk_refuses_a_second_walk(uploads):
    bucket("uploads").put("a.txt", b"hello world")
    read = bucket("uploads").get("a.txt")
    b"".join(read)

    with pytest.raises(OcelError, match='the body of "a.txt" was read already'):
        read.bytes()


@pytest.mark.asyncio
async def test_an_async_body_read_whole_twice_answers_the_same_bytes_both_times(uploads):
    await bucket("uploads").put_async("a.txt", b"hello world")

    read = await bucket("uploads").get_async("a.txt")

    assert await read.bytes() == b"hello world"
    assert await read.bytes() == b"hello world"


def test_an_object_opened_for_reading_behaves_like_a_file(uploads):
    bucket("uploads").put("a.txt", b"hello world")

    with bucket("uploads").open("a.txt", "rb") as handle:
        assert handle.read(5) == b"hello"
        assert handle.read() == b" world"


def test_an_object_opened_for_writing_lands_when_it_is_closed(uploads):
    with bucket("uploads").open("a.txt", "wb", content_type="text/plain") as handle:
        handle.write(b"hello ")
        handle.write(b"world")
        assert "a.txt" not in uploads.store.objects

    assert uploads.store.objects["a.txt"].data == b"hello world"
    assert uploads.store.objects["a.txt"].content_type == "text/plain"


def test_an_object_opened_for_writing_lands_nothing_when_the_body_raises(uploads):
    store = bucket("uploads")
    store._single_request_limit_bytes = 8
    store._part_size_bytes = 4

    with pytest.raises(RuntimeError, match="the caller gave up"):
        with store.open("a.txt", "wb") as handle:
            handle.write(b"0123456789abcde")
            raise RuntimeError("the caller gave up")

    assert "a.txt" not in uploads.store.objects
    assert uploads.store.uploads == {}


@pytest.mark.asyncio
async def test_an_object_opened_for_writing_asynchronously_lands_nothing_when_the_body_raises(
    uploads,
):
    store = bucket("uploads")
    store._single_request_limit_bytes = 8
    store._part_size_bytes = 4

    with pytest.raises(RuntimeError, match="the caller gave up"):
        async with store.open_async("a.txt", "wb") as handle:
            await handle.write(b"0123456789abcde")
            raise RuntimeError("the caller gave up")

    assert "a.txt" not in uploads.store.objects
    assert uploads.store.uploads == {}


@pytest.mark.parametrize("single_request_limit_bytes", [16, 8])
def test_an_object_opened_for_writing_describes_what_it_landed_once_closed(
    uploads, single_request_limit_bytes
):
    store = bucket("uploads")
    store._single_request_limit_bytes = single_request_limit_bytes
    store._part_size_bytes = 4

    with store.open("a.txt", "wb", content_type="text/plain", metadata={"by": "me"}) as handle:
        handle.write(b"0123456789abcde")
        assert handle.info is None

    assert handle.info is not None
    assert handle.info.key == "a.txt"
    assert handle.info.size == 15
    assert handle.info.etag == uploads.store.objects["a.txt"].etag
    assert handle.info.content_type == "text/plain"
    assert handle.info.metadata == {"by": "me"}
    assert uploads.store.heads == []


def test_an_object_opened_for_writing_describes_nothing_when_the_body_raises(uploads):
    with pytest.raises(RuntimeError, match="the caller gave up"):
        with bucket("uploads").open("a.txt", "wb") as handle:
            handle.write(b"hello")
            raise RuntimeError("the caller gave up")

    assert handle.info is None


@pytest.mark.asyncio
@pytest.mark.parametrize("single_request_limit_bytes", [16, 8])
async def test_an_object_opened_for_writing_asynchronously_describes_what_it_landed_once_closed(
    uploads, single_request_limit_bytes
):
    store = bucket("uploads")
    store._single_request_limit_bytes = single_request_limit_bytes
    store._part_size_bytes = 4

    async with store.open_async("a.txt", "wb") as handle:
        await handle.write(b"0123456789abcde")
        assert handle.info is None

    assert handle.info is not None
    assert handle.info.size == 15
    assert handle.info.etag == uploads.store.objects["a.txt"].etag
    assert handle.info.content_type == "application/octet-stream"


def test_a_body_too_big_for_one_request_goes_up_in_parts(uploads):
    store = bucket("uploads")
    store._single_request_limit_bytes = 8
    store._part_size_bytes = 4

    landed = store.put("a.txt", b"0123456789abcde", content_type="text/plain")

    assert landed.size == 15
    assert uploads.store.objects["a.txt"].data == b"0123456789abcde"
    assert uploads.store.objects["a.txt"].content_type == "text/plain"
    assert uploads.store.uploads == {}


def test_a_part_the_store_refuses_abandons_the_whole_write(uploads):
    uploads.store.refuse_part = 2
    store = bucket("uploads")
    store._single_request_limit_bytes = 8
    store._part_size_bytes = 4

    with pytest.raises(OcelError) as raised:
        store.put("a.txt", b"0123456789abcde")
    assert 'part 2 of "a.txt" was refused' in str(raised.value)
    assert uploads.store.aborted == ["upload-1"]
    assert "a.txt" not in uploads.store.objects


@pytest.mark.asyncio
async def test_an_async_write_abandoned_by_a_refused_part_aborts_after_every_part_settles(uploads):
    uploads.store.refuse_part = 2
    uploads.store.slow_part = 1
    store = bucket("uploads")
    store._single_request_limit_bytes = 8
    store._part_size_bytes = 4

    with pytest.raises(OcelError):
        await store.put_async("a.txt", b"0123456789abcde")
    assert uploads.store.events.index("part1-done") < uploads.store.events.index("abort")


def test_a_write_in_parts_that_must_not_replace_is_refused(uploads):
    store = bucket("uploads")
    store._single_request_limit_bytes = 8
    store._part_size_bytes = 4
    store.put("a.txt", b"first")

    with pytest.raises(PreconditionFailed):
        store.put("a.txt", b"0123456789abcde", if_not_exists=True)
    assert uploads.store.objects["a.txt"].data == b"first"


def test_a_put_returns_the_etag_of_its_own_write_though_another_write_replaces_the_object_at_once(
    uploads,
):
    uploads.store.lands_after_each_write = b"theirs"

    landed = bucket("uploads").put(
        "a.txt", b"mine", content_type="text/plain", metadata={"by": "me"}
    )

    assert landed == ObjectInfo(
        key="a.txt",
        size=4,
        etag=fakebucket.etag_of(b"mine"),
        content_type="text/plain",
        uploaded_at=None,
        metadata={"by": "me"},
    )
    uploads.store.lands_after_each_write = None
    with pytest.raises(PreconditionFailed):
        bucket("uploads").put("a.txt", b"mine again", if_match=landed.etag)
    assert uploads.store.objects["a.txt"].data == b"theirs"


def test_a_put_in_parts_returns_the_etag_of_its_own_write_though_another_replaces_it_at_once(
    uploads,
):
    uploads.store.lands_after_each_write = b"theirs"
    store = bucket("uploads")
    store._single_request_limit_bytes = 8
    store._part_size_bytes = 4

    landed = store.put("a.txt", b"0123456789abcde", content_type="text/plain")

    assert landed == ObjectInfo(
        key="a.txt",
        size=15,
        etag=fakebucket.etag_of(b"0123456789abcde"),
        content_type="text/plain",
        uploaded_at=None,
        metadata={},
    )
    assert uploads.store.objects["a.txt"].data == b"theirs"


@pytest.mark.asyncio
async def test_an_async_put_returns_the_etag_of_its_own_write_though_another_replaces_it_at_once(
    uploads,
):
    uploads.store.lands_after_each_write = b"theirs"

    landed = await bucket("uploads").put_async("a.txt", b"mine")

    assert landed.etag == fakebucket.etag_of(b"mine")
    assert landed.size == 4


@pytest.mark.parametrize("single_request_limit_bytes", [16, 8])
def test_a_put_makes_no_head_call(uploads, single_request_limit_bytes):
    store = bucket("uploads")
    store._single_request_limit_bytes = single_request_limit_bytes
    store._part_size_bytes = 4

    store.put("a.txt", b"0123456789abcde")
    with store.open("b.txt", "wb") as handle:
        handle.write(b"0123456789abcde")

    assert uploads.store.heads == []


def test_a_put_with_no_media_type_writes_and_answers_application_octet_stream(uploads):
    landed = bucket("uploads").put("a.bin", b"\x00\x01")

    assert landed.content_type == "application/octet-stream"
    assert uploads.store.objects["a.bin"].content_type == "application/octet-stream"


@pytest.mark.parametrize("single_request_limit_bytes", [16, 8])
def test_a_write_the_store_answers_without_its_etag_fails_naming_the_key(
    uploads, single_request_limit_bytes
):
    uploads.store.withhold_etag = True
    store = bucket("uploads")
    store._single_request_limit_bytes = single_request_limit_bytes
    store._part_size_bytes = 4

    with pytest.raises(OcelError) as raised:
        store.put("a.txt", b"0123456789abcde")
    assert str(raised.value) == 'the store answered the write of "a.txt" without its etag'


def test_a_get_never_answers_one_versions_info_with_another_versions_bytes(uploads):
    uploads.store.objects["a.txt"] = fakebucket.Stored(b"first", "text/plain", {})
    uploads.store.replace_before_reads = 1

    read = bucket("uploads").get("a.txt")

    assert read.bytes() == b"replacement 1"
    assert read.info.etag == fakebucket.etag_of(b"replacement 1")
    assert read.info.size == len(b"replacement 1")


@pytest.mark.asyncio
async def test_an_async_get_never_answers_one_versions_info_with_another_versions_bytes(uploads):
    uploads.store.objects["a.txt"] = fakebucket.Stored(b"first", "text/plain", {})
    uploads.store.replace_before_reads = 1

    read = await bucket("uploads").get_async("a.txt", range=(0, 11))

    assert await read.bytes() == b"replacement"
    assert read.info.etag == fakebucket.etag_of(b"replacement 1")


@pytest.mark.parametrize(
    "read",
    [
        lambda store: store.get("a.txt"),
        lambda store: store.get("a.txt", range=(0, 2)),
        lambda store: asyncio.run(store.get_async("a.txt")),
    ],
)
def test_a_get_of_an_object_replaced_under_every_read_fails_after_three_attempts(uploads, read):
    uploads.store.objects["a.txt"] = fakebucket.Stored(b"first", "text/plain", {})
    uploads.store.replace_before_reads = 99

    with pytest.raises(PreconditionFailed) as raised:
        read(bucket("uploads"))
    assert str(raised.value) == 'the object under "a.txt" changed while it was read'
    assert raised.value.key == "a.txt"
    assert uploads.store.heads == ["a.txt"] * 3


def test_a_get_the_store_answers_without_its_etag_fails_as_changed(uploads):
    uploads.store.objects["a.txt"] = fakebucket.Stored(b"first", "text/plain", {})
    uploads.store.withhold_etag = True

    with pytest.raises(PreconditionFailed):
        bucket("uploads").get("a.txt")


def test_a_signed_url_is_signed_for_someone_outside_the_runtime(uploads):
    uploads.store.objects["a.txt"] = fakebucket.Stored(b"hello", "text/plain", {})

    url = bucket("uploads").signed_url("a.txt", expires_in=timedelta(minutes=5), download="a.txt")

    assert url.startswith(uploads.url)
    key, operation, audience, expires = uploads.store.signed[0]
    assert key == "a.txt"
    assert operation is SignedOperation.GET
    assert audience is SignedAudience.EXTERNAL
    assert expires == 300
    assert "download=a.txt" in url


def test_a_signed_upload_includes_the_form_the_uploader_must_send(uploads):
    signed = bucket("uploads").signed_upload(
        "a.txt", expires_in=60, max_size=1024, content_type="text/plain"
    )

    assert signed.url.startswith(uploads.url)
    assert signed.method == "POST"
    assert signed.fields == {"key": "a.txt"}
    assert signed.headers == {"x-fake-signature": "signed"}
    _, operation, audience, _ = uploads.store.signed[0]
    assert operation is SignedOperation.POST_UPLOAD
    assert audience is SignedAudience.EXTERNAL


def test_a_public_object_is_addressed_under_the_address_the_deploy_delivered(uploads):
    assert bucket("uploads").public_url("a/b.txt") == "https://storage.example/uploads/a/b.txt"


def test_a_public_url_escapes_what_a_key_segment_may_contain(uploads):
    assert bucket("uploads").public_url("a b/c#d?e.png") == (
        "https://storage.example/uploads/a%20b/c%23d%3Fe.png"
    )


def test_a_bucket_with_no_public_address_says_what_would_give_it_one(uploads, monkeypatch):
    monkeypatch.setenv(
        "OCEL_RESOURCE_BUCKET_uploads",
        json.dumps({"name": "uploads", "bucket": {"bucket": "shop-prod-uploads"}}),
    )

    with pytest.raises(OcelError) as raised:
        bucket("uploads").public_url("a.txt")
    assert str(raised.value) == (
        'this bucket has no public address, so "a.txt" has no public url: declare the '
        "bucket with public=True and give the project a domain to serve it from"
    )


@pytest.mark.asyncio
async def test_bytes_written_by_the_async_twin_come_back_as_they_went_in(uploads):
    store = bucket("uploads")

    landed = await store.put_async("a.txt", b"hello", content_type="text/plain")

    assert landed.size == 5
    read = await store.get_async("a.txt")
    assert await read.text() == "hello"


@pytest.mark.asyncio
async def test_the_async_twins_answer_for_the_keys_the_bucket_has(uploads):
    store = bucket("uploads")
    await store.put_async("kept/a", b"1")
    await store.put_async("kept/b", b"2")

    assert (await store.head_async("kept/a")).size == 1
    assert await store.exists_async("kept/b") is True
    assert [info.key async for info in store.list_async(prefix="kept/")] == ["kept/a", "kept/b"]

    await store.copy_async("kept/a", "kept/c")
    await store.delete_async("kept/a", "gone.txt")

    assert await store.head_async("kept/a") is None
    assert await store.exists_async("kept/c") is True


@pytest.mark.asyncio
async def test_an_async_read_of_a_key_the_bucket_does_not_have_names_what_was_missing(uploads):
    with pytest.raises(ObjectNotFound):
        await bucket("uploads").get_async("gone.txt")


@pytest.mark.asyncio
async def test_an_async_body_too_big_for_one_request_goes_up_in_parts(uploads):
    store = bucket("uploads")
    store._single_request_limit_bytes = 8
    store._part_size_bytes = 4

    await store.put_async("a.txt", b"0123456789abcde")

    assert uploads.store.objects["a.txt"].data == b"0123456789abcde"
    assert uploads.store.uploads == {}


@pytest.mark.asyncio
async def test_an_async_part_the_store_refuses_abandons_the_whole_write(uploads):
    uploads.store.refuse_part = 2
    store = bucket("uploads")
    store._single_request_limit_bytes = 8
    store._part_size_bytes = 4

    with pytest.raises(OcelError):
        await store.put_async("a.txt", b"0123456789abcde")
    assert uploads.store.aborted == ["upload-1"]
    assert "a.txt" not in uploads.store.objects


@pytest.mark.asyncio
async def test_an_async_write_that_must_not_replace_refuses_a_key_the_bucket_has(uploads):
    store = bucket("uploads")
    await store.put_async("a.txt", b"first")

    with pytest.raises(PreconditionFailed):
        await store.put_async("a.txt", b"second", if_not_exists=True)


@pytest.mark.asyncio
async def test_an_object_opened_async_for_writing_lands_when_it_is_closed(uploads):
    store = bucket("uploads")

    async with store.open_async("a.txt", "wb") as handle:
        await handle.write(b"hello ")
        await handle.write(b"world")

    assert uploads.store.objects["a.txt"].data == b"hello world"

    async with store.open_async("a.txt", "rb") as handle:
        assert await handle.read(5) == b"hello"
        assert await handle.read() == b" world"


@pytest.mark.asyncio
async def test_an_async_signed_upload_includes_the_form_the_uploader_must_send(uploads):
    signed = await bucket("uploads").signed_upload_async("a.txt", max_size=1024)

    assert signed.method == "POST"
    assert signed.fields == {"key": "a.txt"}
    assert (await bucket("uploads").signed_url_async("a.txt")).startswith(uploads.url)


class _ThreadRecordingFile(io.BytesIO):
    def __init__(self, data: bytes):
        super().__init__(data)
        self.read_on: set[int] = set()

    def read(self, size=-1):
        self.read_on.add(threading.get_ident())
        return super().read(size)


@pytest.mark.asyncio
async def test_an_async_put_reads_a_file_body_off_the_event_loop(uploads):
    source = _ThreadRecordingFile(b"0123456789abcde")
    store = bucket("uploads")
    store._part_size_bytes = 4

    await store.put_async("a.txt", source)

    assert uploads.store.objects["a.txt"].data == b"0123456789abcde"
    assert source.read_on
    assert threading.get_ident() not in source.read_on


@pytest.mark.parametrize(
    "operation",
    [
        lambda store: store.head("a.txt"),
        lambda store: store.delete("a.txt"),
        lambda store: list(store.list(prefix="kept/")),
        lambda store: asyncio.run(store.head_async("a.txt")),
    ],
)
def test_a_call_the_runtime_refuses_raises_an_ocel_error_that_keeps_its_code(uploads, operation):
    uploads.store.refusal = ConnectError(Code.UNAVAILABLE, "the runtime is restarting")

    with pytest.raises(RuntimeRefusedError) as raised:
        operation(bucket("uploads"))

    assert raised.value.code == "unavailable"
    assert "the runtime is restarting" in str(raised.value)
    assert isinstance(raised.value, OcelError)
    assert not isinstance(raised.value, ConnectError)
