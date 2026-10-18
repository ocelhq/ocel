import inspect
import os
from collections.abc import AsyncIterator, Callable, Generator, Iterator, Mapping, Sequence
from contextlib import AbstractAsyncContextManager, AbstractContextManager
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from functools import partial
from typing import IO, BinaryIO, Protocol, TypeVar, cast
from urllib.parse import quote

from protobuf import Oneof
from pyqwest import Client, Headers, Response, SyncClient, SyncResponse

from ocel._binding import read_bucket_binding, refuse_unprovisioned
from ocel._declare import declare, is_discovering
from ocel._error import OcelError, RuntimeRefusedError
from ocel._object_files import AsyncReadStream, AsyncWriteStream, ReadStream, WriteStream
from ocel._runtime import AwaitedCalls, Operation, RuntimeConnection, Session
from ocel._wire import encode_duration
from ocel.gen.app.bucket.v1.bucket_connect import (
    BucketServiceClient,
    BucketServiceClientSync,
)
from ocel.gen.app.bucket.v1.bucket_pb import (
    AbortMultipartRequest,
    CompletedPart,
    CompleteMultipartRequest,
    CopyRequest,
    CreateMultipartRequest,
    DeleteRequest,
    HeadRequest,
    ListRequest,
    PresignedTarget,
    SignConstraints,
    SignedAudience,
    SignedOperation,
    SignedPart,
    SignPartsRequest,
    SignRequest,
)
from ocel.gen.app.bucket.v1.bucket_pb import (
    ObjectInfo as WireObjectInfo,
)
from ocel.gen.app.resources.v1.resources_pb import (
    BucketConfig,
    DeclareRequest,
    ResourceIdentifier,
    ResourceType,
)
from ocel.gen.common.bindings.v1.bindings_pb import BucketProperties

_KIND = "bucket"
_SINGLE_REQUEST_LIMIT_BYTES = 16 << 20
_PART_SIZE_BYTES = 8 << 20
_PARTS_IN_FLIGHT = 4
_READ_ATTEMPTS = 3
_DEFAULT_CONTENT_TYPE = "application/octet-stream"

_Session = Session[BucketServiceClient]
_Body = bytes | str | BinaryIO | os.PathLike[str]


class _Answered(Protocol):
    @property
    def status(self) -> int: ...

    @property
    def headers(self) -> Headers: ...


_Stream = TypeVar("_Stream")
_Answer = TypeVar("_Answer", bound=_Answered)
T = TypeVar("T")


class ObjectNotFound(OcelError, FileNotFoundError):
    """Raised when an operation that cannot answer with ``None`` names an object the bucket
    does not have."""

    #: The key that named nothing.
    key: str

    def __init__(self, key: str):
        super().__init__(f'the bucket has no object under "{key}"')
        self.key = key

    def __str__(self) -> str:
        return f'the bucket has no object under "{self.key}"'


class PreconditionFailed(OcelError):
    """Raised when a write set ``if_not_exists`` or ``if_match`` and the object did not
    meet it, or when the object changed under every attempt to read one version of it."""

    #: The key whose current state refused the write or the read.
    key: str

    def __init__(self, key: str, message: str | None = None):
        super().__init__(
            message or f'the object under "{key}" did not meet the condition this write set'
        )
        self.key = key


@dataclass(frozen=True)
class ObjectInfo:
    """What a bucket knows about one of its objects."""

    #: The key the object is addressed by.
    key: str
    #: The object's length in bytes.
    size: int
    #: The store's opaque version tag for these bytes.
    etag: str
    #: The media type the object was written with.
    content_type: str
    #: When the object last took its current bytes, as the store reports it. It is
    #: ``None`` on the info a write returns, which describes the write itself.
    uploaded_at: datetime | None = None
    #: The user metadata written alongside the object.
    metadata: Mapping[str, str] = field(default_factory=dict[str, str])


