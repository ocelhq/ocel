import functools
from collections.abc import Awaitable, Callable, Iterable, Mapping, Sequence
from dataclasses import dataclass, field
from datetime import datetime
from typing import Any, Generic, Literal, Protocol, TypeVar, cast, overload

from protobuf import Oneof

from ocel import _registry
from ocel._binding import read_runtime, refuse_unbound, refuse_unprovisioned
from ocel._declare import declare, find_caller_source, is_discovering
from ocel._payload import Codec, encode_json, find_payload_type
from ocel._wire import Seconds, encode_due_at, encode_duration, encode_lane, encode_retry_policy
from ocel.gen.app.resources.v1.resources_pb import (
    BatchPolicy,
    DeclareRequest,
    ResourceIdentifier,
    ResourceType,
    TaskConfig,
)
from ocel.gen.app.task.v1.task_connect import TaskServiceClient, TaskServiceClientSync
from ocel.gen.app.task.v1.task_pb import (
    BatchTriggerItem,
    BatchTriggerRequest,
    TriggerOptions,
    TriggerRequest,
)
from ocel.gen.app.task.v1.task_pb import Debounce as WireDebounce
from ocel.run import RunContext, RunHandle
from ocel.topic import Lane, Retry
from ocel.worker import Worker, get_worker_name

P = TypeVar("P")
R = TypeVar("R")

_KIND = "task"


@dataclass(frozen=True)
class Batch:
    """Runs a task over several payloads at once: its function receives a list of them."""

    #: The most payloads one run receives, up to 1,000, or 10 for an ordered task.
    size: int
    #: How long a partial batch waits for more payloads before it runs, up to 300 seconds.
    timeout: Seconds | None = None


@dataclass(frozen=True)
class Debounce:
    """Collapses the triggers sharing ``key`` into one run that starts once ``delay`` has
    passed without another."""

    #: What the triggers that collapse together share.
    key: str
    #: How long the run waits for another trigger, up to 30 days.
    delay: Seconds


@dataclass(frozen=True)
class Trigger(Generic[P]):
    """One payload of :meth:`Task.batch_trigger` with the options :meth:`Task.trigger`
    takes."""

    #: What the run receives.
    payload: P
    #: How long until the run is due, or the moment it is due, up to 30 days ahead.
    delay: Seconds | datetime | None = field(default=None, kw_only=True)
    #: How long the run may wait to start before it expires, up to 14 days.
    ttl: Seconds | None = field(default=None, kw_only=True)
    #: A key that makes a repeated trigger answer the run the first one started.
    idempotency_key: str | None = field(default=None, kw_only=True)
    #: How long ``idempotency_key`` is remembered; 30 days when ``None``.
    idempotency_key_ttl: Seconds | None = field(default=None, kw_only=True)
    #: Collapses triggers into one run.
    debounce: Debounce | None = field(default=None, kw_only=True)
    #: The ordering key of an ordered task: runs sharing it execute one at a time, in order.
    key: str | None = field(default=None, kw_only=True)
    #: The lane the run is read from.
    lane: Lane | Literal["high", "default", "low"] | None = field(default=None, kw_only=True)
    #: Lowers the task's attempts for this run.
    max_attempts: int | None = field(default=None, kw_only=True)
    #: Labels that :meth:`Runs.list` filters by.
    tags: Sequence[str] = field(default=(), kw_only=True)
    #: JSON the run record keeps beside the payload.
    metadata: Mapping[str, Any] | None = field(default=None, kw_only=True)


