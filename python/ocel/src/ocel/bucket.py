import asyncio
import inspect
import io
import os
from collections.abc import AsyncIterator, Iterator, Mapping, Sequence
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from typing import IO, BinaryIO, Protocol, runtime_checkable
from urllib.parse import quote

from connectrpc.code import Code
from connectrpc.errors import ConnectError
from protobuf import Oneof
from protobuf.wkt import Duration
from pyqwest import Client, SyncClient

from ocel._binding import bucket_binding, unprovisioned
from ocel._declare import declare, discovering
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

_KIND = "bucket"
_RUNTIME_ADDRESS_ENV = "OCEL_RUNTIME_ADDRESS"
_SESSION_TOKEN_ENV = "OCEL_SESSION_TOKEN"
_SINGLE_REQUEST_CEILING = 16 << 20
_PART_SIZE = 8 << 20
_PARTS_IN_FLIGHT = 4


class ObjectNotFound(FileNotFoundError):
    """Raised when an operation that cannot answer with ``None`` names an object the bucket
    does not hold."""

    #: The key that named nothing.
    key: str

    def __init__(self, key: str):
        super().__init__(f'the bucket holds no object under "{key}"')
        self.key = key

    def __str__(self) -> str:
        return f'the bucket holds no object under "{self.key}"'


class PreconditionFailed(Exception):
    """Raised when a write carried ``if_not_exists`` or ``if_match`` and the object did not
    meet it."""

    #: The key whose current state refused the write.
    key: str

    def __init__(self, key: str):
        super().__init__(f'the object under "{key}" did not meet the condition this write carried')
        self.key = key


@dataclass(frozen=True)
class ObjectInfo:
    """What a bucket knows about one object it holds."""

    #: The key the object is addressed by.
    key: str
    #: The object's length in bytes.
    size: int
    #: The store's opaque version tag for these bytes.
    etag: str
    #: The media type the object was written with.
    content_type: str
    #: When the object last took its current bytes, as the store reports it.
    uploaded_at: datetime | None = None
    #: The user metadata written alongside the object.
    metadata: Mapping[str, str] = field(default_factory=dict)


@dataclass(frozen=True)
class SignedUpload:
    """A target someone without a credential writes one object through, for as long as it
    stays valid."""

    #: Where the body is sent.
    url: str
    #: The HTTP method to send it with.
    method: str
    #: The headers the signature covers, which the caller must send unchanged.
    headers: Mapping[str, str] = field(default_factory=dict)
    #: The form fields a POST target carries, which are empty for a PUT target.
    fields: Mapping[str, str] = field(default_factory=dict)
    #: When the target stops being valid, or ``None`` when its lifetime was left to the
    #: runtime.
    expires: datetime | None = None


@runtime_checkable
class SyncBucket(Protocol):
    """The synchronous surface a bucket handle answers to, which a fake stands in for."""

    #: The name the bucket was declared under.
    name: str

    def put(
        self,
        key: str,
        data: bytes | str | BinaryIO | os.PathLike,
        *,
        content_type: str | None = None,
        cache_control: str | None = None,
        metadata: Mapping[str, str] | None = None,
        if_not_exists: bool = False,
        if_match: str | None = None,
    ) -> ObjectInfo:
        """Write ``data`` as the whole object under ``key``."""
        ...

    def get(self, key: str, *, range: tuple[int, int | None] | None = None) -> "ObjectBody":
        """Read the object under ``key``."""
        ...

    def open(self, key: str, mode: str = "rb", **write_options) -> IO[bytes]:
        """Open the object under ``key`` as a binary file."""
        ...

    def head(self, key: str) -> ObjectInfo | None:
        """What the bucket knows about ``key``."""
        ...

    def exists(self, key: str) -> bool:
        """Whether the bucket holds an object under ``key``."""
        ...

    def delete(self, *keys: str) -> None:
        """Remove the objects under ``keys``."""
        ...

    def copy(self, src: str, dst: str) -> ObjectInfo:
        """Copy ``src`` to ``dst`` within the bucket."""
        ...

    def list(self, *, prefix: str | None = None, limit: int | None = None) -> Iterator[ObjectInfo]:
        """Walk the objects under a prefix."""
        ...

    def signed_url(
        self,
        key: str,
        *,
        expires_in: float | timedelta | None = None,
        download: bool | str | None = None,
    ) -> str:
        """A url that reads ``key`` without a credential."""
        ...

    def signed_upload(
        self,
        key: str,
        *,
        expires_in: float | timedelta | None = None,
        max_size: int | None = None,
        content_type: str | None = None,
    ) -> SignedUpload:
        """A target that writes ``key`` without a credential."""
        ...

    def public_url(self, key: str) -> str:
        """The address ``key`` is served at anonymously."""
        ...