@dataclass(frozen=True)
class SignedUpload:
    """A target someone without a credential writes one object through, for as long as it
    stays valid."""

    #: Where the body is sent.
    url: str
    #: The HTTP method to send it with.
    method: str
    #: The headers the signature covers, which the caller must send unchanged.
    headers: Mapping[str, str] = field(default_factory=dict[str, str])
    #: The form fields a POST upload sends with the body, which are empty for a PUT target.
    fields: Mapping[str, str] = field(default_factory=dict[str, str])
    #: When the target stops being valid, or ``None`` when its lifetime was left to the
    #: runtime.
    expires: datetime | None = None


class Bucket:
    """A bucket an app declares and reads and writes its objects through. Every operation
    that reaches the store has a blocking form and an awaited ``_async`` form."""

    #: The name the bucket was declared under, and the name its binding is delivered as.
    name: str

    def __init__(self, name: str):
        """Take the handle for the bucket named ``name``. Prefer :func:`bucket`, which
        declares the bucket as well as handing back its handle."""
        self.name = name
        self._properties: BucketProperties | None = None
        self._runtime = RuntimeConnection(
            f'bucket("{name}")',
            lambda address: BucketServiceClientSync(address, send_compression=None),
            lambda address: BucketServiceClient(address, send_compression=None),
            self._read_properties,
        )
        self._http = SyncClient()
        self._http_async = Client()
        self._single_request_limit_bytes = _SINGLE_REQUEST_LIMIT_BYTES
        self._part_size_bytes = _PART_SIZE_BYTES

    def put(
        self,
        key: str,
        data: _Body,
        *,
        content_type: str | None = None,
        cache_control: str | None = None,
        metadata: Mapping[str, str] | None = None,
        if_not_exists: bool = False,
        if_match: str | None = None,
    ) -> ObjectInfo:
        """Write ``data`` as the whole object under ``key`` and return the info of the
        version this write stored: its etag is the one the store answered this write with,
        even when another write replaces the object at once, and its ``uploaded_at`` is
        ``None``. A body larger than one request can send is written in parts. The object
        takes ``content_type``, or ``application/octet-stream`` when none is given. With
        ``if_not_exists`` the write goes through only while the bucket has nothing under
        the key, and with ``if_match`` only while the object under it has that etag;
        either unmet raises :class:`PreconditionFailed`."""
        options = _WriteOptions(content_type, cache_control, metadata, if_not_exists, if_match)
        return self._runtime.run("put", partial(self._put, key=key, data=data, options=options))

    async def put_async(
        self,
        key: str,
        data: _Body,
        *,
        content_type: str | None = None,
        cache_control: str | None = None,
        metadata: Mapping[str, str] | None = None,
        if_not_exists: bool = False,
        if_match: str | None = None,
    ) -> ObjectInfo:
        """What :meth:`put` does, awaited, reading a file or path body off the event loop."""
        options = _WriteOptions(content_type, cache_control, metadata, if_not_exists, if_match)
        return await self._runtime.run_async(
            "put_async", partial(self._put, key=key, data=data, options=options)
        )

    def get(self, key: str, *, range: tuple[int, int | None] | None = None) -> "ObjectBody":
        """Read the object under ``key``, or the ``(offset, length)`` byte range of it that
        ``range`` names, with a ``length`` of ``None`` reading to the end. The body's info
        describes the version its bytes come from: a read the object changes under is
        tried again, and raises :class:`PreconditionFailed` when it changes under every
        attempt. Raises :class:`ObjectNotFound` when the bucket has none."""
        info, stream, response = self._runtime.run(
            "get",
            lambda session: self._read(
                session,
                key=key,
                byte_range=range,
                send=partial(_send_read, self._http),
                release=_release_read,
            ),
        )
        return ObjectBody(info, stream, response)

    async def get_async(
        self, key: str, *, range: tuple[int, int | None] | None = None
    ) -> "AsyncObjectBody":
        """What :meth:`get` does, awaited."""
        info, stream, response = await self._runtime.run_async(
            "get_async",
            lambda session: self._read(
                session,
                key=key,
                byte_range=range,
                send=partial(_send_read_async, self._http_async),
                release=_release_read_async,
            ),
        )
        return AsyncObjectBody(info, stream, response)

    def open(self, key: str, mode: str = "rb", **write_options: object) -> IO[bytes]:
        """Open the object under ``key`` as a binary file. ``"rb"`` reads it and ``"wb"``
        writes it, taking the keyword options :meth:`put` takes and landing the object when
        the handle closes."""
        if mode == "rb":
            return cast(IO[bytes], ReadStream(self.get(key)))
        if mode == "wb":
            options = _WriteOptions.take("open", write_options)
            writer = Writer(self, self._runtime.session("put", True), key, options)
            return cast(IO[bytes], WriteStream(writer))
        raise ValueError(f"a bucket object opens as 'rb' or 'wb', not {mode!r}")

    def open_async(
        self, key: str, mode: str = "rb", **write_options: object
    ) -> "AsyncReadStream | AsyncWriteStream":
        """What :meth:`open` does, as an async context manager whose ``read`` and ``write``
        are awaited."""
        if mode == "rb":
            return AsyncReadStream(self, key)
        if mode == "wb":
            options = _WriteOptions.take("open_async", write_options)
            writer = Writer(self, self._runtime.session("put_async", False), key, options)
            return AsyncWriteStream(writer)
        raise ValueError(f"a bucket object opens as 'rb' or 'wb', not {mode!r}")

    def head(self, key: str) -> ObjectInfo | None:
        """What the bucket knows about the object under ``key``, or ``None`` when it has
        none."""
        return self._runtime.run("head", partial(self._head, key=key))

    async def head_async(self, key: str) -> ObjectInfo | None:
        """What :meth:`head` does, awaited."""
        return await self._runtime.run_async("head_async", partial(self._head, key=key))

    def exists(self, key: str) -> bool:
        """Whether the bucket has an object under ``key``."""
        return self.head(key) is not None

    async def exists_async(self, key: str) -> bool:
        """What :meth:`exists` does, awaited."""
        return await self.head_async(key) is not None

    def delete(self, *keys: str) -> None:
        """Remove the objects under ``keys``. A key with no object under it is not
        an error."""
        if keys:
            self._runtime.run("delete", partial(self._delete, keys=keys))

    async def delete_async(self, *keys: str) -> None:
        """What :meth:`delete` does, awaited."""
        if keys:
            await self._runtime.run_async("delete_async", partial(self._delete, keys=keys))

    def copy(self, src: str, dst: str) -> ObjectInfo:
        """Copy the object under ``src`` to ``dst`` within the same bucket, and return the
        object that landed. Raises :class:`ObjectNotFound` when the bucket has nothing
        under ``src``."""
        return self._runtime.run("copy", partial(self._copy, src=src, dst=dst))

    async def copy_async(self, src: str, dst: str) -> ObjectInfo:
        """What :meth:`copy` does, awaited."""
        return await self._runtime.run_async("copy_async", partial(self._copy, src=src, dst=dst))

    def list(self, *, prefix: str | None = None, limit: int | None = None) -> Iterator[ObjectInfo]:
        """Walk the bucket's objects under ``prefix``, a page at a time, yielding at most
        ``limit`` objects per page."""
        cursor = ""
        while True:
            page, cursor = self._runtime.run(
                "list", partial(self._list_page, prefix=prefix, limit=limit, cursor=cursor)
            )
            yield from page
            if not cursor:
                return

    async def list_async(
        self, *, prefix: str | None = None, limit: int | None = None
    ) -> AsyncIterator[ObjectInfo]:
        """What :meth:`list` does, walked with ``async for``."""
        cursor = ""
        while True:
            page, cursor = await self._runtime.run_async(
                "list_async", partial(self._list_page, prefix=prefix, limit=limit, cursor=cursor)
            )
            for info in page:
                yield info
            if not cursor:
                return

    def signed_url(
        self,
        key: str,
        *,
        expires_in: float | timedelta | None = None,
        download: bool | str | None = None,
    ) -> str:
        """A url that reads the object under ``key`` without a credential, for as long as
        it stays valid. ``download`` serves the object as a download, under the filename it
        names when it is a string."""
        return self._runtime.run("signed_url", self._sign_read(key, expires_in, download)).url

    async def signed_url_async(
        self,
        key: str,
        *,
        expires_in: float | timedelta | None = None,
        download: bool | str | None = None,
    ) -> str:
        """What :meth:`signed_url` does, awaited."""
        sign = self._sign_read(key, expires_in, download)
        return (await self._runtime.run_async("signed_url_async", sign)).url

    def signed_upload(
        self,
        key: str,
        *,
        expires_in: float | timedelta | None = None,
        max_size: int | None = None,
        content_type: str | None = None,
    ) -> SignedUpload:
        """A target that writes the object under ``key`` without a credential, for as long
        as it stays valid, accepting at most ``max_size`` bytes of ``content_type``."""
        sign = self._sign_upload(key, expires_in, max_size, content_type)
        return _build_signed_upload(self._runtime.run("signed_upload", sign), expires_in)

    async def signed_upload_async(
        self,
        key: str,
        *,
        expires_in: float | timedelta | None = None,
        max_size: int | None = None,
        content_type: str | None = None,
    ) -> SignedUpload:
        """What :meth:`signed_upload` does, awaited."""
        sign = self._sign_upload(key, expires_in, max_size, content_type)
        target = await self._runtime.run_async("signed_upload_async", sign)
        return _build_signed_upload(target, expires_in)

    def public_url(self, key: str) -> str:
        """The address the object under ``key`` is served at anonymously. It fails on a
        bucket that has no public address."""
        if is_discovering():
            raise refuse_unprovisioned(self._runtime.what, "public_url")
        base = self._read_properties().public_base_url
        if not base:
            raise OcelError(
                f'this bucket has no public address, so "{key}" has no public url: '
                f"declare the bucket with public=True and give the project a domain to "
                f"serve it from"
            )
        path = "/".join(quote(segment, safe="") for segment in key.split("/"))
        return f"{base.rstrip('/')}/{path}"

    def _read_properties(self) -> BucketProperties:
        if self._properties is None:
            self._properties = read_bucket_binding(self.name)
        return self._properties

    def _http_for(self, session: _Session) -> Client:
        return cast(Client, AwaitedCalls(self._http)) if session.blocking else self._http_async

    async def _put(
        self, session: _Session, *, key: str, data: _Body, options: "_WriteOptions"
    ) -> ObjectInfo:
        writer = Writer(self, session, key, options)
        chunks = _read_chunks(data, self._part_size_bytes)
        try:
            while (chunk := await session.offload(_read_next_chunk, chunks)) is not None:
                await writer.write(chunk)
        except BaseException:
            await session.offload(chunks.close)
            await writer.abort()
            raise
        return await writer.land()

    async def _read(
        self,
        session: _Session,
        *,
        key: str,
        byte_range: tuple[int, int | None] | None,
        send: Callable[[PresignedTarget, dict[str, str]], Operation[tuple[_Stream, _Answer]]],
        release: Callable[[_Stream], Operation[None]],
    ) -> tuple[ObjectInfo, _Stream, _Answer]:
        for _ in range(_READ_ATTEMPTS):
            info = await self._head(session, key=key)
            if info is None:
                raise ObjectNotFound(key)
            target = await self._sign(session, key, SignedOperation.GET, SignedAudience.INTERNAL)
            stream, answer = await send(target, _build_read_headers(target, byte_range))
            if answer.status >= 300:
                await release(stream)
                raise _refuse_read(key, answer.status)
            etag = _read_etag(answer.headers, target)
            if etag and etag == info.etag:
                return info, stream, answer
            await release(stream)
        raise PreconditionFailed(key, f'the object under "{key}" changed while it was read')

    async def _head(self, session: _Session, *, key: str) -> ObjectInfo | None:
        request = HeadRequest(bucket=self._read_properties().bucket, key=key)
        response = await session.client.head(request)
        return _build_object_info(response.object, key) if response.object else None

    async def _delete(self, session: _Session, *, keys: tuple[str, ...]) -> None:
        await session.client.delete(
            DeleteRequest(bucket=self._read_properties().bucket, keys=list(keys))
        )

    async def _copy(self, session: _Session, *, src: str, dst: str) -> ObjectInfo:
        request = CopyRequest(
            bucket=self._read_properties().bucket, source_key=src, destination_key=dst
        )
        try:
            response = await session.client.copy(request)
        except RuntimeRefusedError as refused:
            raise _refine_refusal(src, refused) from None
        return _build_object_info(response.object, dst)

    async def _list_page(
        self, session: _Session, *, prefix: str | None, limit: int | None, cursor: str
    ) -> tuple[Sequence[ObjectInfo], str]:
        bucket = self._read_properties().bucket
        request = ListRequest(bucket=bucket, prefix=prefix or "", limit=limit or 0, cursor=cursor)
        response = await session.client.list(request)
        page = [_build_object_info(entry, entry.key) for entry in response.objects]
        return page, response.next_cursor

    def _sign_read(
        self, key: str, expires_in: float | timedelta | None, download: bool | str | None
    ) -> Callable[[_Session], Operation[PresignedTarget]]:
        constraints = SignConstraints(download_filename=_choose_download_filename(key, download))
        return lambda session: self._sign(
            session, key, SignedOperation.GET, SignedAudience.EXTERNAL, constraints, expires_in
        )

    def _sign_upload(
        self,
        key: str,
        expires_in: float | timedelta | None,
        max_size: int | None,
        content_type: str | None,
    ) -> Callable[[_Session], Operation[PresignedTarget]]:
        constraints = SignConstraints(content_type=content_type or "", max_size=max_size or 0)
        return lambda session: self._sign(
            session,
            key,
            SignedOperation.POST_UPLOAD,
            SignedAudience.EXTERNAL,
            constraints,
            expires_in,
        )

    async def _sign(
        self,
        session: _Session,
        key: str,
        operation: SignedOperation,
        audience: SignedAudience,
        constraints: SignConstraints | None = None,
        expires_in: float | timedelta | None = None,
    ) -> PresignedTarget:
        request = SignRequest(
            bucket=self._read_properties().bucket,
            key=key,
            operation=operation,
            audience=audience,
            constraints=constraints,
        )
        if expires_in is not None:
            request.expires_in = encode_duration(expires_in)
        try:
            response = await session.client.sign(request)
        except RuntimeRefusedError as refused:
            raise _refine_refusal(key, refused) from None
        if response.target is None:
            raise OcelError(f'the runtime signed nothing for "{key}"')
        return response.target


