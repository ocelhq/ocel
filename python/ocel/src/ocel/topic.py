import functools
from collections.abc import Callable, Iterable, Sequence
from dataclasses import dataclass
from datetime import datetime
from enum import StrEnum
from typing import Generic, Literal, TypeVar, overload

from protobuf import Oneof

from ocel import _registry
from ocel._binding import require_binding
from ocel._declare import declare, find_caller_source, is_discovering
from ocel._payload import Codec, JsonValue, decode_json, find_payload_type
from ocel._runtime import RuntimeConnection
from ocel._wire import (
    Seconds,
    decode_timestamp,
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
    payload: JsonValue
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

    def __init__(self, name: str, topic: "Topic[P]", run: Callable[..., object]):
        """Wrap ``run`` as the consumer named ``name``. Prefer :meth:`Topic.consumer`, which
        declares the consumer and registers it with its worker as well."""
        self.name = name
        self.topic = topic
        self._run = run
        functools.update_wrapper(self, run)

    def __call__(self, *arguments: object, **keyword_arguments: object) -> object:
        """Call the decorated function directly, as if it were not a consumer."""
        return self._run(*arguments, **keyword_arguments)

    def __repr__(self) -> str:
        return f"Consumer({self.topic.name!r}, {self.name!r})"


class DeadLetters(Generic[P]):
    """The messages one consumer of a topic exhausted its attempts on. Each operation has a
    blocking form and an awaited ``_async`` form."""

    def __init__(self, topic: "Topic[P]", consumer: str):
        """Take the dead letters of ``consumer`` on ``topic``. Prefer
        :meth:`Topic.dead_letter`."""
        self._topic = topic
        self._consumer = consumer

    def list(self, *, cursor: str | None = None, limit: int | None = None) -> DeadLetterPage:
        """Read one page of dead letters, from ``cursor`` when given, of at most ``limit``
        entries."""
        request = ListDeadLettersRequest(
            topic=self._topic.name, consumer=self._consumer, cursor=cursor or "", limit=limit or 0
        )
        response = self._topic._runtime.call(
            "dead_letter.list", lambda client: client.list_dead_letters(request)
        )
        return _build_dead_letter_page(response)

    async def list_async(
        self, *, cursor: str | None = None, limit: int | None = None
    ) -> DeadLetterPage:
        """Read one page of dead letters, as :meth:`list` does."""
        request = ListDeadLettersRequest(
            topic=self._topic.name, consumer=self._consumer, cursor=cursor or "", limit=limit or 0
        )
        response = await self._topic._runtime.call_async(
            "dead_letter.list_async", lambda client: client.list_dead_letters(request)
        )
        return _build_dead_letter_page(response)

    def redrive(self, executions: Iterable[str] | None = None) -> int:
        """Send the dead letters named by ``executions``, or all of them, back to the
        consumer, and return how many were sent."""
        request = RedriveDeadLettersRequest(
            topic=self._topic.name, consumer=self._consumer, executions=list(executions or ())
        )
        response = self._topic._runtime.call(
            "dead_letter.redrive", lambda client: client.redrive_dead_letters(request)
        )
        return response.redriven

    async def redrive_async(self, executions: Iterable[str] | None = None) -> int:
        """Send dead letters back to the consumer, as :meth:`redrive` does."""
        request = RedriveDeadLettersRequest(
            topic=self._topic.name, consumer=self._consumer, executions=list(executions or ())
        )
        response = await self._topic._runtime.call_async(
            "dead_letter.redrive_async", lambda client: client.redrive_dead_letters(request)
        )
        return response.redriven

    def purge(self, executions: Iterable[str] | None = None) -> int:
        """Delete the dead letters named by ``executions``, or all of them, and return how
        many were deleted."""
        request = PurgeDeadLettersRequest(
            topic=self._topic.name, consumer=self._consumer, executions=list(executions or ())
        )
        response = self._topic._runtime.call(
            "dead_letter.purge", lambda client: client.purge_dead_letters(request)
        )
        return response.purged

    async def purge_async(self, executions: Iterable[str] | None = None) -> int:
        """Delete dead letters, as :meth:`purge` does."""
        request = PurgeDeadLettersRequest(
            topic=self._topic.name, consumer=self._consumer, executions=list(executions or ())
        )
        response = await self._topic._runtime.call_async(
            "dead_letter.purge_async", lambda client: client.purge_dead_letters(request)
        )
        return response.purged

    def count(self) -> int:
        """How many dead letters the consumer has."""
        request = CountDeadLettersRequest(topic=self._topic.name, consumer=self._consumer)
        response = self._topic._runtime.call(
            "dead_letter.count", lambda client: client.count_dead_letters(request)
        )
        return response.count

    async def count_async(self) -> int:
        """How many dead letters the consumer has, as :meth:`count` answers."""
        request = CountDeadLettersRequest(topic=self._topic.name, consumer=self._consumer)
        response = await self._topic._runtime.call_async(
            "dead_letter.count_async", lambda client: client.count_dead_letters(request)
        )
        return response.count


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
        self._runtime = RuntimeConnection(
            f'topic("{name}")',
            lambda address: TopicServiceClientSync(address, send_compression=None),
            lambda address: TopicServiceClient(address, send_compression=None),
            lambda: require_binding(name, "topic"),
        )

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
    ) -> Callable[[Callable[..., object]], Consumer[P]]:
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
    ) -> Callable[[Callable[..., object]], Consumer[P]]:
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
        request = self._build_send_request(payload, delay, idempotency_key, key, lane)
        return self._runtime.call("send", lambda client: client.send(request)).message_id

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
        request = self._build_send_request(payload, delay, idempotency_key, key, lane)
        response = await self._runtime.call_async("send_async", lambda client: client.send(request))
        return response.message_id

    def dead_letter(self, consumer: "Consumer[P] | str") -> DeadLetters[P]:
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
    ) -> Callable[[Callable[..., object]], Consumer[P]]:
        def decorate(run: Callable[..., object]) -> Consumer[P]:
            worker_name = get_worker_name(worker)
            codec = self._codec
            if not codec.schema:
                codec = Codec(find_payload_type(f'consumer "{name}"', run, batch is not None))
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


@overload
def topic(
    name: str, *, schema: type[P], ordered: bool = False, retry: Retry | None = None
) -> Topic[P]: ...
@overload
def topic(
    name: str, *, schema: None = None, ordered: bool = False, retry: Retry | None = None
) -> Topic[object]: ...
def topic(
    name: str,
    *,
    schema: type[P] | None = None,
    ordered: bool = False,
    retry: Retry | None = None,
) -> Topic[P] | Topic[object]:
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
    return Topic[object](name, codec)


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
        payload=decode_json(letter.payload),
        attempts=letter.attempts,
        error=letter.error,
        failed_at=decode_timestamp(letter.failed_at),
    )
