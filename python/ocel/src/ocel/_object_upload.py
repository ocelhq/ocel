import os
from collections.abc import Generator, Iterator, Mapping
from dataclasses import dataclass
from typing import TYPE_CHECKING, Any, BinaryIO, TypeVar

from ocel._error import OcelError, RuntimeRefusedError
from ocel._object import ObjectInfo, read_etag, refine_refusal, refuse_status
from ocel._object_http import ObjectHttp
from ocel._runtime import Operation, Session
from ocel.gen.app.bucket.v1.bucket_connect import BucketServiceClient
from ocel.gen.app.bucket.v1.bucket_pb import (
    AbortMultipartRequest,
    CompletedPart,
    CompleteMultipartRequest,
    CreateMultipartRequest,
    PresignedTarget,
    SignConstraints,
    SignedAudience,
    SignedOperation,
    SignedPart,
    SignPartsRequest,
)

if TYPE_CHECKING:
    from ocel.bucket import Bucket

_PARTS_IN_FLIGHT = 4
_DEFAULT_CONTENT_TYPE = "application/octet-stream"

ObjectSession = Session[BucketServiceClient, ObjectHttp[Any, Any]]
Body = bytes | str | BinaryIO | os.PathLike[str]
T = TypeVar("T")


@dataclass
class WriteOptions:
    content_type: str | None = None
    cache_control: str | None = None
    metadata: Mapping[str, str] | None = None
    if_not_exists: bool = False
    if_match: str | None = None

    @property
    def if_none_match(self) -> str:
        return "*" if self.if_not_exists else ""

    @property
    def written_content_type(self) -> str:
        return self.content_type or _DEFAULT_CONTENT_TYPE

    @classmethod
    def take(cls, opener: str, given: dict[str, object]) -> "WriteOptions":
        options = cls()
        for name, value in given.items():
            if name not in cls.__dataclass_fields__:
                raise TypeError(f"{opener}() got an unexpected keyword argument '{name}'")
            setattr(options, name, value)
        return options