def bucket(name: str, *, public: bool = False, allowed_origins: Sequence[str] = ()) -> Bucket:
    """Declare a bucket named ``name`` and return the handle an app reads and writes its
    objects through. Call it from a file under the project's discovery folder: during
    discovery the call is the declaration, and at runtime it reads the binding the deploy
    delivered for that name. A ``public`` bucket serves every one of its objects anonymously
    over HTTP, and ``allowed_origins`` names the browser origins allowed to upload straight
    to the store."""
    if not is_discovering():
        return Bucket(name)
    caller = inspect.stack(0)[1]
    declare(
        DeclareRequest(
            resource=ResourceIdentifier(type=ResourceType.BUCKET, name=name),
            config=Oneof(_KIND, BucketConfig(public=public, allowed_origins=list(allowed_origins))),
            source=f"{caller.filename}:{caller.lineno}",
        )
    )
    return Bucket(name)


class ObjectBody:
    """An object's bytes, with what the bucket knows about it beside them."""

    #: What the bucket knows about the object.
    info: ObjectInfo

    def __init__(
        self,
        info: ObjectInfo,
        stream: AbstractContextManager[SyncResponse],
        response: SyncResponse,
    ):
        """Wrap a streaming read of the object ``info`` describes. :meth:`Bucket.get`
        hands one back."""
        self.info = info
        self._stream = stream
        self._response = response
        self._walked = False
        self._whole: bytes | None = None

    def __iter__(self) -> Iterator[bytes]:
        """The bytes, a chunk at a time, as they arrive. A body is walked once; one read
        whole with :meth:`bytes` walks as that one chunk."""
        if self._whole is not None:
            yield self._whole
            return
        _refuse_second_walk(self.info.key, self._walked)
        self._walked = True
        try:
            for chunk in self._response.content:
                yield bytes(chunk)
        finally:
            self.close()

    def bytes(self) -> bytes:
        """Every byte of the object, read to the end, and the same bytes on every call
        after."""
        if self._whole is None:
            self._whole = b"".join(self)
        return self._whole

    def text(self, encoding: str = "utf-8") -> str:
        """Every byte of the object, decoded."""
        return self.bytes().decode(encoding)

    def close(self) -> None:
        """Release the connection the object is streaming over."""
        self._stream.__exit__(None, None, None)

    def __enter__(self) -> "ObjectBody":
        """Hand back the body, which closes when the block ends."""
        return self

    def __exit__(self, *_exception: object) -> None:
        """Release the connection the object is streaming over."""
        self.close()


