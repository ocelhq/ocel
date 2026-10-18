import io
from collections.abc import AsyncIterator
from types import TracebackType
from typing import TYPE_CHECKING

from ocel._runtime import run_to_completion

if TYPE_CHECKING:
    from _typeshed import ReadableBuffer, WriteableBuffer

    from ocel.bucket import AsyncObjectBody, Bucket, ObjectBody, ObjectInfo, Writer


class ReadStream(io.RawIOBase):
    def __init__(self, body: "ObjectBody"):
        self._body = body
        self._chunks = iter(body)
        self._unread = b""

    def readable(self) -> bool:
        return True

    def readinto(self, buffer: "WriteableBuffer", /) -> int:
        target = memoryview(buffer).cast("B")
        while not self._unread:
            chunk = next(self._chunks, b"")
            if not chunk:
                return 0
            self._unread = chunk
        taken = min(len(target), len(self._unread))
        target[:taken] = self._unread[:taken]
        self._unread = self._unread[taken:]
        return taken

    def close(self) -> None:
        self._body.close()
        super().close()


class WriteStream(io.RawIOBase):
    """A bucket object opened for writing with :meth:`Bucket.open` in ``"wb"`` mode. The
    object lands when the handle closes, and not at all when the block it is used in
    raises."""

    def __init__(self, writer: "Writer"):
        """Write through ``writer``. :meth:`Bucket.open` hands one back."""
        self._writer = writer

    @property
    def info(self) -> "ObjectInfo | None":
        """What the bucket knows about the object this handle wrote, once it has closed and
        the object has landed: its etag is this write's own. ``None`` while the handle is
        open, and after a write that raised."""
        return self._writer.info

    def writable(self) -> bool:
        """A write handle is always writable."""
        return True

    def write(self, buffer: "ReadableBuffer", /) -> int:
        """Buffer ``buffer``, sending a part on its way whenever enough bytes have come in
        to fill one."""
        data = bytes(buffer)
        run_to_completion(self._writer.write(data))
        return len(data)

    def close(self) -> None:
        """Finish the write and land the object."""
        if not self.closed:
            run_to_completion(self._writer.close())
        super().close()

    def __exit__(
        self,
        kind: type[BaseException] | None,
        error: BaseException | None,
        traceback: TracebackType | None,
    ) -> None:
        """Land the object when the block ends, or discard what was sent when it raised."""
        if kind is None:
            self.close()
            return
        run_to_completion(self._writer.abort())
        super().close()


class AsyncReadStream:
    def __init__(self, store: "Bucket", key: str):
        self._store = store
        self._key = key
        self._body: AsyncObjectBody | None = None
        self._chunks: AsyncIterator[bytes] | None = None
        self._unread = b""

    async def read(self, size: int = -1) -> bytes:
        """The next ``size`` bytes of the object, or every byte left when ``size`` is
        negative."""
        read = bytearray()
        while size < 0 or len(read) < size:
            if not self._unread:
                self._unread = await anext(await self._open_chunks(), b"")
                if not self._unread:
                    break
            taken = len(self._unread) if size < 0 else min(size - len(read), len(self._unread))
            read += self._unread[:taken]
            self._unread = self._unread[taken:]
        return bytes(read)

    async def aclose(self) -> None:
        """Release the connection the object is streaming over."""
        if self._body is not None:
            await self._body.aclose()

    async def __aenter__(self) -> "AsyncReadStream":
        return self

    async def __aexit__(self, *_exception: object) -> None:
        await self.aclose()

    async def _open_chunks(self) -> AsyncIterator[bytes]:
        if self._chunks is None:
            self._body = await self._store.get_async(self._key)
            self._chunks = self._body.__aiter__()
        return self._chunks


class AsyncWriteStream:
    """A bucket object opened for writing with :meth:`Bucket.open_async` in ``"wb"`` mode,
    used as an async context manager. The object lands when the block ends, and not at all
    when it raises."""

    def __init__(self, writer: "Writer"):
        """Write through ``writer``. :meth:`Bucket.open_async` hands one back."""
        self._writer = writer

    @property
    def info(self) -> "ObjectInfo | None":
        """What the bucket knows about the object this handle wrote, once it has closed and
        the object has landed: its etag is this write's own. ``None`` while the handle is
        open, and after a write that raised."""
        return self._writer.info

    async def write(self, data: bytes) -> int:
        """Buffer ``data``, sending a part on its way whenever enough bytes have come in to
        fill one."""
        await self._writer.write(bytes(data))
        return len(data)

    async def aclose(self) -> None:
        """Finish the write and land the object."""
        await self._writer.close()

    async def __aenter__(self) -> "AsyncWriteStream":
        """Hand back the handle, which lands the object when the block ends."""
        return self

    async def __aexit__(self, kind: type[BaseException] | None, *_exception: object) -> None:
        """Land the object when the block ends, or discard what was sent when it raised."""
        if kind is None:
            await self.aclose()
        else:
            await self._writer.abort()
