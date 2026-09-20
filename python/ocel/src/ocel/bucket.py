import inspect
import io
import os
from collections.abc import Iterator, Mapping, Sequence
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from typing import IO, BinaryIO

from connectrpc.code import Code
from connectrpc.errors import ConnectError
from protobuf import Oneof
from protobuf.wkt import Duration
from pyqwest import SyncClient

from ocel._binding import bucket_binding, unprovisioned
from ocel._declare import declare, discovering
from ocel.gen.app.bucket.v1.bucket_connect import BucketServiceClientSync
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


class _Reached:
    def __init__(
        self,
        client: BucketServiceClientSync,
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
        self._http = SyncClient()
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
        return f"{reached.public_base_url.rstrip('/')}/{key}"

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
        request = SignRequest(
            bucket=reached.bucket,
            key=key,
            operation=operation,
            audience=audience,
            constraints=constraints,
        )
        if expires_in is not None:
            request.expires_in = _duration(expires_in)
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
            self._reached = _Reached(
                BucketServiceClientSync(address.rstrip("/"), send_compression=None),
                properties.bucket,
                properties.public_base_url,
                {"Authorization": f"Bearer {token}"},
            )
        return self._reached


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
                CreateMultipartRequest(
                    bucket=self._reached.bucket,
                    key=self._key,
                    content_type=self._options.content_type or "",
                    cache_control=self._options.cache_control or "",
                    metadata=dict(self._options.metadata or {}),
                ),
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
                SignPartsRequest(
                    bucket=self._reached.bucket,
                    key=self._key,
                    upload_id=self._upload_id,
                    part_numbers=[number for number, _ in self._pending],
                    audience=SignedAudience.INTERNAL,
                ),
                headers=self._reached.headers,
            )
        except ConnectError as error:
            raise _refused(self._key, error) from None
        targets = {part.part_number: part for part in response.parts}
        pending, self._pending = self._pending, []
        with ThreadPoolExecutor(max_workers=_PARTS_IN_FLIGHT) as sending:
            sent = [
                sending.submit(self._send, targets[number], number, data)
                for number, data in pending
                if number in targets
            ]
            if len(sent) != len(pending):
                signed = {number for number, _ in pending} - set(targets)
                raise RuntimeError(
                    f'the runtime signed no url for part {min(signed)} of "{self._key}"'
                )
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
                CompleteMultipartRequest(
                    bucket=self._reached.bucket,
                    key=self._key,
                    upload_id=upload_id,
                    parts=self._completed,
                    if_none_match=self._options.if_none_match,
                    if_match=self._options.if_match or "",
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
            SignConstraints(
                content_type=options.content_type or "",
                if_none_match=options.if_none_match,
                if_match=options.if_match or "",
            ),
        )
        headers = dict(target.headers)
        if options.content_type:
            headers["content-type"] = options.content_type
        if options.cache_control:
            headers["cache-control"] = options.cache_control
        if options.if_none_match:
            headers["if-none-match"] = options.if_none_match
        if options.if_match:
            headers["if-match"] = options.if_match
        for name, value in (options.metadata or {}).items():
            headers[f"x-amz-meta-{name}"] = value
        response = self._store._http.execute(
            target.method or "PUT", target.url, headers, bytes(self._buffered)
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
