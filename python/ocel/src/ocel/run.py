import builtins
import threading
from collections.abc import Iterable
from dataclasses import dataclass, field
from datetime import datetime
from enum import StrEnum
from typing import Any, Literal, Protocol

from ocel._binding import read_runtime, refuse_unprovisioned
from ocel._declare import is_discovering
from ocel._wire import Seconds, convert_whole_floats, decode_timestamp, decode_value, encode_due_at
from ocel.gen.app.task.v1.task_connect import TaskServiceClient, TaskServiceClientSync
from ocel.gen.app.task.v1.task_pb import (
    CancelRunRequest,
    ListRunsRequest,
    ListRunsResponse,
    ReplayRunRequest,
    RescheduleRunRequest,
    RetrieveRunRequest,
)
from ocel.gen.app.task.v1.task_pb import Run as WireRun
from ocel.gen.app.task.v1.task_pb import RunStatus as WireRunStatus


class RunStatus(StrEnum):
    """Where a task's run stands."""

    #: Triggered with a delay that has not passed yet.
    DELAYED = "DELAYED"
    #: Waiting for a worker to take it.
    QUEUED = "QUEUED"
    #: An attempt is running.
    EXECUTING = "EXECUTING"
    #: An attempt succeeded.
    COMPLETED = "COMPLETED"
    #: The last attempt failed, or one aborted.
    FAILED = "FAILED"
    #: Canceled before it finished.
    CANCELED = "CANCELED"
    #: Its ``ttl`` passed before it started.
    EXPIRED = "EXPIRED"
    #: An attempt ran past the task's ``max_duration``.
    TIMED_OUT = "TIMED_OUT"


@dataclass(frozen=True)
class Run:
    """The record of one run of a task."""

    #: The run's id.
    id: str
    #: The name its task was declared under.
    task: str
    #: Where the run stands.
    status: RunStatus
    #: The payload it was triggered with, decoded from JSON.
    payload: Any
    #: What the task returned, decoded from JSON, once it completed.
    output: Any
    #: The last attempt's error, when one failed.
    error: str
    #: How many attempts were made.
    attempts: int
    #: The tags it was triggered with.
    tags: builtins.list[str]
    #: The metadata it was triggered with.
    metadata: dict[str, Any]
    #: When it was triggered.
    created_at: datetime | None
    #: When it was or is due to start.
    due_at: datetime | None
    #: When its first attempt started.
    started_at: datetime | None
    #: When it finished.
    finished_at: datetime | None
    #: When it expires unless it starts first.
    expires_at: datetime | None


@dataclass(frozen=True)
class RunPage:
    """One page of runs."""

    #: The runs on this page.
    runs: builtins.list[Run]
    #: The cursor that reads the next page, or ``""`` on the last one.
    next_cursor: str


class _NamedTask(Protocol):
    name: str


class AbortTaskRunError(Exception):
    """Raise it from a task or consumer to fail the run without retrying it, whatever
    attempts remain. Its message is the reason the run records."""


@dataclass(frozen=True)
class RunResult:
    """How a run ended, as a task's ``on_complete`` hook receives it."""

    #: Whether the run succeeded.
    ok: bool
    #: What the task returned, when it succeeded.
    output: Any = None
    #: What failed the run, when it did not.
    error: BaseException | None = None


@dataclass(frozen=True)
class CatchErrorResult:
    """What a task's ``catch_error`` hook answers about an attempt that raised."""

    #: Fail the run now, without retrying it, whatever attempts remain.
    skip_retrying: bool = False


@dataclass(frozen=True)
class RunHandle:
    """A run a trigger started, by id; :data:`ocel.runs` reads and steers it."""

    #: The run's id.
    id: str


@dataclass(frozen=True)
class RunAttempt:
    """Which attempt at a run is executing."""

    #: This attempt's number, counting from 1.
    number: int
    #: How many attempts the run gets in all.
    of: int
    #: When the first attempt started.
    first_attempted_at: datetime | None = None


@dataclass(frozen=True)
class RunMessage:
    """The message a run executes."""

    #: The message id, a 26-character ULID.
    id: str
    #: When the message was sent or the task triggered.
    published_at: datetime | None = None


@dataclass(frozen=True)
class RunContext:
    """What a task, a consumer, their hooks and a worker's middleware know about the run
    executing."""

    #: ``"task"`` for a task's run and ``"consumer"`` for a topic consumer's.
    kind: Literal["task", "consumer"]
    #: The task's name, or the consumer's.
    name: str
    #: The topic the run was read from, which for a task is the task's name.
    topic: str
    #: The run's id: a task's run id, or the consumer's execution id. A batch takes its
    #: first message's.
    id: str
    #: Which attempt is executing.
    attempt: RunAttempt
    #: The message being run; a batch's first message.
    message: RunMessage
    _canceled: threading.Event = field(default_factory=threading.Event, repr=False)

    @property
    def canceled(self) -> bool:
        """Whether the run was canceled while it executed. An async handler is cancelled
        outright; a plain function runs on in its thread, and checks this to stop early."""
        return self._canceled.is_set()


