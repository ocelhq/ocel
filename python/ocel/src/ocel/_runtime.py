import asyncio
from collections.abc import Callable, Coroutine, Mapping
from concurrent.futures import ThreadPoolExecutor
from typing import Any, Generic, TypeVar, cast

from connectrpc.errors import ConnectError

from ocel._binding import read_runtime, refuse_unprovisioned
from ocel._declare import is_discovering
from ocel._error import refuse_runtime_call

C = TypeVar("C")
H = TypeVar("H")
H_co = TypeVar("H_co", covariant=True)
T = TypeVar("T")

Operation = Coroutine[Any, Any, T]


def run_to_completion(operation: Operation[T]) -> T:
    try:
        operation.send(None)
    except StopIteration as finished:
        return cast(T, finished.value)
    operation.close()
    raise RuntimeError("a blocking operation awaited something that suspends")


class _RuntimeCalls:
    def __init__(self, target: object, blocking: bool, headers: Mapping[str, str], what: str):
        self._target = target
        self._blocking = blocking
        self._headers = headers
        self._what = what

    def __getattr__(self, name: str) -> Callable[..., Operation[Any]]:
        method = getattr(self._target, name)

        async def call(*arguments: Any, **keywords: Any) -> Any:
            try:
                answer = method(*arguments, headers=self._headers, **keywords)
                return answer if self._blocking else await answer
            except ConnectError as error:
                raise refuse_runtime_call(self._what, name, error) from None

        return call


class Session(Generic[C, H_co]):
    def __init__(self, client: C, blocking: bool, http: H_co):
        self.client = client
        self.blocking = blocking
        self._http = http

    @property
    def http(self) -> H_co:
        return self._http

    async def offload(self, function: Callable[..., T], *arguments: Any) -> T:
        if self.blocking:
            return function(*arguments)
        return await asyncio.to_thread(function, *arguments)

    async def gather(self, operations: list[Operation[T]]) -> list[T]:
        if not self.blocking:
            settled = await asyncio.gather(*operations, return_exceptions=True)
            for outcome in settled:
                if isinstance(outcome, BaseException):
                    raise outcome
            return cast(list[T], settled)
        with ThreadPoolExecutor(max_workers=max(len(operations), 1)) as sending:
            running = [sending.submit(run_to_completion, operation) for operation in operations]
            return [future.result() for future in running]


class RuntimeConnection(Generic[C]):
    def __init__(
        self,
        what: str,
        blocking_client: Callable[[str], object],
        awaited_client: Callable[[str], C],
        bind: Callable[[], object] = lambda: None,
    ):
        self.what = what
        self._blocking_client = blocking_client
        self._awaited_client = awaited_client
        self._bind = bind
        self._bound = False
        self._targets: dict[tuple[str, bool], object] = {}

    def open(self, access: str, blocking: bool) -> C:
        if is_discovering():
            raise refuse_unprovisioned(self.what, access)
        if not self._bound:
            self._bind()
            self._bound = True
        address, headers = read_runtime()
        target = self._targets.get((address, blocking))
        if target is None:
            build = self._blocking_client if blocking else self._awaited_client
            target = self._targets[(address, blocking)] = build(address)
        return cast(C, _RuntimeCalls(target, blocking, headers, self.what))

    def session(self, access: str, blocking: bool, http: H) -> Session[C, H]:
        return Session(self.open(access, blocking), blocking, http)

    def call(self, access: str, send: Callable[[C], Operation[T]]) -> T:
        return run_to_completion(send(self.open(access, True)))

    async def call_async(self, access: str, send: Callable[[C], Operation[T]]) -> T:
        return await send(self.open(access, False))

    def run(self, access: str, http: H, operation: Callable[[Session[C, H]], Operation[T]]) -> T:
        return run_to_completion(operation(self.session(access, True, http)))

    async def run_async(
        self, access: str, http: H, operation: Callable[[Session[C, H]], Operation[T]]
    ) -> T:
        return await operation(self.session(access, False, http))
