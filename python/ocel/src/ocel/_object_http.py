from contextlib import AbstractAsyncContextManager, AbstractContextManager
from typing import Protocol, TypeVar

from pyqwest import Client, Headers, Response, SyncClient, SyncResponse


class Answered(Protocol):
    @property
    def status(self) -> int: ...

    @property
    def headers(self) -> Headers: ...


Stream = TypeVar("Stream")
Answer = TypeVar("Answer", bound=Answered, covariant=True)


class ObjectHttp(Protocol[Stream, Answer]):
    async def put(
        self, url: str, headers: dict[str, str], body: bytes, method: str = "PUT"
    ) -> Answered: ...

    async def open_read(self, url: str, headers: dict[str, str]) -> tuple[Stream, Answer]: ...

    async def release(self, stream: Stream) -> None: ...


class BlockingHttp:
    def __init__(self, client: SyncClient):
        self._client = client

    async def put(
        self, url: str, headers: dict[str, str], body: bytes, method: str = "PUT"
    ) -> Answered:
        return self._client.execute(method, url, headers, body)

    async def open_read(
        self, url: str, headers: dict[str, str]
    ) -> tuple[AbstractContextManager[SyncResponse], SyncResponse]:
        stream = self._client.stream("GET", url, headers)
        return stream, stream.__enter__()

    async def release(self, stream: AbstractContextManager[SyncResponse]) -> None:
        stream.__exit__(None, None, None)


class AwaitedHttp:
    def __init__(self, client: Client):
        self._client = client

    async def put(
        self, url: str, headers: dict[str, str], body: bytes, method: str = "PUT"
    ) -> Answered:
        return await self._client.execute(method, url, headers, body)

    async def open_read(
        self, url: str, headers: dict[str, str]
    ) -> tuple[AbstractAsyncContextManager[Response], Response]:
        stream = self._client.stream("GET", url, headers)
        return stream, await stream.__aenter__()

    async def release(self, stream: AbstractAsyncContextManager[Response]) -> None:
        await stream.__aexit__(None, None, None)