class Upload:
    def __init__(self, store: "Bucket", session: ObjectSession, key: str, options: WriteOptions):
        self._store = store
        self._session = session
        self._bucket = store._read_properties().bucket
        self._key = key
        self._options = options
        self._buffered = bytearray()
        self._pending: list[tuple[int, bytes]] = []
        self._completed: list[CompletedPart] = []
        self._upload_id = ""
        self._next = 1
        self._written = 0
        self._closed = False
        self.info: ObjectInfo | None = None

    async def write(self, data: bytes) -> None:
        self._refuse_closed()
        self._buffered += data
        self._written += len(data)
        await self._run_or_discard(self._drain_buffer(False))

    async def close(self) -> None:
        if not self._closed:
            await self.land()

    async def land(self) -> ObjectInfo:
        self._refuse_closed()
        self._closed = True
        if not self._upload_id and len(self._buffered) <= self._store._single_request_limit_bytes:
            etag = await self._run_or_discard(self._put_whole_object())
        else:
            await self._run_or_discard(self._drain_buffer(True))
            etag = await self._run_or_discard(self._complete_upload())
        if not etag:
            raise OcelError(f'the store answered the write of "{self._key}" without its etag')
        options = self._options
        self.info = ObjectInfo(
            key=self._key,
            size=self._written,
            etag=etag,
            content_type=options.written_content_type,
            metadata=dict(options.metadata or {}),
        )
        return self.info

    async def abort(self) -> None:
        self._closed = True
        await self._discard_upload()

    def _refuse_closed(self) -> None:
        if self._closed:
            raise ValueError(f'the writer for "{self._key}" is closed')

    async def _run_or_discard(self, work: Operation[T]) -> T:
        try:
            return await work
        except BaseException:
            await self._discard_upload()
            raise

    async def _discard_upload(self) -> None:
        if not self._upload_id:
            return
        upload_id, self._upload_id = self._upload_id, ""
        await self._session.client.abort_multipart(
            AbortMultipartRequest(bucket=self._bucket, key=self._key, upload_id=upload_id)
        )

    async def _drain_buffer(self, final: bool) -> None:
        if not self._upload_id:
            if final or len(self._buffered) <= self._store._single_request_limit_bytes:
                return
            await self._begin_upload()
        part_size = self._store._part_size_bytes
        while len(self._buffered) >= part_size:
            self._stage_part(bytes(self._buffered[:part_size]))
            del self._buffered[:part_size]
            if len(self._pending) == _PARTS_IN_FLIGHT:
                await self._flush_parts()
        if not final:
            return
        if self._buffered:
            self._stage_part(bytes(self._buffered))
            self._buffered.clear()
        await self._flush_parts()

    async def _begin_upload(self) -> None:
        options = self._options
        try:
            response = await self._session.client.create_multipart(
                CreateMultipartRequest(
                    bucket=self._bucket,
                    key=self._key,
                    content_type=options.written_content_type,
                    cache_control=options.cache_control or "",
                    metadata=dict(options.metadata or {}),
                )
            )
        except RuntimeRefusedError as refused:
            raise refine_refusal(self._key, refused) from None
        self._upload_id = response.upload_id

    def _stage_part(self, data: bytes) -> None:
        self._pending.append((self._next, data))
        self._next += 1

    async def _flush_parts(self) -> None:
        if not self._pending:
            return
        try:
            response = await self._session.client.sign_parts(
                SignPartsRequest(
                    bucket=self._bucket,
                    key=self._key,
                    upload_id=self._upload_id,
                    part_numbers=[number for number, _ in self._pending],
                    audience=SignedAudience.INTERNAL,
                )
            )
        except RuntimeRefusedError as refused:
            raise refine_refusal(self._key, refused) from None
        targets = {part.part_number: part for part in response.parts}
        pending, self._pending = self._pending, []
        missing = sorted({number for number, _ in pending} - set(targets))
        if missing:
            raise OcelError(f'the runtime signed no url for part {missing[0]} of "{self._key}"')
        self._completed.extend(
            await self._session.gather(
                [self._send_part(targets[number], number, data) for number, data in pending]
            )
        )

    async def _send_part(self, target: SignedPart, number: int, data: bytes) -> CompletedPart:
        response = await self._session.http.put(target.url, dict(target.headers), data)
        if response.status >= 300:
            raise OcelError(f'part {number} of "{self._key}" was refused ({response.status})')
        return CompletedPart(part_number=number, etag=response.headers.get("etag", ""))

    async def _complete_upload(self) -> str:
        self._completed.sort(key=lambda part: part.part_number)
        upload_id, self._upload_id = self._upload_id, ""
        try:
            response = await self._session.client.complete_multipart(
                CompleteMultipartRequest(
                    bucket=self._bucket,
                    key=self._key,
                    upload_id=upload_id,
                    parts=self._completed,
                    if_none_match=self._options.if_none_match,
                    if_match=self._options.if_match or "",
                )
            )
        except RuntimeRefusedError as refused:
            self._upload_id = upload_id
            raise refine_refusal(self._key, refused) from None
        return response.etag

    async def _put_whole_object(self) -> str:
        options = self._options
        target = await self._store._sign(
            self._session,
            self._key,
            SignedOperation.PUT,
            SignedAudience.INTERNAL,
            SignConstraints(
                content_type=options.written_content_type,
                if_none_match=options.if_none_match,
                if_match=options.if_match or "",
                cache_control=options.cache_control or "",
                metadata=dict(options.metadata or {}),
            ),
        )
        response = await self._session.http.put(
            target.url,
            _build_put_headers(target, options),
            bytes(self._buffered),
            target.method or "PUT",
        )
        if response.status >= 300:
            raise refuse_status(self._key, response.status) or OcelError(
                f'writing "{self._key}" was refused ({response.status})'
            )
        return read_etag(response.headers, target)


def _build_put_headers(target: PresignedTarget, options: WriteOptions) -> dict[str, str]:
    headers = dict(target.headers)
    headers["content-type"] = options.written_content_type
    if options.cache_control:
        headers["cache-control"] = options.cache_control
    if options.if_none_match:
        headers["if-none-match"] = options.if_none_match
    if options.if_match:
        headers["if-match"] = options.if_match
    return headers


def read_next_chunk(chunks: Iterator[bytes]) -> bytes | None:
    return next(chunks, None)


def read_chunks(data: Body, size: int) -> Generator[bytes]:
    if isinstance(data, str):
        yield data.encode()
        return
    if isinstance(data, (bytes, bytearray, memoryview)):
        yield bytes(data)
        return
    if isinstance(data, os.PathLike):
        with open(data, "rb") as handle:
            yield from read_chunks(handle, size)
        return
    while True:
        chunk = data.read(size)
        if not chunk:
            return
        yield chunk
