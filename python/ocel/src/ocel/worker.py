import asyncio
import inspect
import json
import sys
from collections.abc import Awaitable, Callable, Coroutine
from typing import Any

from protobuf import Oneof

from ocel import _registry
from ocel._declare import declare, find_caller_source, is_discovering
from ocel._error import format_error
from ocel._payload import encode_json
from ocel._wire import decode_timestamp, decode_value
from ocel.gen.app.resources.v1.resources_pb import (
    DeclareRequest,
    ResourceIdentifier,
    ResourceType,
    WorkerConfig,
)
from ocel.gen.app.topic.v1.topic_pb import Attempt, Envelope, Message
from ocel.run import AbortTaskRunError, RunAttempt, RunContext, RunMessage, RunResult

_KIND = "worker"


class Worker:
    """The compute that serves the tasks and consumers placed on it. Pass it as ``worker=``
    to :func:`ocel.task`, :meth:`ocel.Topic.consumer` or :meth:`ocel.Topic.batch_consumer`;
    a task or consumer given none runs on the default worker named ``worker``."""

    #: The name the worker was declared under, which is the ``ocel.json`` app it joins.
    name: str
    #: The most runs this worker executes at once in one process, across all its tasks and
    #: consumers, or ``None`` for no cap.
    concurrency: int | None
    #: What runs once per process before the worker's first run.
    on_start: Callable[[], Any] | None
    #: What wraps every run of every task and consumer on this worker.
    middleware: Callable[..., Any] | None

    def __init__(
        self,
        name: str,
        *,
        concurrency: int | None = None,
        on_start: Callable[[], Any] | None = None,
        middleware: Callable[..., Any] | None = None,
    ):
        """Take the handle for the worker named ``name``. Prefer :func:`worker`, which
        declares the worker as well as handing back its handle."""
        self.name = name
        self.concurrency = concurrency
        self.on_start = on_start
        self.middleware = middleware
        self._started = on_start is None
        self._starting: asyncio.Future[Any] | None = None
        self._slots = asyncio.Semaphore(concurrency) if concurrency else None

    def __repr__(self) -> str:
        return f"Worker({self.name!r})"

    async def _start(self) -> None:
        if self._started:
            return
        if self._starting is None:
            self._starting = asyncio.ensure_future(_call_function(self.on_start))
        starting = self._starting
        try:
            await asyncio.shield(starting)
        except asyncio.CancelledError:
            raise
        except BaseException:
            if self._starting is starting:
                self._starting = None
            raise
        self._started = True


def worker(
    name: str,
    *,
    concurrency: int | None = None,
    on_start: Callable[[], Any | Awaitable[Any]] | None = None,
    middleware: Callable[..., Any] | None = None,
) -> Worker:
    """Declare a worker named ``name`` and return its handle. The worker joins the
    ``ocel.json`` app of the same name, or one Ocel adds with the declaring app's folder.

    ``concurrency`` caps the runs one process executes at once across every task and
    consumer on the worker. ``on_start()`` runs once per process before the first run; when
    it raises, every delivery fails until a later one runs it successfully.
    ``middleware(ctx, next)`` wraps every run: it receives the :class:`RunContext` and
    returns what awaiting ``next()`` returns, or ``next()`` itself when the middleware is a
    plain function, which then runs in a thread. Either hook may be sync or async."""
    handle = Worker(name, concurrency=concurrency, on_start=on_start, middleware=middleware)
    _registry.workers[name] = handle
    if is_discovering():
        declare(
            DeclareRequest(
                resource=ResourceIdentifier(type=ResourceType.WORKER, name=name),
                config=Oneof(_KIND, WorkerConfig(concurrency=concurrency or 0)),
                source=find_caller_source(),
            )
        )
    return handle


def get_worker_name(worker: Worker | None) -> str:
    if worker is None:
        return ""
    if not isinstance(worker, Worker):
        raise TypeError(
            f"worker= takes the Worker that ocel.worker() returns, not {worker!r}: "
            f"declare it with ocel.worker({worker!r}) and pass that"
        )
    return worker.name