class Task(Generic[P, R]):
    """A task an app declares with :func:`task`: the function it decorated, which still
    runs when called, and the handle its runs are triggered through. Each operation has a
    blocking form and an awaited ``_async`` form."""

    #: The name the task was declared under, and the name its binding is delivered as.
    name: str

    def __init__(self, name: str, run: Callable[..., Any], codec: Codec):
        """Wrap ``run`` as the task named ``name``. Prefer :func:`task`, which declares the
        task and registers it with its worker as well."""
        self.name = name
        self._run = run
        self._codec = codec
        self._connection: tuple[Any, dict[str, str]] | None = None
        self._async_connection: tuple[Any, dict[str, str]] | None = None
        functools.update_wrapper(self, run)

    def __call__(self, *arguments: Any, **keyword_arguments: Any) -> Any:
        """Call the decorated function directly, as if it were not a task."""
        return self._run(*arguments, **keyword_arguments)

    def __repr__(self) -> str:
        return f"Task({self.name!r})"

    def trigger(
        self,
        payload: P,
        *,
        delay: Seconds | datetime | None = None,
        ttl: Seconds | None = None,
        idempotency_key: str | None = None,
        idempotency_key_ttl: Seconds | None = None,
        debounce: Debounce | None = None,
        key: str | None = None,
        lane: Lane | Literal["high", "default", "low"] | None = None,
        max_attempts: int | None = None,
        tags: Sequence[str] = (),
        metadata: Mapping[str, Any] | None = None,
    ) -> RunHandle:
        """Start a run of the task with ``payload`` and return its handle. The options
        are those :class:`Trigger` documents. A payload whose JSON exceeds 256 KiB is
        refused before anything is sent."""
        request = self._build_trigger_request(
            "trigger",
            payload,
            delay=delay,
            ttl=ttl,
            idempotency_key=idempotency_key,
            idempotency_key_ttl=idempotency_key_ttl,
            debounce=debounce,
            key=key,
            lane=lane,
            max_attempts=max_attempts,
            tags=tags,
            metadata=metadata,
        )
        client, headers = self._ensure_connection("trigger")
        return RunHandle(client.trigger(request, headers=headers).id)

    async def trigger_async(
        self,
        payload: P,
        *,
        delay: Seconds | datetime | None = None,
        ttl: Seconds | None = None,
        idempotency_key: str | None = None,
        idempotency_key_ttl: Seconds | None = None,
        debounce: Debounce | None = None,
        key: str | None = None,
        lane: Lane | Literal["high", "default", "low"] | None = None,
        max_attempts: int | None = None,
        tags: Sequence[str] = (),
        metadata: Mapping[str, Any] | None = None,
    ) -> RunHandle:
        """Start a run of the task with ``payload``, as :meth:`trigger` does."""
        request = self._build_trigger_request(
            "trigger_async",
            payload,
            delay=delay,
            ttl=ttl,
            idempotency_key=idempotency_key,
            idempotency_key_ttl=idempotency_key_ttl,
            debounce=debounce,
            key=key,
            lane=lane,
            max_attempts=max_attempts,
            tags=tags,
            metadata=metadata,
        )
        client, headers = self._ensure_async_connection("trigger_async")
        return RunHandle((await client.trigger(request, headers=headers)).id)

    def batch_trigger(self, items: Iterable[Trigger[P] | P]) -> list[RunHandle]:
        """Start one run per item, up to 1,000, and return their handles in the same order.
        An item is a payload, or a :class:`Trigger` carrying a payload with its options."""
        request = self._build_batch_trigger_request("batch_trigger", items)
        client, headers = self._ensure_connection("batch_trigger")
        return [RunHandle(id) for id in client.batch_trigger(request, headers=headers).ids]

    async def batch_trigger_async(self, items: Iterable[Trigger[P] | P]) -> list[RunHandle]:
        """Start one run per item, as :meth:`batch_trigger` does."""
        request = self._build_batch_trigger_request("batch_trigger_async", items)
        client, headers = self._ensure_async_connection("batch_trigger_async")
        response = await client.batch_trigger(request, headers=headers)
        return [RunHandle(id) for id in response.ids]

    def _build_trigger_request(self, access: str, payload: P, **options: Any) -> TriggerRequest:
        self._ensure_connection(access)
        return TriggerRequest(
            task=self.name,
            payload=self._codec.encode(payload),
            options=_build_trigger_options(Trigger(payload, **options)),
        )

    def _build_batch_trigger_request(
        self, access: str, items: Iterable[Trigger[P] | P]
    ) -> BatchTriggerRequest:
        self._ensure_connection(access)
        triggers = [item if isinstance(item, Trigger) else Trigger(item) for item in items]
        return BatchTriggerRequest(
            task=self.name,
            items=[
                BatchTriggerItem(
                    payload=self._codec.encode(trigger.payload),
                    options=_build_trigger_options(trigger),
                )
                for trigger in triggers
            ],
        )

    def _ensure_connection(self, access: str) -> tuple[Any, dict[str, str]]:
        if is_discovering():
            raise refuse_unprovisioned(f'task("{self.name}")', access)
        if self._connection is None:
            address, headers = read_runtime()
            client = TaskServiceClientSync(address, send_compression=None)
            if refusal := refuse_unbound(self.name, "task"):
                raise refusal
            self._connection = (client, headers)
        return self._connection

    def _ensure_async_connection(self, access: str) -> tuple[Any, dict[str, str]]:
        if is_discovering():
            raise refuse_unprovisioned(f'task("{self.name}")', access)
        if self._async_connection is None:
            address, headers = read_runtime()
            client = TaskServiceClient(address, send_compression=None)
            if refusal := refuse_unbound(self.name, "task"):
                raise refusal
            self._async_connection = (client, headers)
        return self._async_connection