class AsyncObjectBody:
    """An object's bytes, with what the bucket knows about it beside them, read with
    ``await`` and ``async for``."""

    #: What the bucket knows about the object.
    info: ObjectInfo

    def __init__(
        self,
        info: ObjectInfo,
        stream: AbstractAsyncContextManager[Response],
        response: Response,
    ):
        """Wrap a streaming read of the object ``info`` describes. :meth:`Bucket.get_async`
        hands one back."""
        self.info = info
        self._stream = stream
        self._response = response
        self._walked = False
        self._whole: bytes | None = None

    async def __aiter__(self) -> AsyncIterator[bytes]:
        """The bytes, a chunk at a time, as they arrive. A body is walked once; one read
        whole with :meth:`bytes` walks as that one chunk."""
        if self._whole is not None:
            yield self._whole
            return
        _refuse_second_walk(self.info.key, self._walked)
        self._walked = True
        try:
            async for chunk in self._response.content:
                yield bytes(chunk)
        finally:
            await self.aclose()

    async def bytes(self) -> bytes:
        """Every byte of the object, read to the end, and the same bytes on every call
        after."""
        if self._whole is None:
            self._whole = b"".join([chunk async for chunk in self])
        return self._whole

    async def text(self, encoding: str = "utf-8") -> str:
        """Every byte of the object, decoded."""
        return (await self.bytes()).decode(encoding)

    async def aclose(self) -> None:
        """Release the connection the object is streaming over."""
        await self._stream.__aexit__(None, None, None)

    async def __aenter__(self) -> "AsyncObjectBody":
        """Hand back the body, which closes when the block ends."""
        return self

    async def __aexit__(self, *_exception: object) -> None:
        """Release the connection the object is streaming over."""
        await self.aclose()