@runtime_checkable
class AsyncBucket(Protocol):
    """The awaited surface a bucket handle answers to, which a fake stands in for."""

    #: The name the bucket was declared under.
    name: str

    async def put_async(
        self,
        key: str,
        data: bytes | str | BinaryIO | os.PathLike,
        *,
        content_type: str | None = None,
        cache_control: str | None = None,
        metadata: Mapping[str, str] | None = None,
        if_not_exists: bool = False,
        if_match: str | None = None,
    ) -> ObjectInfo:
        """Write ``data`` as the whole object under ``key``."""
        ...

    async def get_async(
        self, key: str, *, range: tuple[int, int | None] | None = None
    ) -> "AsyncObjectBody":
        """Read the object under ``key``."""
        ...

    def open_async(self, key: str, mode: str = "rb", **write_options):
        """Open the object under ``key`` as an awaited binary file."""
        ...

    async def head_async(self, key: str) -> ObjectInfo | None:
        """What the bucket knows about ``key``."""
        ...

    async def exists_async(self, key: str) -> bool:
        """Whether the bucket holds an object under ``key``."""
        ...

    async def delete_async(self, *keys: str) -> None:
        """Remove the objects under ``keys``."""
        ...

    async def copy_async(self, src: str, dst: str) -> ObjectInfo:
        """Copy ``src`` to ``dst`` within the bucket."""
        ...

    def list_async(
        self, *, prefix: str | None = None, limit: int | None = None
    ) -> AsyncIterator[ObjectInfo]:
        """Walk the objects under a prefix."""
        ...

    async def signed_url_async(
        self,
        key: str,
        *,
        expires_in: float | timedelta | None = None,
        download: bool | str | None = None,
    ) -> str:
        """A url that reads ``key`` without a credential."""
        ...

    async def signed_upload_async(
        self,
        key: str,
        *,
        expires_in: float | timedelta | None = None,
        max_size: int | None = None,
        content_type: str | None = None,
    ) -> SignedUpload:
        """A target that writes ``key`` without a credential."""
        ...

    def public_url(self, key: str) -> str:
        """The address ``key`` is served at anonymously."""
        ...


class _Reached:
    def __init__(
        self,
        client,
        name: str,
        public_base_url: str,
        headers: Mapping[str, str],
    ):
        self.client = client
        self.bucket = name
        self.public_base_url = public_base_url
        self.headers = headers


