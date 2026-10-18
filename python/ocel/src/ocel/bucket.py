import inspect
from collections.abc import AsyncIterator, Callable, Iterator, Mapping, Sequence
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from functools import partial
from typing import IO, Literal, cast, overload
from urllib.parse import quote

from protobuf import Oneof
from pyqwest import Client, SyncClient

from ocel._binding import read_bucket_binding, refuse_unprovisioned
from ocel._declare import declare, is_discovering
from ocel._error import OcelError, RuntimeRefusedError
from ocel._object import (
    ObjectInfo,
    ObjectNotFound,
    PreconditionFailed,
    read_etag,
    refine_refusal,
    refuse_status,
)
from ocel._object_files import (
    AsyncObjectBody,
    AsyncReadStream,
    AsyncWriteStream,
    ObjectBody,
    ReadStream,
    WriteStream,
)
from ocel._object_http import Answer, AwaitedHttp, BlockingHttp, ObjectHttp, Stream
from ocel._object_upload import (
    Body,
    ObjectSession,
    Upload,
    WriteOptions,
    read_chunks,
    read_next_chunk,
)
from ocel._runtime import Operation, RuntimeConnection, Session
from ocel._wire import encode_duration
from ocel.gen.app.bucket.v1.bucket_connect import (
    BucketServiceClient,
    BucketServiceClientSync,
)
from ocel.gen.app.bucket.v1.bucket_pb import (
    CopyRequest,
    DeleteRequest,
    HeadRequest,
    ListRequest,
    PresignedTarget,
    SignConstraints,
    SignedAudience,
    SignedOperation,
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
_READ_ATTEMPTS = 3


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
        self._blocking_http = BlockingHttp(SyncClient())
        self._awaited_http = AwaitedHttp(Client())
        self._single_request_limit_bytes = _SINGLE_REQUEST_LIMIT_BYTES
        self._part_size_bytes = _PART_SIZE_BYTES

    def put(
        self,
        key: str,
        data: Body,
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
        options = WriteOptions(content_type, cache_control, metadata, if_not_exists, if_match)
        return self._runtime.run(
            "put", self._blocking_http, partial(self._put, key=key, data=data, options=options)
        )

    async def put_async(
        self,
        key: str,
        data: Body,
        *,
        content_type: str | None = None,
        cache_control: str | None = None,
        metadata: Mapping[str, str] | None = None,
        if_not_exists: bool = False,
        if_match: str | None = None,
    ) -> ObjectInfo:
        """What :meth:`put` does, awaited, reading a file or path body off the event loop."""
        options = WriteOptions(content_type, cache_control, metadata, if_not_exists, if_match)
        return await self._runtime.run_async(
            "put_async",
            self._awaited_http,
            partial(self._put, key=key, data=data, options=options),
        )

    def get(self, key: str, *, range: tuple[int, int | None] | None = None) -> ObjectBody:
        """Read the object under ``key``, or the ``(offset, length)`` byte range of it that
        ``range`` names, with a ``length`` of ``None`` reading to the end. The body's info
        describes the version its bytes come from: a read the object changes under is
        tried again, and raises :class:`PreconditionFailed` when it changes under every
        attempt. Raises :class:`ObjectNotFound` when the bucket has none."""
        info, stream, response = self._runtime.run(
            "get", self._blocking_http, partial(self._read, key=key, byte_range=range)
        )
        return ObjectBody(info, stream, response)

    async def get_async(
        self, key: str, *, range: tuple[int, int | None] | None = None
    ) -> AsyncObjectBody:
        """What :meth:`get` does, awaited."""
        info, stream, response = await self._runtime.run_async(
            "get_async", self._awaited_http, partial(self._read, key=key, byte_range=range)
        )
        return AsyncObjectBody(info, stream, response)

    @overload
    def open(self, key: str, mode: Literal["rb"] = "rb") -> IO[bytes]: ...

    @overload
    def open(self, key: str, mode: Literal["wb"], **write_options: object) -> WriteStream: ...

    def open(self, key: str, mode: str = "rb", **write_options: object) -> IO[bytes] | WriteStream:
        """Open the object under ``key`` as a binary file. ``"rb"`` reads it and ``"wb"``
        writes it, taking the keyword options :meth:`put` takes and landing the object when
        the handle closes. A ``"wb"`` handle's :attr:`WriteStream.info` then describes the
        object it wrote, as :meth:`put` returns it."""
        if mode == "rb":
            return cast(IO[bytes], ReadStream(self.get(key)))
        if mode == "wb":
            options = WriteOptions.take("open", write_options)
            writer = Upload(
                self, self._runtime.session("put", True, self._blocking_http), key, options
            )
            return WriteStream(writer)
        raise ValueError(f"a bucket object opens as 'rb' or 'wb', not {mode!r}")

    @overload
    def open_async(self, key: str, mode: Literal["rb"] = "rb") -> AsyncReadStream: ...

    @overload
    def open_async(
        self, key: str, mode: Literal["wb"], **write_options: object
    ) -> AsyncWriteStream: ...

    def open_async(
        self, key: str, mode: str = "rb", **write_options: object
    ) -> AsyncReadStream | AsyncWriteStream:
        """What :meth:`open` does, as an async context manager whose ``read`` and ``write``
        are awaited. A ``"wb"`` handle's :attr:`AsyncWriteStream.info` describes the object
        it wrote once the block ends."""
        if mode == "rb":
            return AsyncReadStream(self, key)
        if mode == "wb":
            options = WriteOptions.take("open_async", write_options)
            writer = Upload(
                self, self._runtime.session("put_async", False, self._awaited_http), key, options
            )
            return AsyncWriteStream(writer)
        raise ValueError(f"a bucket object opens as 'rb' or 'wb', not {mode!r}")

    def head(self, key: str) -> ObjectInfo | None:
        """What the bucket knows about the object under ``key``, or ``None`` when it has
        none."""
        return self._runtime.run("head", self._blocking_http, partial(self._head, key=key))

    async def head_async(self, key: str) -> ObjectInfo | None:
        """What :meth:`head` does, awaited."""
        return await self._runtime.run_async(
            "head_async", self._awaited_http, partial(self._head, key=key)
        )

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
            self._runtime.run("delete", self._blocking_http, partial(self._delete, keys=keys))

    async def delete_async(self, *keys: str) -> None:
        """What :meth:`delete` does, awaited."""
        if keys:
            await self._runtime.run_async(
                "delete_async", self._awaited_http, partial(self._delete, keys=keys)
            )

    def copy(self, src: str, dst: str) -> ObjectInfo:
        """Copy the object under ``src`` to ``dst`` within the same bucket, and return the
        object that landed. Raises :class:`ObjectNotFound` when the bucket has nothing
        under ``src``."""
        return self._runtime.run("copy", self._blocking_http, partial(self._copy, src=src, dst=dst))

    async def copy_async(self, src: str, dst: str) -> ObjectInfo:
        """What :meth:`copy` does, awaited."""
        return await self._runtime.run_async(
            "copy_async", self._awaited_http, partial(self._copy, src=src, dst=dst)
        )

    def list(self, *, prefix: str | None = None, limit: int | None = None) -> Iterator[ObjectInfo]:
        """Walk the bucket's objects under ``prefix``, a page at a time, yielding at most
        ``limit`` objects per page."""
        cursor = ""
        while True:
            page, cursor = self._runtime.run(
                "list",
                self._blocking_http,
                partial(self._list_page, prefix=prefix, limit=limit, cursor=cursor),
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
                "list_async",
                self._awaited_http,
                partial(self._list_page, prefix=prefix, limit=limit, cursor=cursor),
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
        sign = self._sign_read(key, expires_in, download)
        return self._runtime.run("signed_url", self._blocking_http, sign).url

    async def signed_url_async(
        self,
        key: str,
        *,
        expires_in: float | timedelta | None = None,
        download: bool | str | None = None,
    ) -> str:
        """What :meth:`signed_url` does, awaited."""
        sign = self._sign_read(key, expires_in, download)
        return (await self._runtime.run_async("signed_url_async", self._awaited_http, sign)).url

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
        target = self._runtime.run("signed_upload", self._blocking_http, sign)
        return _build_signed_upload(target, expires_in)

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
        target = await self._runtime.run_async("signed_upload_async", self._awaited_http, sign)
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

    async def _put(
        self, session: ObjectSession, *, key: str, data: Body, options: WriteOptions
    ) -> ObjectInfo:
        writer = Upload(self, session, key, options)
        chunks = read_chunks(data, self._part_size_bytes)
        try:
            while (chunk := await session.offload(read_next_chunk, chunks)) is not None:
                await writer.write(chunk)
        except BaseException:
            await session.offload(chunks.close)
            await writer.abort()
            raise
        return await writer.land()

    async def _read(
        self,
        session: Session[BucketServiceClient, ObjectHttp[Stream, Answer]],
        *,
        key: str,
        byte_range: tuple[int, int | None] | None,
    ) -> tuple[ObjectInfo, Stream, Answer]:
        for _ in range(_READ_ATTEMPTS):
            info = await self._head(session, key=key)
            if info is None:
                raise ObjectNotFound(key)
            target = await self._sign(session, key, SignedOperation.GET, SignedAudience.INTERNAL)
            stream, answer = await session.http.open_read(
                target.url, _build_read_headers(target, byte_range)
            )
            if answer.status >= 300:
                await session.http.release(stream)
                raise _refuse_read(key, answer.status)
            etag = read_etag(answer.headers, target)
            if etag and etag == info.etag:
                return info, stream, answer
            await session.http.release(stream)
        raise PreconditionFailed(key, f'the object under "{key}" changed while it was read')

    async def _head(self, session: ObjectSession, *, key: str) -> ObjectInfo | None:
        request = HeadRequest(bucket=self._read_properties().bucket, key=key)
        response = await session.client.head(request)
        return _build_object_info(response.object, key) if response.object else None

    async def _delete(self, session: ObjectSession, *, keys: tuple[str, ...]) -> None:
        await session.client.delete(
            DeleteRequest(bucket=self._read_properties().bucket, keys=list(keys))
        )

    async def _copy(self, session: ObjectSession, *, src: str, dst: str) -> ObjectInfo:
        request = CopyRequest(
            bucket=self._read_properties().bucket, source_key=src, destination_key=dst
        )
        try:
            response = await session.client.copy(request)
        except RuntimeRefusedError as refused:
            raise refine_refusal(src, refused) from None
        return _build_object_info(response.object, dst)

    async def _list_page(
        self, session: ObjectSession, *, prefix: str | None, limit: int | None, cursor: str
    ) -> tuple[Sequence[ObjectInfo], str]:
        bucket = self._read_properties().bucket
        request = ListRequest(bucket=bucket, prefix=prefix or "", limit=limit or 0, cursor=cursor)
        response = await session.client.list(request)
        page = [_build_object_info(entry, entry.key) for entry in response.objects]
        return page, response.next_cursor

    def _sign_read(
        self, key: str, expires_in: float | timedelta | None, download: bool | str | None
    ) -> Callable[[ObjectSession], Operation[PresignedTarget]]:
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
    ) -> Callable[[ObjectSession], Operation[PresignedTarget]]:
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
        session: ObjectSession,
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
            raise refine_refusal(key, refused) from None
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


def _build_read_headers(
    target: PresignedTarget, range: tuple[int, int | None] | None
) -> dict[str, str]:
    headers = dict(target.headers)
    if range is not None:
        offset, length = range
        end = "" if length is None else str(offset + length - 1)
        headers["range"] = f"bytes={offset}-{end}"
    return headers


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


def _refuse_read(key: str, status: int) -> OcelError:
    return refuse_status(key, status) or OcelError(f'reading "{key}" was refused ({status})')