@dataclass
class _WriteOptions:
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
    def take(cls, opener: str, given: dict[str, object]) -> "_WriteOptions":
        options = cls()
        for name, value in given.items():
            if name not in cls.__dataclass_fields__:
                raise TypeError(f"{opener}() got an unexpected keyword argument '{name}'")
            setattr(options, name, value)
        return options


class Writer:
    def __init__(self, store: Bucket, session: _Session, key: str, options: _WriteOptions):
        self._store = store
        self._session = session
        self._http = store._http_for(session)
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
        return ObjectInfo(
            key=self._key,
            size=self._written,
            etag=etag,
            content_type=options.written_content_type,
            metadata=dict(options.metadata or {}),
        )

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
            raise _refine_refusal(self._key, refused) from None
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
            raise _refine_refusal(self._key, refused) from None
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
        response = await self._http.put(target.url, dict(target.headers), data)
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
            raise _refine_refusal(self._key, refused) from None
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
        response = await self._http.execute(
            target.method or "PUT",
            target.url,
            _build_put_headers(target, options),
            bytes(self._buffered),
        )
        if response.status >= 300:
            raise _refuse_status(self._key, response.status) or OcelError(
                f'writing "{self._key}" was refused ({response.status})'
            )
        return _read_etag(response.headers, target)