class Bucket:
    """A bucket an app declares and reads and writes its objects through."""

    #: The name the bucket was declared under, and the name its binding is delivered as.
    name: str

    def __init__(self, name: str):
        """Take the handle for the bucket named ``name``. Prefer :func:`bucket`, which
        declares the bucket as well as handing back its handle."""
        self.name = name
        self._reached: _Reached | None = None
        self._reached_async: _Reached | None = None
        self._http = SyncClient()
        self._http_async = Client()
        self._single_ceiling = _SINGLE_REQUEST_CEILING
        self._part_size = _PART_SIZE

    def put(
        self,
        key: str,
        data: bytes | str | BinaryIO | os.PathLike,
        *,
        content_type: str | None = None,
        cache_control: str | None = None,
        metadata: Mapping[str, str] | None = None,
        if_not_exists: bool = False,
        if_match: str | None = None,
    ) -> ObjectInfo:
        """Write ``data`` as the whole object under ``key`` and return the object that
        landed. A body that outgrows what one request carries is written in parts. With
        ``if_not_exists`` the write goes through only while the bucket holds nothing under
        the key, and with ``if_match`` only while the object it holds carries that etag;
        either unmet raises :class:`PreconditionFailed`."""
        options = _WriteOptions(content_type, cache_control, metadata, if_not_exists, if_match)
        writer = _Writer(self, key, options)
        try:
            for chunk in _read_chunks(data, self._part_size):
                writer.write(chunk)
        except BaseException:
            writer.abort()
            raise
        writer.close()
        held = self.head(key)
        if held is None:
            raise ObjectNotFound(key)
        return held

    def get(self, key: str, *, range: tuple[int, int | None] | None = None) -> "ObjectBody":
        """Read the object under ``key``, or the ``(offset, length)`` byte range of it that
        ``range`` names, with a ``length`` of ``None`` reading to the end. Raises
        :class:`ObjectNotFound` when the bucket holds none."""
        held = self.head(key)
        if held is None:
            raise ObjectNotFound(key)
        target = self._sign("get", key, SignedOperation.GET, SignedAudience.INTERNAL)
        headers = dict(target.headers)
        if range is not None:
            headers["range"] = _byte_range(*range)
        stream = self._http.stream("GET", target.url, headers)
        response = stream.__enter__()
        if response.status >= 300:
            stream.__exit__(None, None, None)
            raise _refused_status(key, response.status) or RuntimeError(
                f'reading "{key}" was refused ({response.status})'
            )
        return ObjectBody(held, stream, response)

    def open(self, key: str, mode: str = "rb", **write_options) -> IO[bytes]:
        """Open the object under ``key`` as a binary file. ``"rb"`` reads it and ``"wb"``
        writes it, taking the keyword options :meth:`put` takes and landing the object when
        the handle closes."""
        if mode == "rb":
            return _ReadStream(self.get(key))
        if mode == "wb":
            options = _WriteOptions(
                write_options.pop("content_type", None),
                write_options.pop("cache_control", None),
                write_options.pop("metadata", None),
                write_options.pop("if_not_exists", False),
                write_options.pop("if_match", None),
            )
            if write_options:
                unknown = [*write_options][0]
                raise TypeError(f"open() got an unexpected keyword argument '{unknown}'")
            return _WriteStream(_Writer(self, key, options))
        raise ValueError(f"a bucket object opens as 'rb' or 'wb', not {mode!r}")

    def head(self, key: str) -> ObjectInfo | None:
        """What the bucket knows about the object under ``key``, or ``None`` when it holds
        none."""
        reached = self._runtime("head")
        response = reached.client.head(
            HeadRequest(bucket=reached.bucket, key=key), headers=reached.headers
        )
        return _object(response.object, key) if response.object else None

    def exists(self, key: str) -> bool:
        """Whether the bucket holds an object under ``key``."""
        return self.head(key) is not None

    def delete(self, *keys: str) -> None:
        """Remove the objects under ``keys``. A key the bucket does not hold is not an
        error."""
        if not keys:
            return
        reached = self._runtime("delete")
        reached.client.delete(
            DeleteRequest(bucket=reached.bucket, keys=list(keys)), headers=reached.headers
        )

    def copy(self, src: str, dst: str) -> ObjectInfo:
        """Copy the object under ``src`` to ``dst`` within the same bucket, and return the
        object that landed. Raises :class:`ObjectNotFound` when the bucket holds nothing
        under ``src``."""
        reached = self._runtime("copy")
        try:
            response = reached.client.copy(
                CopyRequest(bucket=reached.bucket, source_key=src, destination_key=dst),
                headers=reached.headers,
            )
        except ConnectError as error:
            raise _refused(src, error) from None
        return _object(response.object, dst)

    def list(self, *, prefix: str | None = None, limit: int | None = None) -> Iterator[ObjectInfo]:
        """Walk the objects the bucket holds under ``prefix``, a page at a time, yielding
        at most ``limit`` objects per page."""
        reached = self._runtime("list")
        cursor = ""
        while True:
            response = reached.client.list(
                ListRequest(
                    bucket=reached.bucket,
                    prefix=prefix or "",
                    limit=limit or 0,
                    cursor=cursor,
                ),
                headers=reached.headers,
            )
            for held in response.objects:
                yield _object(held, held.key)
            cursor = response.next_cursor
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
        target = self._sign(
            "signed_url",
            key,
            SignedOperation.GET,
            SignedAudience.EXTERNAL,
            SignConstraints(download_filename=_download_filename(key, download)),
            expires_in,
        )
        return target.url

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
        target = self._sign(
            "signed_upload",
            key,
            SignedOperation.POST_UPLOAD,
            SignedAudience.EXTERNAL,
            SignConstraints(content_type=content_type or "", max_size=max_size or 0),
            expires_in,
        )
        return SignedUpload(
            url=target.url,
            method=target.method or "POST",
            headers=dict(target.headers),
            fields=dict(target.fields),
            expires=_expires_at(expires_in),
        )

    def public_url(self, key: str) -> str:
        """The address the object under ``key`` is served at anonymously. It fails on a
        bucket that carries no public address."""
        reached = self._runtime("public_url")
        if not reached.public_base_url:
            raise RuntimeError(
                f'this bucket carries no public address, so "{key}" has no public url: '
                f"declare the bucket with public=True and give the project a domain to "
                f"serve it from"
            )
        path = "/".join(quote(segment, safe="") for segment in key.split("/"))
        return f"{reached.public_base_url.rstrip('/')}/{path}"

    async def put_async(
        self,
        key: str,
        data: bytes | str | BinaryIO | os.PathLike,
        *,
        content_type: str | None = None,
        cache_control: str | None = None,
        metadata: Mapping[str, str] | None = None,
        if_not_exists: bool = False,
        if_match: str | None = None,
    ) -> ObjectInfo:
        """What :meth:`put` does, awaited."""
        options = _WriteOptions(content_type, cache_control, metadata, if_not_exists, if_match)
        writer = _AsyncWriter(self, key, options)
        try:
            for chunk in _read_chunks(data, self._part_size):
                await writer.write(chunk)
        except BaseException:
            await writer.abort()
            raise
        await writer.close()
        held = await self.head_async(key)
        if held is None:
            raise ObjectNotFound(key)
        return held

    async def get_async(
        self, key: str, *, range: tuple[int, int | None] | None = None
    ) -> "AsyncObjectBody":
        """What :meth:`get` does, awaited."""
        held = await self.head_async(key)
        if held is None:
            raise ObjectNotFound(key)
        target = await self._sign_async(
            "get_async", key, SignedOperation.GET, SignedAudience.INTERNAL
        )
        headers = dict(target.headers)
        if range is not None:
            headers["range"] = _byte_range(*range)
        stream = self._http_async.stream("GET", target.url, headers)
        response = await stream.__aenter__()
        if response.status >= 300:
            await stream.__aexit__(None, None, None)
            raise _refused_status(key, response.status) or RuntimeError(
                f'reading "{key}" was refused ({response.status})'
            )
        return AsyncObjectBody(held, stream, response)

    def open_async(self, key: str, mode: str = "rb", **write_options):
        """What :meth:`open` does, as an async context manager whose ``read`` and ``write``
        are awaited."""
        if mode == "rb":
            return _AsyncReadStream(self, key)
        if mode == "wb":
            options = _WriteOptions(
                write_options.pop("content_type", None),
                write_options.pop("cache_control", None),
                write_options.pop("metadata", None),
                write_options.pop("if_not_exists", False),
                write_options.pop("if_match", None),
            )
            if write_options:
                unknown = [*write_options][0]
                raise TypeError(f"open_async() got an unexpected keyword argument '{unknown}'")
            return _AsyncWriteStream(_AsyncWriter(self, key, options))
        raise ValueError(f"a bucket object opens as 'rb' or 'wb', not {mode!r}")

    async def head_async(self, key: str) -> ObjectInfo | None:
        """What :meth:`head` does, awaited."""
        reached = self._runtime_async("head_async")
        response = await reached.client.head(
            HeadRequest(bucket=reached.bucket, key=key), headers=reached.headers
        )
        return _object(response.object, key) if response.object else None

    async def exists_async(self, key: str) -> bool:
        """What :meth:`exists` does, awaited."""
        return await self.head_async(key) is not None

    async def delete_async(self, *keys: str) -> None:
        """What :meth:`delete` does, awaited."""
        if not keys:
            return
        reached = self._runtime_async("delete_async")
        await reached.client.delete(
            DeleteRequest(bucket=reached.bucket, keys=list(keys)), headers=reached.headers
        )

    async def copy_async(self, src: str, dst: str) -> ObjectInfo:
        """What :meth:`copy` does, awaited."""
        reached = self._runtime_async("copy_async")
        try:
            response = await reached.client.copy(
                CopyRequest(bucket=reached.bucket, source_key=src, destination_key=dst),
                headers=reached.headers,
            )
        except ConnectError as error:
            raise _refused(src, error) from None
        return _object(response.object, dst)

    async def list_async(
        self, *, prefix: str | None = None, limit: int | None = None
    ) -> AsyncIterator[ObjectInfo]:
        """What :meth:`list` does, walked with ``async for``."""
        reached = self._runtime_async("list_async")
        cursor = ""
        while True:
            response = await reached.client.list(
                ListRequest(
                    bucket=reached.bucket, prefix=prefix or "", limit=limit or 0, cursor=cursor
                ),
                headers=reached.headers,
            )
            for held in response.objects:
                yield _object(held, held.key)
            cursor = response.next_cursor
            if not cursor:
                return

    async def signed_url_async(
        self,
        key: str,
        *,
        expires_in: float | timedelta | None = None,
        download: bool | str | None = None,
    ) -> str:
        """What :meth:`signed_url` does, awaited."""
        target = await self._sign_async(
            "signed_url_async",
            key,
            SignedOperation.GET,
            SignedAudience.EXTERNAL,
            SignConstraints(download_filename=_download_filename(key, download)),
            expires_in,
        )
        return target.url

    async def signed_upload_async(
        self,
        key: str,
        *,
        expires_in: float | timedelta | None = None,
        max_size: int | None = None,
        content_type: str | None = None,
    ) -> SignedUpload:
        """What :meth:`signed_upload` does, awaited."""
        target = await self._sign_async(
            "signed_upload_async",
            key,
            SignedOperation.POST_UPLOAD,
            SignedAudience.EXTERNAL,
            SignConstraints(content_type=content_type or "", max_size=max_size or 0),
            expires_in,
        )
        return SignedUpload(
            url=target.url,
            method=target.method or "POST",
            headers=dict(target.headers),
            fields=dict(target.fields),
            expires=_expires_at(expires_in),
        )

    async def _sign_async(
        self,
        access: str,
        key: str,
        operation: SignedOperation,
        audience: SignedAudience,
        constraints: SignConstraints | None = None,
        expires_in: float | timedelta | None = None,
    ) -> PresignedTarget:
        reached = self._runtime_async(access)
        request = _sign_request(reached, key, operation, audience, constraints, expires_in)
        try:
            response = await reached.client.sign(request, headers=reached.headers)
        except ConnectError as error:
            raise _refused(key, error) from None
        if response.target is None:
            raise RuntimeError(f'the runtime signed nothing for "{key}"')
        return response.target

    def _sign(
        self,
        access: str,
        key: str,
        operation: SignedOperation,
        audience: SignedAudience,
        constraints: SignConstraints | None = None,
        expires_in: float | timedelta | None = None,
    ) -> PresignedTarget:
        reached = self._runtime(access)
        request = _sign_request(reached, key, operation, audience, constraints, expires_in)
        try:
            response = reached.client.sign(request, headers=reached.headers)
        except ConnectError as error:
            raise _refused(key, error) from None
        if response.target is None:
            raise RuntimeError(f'the runtime signed nothing for "{key}"')
        return response.target

    def _runtime(self, access: str) -> _Reached:
        if discovering():
            raise unprovisioned(f'bucket("{self.name}")', access)
        if self._reached is None:
            address, headers, properties = self._delivered()
            self._reached = _Reached(
                BucketServiceClientSync(address, send_compression=None),
                properties.bucket,
                properties.public_base_url,
                headers,
            )
        return self._reached

    def _runtime_async(self, access: str) -> _Reached:
        if discovering():
            raise unprovisioned(f'bucket("{self.name}")', access)
        if self._reached_async is None:
            address, headers, properties = self._delivered()
            self._reached_async = _Reached(
                BucketServiceClient(address, send_compression=None),
                properties.bucket,
                properties.public_base_url,
                headers,
            )
        return self._reached_async

    def _delivered(self):
        properties = bucket_binding(self.name)
        address = os.environ.get(_RUNTIME_ADDRESS_ENV)
        if not address:
            raise RuntimeError(
                f"{_RUNTIME_ADDRESS_ENV} is not defined, so no resource the ocel runtime "
                f"serves can be reached. Run `ocel dev` to serve it locally, or "
                f"`ocel deploy` to have the deployed runtime's address delivered."
            )
        token = os.environ.get(_SESSION_TOKEN_ENV)
        if not token:
            raise RuntimeError(
                f"{_SESSION_TOKEN_ENV} is not defined, so the ocel runtime at {address} "
                f"would refuse every call. It is delivered beside {_RUNTIME_ADDRESS_ENV} by "
                f"`ocel dev` and by the deployed runtime, never set by hand."
            )
        return address.rstrip("/"), {"Authorization": f"Bearer {token}"}, properties


