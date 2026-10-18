import asyncio
import threading
from collections.abc import Awaitable, Callable
from typing import Generic, TypeVar

T = TypeVar("T")


def _find_running_loop() -> asyncio.AbstractEventLoop | None:
    try:
        return asyncio.get_running_loop()
    except RuntimeError:
        return None


class LoopLocal(Generic[T]):
    def __init__(self, close: Callable[[T], Awaitable[object]] | None = None):
        self._close = close
        self._lock = threading.Lock()
        self._values: dict[asyncio.AbstractEventLoop | None, T] = {}
        self._closers: dict[asyncio.AbstractEventLoop, asyncio.Task[None]] = {}

    def get(self, open: Callable[[], T]) -> T:
        loop = _find_running_loop()
        with self._lock:
            for gone in [seen for seen in self._values if seen is not None and seen.is_closed()]:
                del self._values[gone]
                self._closers.pop(gone, None)
            if loop not in self._values:
                value = self._values[loop] = open()
                if loop is not None and self._close is not None:
                    self._closers[loop] = loop.create_task(self._close_at_shutdown(value))
            return self._values[loop]

    def forget(self, value: T) -> None:
        loop = _find_running_loop()
        with self._lock:
            if self._values.get(loop) is value:
                del self._values[loop]
                if loop is not None and (closer := self._closers.pop(loop, None)):
                    closer.cancel()

    async def _close_at_shutdown(self, value: T) -> None:
        try:
            await asyncio.get_running_loop().create_future()
        except asyncio.CancelledError:
            if self._close is not None and self._values.get(asyncio.get_running_loop()) is value:
                await self._close(value)
            raise