async def _send_read(
    http: SyncClient, target: PresignedTarget, headers: dict[str, str]
) -> tuple[AbstractContextManager[SyncResponse], SyncResponse]:
    stream = http.stream("GET", target.url, headers)
    return stream, stream.__enter__()


async def _release_read(stream: AbstractContextManager[SyncResponse]) -> None:
    stream.__exit__(None, None, None)


async def _send_read_async(
    http: Client, target: PresignedTarget, headers: dict[str, str]
) -> tuple[AbstractAsyncContextManager[Response], Response]:
    stream = http.stream("GET", target.url, headers)
    return stream, await stream.__aenter__()


async def _release_read_async(stream: AbstractAsyncContextManager[Response]) -> None:
    await stream.__aexit__(None, None, None)


def _read_etag(headers: Headers, target: PresignedTarget) -> str:
    return headers.get(target.etag_header, "") if target.etag_header else ""


def _build_put_headers(target: PresignedTarget, options: _WriteOptions) -> dict[str, str]:
    headers = dict(target.headers)
    headers["content-type"] = options.written_content_type
    if options.cache_control:
        headers["cache-control"] = options.cache_control
    if options.if_none_match:
        headers["if-none-match"] = options.if_none_match
    if options.if_match:
        headers["if-match"] = options.if_match
    return headers


