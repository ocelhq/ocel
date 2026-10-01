import functools
from collections.abc import Callable, Iterable, Sequence
from dataclasses import dataclass
from datetime import datetime
from enum import StrEnum
from typing import Any, Generic, Literal, TypeVar, overload

from protobuf import Oneof

from ocel import _registry
from ocel._binding import read_runtime, refuse_unbound, refuse_unprovisioned
from ocel._declare import declare, find_caller_source, is_discovering
from ocel._payload import Codec, find_payload_type
from ocel._wire import (
    Seconds,
    decode_timestamp,
    decode_value,
    encode_due_at,
    encode_duration,
    encode_lane,
    encode_retry_policy,
)
from ocel.gen.app.resources.v1.resources_pb import (
    BatchPolicy,
    ConsumerConfig,
    DeclareRequest,
    ResourceIdentifier,
    ResourceType,
    TopicConfig,
)
from ocel.gen.app.topic.v1.topic_connect import TopicServiceClient, TopicServiceClientSync
from ocel.gen.app.topic.v1.topic_pb import (
    CountDeadLettersRequest,
    ListDeadLettersRequest,
    ListDeadLettersResponse,
    PurgeDeadLettersRequest,
    RedriveDeadLettersRequest,
    SendRequest,
)
from ocel.gen.app.topic.v1.topic_pb import DeadLetter as WireDeadLetter
from ocel.worker import Worker, get_worker_name

P = TypeVar("P")

_TOPIC = "topic"
_CONSUMER = "consumer"


class Lane(StrEnum):
    """The lane a message or run is read from. Lanes weight unordered reads 6:3:1, so a
    busy high lane never starves the others."""

    HIGH = "high"
    DEFAULT = "default"
    LOW = "low"


@dataclass(frozen=True)
class Retry:
    """How a failed attempt is retried. A field left ``None`` takes Ocel's default."""

    #: How many attempts a run or message gets in all, from 1 to 100.
    max_attempts: int | None = None
    #: The backoff before the first retry, in seconds or as a ``timedelta``.
    min_delay: Seconds | None = None
    #: The longest backoff between attempts, at most 600 seconds.
    max_delay: Seconds | None = None


@dataclass(frozen=True)
class DeadLetter:
    """A message a consumer exhausted its attempts on."""

    #: The execution that failed: the message id and the consumer's name.
    execution: str
    #: The id of the message.
    message_id: str
    #: When the message was sent.
    published_at: datetime | None
    #: The message's payload, decoded from JSON.
    payload: Any
    #: How many attempts the consumer made.
    attempts: int
    #: The last attempt's error.
    error: str
    #: When the last attempt failed.
    failed_at: datetime | None


@dataclass(frozen=True)
class DeadLetterPage:
    """One page of a consumer's dead letters."""

    #: The dead letters on this page.
    dead_letters: list[DeadLetter]
    #: The cursor that reads the next page, or ``""`` on the last one.
    next_cursor: str


class Consumer(Generic[P]):
    """A consumer an app declares with :meth:`Topic.consumer` or
    :meth:`Topic.batch_consumer`: the function it decorated, which still runs when called."""

    #: The name the consumer was declared under.
    name: str
    #: The topic it reads.
    topic: "Topic[P]"

    def __init__(self, name: str, topic: "Topic[P]", run: Callable[..., Any]):
        """Wrap ``run`` as the consumer named ``name``. Prefer :meth:`Topic.consumer`, which
        declares the consumer and registers it with its worker as well."""
        self.name = name
        self.topic = topic
        self._run = run
        functools.update_wrapper(self, run)

    def __call__(self, *arguments: Any, **keyword_arguments: Any) -> Any:
        """Call the decorated function directly, as if it were not a consumer."""
        return self._run(*arguments, **keyword_arguments)

    def __repr__(self) -> str:
        return f"Consumer({self.topic.name!r}, {self.name!r})"