def bucket(name: str, *, public: bool = False, allowed_origins: Sequence[str] = ()) -> Bucket:
    """Declare a bucket named ``name`` and return the handle an app reads and writes its
    objects through. Call it from a file under the project's discovery folder: during
    discovery the call is the declaration, and at runtime it reads the binding the deploy
    delivered for that name. A ``public`` bucket serves every object it holds anonymously
    over HTTP, and ``allowed_origins`` names the browser origins allowed to upload straight
    to the store."""
    if not discovering():
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

    def __init__(self, info: ObjectInfo, stream, response):
        self.info = info
        self._stream = stream
        self._response = response
        self._chunks: Iterator[bytes] | None = None

    def __iter__(self) -> Iterator[bytes]:
        """The bytes, a chunk at a time, as they arrive."""
        if self._chunks is None:
            self._chunks = iter(self._response.content)
        try:
            for chunk in self._chunks:
                yield bytes(chunk)
        finally:
            self.close()

    def bytes(self) -> bytes:
        """Every byte of the object, read to the end."""
        return b"".join(self)

    def text(self, encoding: str = "utf-8") -> str:
        """Every byte of the object, decoded."""
        return self.bytes().decode(encoding)

    def close(self) -> None:
        """Release the connection the object is streaming over."""
        self._stream.__exit__(None, None, None)

    def __enter__(self) -> "ObjectBody":
        """Hand back the body, which closes when the block ends."""
        return self

    def __exit__(self, *_exception) -> None:
        """Release the connection the object is streaming over."""
        self.close()