async def deliver(worker: str, body: bytes) -> tuple[int, bytes]:
    """Run one delivery for the worker named ``worker`` and answer the ``(status, body)``
    the Ocel runtime reads its outcome from. Ocel's generated worker entry calls it for each
    request; apps do not.

    ``body`` is a delivery envelope as JSON. It answers 200 with the run's output as JSON
    on success, 422 with the abort reason when the run must not be retried, 500 with the
    error's message when it may be, 404 when no task or consumer this process registered on
    ``worker`` matches, and 400 when ``body`` is not an envelope, or carries a batch for a
    task or consumer that is not batched. A payload that fails its schema, or an output
    that cannot be encoded as JSON, aborts the run.

    Cancelling the awaiting task cancels the run and runs its task's ``on_cancel`` hook,
    and no other lifecycle hook. A run that returns or raises once cancelled is answered
    for what it did."""
    try:
        envelope = Envelope.from_json(body)
    except Exception as error:
        return 400, f"the body is not a delivery envelope: {format_error(error)}".encode()
    registration = _registry.registrations.get((envelope.topic, envelope.consumer))
    if registration is None:
        return 404, (
            f'no consumer "{envelope.consumer}" of topic "{envelope.topic}" is registered '
            f'in this process for worker "{worker}"'
        ).encode()
    if registration.worker != worker:
        return 404, (
            f'consumer "{envelope.consumer}" of topic "{envelope.topic}" runs on worker '
            f'"{registration.worker}", not on worker "{worker}"'
        ).encode()
    if envelope.messages and not registration.batch:
        return 400, (
            f'{registration.kind} "{registration.name}" is not batched, and the delivery '
            f"carries a batch of {len(envelope.messages)} messages"
        ).encode()
    handle = _registry.workers.setdefault(worker, Worker(worker))
    try:
        await handle._start()
    except Exception as error:
        return 500, f'worker "{worker}" failed to start: {format_error(error)}'.encode()
    if handle._slots is None:
        return await _run_attempt(handle, registration, envelope)
    async with handle._slots:
        return await _run_attempt(handle, registration, envelope)


async def _run_attempt(
    handle: Worker, registration: _registry.Registration, envelope: Envelope
) -> tuple[int, bytes]:
    ctx = _build_context(registration, envelope)
    try:
        payload = _decode_payload(registration, envelope)
    except Exception as error:
        return _build_abort_answer(error)
    hooks = registration.hooks

    async def run_handler() -> Any:
        arguments = _build_arguments(registration.handler, payload, ctx)
        return await _call_function(registration.handler, *arguments)

    async def run_task_middleware() -> Any:
        if hooks.middleware is None:
            return await run_handler()
        return await _call_middleware(hooks.middleware, (payload, ctx), run_handler)

    async def run_attempt() -> Any:
        if hooks.on_start_attempt is not None:
            await _call_function(hooks.on_start_attempt, payload, ctx)
        return await run_task_middleware()

    failure: Exception | None = None
    try:
        if handle.middleware is None:
            output = await run_attempt()
        else:
            output = await _call_middleware(handle.middleware, (ctx,), run_attempt)
    except asyncio.CancelledError:
        await _cancel_run(registration, payload, ctx)
        raise
    except Exception as error:
        failure = error
    cancelled = _is_cancelling()
    if cancelled:
        await _cancel_run(registration, payload, ctx)
    if failure is None:
        try:
            answer = encode_json(output).encode()
        except Exception as error:
            if not cancelled:
                await _end_run(registration, payload, ctx, error=error)
            return _build_abort_answer(error)
        if not cancelled:
            await _end_run(registration, payload, ctx, output=output)
        return 200, answer
    if cancelled:
        if isinstance(failure, AbortTaskRunError):
            return _build_abort_answer(failure)
        return 500, format_error(failure).encode()
    if isinstance(failure, AbortTaskRunError) or await _call_catch_error(
        registration, payload, failure, ctx
    ):
        await _end_run(registration, payload, ctx, error=failure)
        return _build_abort_answer(failure)
    if ctx.attempt.number >= ctx.attempt.of:
        await _end_run(registration, payload, ctx, error=failure)
    return 500, format_error(failure).encode()


def _decode_payload(registration: _registry.Registration, envelope: Envelope) -> Any:
    if not registration.batch:
        return registration.decode(decode_value(envelope.payload))
    if not envelope.messages:
        return [registration.decode(decode_value(envelope.payload))]
    return [registration.decode(decode_value(each.payload)) for each in envelope.messages]


def _is_cancelling() -> bool:
    task = asyncio.current_task()
    return task is not None and task.cancelling() > 0