class DeadLetters:
    """The messages one consumer of a topic exhausted its attempts on. Each operation has a
    blocking form and an awaited ``_async`` form."""

    def __init__(self, topic: "Topic[Any]", consumer: str):
        """Take the dead letters of ``consumer`` on ``topic``. Prefer
        :meth:`Topic.dead_letter`."""
        self._topic = topic
        self._consumer = consumer

    def list(self, *, cursor: str | None = None, limit: int | None = None) -> DeadLetterPage:
        """Read one page of dead letters, from ``cursor`` when given, of at most ``limit``
        entries."""
        client, headers = self._topic._ensure_connection("dead_letter.list")
        request = ListDeadLettersRequest(
            topic=self._topic.name, consumer=self._consumer, cursor=cursor or "", limit=limit or 0
        )
        return _build_dead_letter_page(client.list_dead_letters(request, headers=headers))

    async def list_async(
        self, *, cursor: str | None = None, limit: int | None = None
    ) -> DeadLetterPage:
        """Read one page of dead letters, as :meth:`list` does."""
        client, headers = self._topic._ensure_async_connection("dead_letter.list_async")
        request = ListDeadLettersRequest(
            topic=self._topic.name, consumer=self._consumer, cursor=cursor or "", limit=limit or 0
        )
        return _build_dead_letter_page(await client.list_dead_letters(request, headers=headers))

    def redrive(self, executions: Iterable[str] | None = None) -> int:
        """Send the dead letters named by ``executions``, or all of them, back to the
        consumer, and return how many were sent."""
        client, headers = self._topic._ensure_connection("dead_letter.redrive")
        request = RedriveDeadLettersRequest(
            topic=self._topic.name, consumer=self._consumer, executions=list(executions or ())
        )
        return client.redrive_dead_letters(request, headers=headers).redriven

    async def redrive_async(self, executions: Iterable[str] | None = None) -> int:
        """Send dead letters back to the consumer, as :meth:`redrive` does."""
        client, headers = self._topic._ensure_async_connection("dead_letter.redrive_async")
        request = RedriveDeadLettersRequest(
            topic=self._topic.name, consumer=self._consumer, executions=list(executions or ())
        )
        return (await client.redrive_dead_letters(request, headers=headers)).redriven

    def purge(self, executions: Iterable[str] | None = None) -> int:
        """Delete the dead letters named by ``executions``, or all of them, and return how
        many were deleted."""
        client, headers = self._topic._ensure_connection("dead_letter.purge")
        request = PurgeDeadLettersRequest(
            topic=self._topic.name, consumer=self._consumer, executions=list(executions or ())
        )
        return client.purge_dead_letters(request, headers=headers).purged

    async def purge_async(self, executions: Iterable[str] | None = None) -> int:
        """Delete dead letters, as :meth:`purge` does."""
        client, headers = self._topic._ensure_async_connection("dead_letter.purge_async")
        request = PurgeDeadLettersRequest(
            topic=self._topic.name, consumer=self._consumer, executions=list(executions or ())
        )
        return (await client.purge_dead_letters(request, headers=headers)).purged

    def count(self) -> int:
        """How many dead letters the consumer has."""
        client, headers = self._topic._ensure_connection("dead_letter.count")
        request = CountDeadLettersRequest(topic=self._topic.name, consumer=self._consumer)
        return client.count_dead_letters(request, headers=headers).count

    async def count_async(self) -> int:
        """How many dead letters the consumer has, as :meth:`count` answers."""
        client, headers = self._topic._ensure_async_connection("dead_letter.count_async")
        request = CountDeadLettersRequest(topic=self._topic.name, consumer=self._consumer)
        return (await client.count_dead_letters(request, headers=headers)).count