class AsyncObjectBody:
    """An object's bytes, with what the bucket knows about it beside them, read with
    ``await`` and ``async for``."""

    #: What the bucket knows about the object.
    info: ObjectInfo

    def __init__(self, info: ObjectInfo, stream, response):
        self.info = info
        self._stream = stream
        self._response = response
        self._chunks: AsyncIterator | None = None

    async def __aiter__(self) -> AsyncIterator[bytes]:
        """The bytes, a chunk at a time, as they arrive."""
        if self._chunks is None:
            self._chunks = self._response.content.__aiter__()
        try:
            async for chunk in self._chunks:
                yield bytes(chunk)
        finally:
            await self.aclose()

    async def bytes(self) -> bytes:
        """Every byte of the object, read to the end."""
        held = [chunk async for chunk in self]
        return b"".join(held)

    async def text(self, encoding: str = "utf-8") -> str:
        """Every byte of the object, decoded."""
        return (await self.bytes()).decode(encoding)

    async def aclose(self) -> None:
        """Release the connection the object is streaming over."""
        await self._stream.__aexit__(None, None, None)

    async def __aenter__(self) -> "AsyncObjectBody":
        """Hand back the body, which closes when the block ends."""
        return self

    async def __aexit__(self, *_exception) -> None:
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


class _Writer:
    def __init__(self, store: Bucket, key: str, options: _WriteOptions):
        self._store = store
        self._key = key
        self._options = options
        self._reached = store._runtime("put")
        self._buffered = bytearray()
        self._pending: list[tuple[int, bytes]] = []
        self._completed: list[CompletedPart] = []
        self._upload_id = ""
        self._next = 1
        self._closed = False

    def write(self, data: bytes) -> None:
        if self._closed:
            raise ValueError(f'the writer for "{self._key}" is closed')
        self._buffered += data
        self._guarded(lambda: self._drain(False))

    def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        if not self._upload_id and len(self._buffered) <= self._store._single_ceiling:
            self._guarded(self._put_whole)
            return
        self._guarded(lambda: self._drain(True))
        self._guarded(self._settle)

    def abort(self) -> None:
        self._closed = True
        self._discard()

    def _guarded(self, work) -> None:
        try:
            work()
        except BaseException:
            self._discard()
            raise

    def _discard(self) -> None:
        if not self._upload_id:
            return
        upload_id, self._upload_id = self._upload_id, ""
        self._reached.client.abort_multipart(
            AbortMultipartRequest(bucket=self._reached.bucket, key=self._key, upload_id=upload_id),
            headers=self._reached.headers,
        )

    def _drain(self, final: bool) -> None:
        if not self._upload_id:
            if final or len(self._buffered) <= self._store._single_ceiling:
                return
            self._begin()
        part_size = self._store._part_size
        while len(self._buffered) >= part_size:
            self._stage(bytes(self._buffered[:part_size]))
            del self._buffered[:part_size]
            if len(self._pending) == _PARTS_IN_FLIGHT:
                self._flush()
        if not final:
            return
        if self._buffered:
            self._stage(bytes(self._buffered))
            self._buffered.clear()
        self._flush()

    def _begin(self) -> None:
        try:
            response = self._reached.client.create_multipart(
                _create_multipart(self._reached, self._key, self._options),
                headers=self._reached.headers,
            )
        except ConnectError as error:
            raise _refused(self._key, error) from None
        self._upload_id = response.upload_id

    def _stage(self, data: bytes) -> None:
        self._pending.append((self._next, data))
        self._next += 1

    def _flush(self) -> None:
        if not self._pending:
            return
        try:
            response = self._reached.client.sign_parts(
                _sign_parts(
                    self._reached, self._key, self._upload_id, [n for n, _ in self._pending]
                ),
                headers=self._reached.headers,
            )
        except ConnectError as error:
            raise _refused(self._key, error) from None
        targets = {part.part_number: part for part in response.parts}
        pending, self._pending = self._pending, []
        _unsigned(self._key, pending, targets)
        with ThreadPoolExecutor(max_workers=_PARTS_IN_FLIGHT) as sending:
            sent = [
                sending.submit(self._send, targets[number], number, data)
                for number, data in pending
            ]
            for part in sent:
                self._completed.append(part.result())

    def _send(self, target: SignedPart, number: int, data: bytes) -> CompletedPart:
        response = self._store._http.put(target.url, dict(target.headers), data)
        if response.status >= 300:
            raise RuntimeError(f'part {number} of "{self._key}" was refused ({response.status})')
        return CompletedPart(part_number=number, etag=response.headers.get("etag", ""))

    def _settle(self) -> None:
        self._completed.sort(key=lambda part: part.part_number)
        upload_id, self._upload_id = self._upload_id, ""
        try:
            self._reached.client.complete_multipart(
                _complete_multipart(
                    self._reached, self._key, upload_id, self._completed, self._options
                ),
                headers=self._reached.headers,
            )
        except ConnectError as error:
            self._upload_id = upload_id
            raise _refused(self._key, error) from None

    def _put_whole(self) -> None:
        options = self._options
        target = self._store._sign(
            "put",
            self._key,
            SignedOperation.PUT,
            SignedAudience.INTERNAL,
            _put_constraints(options),
        )
        response = self._store._http.execute(
            target.method or "PUT",
            target.url,
            _put_headers(target, options),
            bytes(self._buffered),
        )
        if response.status >= 300:
            raise _refused_status(self._key, response.status) or RuntimeError(
                f'writing "{self._key}" was refused ({response.status})'
            )