def _build_read_headers(
    target: PresignedTarget, range: tuple[int, int | None] | None
) -> dict[str, str]:
    headers = dict(target.headers)
    if range is not None:
        offset, length = range
        end = "" if length is None else str(offset + length - 1)
        headers["range"] = f"bytes={offset}-{end}"
    return headers


def _read_next_chunk(chunks: Iterator[bytes]) -> bytes | None:
    return next(chunks, None)


def _read_chunks(data: _Body, size: int) -> Generator[bytes]:
    if isinstance(data, str):
        yield data.encode()
        return
    if isinstance(data, (bytes, bytearray, memoryview)):
        yield bytes(data)
        return
    if isinstance(data, os.PathLike):
        with open(data, "rb") as handle:
            yield from _read_chunks(handle, size)
        return
    while True:
        chunk = data.read(size)
        if not chunk:
            return
        yield chunk


def _choose_download_filename(key: str, download: bool | str | None) -> str:
    if isinstance(download, str):
        return download
    return key.rsplit("/", 1)[-1] if download else ""


def _build_signed_upload(
    target: PresignedTarget, expires_in: float | timedelta | None
) -> SignedUpload:
    expires = None
    if expires_in is not None:
        lifetime = (
            expires_in if isinstance(expires_in, timedelta) else timedelta(seconds=expires_in)
        )
        expires = datetime.now(timezone.utc) + lifetime
    return SignedUpload(
        url=target.url,
        method=target.method or "POST",
        headers=dict(target.headers),
        fields=dict(target.fields),
        expires=expires,
    )


def _build_object_info(wire: WireObjectInfo | None, key: str) -> ObjectInfo:
    if wire is None:
        return ObjectInfo(key=key, size=0, etag="", content_type="")
    return ObjectInfo(
        key=wire.key or key,
        size=wire.size,
        etag=wire.etag,
        content_type=wire.content_type,
        uploaded_at=wire.uploaded_at.to_datetime() if wire.uploaded_at else None,
        metadata=dict(wire.metadata),
    )


def _refine_refusal(key: str, refused: RuntimeRefusedError) -> OcelError:
    if refused.code == "not_found":
        return ObjectNotFound(key)
    if refused.code == "failed_precondition":
        return PreconditionFailed(key)
    return refused


def _refuse_second_walk(key: str, walked: bool) -> None:
    if walked:
        raise OcelError(f'the body of "{key}" was read already, and a body is read once')


def _refuse_read(key: str, status: int) -> OcelError:
    return _refuse_status(key, status) or OcelError(f'reading "{key}" was refused ({status})')


def _refuse_status(key: str, status: int) -> OcelError | None:
    if status == 404:
        return ObjectNotFound(key)
    if status in (409, 412):
        return PreconditionFailed(key)
    return None