class Topic(Generic[P]):
    """A topic an app declares with :func:`topic`: messages sent to it fan out to every
    consumer declared on it. Each operation has a blocking form and an awaited ``_async``
    form."""

    #: The name the topic was declared under, and the name its binding is delivered as.
    name: str

    def __init__(self, name: str, codec: Codec):
        """Take the handle for the topic named ``name``. Prefer :func:`topic`, which
        declares the topic as well as handing back its handle."""
        self.name = name
        self._codec = codec
        self._connection: tuple[Any, dict[str, str]] | None = None
        self._async_connection: tuple[Any, dict[str, str]] | None = None

    def __repr__(self) -> str:
        return f"Topic({self.name!r})"

    def consumer(
        self,
        name: str,
        *,
        retry: Retry | None = None,
        concurrency: int | None = None,
        lanes: Sequence[Lane | Literal["high", "default", "low"]] = (),
        max_duration: Seconds | None = None,
        worker: Worker | None = None,
    ) -> Callable[[Callable[..., Any]], Consumer[P]]:
        """Declare the decorated function as the consumer named ``name``, which receives
        every message sent to the topic as ``(payload, ctx)``. It may be sync, when it runs
        in a thread, or async. Raising fails the attempt, which is retried by ``retry``;
        raising :class:`AbortTaskRunError` dead-letters the message at once.

        ``lanes`` limits the lanes it reads; ``worker`` places it on the :class:`Worker`
        that :func:`ocel.worker` returned."""
        source = find_caller_source() if is_discovering() else ""
        return self._declare_consumer(
            name, source, retry, concurrency, lanes, max_duration, worker, None
        )

    def batch_consumer(
        self,
        name: str,
        *,
        batch_size: int,
        batch_timeout: Seconds | None = None,
        retry: Retry | None = None,
        concurrency: int | None = None,
        lanes: Sequence[Lane | Literal["high", "default", "low"]] = (),
        max_duration: Seconds | None = None,
        worker: Worker | None = None,
    ) -> Callable[[Callable[..., Any]], Consumer[P]]:
        """Declare the decorated function as the consumer named ``name``, which receives the
        topic's messages as a list of up to ``batch_size`` payloads, waiting at most
        ``batch_timeout`` for a partial batch. The other options are :meth:`consumer`'s."""
        source = find_caller_source() if is_discovering() else ""
        batch = BatchPolicy(size=batch_size, timeout=encode_duration(batch_timeout))
        return self._declare_consumer(
            name, source, retry, concurrency, lanes, max_duration, worker, batch
        )

    def send(
        self,
        payload: P,
        *,
        delay: Seconds | datetime | None = None,
        idempotency_key: str | None = None,
        key: str | None = None,
        lane: Lane | Literal["high", "default", "low"] | None = None,
    ) -> str:
        """Publish ``payload`` to every consumer and return the message id. ``delay`` is
        how long until it is delivered, or the moment it is, up to 30 days ahead; a repeated
        ``idempotency_key`` publishes nothing new; ``key`` orders the messages of an ordered
        topic; ``lane`` is the lane it is read from. A payload whose JSON exceeds 256 KiB
        is refused before anything is sent."""
        client, headers = self._ensure_connection("send")
        request = self._build_send_request(payload, delay, idempotency_key, key, lane)
        return client.send(request, headers=headers).message_id

    async def send_async(
        self,
        payload: P,
        *,
        delay: Seconds | datetime | None = None,
        idempotency_key: str | None = None,
        key: str | None = None,
        lane: Lane | Literal["high", "default", "low"] | None = None,
    ) -> str:
        """Publish ``payload`` to every consumer, as :meth:`send` does."""
        client, headers = self._ensure_async_connection("send_async")
        request = self._build_send_request(payload, delay, idempotency_key, key, lane)
        return (await client.send(request, headers=headers)).message_id

    def dead_letter(self, consumer: "Consumer[P] | str") -> DeadLetters:
        """The dead letters of ``consumer``, given by handle or by name."""
        return DeadLetters(self, consumer if isinstance(consumer, str) else consumer.name)

    def _declare_consumer(
        self,
        name: str,
        source: str,
        retry: Retry | None,
        concurrency: int | None,
        lanes: Sequence[Lane | str],
        max_duration: Seconds | None,
        worker: Worker | None,
        batch: BatchPolicy | None,
    ) -> Callable[[Callable[..., Any]], Consumer[P]]:
        def decorate(run: Callable[..., Any]) -> Consumer[P]:
            worker_name = get_worker_name(worker)
            codec = self._codec
            if not codec.schema:
                codec = Codec(find_payload_type(run, batch is not None))
            _registry.register(
                _registry.Registration(
                    kind="consumer",
                    topic=self.name,
                    name=name,
                    worker=worker_name or _registry.DEFAULT_WORKER,
                    handler=run,
                    decode=codec.decode,
                    batch=batch is not None,
                )
            )
            if is_discovering():
                config = ConsumerConfig(
                    topic=self.name,
                    worker=worker_name,
                    retry=encode_retry_policy(retry),
                    concurrency=concurrency or 0,
                    max_duration=encode_duration(max_duration),
                    lanes=[encode_lane(lane) for lane in lanes],
                    batch=batch,
                )
                declare(
                    DeclareRequest(
                        resource=ResourceIdentifier(type=ResourceType.CONSUMER, name=name),
                        config=Oneof(_CONSUMER, config),
                        source=source,
                    )
                )
            return Consumer(name, self, run)

        return decorate

    def _build_send_request(
        self,
        payload: P,
        delay: Seconds | datetime | None,
        idempotency_key: str | None,
        key: str | None,
        lane: Lane | str | None,
    ) -> SendRequest:
        return SendRequest(
            topic=self.name,
            payload=self._codec.encode(payload),
            due_at=encode_due_at(delay),
            idempotency_key=idempotency_key or "",
            key=key or "",
            lane=encode_lane(lane),
        )

    def _ensure_connection(self, access: str) -> tuple[Any, dict[str, str]]:
        if is_discovering():
            raise refuse_unprovisioned(f'topic("{self.name}")', access)
        if self._connection is None:
            address, headers = read_runtime()
            client = TopicServiceClientSync(address, send_compression=None)
            if refusal := refuse_unbound(self.name, "topic"):
                raise refusal
            self._connection = (client, headers)
        return self._connection

    def _ensure_async_connection(self, access: str) -> tuple[Any, dict[str, str]]:
        if is_discovering():
            raise refuse_unprovisioned(f'topic("{self.name}")', access)
        if self._async_connection is None:
            address, headers = read_runtime()
            client = TopicServiceClient(address, send_compression=None)
            if refusal := refuse_unbound(self.name, "topic"):
                raise refusal
            self._async_connection = (client, headers)
        return self._async_connection


