import json
import os
from datetime import datetime, timedelta, timezone

import fakebucket
import pytest

from ocel import (
    AsyncBucket,
    ObjectInfo,
    ObjectNotFound,
    PreconditionFailed,
    SyncBucket,
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


def test_what_the_bucket_knows_about_an_object_it_holds(uploads):
    uploads.store.objects["a.txt"] = fakebucket.Held(b"hello", "text/plain", {"by": "me"})

    held = bucket("uploads").head("a.txt")

    assert held is not None
    assert held.key == "a.txt"
    assert held.size == 5
    assert held.content_type == "text/plain"
    assert held.metadata == {"by": "me"}
    assert held.etag
    assert held.uploaded_at == datetime(2023, 11, 14, 22, 13, 20, tzinfo=timezone.utc)


def test_a_key_the_bucket_does_not_hold_is_known_as_nothing(uploads):
    assert bucket("uploads").head("gone.txt") is None
    assert bucket("uploads").exists("gone.txt") is False


def test_every_call_carries_the_runtime_session_token(uploads):
    bucket("uploads").head("a.txt")

    assert uploads.store.authorizations == ["Bearer letmein"]


def test_deleting_a_key_the_bucket_does_not_hold_is_not_an_error(uploads):
    uploads.store.objects["a.txt"] = fakebucket.Held(b"hello", "text/plain", {})

    bucket("uploads").delete("a.txt", "gone.txt")

    assert uploads.store.objects == {}


def test_copying_an_object_the_bucket_does_not_hold_names_what_was_missing(uploads):
    with pytest.raises(ObjectNotFound) as raised:
        bucket("uploads").copy("gone.txt", "kept.txt")
    assert str(raised.value) == 'the bucket holds no object under "gone.txt"'
    assert isinstance(raised.value, FileNotFoundError)


def test_a_copy_lands_under_the_destination_key(uploads):
    uploads.store.objects["a.txt"] = fakebucket.Held(b"hello", "text/plain", {})

    held = bucket("uploads").copy("a.txt", "b.txt")

    assert held.key == "b.txt"
    assert uploads.store.objects["b.txt"].data == b"hello"


def test_a_listing_walks_every_page_of_the_prefix_it_was_given(uploads):
    for key in ["kept/a", "kept/b", "kept/c", "other/d"]:
        uploads.store.objects[key] = fakebucket.Held(b"x", "", {})

    listed = list(bucket("uploads").list(prefix="kept/"))

    assert [held.key for held in listed] == ["kept/a", "kept/b", "kept/c"]
    assert [asked[2] for asked in uploads.store.listed] == ["", "kept/c"]


def test_bytes_written_under_a_key_come_back_as_they_went_in(uploads):
    held = bucket("uploads").put("a.txt", b"hello")

    assert held.key == "a.txt"
    assert held.size == 5
    assert bucket("uploads").get("a.txt").bytes() == b"hello"


def test_text_written_under_a_key_comes_back_decoded(uploads):
    bucket("uploads").put("a.txt", "héllo")

    assert bucket("uploads").get("a.txt").text() == "héllo"


def test_a_file_written_by_path_carries_its_bytes(uploads, tmp_path):
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


def test_a_write_carries_the_media_type_and_metadata_it_was_given(uploads):
    bucket("uploads").put(
        "a.txt",
        b"hello",
        content_type="text/plain",
        cache_control="public, max-age=60",
        metadata={"by": "me"},
    )

    held = uploads.store.objects["a.txt"]
    assert held.content_type == "text/plain"
    assert held.cache_control == "public, max-age=60"
    assert held.metadata == {"by": "me"}


def test_a_write_that_must_not_replace_refuses_a_key_the_bucket_holds(uploads):
    bucket("uploads").put("a.txt", b"first")

    with pytest.raises(PreconditionFailed) as raised:
        bucket("uploads").put("a.txt", b"second", if_not_exists=True)
    assert str(raised.value) == (
        'the object under "a.txt" did not meet the condition this write carried'
    )
    assert uploads.store.objects["a.txt"].data == b"first"


def test_a_write_conditioned_on_a_version_refuses_another(uploads):
    bucket("uploads").put("a.txt", b"first")

    with pytest.raises(PreconditionFailed):
        bucket("uploads").put("a.txt", b"second", if_match='"not-the-etag"')


def test_a_write_conditioned_on_the_version_it_holds_goes_through(uploads):
    held = bucket("uploads").put("a.txt", b"first")

    bucket("uploads").put("a.txt", b"second", if_match=held.etag)

    assert uploads.store.objects["a.txt"].data == b"second"


def test_reading_a_key_the_bucket_does_not_hold_names_what_was_missing(uploads):
    with pytest.raises(ObjectNotFound) as raised:
        bucket("uploads").get("gone.txt")
    assert str(raised.value) == 'the bucket holds no object under "gone.txt"'


def test_a_range_reads_the_bytes_it_names(uploads):
    bucket("uploads").put("a.txt", b"hello world")

    assert bucket("uploads").get("a.txt", range=(6, 5)).bytes() == b"world"
    assert bucket("uploads").get("a.txt", range=(6, None)).bytes() == b"world"


def test_an_object_is_read_a_chunk_at_a_time(uploads):
    bucket("uploads").put("a.txt", b"hello world")

    read = bucket("uploads").get("a.txt")

    assert b"".join(read) == b"hello world"
    assert read.info.size == 11


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


def test_a_body_too_big_for_one_request_goes_up_in_parts(uploads):
    store = bucket("uploads")
    store._single_ceiling = 8
    store._part_size = 4

    held = store.put("a.txt", b"0123456789abcde", content_type="text/plain")

    assert held.size == 15
    assert uploads.store.objects["a.txt"].data == b"0123456789abcde"
    assert uploads.store.objects["a.txt"].content_type == "text/plain"
    assert uploads.store.uploads == {}


def test_a_part_the_store_refuses_abandons_the_whole_write(uploads):
    uploads.store.refuse_part = 2
    store = bucket("uploads")
    store._single_ceiling = 8
    store._part_size = 4

    with pytest.raises(RuntimeError) as raised:
        store.put("a.txt", b"0123456789abcde")
    assert 'part 2 of "a.txt" was refused' in str(raised.value)
    assert uploads.store.aborted == ["upload-1"]
    assert "a.txt" not in uploads.store.objects


def test_a_write_in_parts_that_must_not_replace_is_refused(uploads):
    store = bucket("uploads")
    store._single_ceiling = 8
    store._part_size = 4
    store.put("a.txt", b"first")

    with pytest.raises(PreconditionFailed):
        store.put("a.txt", b"0123456789abcde", if_not_exists=True)
    assert uploads.store.objects["a.txt"].data == b"first"


def test_a_signed_url_is_signed_for_someone_outside_the_runtime(uploads):
    uploads.store.objects["a.txt"] = fakebucket.Held(b"hello", "text/plain", {})

    url = bucket("uploads").signed_url("a.txt", expires_in=timedelta(minutes=5), download="a.txt")

    assert url.startswith(uploads.url)
    key, operation, audience, expires = uploads.store.signed[0]
    assert key == "a.txt"
    assert operation is SignedOperation.GET
    assert audience is SignedAudience.EXTERNAL
    assert expires == 300
    assert "download=a.txt" in url


def test_a_signed_upload_carries_the_form_the_uploader_must_send(uploads):
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


def test_a_public_url_escapes_what_a_key_segment_may_hold(uploads):
    assert bucket("uploads").public_url("a b/c#d?e.png") == (
        "https://storage.example/uploads/a%20b/c%23d%3Fe.png"
    )


def test_a_bucket_with_no_public_address_says_what_would_give_it_one(uploads, monkeypatch):
    monkeypatch.setenv(
        "OCEL_RESOURCE_BUCKET_uploads",
        json.dumps({"name": "uploads", "bucket": {"bucket": "shop-prod-uploads"}}),
    )

    with pytest.raises(RuntimeError) as raised:
        bucket("uploads").public_url("a.txt")
    assert str(raised.value) == (
        'this bucket carries no public address, so "a.txt" has no public url: declare the '
        "bucket with public=True and give the project a domain to serve it from"
    )


def test_the_handle_answers_to_the_protocols_a_fake_is_written_against(uploads):
    store = bucket("uploads")

    assert isinstance(store, SyncBucket)
    assert isinstance(store, AsyncBucket)


def test_a_fake_that_answers_like_a_bucket_satisfies_the_sync_protocol():
    class FakeBucket:
        name = "uploads"

        def put(self, key, data, **options):
            return ObjectInfo(key=key, size=0, etag="", content_type="")

        def get(self, key, **options): ...
        def open(self, key, mode="rb", **options): ...
        def head(self, key): ...
        def exists(self, key): ...
        def delete(self, *keys): ...
        def copy(self, src, dst): ...
        def list(self, **options): ...
        def signed_url(self, key, **options): ...
        def signed_upload(self, key, **options): ...
        def public_url(self, key): ...

    assert isinstance(FakeBucket(), SyncBucket)
    assert not isinstance(FakeBucket(), AsyncBucket)


@pytest.mark.asyncio
async def test_bytes_written_by_the_async_twin_come_back_as_they_went_in(uploads):
    store = bucket("uploads")

    held = await store.put_async("a.txt", b"hello", content_type="text/plain")

    assert held.size == 5
    read = await store.get_async("a.txt")
    assert await read.text() == "hello"


@pytest.mark.asyncio
async def test_the_async_twins_answer_for_the_keys_the_bucket_holds(uploads):
    store = bucket("uploads")
    await store.put_async("kept/a", b"1")
    await store.put_async("kept/b", b"2")

    assert (await store.head_async("kept/a")).size == 1
    assert await store.exists_async("kept/b") is True
    assert [held.key async for held in store.list_async(prefix="kept/")] == ["kept/a", "kept/b"]

    await store.copy_async("kept/a", "kept/c")
    await store.delete_async("kept/a", "gone.txt")

    assert await store.head_async("kept/a") is None
    assert await store.exists_async("kept/c") is True


@pytest.mark.asyncio
async def test_an_async_read_of_a_key_the_bucket_does_not_hold_names_what_was_missing(uploads):
    with pytest.raises(ObjectNotFound):
        await bucket("uploads").get_async("gone.txt")


@pytest.mark.asyncio
async def test_an_async_body_too_big_for_one_request_goes_up_in_parts(uploads):
    store = bucket("uploads")
    store._single_ceiling = 8
    store._part_size = 4

    await store.put_async("a.txt", b"0123456789abcde")

    assert uploads.store.objects["a.txt"].data == b"0123456789abcde"
    assert uploads.store.uploads == {}


@pytest.mark.asyncio
async def test_an_async_part_the_store_refuses_abandons_the_whole_write(uploads):
    uploads.store.refuse_part = 2
    store = bucket("uploads")
    store._single_ceiling = 8
    store._part_size = 4

    with pytest.raises(RuntimeError):
        await store.put_async("a.txt", b"0123456789abcde")
    assert uploads.store.aborted == ["upload-1"]
    assert "a.txt" not in uploads.store.objects


@pytest.mark.asyncio
async def test_an_async_write_that_must_not_replace_refuses_a_key_the_bucket_holds(uploads):
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
async def test_an_async_signed_upload_carries_the_form_the_uploader_must_send(uploads):
    signed = await bucket("uploads").signed_upload_async("a.txt", max_size=1024)

    assert signed.method == "POST"
    assert signed.fields == {"key": "a.txt"}
    assert (await bucket("uploads").signed_url_async("a.txt")).startswith(uploads.url)