class _ReadStream(io.RawIOBase):
    def __init__(self, body: ObjectBody):
        self._body = body
        self._chunks = iter(body)
        self._held = b""

    def readable(self) -> bool:
        return True

    def readinto(self, target) -> int:
        while not self._held:
            chunk = next(self._chunks, b"")
            if not chunk:
                return 0
            self._held = chunk
        taken = min(len(target), len(self._held))
        target[:taken] = self._held[:taken]
        self._held = self._held[taken:]
        return taken

    def close(self) -> None:
        self._body.close()
        super().close()


class _WriteStream(io.RawIOBase):
    def __init__(self, writer: _Writer):
        self._writer = writer

    def writable(self) -> bool:
        return True

    def write(self, data) -> int:
        self._writer.write(bytes(data))
        return len(data)

    def close(self) -> None:
        if not self.closed:
            self._writer.close()
        super().close()


class _AsyncWriter:
    def __init__(self, store: Bucket, key: str, options: _WriteOptions):
        self._store = store
        self._key = key
        self._options = options
        self._reached = store._runtime_async("put_async")
        self._buffered = bytearray()
        self._pending: list[tuple[int, bytes]] = []
        self._completed: list[CompletedPart] = []
        self._upload_id = ""
        self._next = 1
        self._closed = False

    async def write(self, data: bytes) -> None:
        if self._closed:
            raise ValueError(f'the writer for "{self._key}" is closed')
        self._buffered += data
        await self._guarded(self._drain(False))

    async def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        if not self._upload_id and len(self._buffered) <= self._store._single_ceiling:
            await self._guarded(self._put_whole())
            return
        await self._guarded(self._drain(True))
        await self._guarded(self._settle())

    async def abort(self) -> None:
        self._closed = True
        await self._discard()

    async def _guarded(self, work) -> None:
        try:
            await work
        except BaseException:
            await self._discard()
            raise

    async def _discard(self) -> None:
        if not self._upload_id:
            return
        upload_id, self._upload_id = self._upload_id, ""
        await self._reached.client.abort_multipart(
            AbortMultipartRequest(bucket=self._reached.bucket, key=self._key, upload_id=upload_id),
            headers=self._reached.headers,
        )

    async def _drain(self, final: bool) -> None:
        if not self._upload_id:
            if final or len(self._buffered) <= self._store._single_ceiling:
                return
            await self._begin()
        part_size = self._store._part_size
        while len(self._buffered) >= part_size:
            self._stage(bytes(self._buffered[:part_size]))
            del self._buffered[:part_size]
            if len(self._pending) == _PARTS_IN_FLIGHT:
                await self._flush()
        if not final:
            return
        if self._buffered:
            self._stage(bytes(self._buffered))
            self._buffered.clear()
        await self._flush()

    async def _begin(self) -> None:
        try:
            response = await self._reached.client.create_multipart(
                _create_multipart(self._reached, self._key, self._options),
                headers=self._reached.headers,
            )
        except ConnectError as error:
            raise _refused(self._key, error) from None
        self._upload_id = response.upload_id

    def _stage(self, data: bytes) -> None:
        self._pending.append((self._next, data))
        self._next += 1

    async def _flush(self) -> None:
        if not self._pending:
            return
        try:
            response = await self._reached.client.sign_parts(
                _sign_parts(
                    self._reached, self._key, self._upload_id, [n for n, _ in self._pending]
                ),
                headers=self._reached.headers,
            )
        except ConnectError as error:
            raise _refused(self._key, error) from None
        targets = {part.part_number: part for part in response.parts}
        pending, self._pending = self._pending, []
        _unsigned(self._key, pending, targets)
        self._completed.extend(
            await asyncio.gather(
                *(self._send(targets[number], number, data) for number, data in pending)
            )
        )

    async def _send(self, target: SignedPart, number: int, data: bytes) -> CompletedPart:
        response = await self._store._http_async.put(target.url, dict(target.headers), data)
        if response.status >= 300:
            raise RuntimeError(f'part {number} of "{self._key}" was refused ({response.status})')
        return CompletedPart(part_number=number, etag=response.headers.get("etag", ""))

    async def _settle(self) -> None:
        self._completed.sort(key=lambda part: part.part_number)
        upload_id, self._upload_id = self._upload_id, ""
        try:
            await self._reached.client.complete_multipart(
                _complete_multipart(
                    self._reached, self._key, upload_id, self._completed, self._options
                ),
                headers=self._reached.headers,
            )
        except ConnectError as error:
            self._upload_id = upload_id
            raise _refused(self._key, error) from None

    async def _put_whole(self) -> None:
        options = self._options
        target = await self._store._sign_async(
            "put_async",
            self._key,
            SignedOperation.PUT,
            SignedAudience.INTERNAL,
            _put_constraints(options),
        )
        response = await self._store._http_async.execute(
            target.method or "PUT",
            target.url,
            _put_headers(target, options),
            bytes(self._buffered),
        )
        if response.status >= 300:
            raise _refused_status(self._key, response.status) or RuntimeError(
                f'writing "{self._key}" was refused ({response.status})'
            )