class Runs:
    """Reads and steers the runs of every task the app declares. Use the :data:`runs`
    instance; each operation has a blocking form and an awaited ``_async`` form."""

    def __init__(self) -> None:
        self._clients: dict[tuple[str, bool], Any] = {}

    def retrieve(self, id: str) -> Run:
        """The record of the run ``id``."""
        client, headers = self._ensure_client("retrieve", False)
        return _build_run(client.retrieve_run(RetrieveRunRequest(id=id), headers=headers).run)

    async def retrieve_async(self, id: str) -> Run:
        """The record of the run ``id``, as :meth:`retrieve` answers."""
        client, headers = self._ensure_client("retrieve_async", True)
        response = await client.retrieve_run(RetrieveRunRequest(id=id), headers=headers)
        return _build_run(response.run)

    def list(
        self,
        *,
        task: _NamedTask | str | None = None,
        status: RunStatus | Iterable[RunStatus] | None = None,
        tags: Iterable[str] = (),
        cursor: str | None = None,
        limit: int | None = None,
    ) -> RunPage:
        """Read one page of runs, newest first: of ``task`` (a task handle or its declared
        name) when given, in any of the statuses ``status`` names, carrying every tag in
        ``tags``, from ``cursor``, at most ``limit`` of them."""
        client, headers = self._ensure_client("list", False)
        request = _build_list_request(task, status, tags, cursor, limit)
        return _build_run_page(client.list_runs(request, headers=headers))

    async def list_async(
        self,
        *,
        task: _NamedTask | str | None = None,
        status: RunStatus | Iterable[RunStatus] | None = None,
        tags: Iterable[str] = (),
        cursor: str | None = None,
        limit: int | None = None,
    ) -> RunPage:
        """Read one page of runs, as :meth:`list` does."""
        client, headers = self._ensure_client("list_async", True)
        request = _build_list_request(task, status, tags, cursor, limit)
        return _build_run_page(await client.list_runs(request, headers=headers))

    def cancel(self, id: str) -> Run:
        """Cancel the run ``id`` and return its record. No further attempt starts; a running
        one is cancelled as far as its worker can."""
        client, headers = self._ensure_client("cancel", False)
        return _build_run(client.cancel_run(CancelRunRequest(id=id), headers=headers).run)

    async def cancel_async(self, id: str) -> Run:
        """Cancel the run ``id``, as :meth:`cancel` does."""
        client, headers = self._ensure_client("cancel_async", True)
        return _build_run((await client.cancel_run(CancelRunRequest(id=id), headers=headers)).run)

    def replay(self, id: str) -> RunHandle:
        """Start a new run with the payload and options of the run ``id``, and return it."""
        client, headers = self._ensure_client("replay", False)
        return RunHandle(client.replay_run(ReplayRunRequest(id=id), headers=headers).id)

    async def replay_async(self, id: str) -> RunHandle:
        """Start a new run from the run ``id``, as :meth:`replay` does."""
        client, headers = self._ensure_client("replay_async", True)
        response = await client.replay_run(ReplayRunRequest(id=id), headers=headers)
        return RunHandle(response.id)

    def reschedule(self, id: str, *, delay: Seconds | datetime) -> Run:
        """Move the delayed run ``id`` to start after ``delay``, or at that moment, up to
        30 days ahead, and return its record."""
        client, headers = self._ensure_client("reschedule", False)
        request = RescheduleRunRequest(id=id, due_at=encode_due_at(delay))
        return _build_run(client.reschedule_run(request, headers=headers).run)

    async def reschedule_async(self, id: str, *, delay: Seconds | datetime) -> Run:
        """Move the delayed run ``id``, as :meth:`reschedule` does."""
        client, headers = self._ensure_client("reschedule_async", True)
        request = RescheduleRunRequest(id=id, due_at=encode_due_at(delay))
        return _build_run((await client.reschedule_run(request, headers=headers)).run)

    def _ensure_client(self, access: str, awaited: bool) -> tuple[Any, dict[str, str]]:
        if is_discovering():
            raise refuse_unprovisioned("runs", access)
        address, headers = read_runtime()
        client = self._clients.get((address, awaited))
        if client is None:
            kind = TaskServiceClient if awaited else TaskServiceClientSync
            client = kind(address, send_compression=None)
            self._clients[(address, awaited)] = client
        return client, headers


#: The runs of every task the app declares.
runs = Runs()


def _build_list_request(
    task: _NamedTask | str | None,
    status: RunStatus | Iterable[RunStatus] | None,
    tags: Iterable[str],
    cursor: str | None,
    limit: int | None,
) -> ListRunsRequest:
    if status is None:
        statuses: builtins.list[RunStatus] = []
    elif isinstance(status, str):
        statuses = [RunStatus(status)]
    else:
        statuses = [RunStatus(each) for each in status]
    name = task if isinstance(task, str) or task is None else task.name
    return ListRunsRequest(
        task=name or "",
        statuses=[WireRunStatus[each.name] for each in statuses],
        tags=builtins.list(tags),
        cursor=cursor or "",
        limit=limit or 0,
    )


def _build_run_page(response: ListRunsResponse) -> RunPage:
    return RunPage(
        runs=[_build_run(run) for run in response.runs], next_cursor=response.next_cursor
    )


def _build_run(wire: WireRun | None) -> Run:
    if wire is None:
        raise RuntimeError("the runtime answered no run record")
    return Run(
        id=wire.id,
        task=wire.task,
        status=RunStatus[wire.status.name],
        payload=decode_value(wire.payload),
        output=decode_value(wire.output),
        error=wire.error,
        attempts=wire.attempts,
        tags=builtins.list(wire.tags),
        metadata=convert_whole_floats(wire.metadata.to_python()) if wire.metadata else {},
        created_at=decode_timestamp(wire.created_at),
        due_at=decode_timestamp(wire.due_at),
        started_at=decode_timestamp(wire.started_at),
        finished_at=decode_timestamp(wire.finished_at),
        expires_at=decode_timestamp(wire.expires_at),
    )