def _build_trigger_options(trigger: Trigger[Any]) -> TriggerOptions | None:
    options = TriggerOptions(
        due_at=encode_due_at(trigger.delay),
        ttl=encode_duration(trigger.ttl),
        idempotency_key=trigger.idempotency_key or "",
        idempotency_key_ttl=encode_duration(trigger.idempotency_key_ttl),
        debounce=(
            WireDebounce(key=trigger.debounce.key, delay=encode_duration(trigger.debounce.delay))
            if trigger.debounce
            else None
        ),
        key=trigger.key or "",
        lane=encode_lane(trigger.lane),
        max_attempts=trigger.max_attempts or 0,
        tags=list(trigger.tags),
        metadata=encode_json(dict(trigger.metadata)).encode() if trigger.metadata else b"",
    )
    return None if options == TriggerOptions() else options


def task(
    name: str,
    *,
    schema: Any = None,
    retry: Retry | None = None,
    concurrency: int | None = None,
    max_duration: Seconds | None = None,
    ttl: Seconds | None = None,
    ordered: bool = False,
    batch: Batch | None = None,
    worker: Worker | None = None,
    cron: str | None = None,
    on_success: Callable[..., Any] | None = None,
    on_failure: Callable[..., Any] | None = None,
    on_complete: Callable[..., Any] | None = None,
    on_cancel: Callable[..., Any] | None = None,
    catch_error: Callable[..., Any] | None = None,
    middleware: Callable[..., Any] | None = None,
    on_start_attempt: Callable[..., Any] | None = None,
) -> "_TaskDecorator":
    """Declare the decorated function as the task named ``name``. The function takes
    ``(payload, ctx)`` and may be sync, when it runs in a thread, or async; what it returns
    is the run's output.

    The payload's type is ``schema``, or else the function's first parameter annotation; a
    type beyond plain JSON (a pydantic model, a dataclass) is validated with pydantic, which
    the ``ocel[pydantic]`` extra installs, and a payload that fails it aborts the run.
    With ``batch`` the function receives a list of payloads.

    ``retry``, ``concurrency``, ``max_duration``, ``ttl``, ``ordered`` and ``cron`` shape
    how runs are scheduled; durations are seconds or a ``timedelta``. ``worker`` places the
    task on the :class:`Worker` that :func:`ocel.worker` returned.

    The hooks may each be sync or async: ``on_start_attempt(payload, ctx)`` and
    ``middleware(payload, ctx, next)`` wrap every attempt; ``catch_error(payload, error,
    ctx)`` may return ``CatchErrorResult(skip_retrying=True)`` to fail without retrying;
    ``on_success(payload, output, ctx)``, ``on_failure(payload, error, ctx)`` and
    ``on_complete(payload, result, ctx)`` run once the run is over, and
    ``on_cancel(payload, ctx)`` when it is canceled while executing."""
    source = find_caller_source() if is_discovering() else ""

    def decorate(run: Callable[..., Any]) -> Task[Any, Any]:
        worker_name = get_worker_name(worker)
        codec = Codec(schema if schema is not None else find_payload_type(run, batch is not None))
        handle: Task[Any, Any] = Task(name, run, codec)
        _registry.register(
            _registry.Registration(
                kind="task",
                topic=name,
                name=name,
                worker=worker_name or _registry.DEFAULT_WORKER,
                handler=run,
                decode=codec.decode,
                batch=batch is not None,
                hooks=_registry.Hooks(
                    on_success=on_success,
                    on_failure=on_failure,
                    on_complete=on_complete,
                    on_cancel=on_cancel,
                    catch_error=catch_error,
                    middleware=middleware,
                    on_start_attempt=on_start_attempt,
                ),
            )
        )
        if is_discovering():
            config = TaskConfig(
                schema=codec.schema,
                ordered=ordered,
                retry=encode_retry_policy(retry),
                concurrency=concurrency or 0,
                max_duration=encode_duration(max_duration),
                ttl=encode_duration(ttl),
                batch=_build_batch_policy(batch),
                worker=worker_name,
                cron=cron or "",
            )
            declare(
                DeclareRequest(
                    resource=ResourceIdentifier(type=ResourceType.TASK, name=name),
                    config=Oneof(_KIND, config),
                    source=source,
                )
            )
        return handle

    return cast(_TaskDecorator, decorate)


class _TaskDecorator(Protocol):
    @overload
    def __call__(self, run: Callable[[P, RunContext], Awaitable[R]], /) -> Task[P, R]: ...
    @overload
    def __call__(self, run: Callable[[P, RunContext], R], /) -> Task[P, R]: ...
    @overload
    def __call__(self, run: Callable[[P], Awaitable[R]], /) -> Task[P, R]: ...
    @overload
    def __call__(self, run: Callable[[P], R], /) -> Task[P, R]: ...


def _build_batch_policy(batch: Batch | None) -> BatchPolicy | None:
    if batch is None:
        return None
    return BatchPolicy(size=batch.size, timeout=encode_duration(batch.timeout))