class _AsyncReadStream:
    def __init__(self, store: Bucket, key: str):
        self._store = store
        self._key = key
        self._body: AsyncObjectBody | None = None
        self._chunks: AsyncIterator[bytes] | None = None
        self._held = b""

    async def read(self, size: int = -1) -> bytes:
        """The next ``size`` bytes of the object, or every byte left when ``size`` is
        negative."""
        read = bytearray()
        while size < 0 or len(read) < size:
            if not self._held:
                self._held = await anext(await self._opened(), b"")
                if not self._held:
                    break
            taken = len(self._held) if size < 0 else min(size - len(read), len(self._held))
            read += self._held[:taken]
            self._held = self._held[taken:]
        return bytes(read)

    async def aclose(self) -> None:
        """Release the connection the object is streaming over."""
        if self._body is not None:
            await self._body.aclose()

    async def __aenter__(self) -> "_AsyncReadStream":
        return self

    async def __aexit__(self, *_exception) -> None:
        await self.aclose()

    async def _opened(self):
        if self._chunks is None:
            self._body = await self._store.get_async(self._key)
            self._chunks = self._body.__aiter__()
        return self._chunks


class _AsyncWriteStream:
    def __init__(self, writer: _AsyncWriter):
        self._writer = writer

    async def write(self, data: bytes) -> int:
        """Buffer ``data``, sending a part on its way whenever enough bytes have come in to
        fill one."""
        await self._writer.write(bytes(data))
        return len(data)

    async def aclose(self) -> None:
        """Settle the write and land the object."""
        await self._writer.close()

    async def __aenter__(self) -> "_AsyncWriteStream":
        return self

    async def __aexit__(self, kind, *_exception) -> None:
        if kind is None:
            await self.aclose()
        else:
            await self._writer.abort()