async def _cancel_run(registration: _registry.Registration, payload: Any, ctx: RunContext) -> None:
    ctx._canceled.set()
    on_cancel = registration.hooks.on_cancel
    if on_cancel is not None:
        await _call_hook(registration, "on_cancel", on_cancel, payload, ctx)


async def _call_catch_error(
    registration: _registry.Registration, payload: Any, error: Exception, ctx: RunContext
) -> bool:
    catch_error = registration.hooks.catch_error
    if catch_error is None:
        return False
    try:
        answer = await _call_function(catch_error, payload, error, ctx)
    except AbortTaskRunError:
        return True
    except Exception as failure:
        _print_failure(registration, "catch_error", failure)
        return False
    return bool(getattr(answer, "skip_retrying", False))


async def _end_run(
    registration: _registry.Registration,
    payload: Any,
    ctx: RunContext,
    *,
    output: Any = None,
    error: BaseException | None = None,
) -> None:
    hooks = registration.hooks
    if error is None:
        if hooks.on_success is not None:
            await _call_hook(registration, "on_success", hooks.on_success, payload, output, ctx)
        result = RunResult(ok=True, output=output)
    else:
        if hooks.on_failure is not None:
            await _call_hook(registration, "on_failure", hooks.on_failure, payload, error, ctx)
        result = RunResult(ok=False, error=error)
    if hooks.on_complete is not None:
        await _call_hook(registration, "on_complete", hooks.on_complete, payload, result, ctx)


async def _call_hook(
    registration: _registry.Registration, hook: str, function: Callable[..., Any], *arguments: Any
) -> None:
    try:
        await _call_function(function, *arguments)
    except Exception as error:
        _print_failure(registration, hook, error)


def _print_failure(registration: _registry.Registration, hook: str, error: Exception) -> None:
    print(
        f'ocel: {registration.kind} "{registration.name}" {hook} raised: {format_error(error)}',
        file=sys.stderr,
    )


async def _call_function(function: Callable[..., Any] | None, *arguments: Any) -> Any:
    if function is None:
        return None
    if _is_async(function):
        return await function(*arguments)
    return await asyncio.to_thread(function, *arguments)


async def _call_middleware(
    middleware: Callable[..., Any],
    arguments: tuple[Any, ...],
    step: Callable[[], Coroutine[Any, Any, Any]],
) -> Any:
    if _is_async(middleware):
        return await middleware(*arguments, step)
    loop = asyncio.get_running_loop()

    def run_step() -> Any:
        return asyncio.run_coroutine_threadsafe(step(), loop).result()

    return await asyncio.to_thread(middleware, *arguments, run_step)


def _is_async(function: Callable[..., Any]) -> bool:
    return inspect.iscoroutinefunction(function) or inspect.iscoroutinefunction(
        type(function).__call__
    )


def _build_arguments(handler: Callable[..., Any], payload: Any, ctx: RunContext) -> tuple[Any, ...]:
    try:
        parameters = inspect.signature(handler).parameters.values()
    except (TypeError, ValueError):
        return (payload, ctx)
    positional = [
        parameter
        for parameter in parameters
        if parameter.kind
        in (inspect.Parameter.POSITIONAL_ONLY, inspect.Parameter.POSITIONAL_OR_KEYWORD)
    ]
    variadic = any(parameter.kind is inspect.Parameter.VAR_POSITIONAL for parameter in parameters)
    return (payload, ctx) if variadic or len(positional) >= 2 else (payload,)


def _build_context(registration: _registry.Registration, envelope: Envelope) -> RunContext:
    execution, message, attempt = envelope.execution, envelope.message, envelope.attempt
    if envelope.messages:
        first = envelope.messages[0]
        execution = first.execution
        message = first.message or message
        attempt = first.attempt or attempt
    message = message or Message()
    attempt = attempt or Attempt(number=1, of=1)
    return RunContext(
        kind=registration.kind,
        name=registration.name,
        topic=registration.topic,
        id=execution,
        attempt=RunAttempt(
            number=attempt.number,
            of=attempt.of,
            first_attempted_at=decode_timestamp(attempt.first_attempted_at),
        ),
        message=RunMessage(id=message.id, published_at=decode_timestamp(message.published_at)),
    )


def _build_abort_answer(error: BaseException) -> tuple[int, bytes]:
    body = {"abort": {"reason": format_error(error)}}
    return 422, json.dumps(body, separators=(",", ":")).encode()