@overload
def topic(
    name: str, *, schema: type[P], ordered: bool = False, retry: Retry | None = None
) -> Topic[P]: ...
@overload
def topic(
    name: str, *, schema: None = None, ordered: bool = False, retry: Retry | None = None
) -> Topic[Any]: ...
def topic(
    name: str,
    *,
    schema: Any = None,
    ordered: bool = False,
    retry: Retry | None = None,
) -> Topic[Any]:
    """Declare a topic named ``name`` and return the handle an app sends to it and declares
    its consumers through. ``schema`` is the payload's type: a type beyond plain JSON (a
    pydantic model, a dataclass) is validated with pydantic, which the ``ocel[pydantic]``
    extra installs. An ``ordered`` topic delivers the messages sharing a ``key`` one at a
    time, in order; ``retry`` is the default every consumer's attempts follow."""
    codec = Codec(schema)
    if is_discovering():
        declare(
            DeclareRequest(
                resource=ResourceIdentifier(type=ResourceType.TOPIC, name=name),
                config=Oneof(
                    _TOPIC,
                    TopicConfig(
                        schema=codec.schema, ordered=ordered, retry=encode_retry_policy(retry)
                    ),
                ),
                source=find_caller_source(),
            )
        )
    return Topic(name, codec)


def _build_dead_letter_page(response: ListDeadLettersResponse) -> DeadLetterPage:
    return DeadLetterPage(
        dead_letters=[_build_dead_letter(letter) for letter in response.dead_letters],
        next_cursor=response.next_cursor,
    )


def _build_dead_letter(letter: WireDeadLetter) -> DeadLetter:
    message = letter.message
    return DeadLetter(
        execution=letter.execution,
        message_id=message.id if message else "",
        published_at=decode_timestamp(message.published_at) if message else None,
        payload=decode_value(letter.payload),
        attempts=letter.attempts,
        error=letter.error,
        failed_at=decode_timestamp(letter.failed_at),
    )