def _sign_request(
    reached: _Reached,
    key: str,
    operation: SignedOperation,
    audience: SignedAudience,
    constraints: SignConstraints | None,
    expires_in: float | timedelta | None,
) -> SignRequest:
    request = SignRequest(
        bucket=reached.bucket,
        key=key,
        operation=operation,
        audience=audience,
        constraints=constraints,
    )
    if expires_in is not None:
        request.expires_in = _duration(expires_in)
    return request


def _create_multipart(
    reached: _Reached, key: str, options: _WriteOptions
) -> CreateMultipartRequest:
    return CreateMultipartRequest(
        bucket=reached.bucket,
        key=key,
        content_type=options.content_type or "",
        cache_control=options.cache_control or "",
        metadata=dict(options.metadata or {}),
    )


def _sign_parts(
    reached: _Reached, key: str, upload_id: str, numbers: list[int]
) -> SignPartsRequest:
    return SignPartsRequest(
        bucket=reached.bucket,
        key=key,
        upload_id=upload_id,
        part_numbers=numbers,
        audience=SignedAudience.INTERNAL,
    )


def _complete_multipart(
    reached: _Reached,
    key: str,
    upload_id: str,
    parts: list[CompletedPart],
    options: _WriteOptions,
) -> CompleteMultipartRequest:
    return CompleteMultipartRequest(
        bucket=reached.bucket,
        key=key,
        upload_id=upload_id,
        parts=parts,
        if_none_match=options.if_none_match,
        if_match=options.if_match or "",
    )


def _put_constraints(options: _WriteOptions) -> SignConstraints:
    return SignConstraints(
        content_type=options.content_type or "",
        if_none_match=options.if_none_match,
        if_match=options.if_match or "",
        cache_control=options.cache_control or "",
        metadata=dict(options.metadata or {}),
    )


def _put_headers(target: PresignedTarget, options: _WriteOptions) -> dict[str, str]:
    headers = dict(target.headers)
    if options.content_type:
        headers["content-type"] = options.content_type
    if options.cache_control:
        headers["cache-control"] = options.cache_control
    if options.if_none_match:
        headers["if-none-match"] = options.if_none_match
    if options.if_match:
        headers["if-match"] = options.if_match
    return headers


def _unsigned(
    key: str, pending: list[tuple[int, bytes]], targets: Mapping[int, SignedPart]
) -> None:
    missing = sorted({number for number, _ in pending} - set(targets))
    if missing:
        raise RuntimeError(f'the runtime signed no url for part {missing[0]} of "{key}"')


def _read_chunks(data: bytes | str | BinaryIO | os.PathLike, size: int) -> Iterator[bytes]:
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


def _byte_range(offset: int, length: int | None) -> str:
    if length is None:
        return f"bytes={offset}-"
    return f"bytes={offset}-{offset + length - 1}"


def _download_filename(key: str, download: bool | str | None) -> str:
    if isinstance(download, str):
        return download
    return key.rsplit("/", 1)[-1] if download else ""


def _expires_at(expires_in: float | timedelta | None) -> datetime | None:
    if expires_in is None:
        return None
    held = expires_in if isinstance(expires_in, timedelta) else timedelta(seconds=expires_in)
    return datetime.now(timezone.utc) + held


def _duration(expires_in: float | timedelta) -> Duration:
    if isinstance(expires_in, timedelta):
        return Duration.from_timedelta(expires_in)
    return Duration.from_seconds(expires_in)


def _object(wire: WireObjectInfo | None, key: str) -> ObjectInfo:
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


def _refused(key: str, error: ConnectError) -> Exception:
    if error.code is Code.NOT_FOUND:
        return ObjectNotFound(key)
    if error.code is Code.FAILED_PRECONDITION:
        return PreconditionFailed(key)
    return error


def _refused_status(key: str, status: int) -> Exception | None:
    if status == 404:
        return ObjectNotFound(key)
    if status in (409, 412):
        return PreconditionFailed(key)
    return None
