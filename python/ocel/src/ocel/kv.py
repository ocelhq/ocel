from __future__ import annotations

import inspect
import re
import ssl
from collections.abc import Callable, Iterable, Mapping
from datetime import timedelta
from enum import Enum
from typing import Any, Generic, Literal, TypeVar
from urllib.parse import quote

from protobuf import Oneof

from ocel._binding import read_kv_binding, refuse_unprovisioned
from ocel._declare import declare, is_discovering
from ocel.gen.app.resources.v1.resources_pb import (
    DeclareRequest,
    KvConfig,
    KvEntry,
    KvShape,
    ResourceIdentifier,
    ResourceType,
)
from ocel.gen.common.bindings.v1.bindings_pb import KvProperties

M = TypeVar("M")

#: A write's ``ttl`` that keeps the key's current TTL.
KEEP = "keep"

_RESERVED_ENTRY_NAMES = {"client", "connectionString", "connection_string", "then", "constructor"}
_RESERVED_PARAMETERS = {"ttl"}
_ENTRY_NAME = re.compile(r"[A-Za-z][A-Za-z0-9_]{0,62}")
_LITERAL = re.compile(r"[A-Za-z0-9._-]+")
_PARAMETER = re.compile(r":[A-Za-z_][A-Za-z0-9_]*")
_DURATION = re.compile(r"([0-9]+(?:\.[0-9]+)?)(ms|s|m|h|d)")
_UNIT_MILLISECONDS = {"ms": 1, "s": 1_000, "m": 60_000, "h": 3_600_000, "d": 86_400_000}
_UNRESERVED = frozenset(b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~")


class InvalidKVValueError(ValueError):
    """Raised when a value written to an entry, or read from one, is not a value of the
    entry's shape: a ``json`` value its model refuses, or a ``counter`` that holds no
    integer."""

    #: The key whose value is invalid.
    key: str

    def __init__(self, key: str, reason: str):
        super().__init__(f'key "{key}" {reason}')
        self.key = key


class _Unset(Enum):
    DECLARED = "declared"

    def __repr__(self) -> str:
        return "the entry's own ttl"


_DECLARED = _Unset.DECLARED
_WriteTtl = str | timedelta | None | _Unset


class _Pattern:
    def __init__(self, written: str):
        if written == "":
            raise ValueError(
                "the pattern is empty: write one or more segments joined by /, "
                "each a literal or a :parameter"
            )
        if "{" in written or "}" in written:
            raise ValueError(
                f'pattern "{written}" holds {{ or }}, which a store reserves as the hash tag syntax'
            )
        self.written = written
        self.segments: list[tuple[str, str]] = []
        for part in written.split("/"):
            if part == "":
                raise ValueError(
                    f'pattern "{written}" has an empty segment: it neither starts nor ends '
                    "with /, and no two / are adjacent"
                )
            if part.startswith(":"):
                if not _PARAMETER.fullmatch(part):
                    raise ValueError(
                        f'pattern "{written}" has segment "{part}", which is no parameter: a '
                        "parameter is : and a name of letters, digits and _ that starts with a "
                        "letter or _"
                    )
                name = part[1:]
                if name in self.parameters:
                    raise ValueError(
                        f'pattern "{written}" names parameter "{name}" twice, so a key could not '
                        "say which value is which"
                    )
                if name in _RESERVED_PARAMETERS:
                    raise ValueError(
                        f'pattern "{written}" names parameter "{name}", which Python reserves for '
                        "the ttl a write takes: name the parameter otherwise"
                    )
                self.segments.append(("parameter", name))
            elif not _LITERAL.fullmatch(part):
                raise ValueError(
                    f'pattern "{written}" has segment "{part}", which is no literal: a literal is '
                    "letters, digits, ., _ and -"
                )
            else:
                self.segments.append(("literal", part))

    @property
    def parameters(self) -> list[str]:
        return [value for kind, value in self.segments if kind == "parameter"]

    def overlaps(self, other: _Pattern) -> bool:
        if len(self.segments) != len(other.segments):
            return False
        return all(
            mine[0] == "parameter" or theirs[0] == "parameter" or mine[1] == theirs[1]
            for mine, theirs in zip(self.segments, other.segments, strict=True)
        )

    def build(self, key: Mapping[str, Any]) -> str:
        expected = set(self.parameters)
        unknown = set(key) - expected
        if unknown:
            raise TypeError(
                f'pattern "{self.written}" has no parameter {", ".join(sorted(unknown))}: its '
                f"parameters are {', '.join(self.parameters) or 'none'}"
            )
        built = []
        for kind, value in self.segments:
            if kind == "literal":
                built.append(value)
                continue
            if value not in key:
                raise TypeError(f'the key of pattern "{self.written}" has no value for {value}')
            parameter = key[value]
            if isinstance(parameter, bool) or not isinstance(parameter, (str, int)):
                raise TypeError(
                    f"parameter {value} of a key is a str or an int, not {type(parameter).__name__}"
                )
            built.append(_encode_parameter(str(parameter)))
        return "/".join(built)


def _encode_parameter(value: str) -> str:
    return "".join(
        chr(byte) if byte in _UNRESERVED else f"%{byte:02X}" for byte in value.encode("utf-8")
    )


def _milliseconds(duration: object) -> int:
    if isinstance(duration, timedelta):
        milliseconds = round(duration.total_seconds() * 1000)
    elif isinstance(duration, str) and (match := _DURATION.fullmatch(duration)):
        milliseconds = round(float(match[1]) * _UNIT_MILLISECONDS[match[2]])
    else:
        raise ValueError(
            f'a ttl is a duration written with its unit, such as "10s", "5m" or "30d", or a '
            f"timedelta, not {duration!r}"
        )
    if milliseconds < 1:
        raise ValueError(f"a ttl is at least 1ms, and {duration!r} is shorter")
    return milliseconds


def _decode(value: Any) -> Any:
    return value.decode("utf-8") if isinstance(value, bytes) else value


def _trust_only(store: str, pem: str) -> ssl.SSLContext:
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    try:
        context.load_verify_locations(cadata=pem)
    except ssl.SSLError:
        raise ValueError(
            f"OCEL_RESOURCE_KV_{store} delivers a caPem for its kv store "
            "that holds no PEM certificate"
        ) from None
    return context


class KV:
    """A key-value store an app declares, one Valkey instance of its own, reached through
    its typed entries or its own redis-py clients."""

    #: The name the store was declared under, and the name its binding is delivered as.
    name: str

    def __init__(self, name: str, config: KvConfig, source: str):
        """Take the handle for the store named ``name``. Prefer :func:`kv`, which declares
        the store as well as handing back its handle."""
        self.name = name
        self._config = config
        self._source = source
        self._entries: list[tuple[str, _Pattern, str]] = []
        self._client = None
        self._sync_client = None

    def _declare(self) -> None:
        if not is_discovering():
            return
        declare(
            DeclareRequest(
                resource=ResourceIdentifier(type=ResourceType.KV, name=self.name),
                config=Oneof("kv", self._config),
                source=self._source,
            )
        )

    def _add_entry(self, name: str, pattern: str, shape: KvShape, source: str) -> _Pattern:
        def refuse(reason: str) -> ValueError:
            return ValueError(f'kv "{self.name}": entry "{name}" {reason}')

        if not _ENTRY_NAME.fullmatch(name):
            raise refuse(
                f"declared at {source} is no name every SDK can hold: it starts with a letter "
                "and goes on in letters, digits and _, at most 63 characters"
            )
        try:
            parsed = _Pattern(pattern)
        except ValueError as error:
            raise refuse(f"declared at {source}: {error}") from None
        if name in _RESERVED_ENTRY_NAMES:
            raise refuse("is reserved, since a store already has a member of that name in some SDK")
        for prior, prior_pattern, prior_source in self._entries:
            if prior == name:
                raise ValueError(
                    f'kv "{self.name}": entry "{name}" is declared already at {prior_source}, '
                    "and a store names each entry once"
                )
            if prior_pattern.overlaps(parsed):
                raise refuse(
                    f'declared at {source} has pattern "{pattern}", which overlaps pattern '
                    f'"{prior_pattern.written}" of entry "{prior}" declared at {prior_source}: '
                    "some key would match both, so neither entry could tell its keys from the "
                    "other's"
                )
        self._entries.append((name, parsed, source))
        self._config.entries.append(KvEntry(name=name, pattern=pattern, shape=shape, source=source))
        self._declare()
        return parsed

    def _properties(self, access: str) -> KvProperties:
        if is_discovering():
            raise refuse_unprovisioned(f'kv("{self.name}")', access)
        return read_kv_binding(self.name)

    @property
    def connection_string(self) -> str:
        """The store's URL, ``redis://`` or ``rediss://`` when it requires TLS, with the
        delivered credentials percent-encoded, for tools that take one."""
        properties = self._properties("connection_string")
        scheme = "rediss" if properties.tls else "redis"
        user = quote(properties.username, safe="")
        password = quote(properties.password, safe="")
        return f"{scheme}://{user}:{password}@{properties.host}:{properties.port}"

    def _client_options(self, access: str) -> dict[str, Any]:
        properties = self._properties(access)
        options: dict[str, Any] = {"host": properties.host, "port": properties.port}
        if properties.username:
            options["username"] = properties.username
        if properties.password:
            options["password"] = properties.password
        if properties.tls and properties.ca_pem:
            options["ssl_authority"] = _trust_only(self.name, properties.ca_pem)
        elif properties.tls and properties.tls_server_name:
            options["ssl_authority"] = ssl.create_default_context()
        elif properties.tls:
            options.update(ssl=True, ssl_cert_reqs="required", ssl_check_hostname=True)
        if "ssl_authority" in options and properties.tls_server_name:
            options["ssl_server_name"] = properties.tls_server_name
        return options

    def client(self):
        """The ``redis.asyncio.Redis`` client connected to the store, opened on the first
        call and returned unchanged on every one after. Over TLS it trusts only the store's
        own certificate authority when the provider names one, verifies the binding's TLS
        server name when it names one, as a binding pointing at a port forward does, and
        raises ``ValueError`` when the delivered authority holds no PEM certificate. redis-py
        is imported here, from the ``ocel[kv]`` extra."""
        if self._client is None:
            options = self._client_options("client")
            _import_redis()
            import redis.asyncio

            if "ssl_authority" in options:
                from ocel._kv_authority import AsyncAuthorityConnection

                pool = redis.asyncio.ConnectionPool(
                    connection_class=AsyncAuthorityConnection, **options
                )
                self._client = redis.asyncio.Redis.from_pool(pool)
            else:
                self._client = redis.asyncio.Redis(**options)
        return self._client

    def sync_client(self):
        """The ``redis.Redis`` client connected to the store, opened on the first call and
        returned unchanged on every one after. It raises as :meth:`client` does."""
        if self._sync_client is None:
            options = self._client_options("sync_client")
            redis = _import_redis()
            if "ssl_authority" in options:
                from ocel._kv_authority import AuthorityConnection

                pool = redis.ConnectionPool(connection_class=AuthorityConnection, **options)
                self._sync_client = redis.Redis.from_pool(pool)
            else:
                self._sync_client = redis.Redis(**options)
        return self._sync_client

    def text(self, name: str, pattern: str, *, ttl: str | timedelta | None = None) -> Text:
        """Declare a ``text`` entry named ``name``: a string under each key built from
        ``pattern``, ``/``-separated segments that are each a literal or a ``:parameter``.
        ``ttl`` is how long a key lives after each write; without it, until it is deleted."""
        return Text(self, name, pattern, KvShape.TEXT, ttl, _caller())

    def counter(self, name: str, pattern: str, *, ttl: str | timedelta | None = None) -> Counter:
        """Declare a ``counter`` entry: an integer under each key, changed atomically."""
        return Counter(self, name, pattern, KvShape.COUNTER, ttl, _caller())

    def json(
        self,
        name: str,
        pattern: str,
        *,
        model: type[M],
        ttl: str | timedelta | None = None,
        on_invalid: Literal["raise", "miss"] = "raise",
    ) -> Json[M]:
        """Declare a ``json`` entry: a value of ``model``, validated with pydantic (the
        ``ocel[pydantic]`` extra) on write and on read. A stored value the model refuses
        raises :class:`InvalidKVValueError` on read, or reads as ``None`` under
        ``on_invalid="miss"``."""
        return Json(self, name, pattern, KvShape.JSON, ttl, _caller(), model, on_invalid)

    def list(self, name: str, pattern: str, *, ttl: str | timedelta | None = None) -> List:
        """Declare a ``list`` entry: a list of strings under each key."""
        return List(self, name, pattern, KvShape.LIST, ttl, _caller())

    def set(self, name: str, pattern: str, *, ttl: str | timedelta | None = None) -> Set:
        """Declare a ``set`` entry: a set of strings under each key."""
        return Set(self, name, pattern, KvShape.SET, ttl, _caller())


def _caller() -> str:
    caller = inspect.stack(0)[2]
    return f"{caller.filename}:{caller.lineno}"


def _import_redis():
    try:
        import redis
    except ImportError:
        raise ImportError(
            "a kv store is reached with redis-py, which is not installed. "
            "Install it with the extra: pip install 'ocel[kv]'"
        ) from None
    return redis


class _Entry:
    def __init__(
        self,
        store: KV,
        name: str,
        pattern: str,
        shape: KvShape,
        ttl: str | timedelta | None,
        source: str,
    ):
        self._store = store
        self._name = name
        self._ttl = None if ttl is None else _milliseconds(ttl)
        self._pattern = store._add_entry(name, pattern, shape, source)

    def __repr__(self) -> str:
        return f'<kv "{self._store.name}" entry "{self._name}" {self._pattern.written}>'

    def _sync(self, operation: str):
        if is_discovering():
            raise refuse_unprovisioned(f'kv("{self._store.name}")', f"{self._name}.{operation}")
        return self._store.sync_client()

    def _async(self, operation: str):
        if is_discovering():
            raise refuse_unprovisioned(f'kv("{self._store.name}")', f"{self._name}.{operation}")
        return self._store.client()

    def _key(self, key: Mapping[str, Any]) -> str:
        return self._pattern.build(key)

    def _resolve_ttl(self, written: _WriteTtl) -> tuple[str, int]:
        if written is _DECLARED:
            return ("clear", 0) if self._ttl is None else ("expire", self._ttl)
        if written is None:
            return ("clear", 0)
        if written == KEEP:
            return ("keep", 0)
        return ("expire", _milliseconds(written))

    def _set_options(self, written: _WriteTtl) -> dict[str, Any]:
        mode, milliseconds = self._resolve_ttl(written)
        if mode == "expire":
            return {"px": milliseconds}
        if mode == "keep":
            return {"keepttl": True}
        return {}

    def _queue(self, pipeline, key: str, written: _WriteTtl, command: Callable[[Any], Any]) -> None:
        command(pipeline)
        mode, milliseconds = self._resolve_ttl(written)
        if mode == "expire":
            pipeline.pexpire(key, milliseconds)
        elif mode == "clear":
            pipeline.persist(key)

    def _write(self, operation: str, key: str, written: _WriteTtl, command: Callable[[Any], Any]):
        with self._sync(operation).pipeline(transaction=True) as pipeline:
            self._queue(pipeline, key, written, command)
            return pipeline.execute()[0]

    async def _write_async(
        self, operation: str, key: str, written: _WriteTtl, command: Callable[[Any], Any]
    ):
        async with self._async(operation).pipeline(transaction=True) as pipeline:
            self._queue(pipeline, key, written, command)
            return (await pipeline.execute())[0]

    def _get_many(self, keys: Iterable[Mapping[str, Any]]) -> list[Any]:
        built = [self._key(key) for key in keys]
        with self._sync("get_many").pipeline(transaction=False) as pipeline:
            for key in built:
                pipeline.get(key)
            return list(zip(built, pipeline.execute(), strict=True))

    async def _get_many_async(self, keys: Iterable[Mapping[str, Any]]) -> list[Any]:
        built = [self._key(key) for key in keys]
        async with self._async("get_many").pipeline(transaction=False) as pipeline:
            for key in built:
                pipeline.get(key)
            return list(zip(built, await pipeline.execute(), strict=True))

    def delete(self, **key: Any) -> bool:
        """Delete the key, answering whether it held a value."""
        return self._sync("delete").delete(self._key(key)) > 0

    async def delete_async(self, **key: Any) -> bool:
        """Delete the key, answering whether it held a value."""
        return await self._async("delete").delete(self._key(key)) > 0


class Text(_Entry):
    """A ``text`` entry: a string under each key. Every operation takes the key's
    parameters as keyword arguments, and has an ``_async`` twin."""

    def get(self, **key: Any) -> str | None:
        """The string under the key, or ``None`` when there is none."""
        return _decode(self._sync("get").get(self._key(key)))

    async def get_async(self, **key: Any) -> str | None:
        """The string under the key, or ``None`` when there is none."""
        return _decode(await self._async("get").get(self._key(key)))

    def get_many(self, keys: Iterable[Mapping[str, Any]]) -> list[str | None]:
        """The string under each key, in order, read in one round trip."""
        return [_decode(value) for _, value in self._get_many(keys)]

    async def get_many_async(self, keys: Iterable[Mapping[str, Any]]) -> list[str | None]:
        """The string under each key, in order, read in one round trip."""
        return [_decode(value) for _, value in await self._get_many_async(keys)]

    def set(
        self, value: str, /, *, ttl: str | timedelta | None | _Unset = _DECLARED, **key: Any
    ) -> None:
        """Write ``value`` under the key. ``ttl`` replaces the entry's own, ``"keep"`` keeps
        the key's current one, and ``None`` clears it."""
        self._sync("set").set(self._key(key), _text(value), **self._set_options(ttl))

    async def set_async(
        self, value: str, /, *, ttl: str | timedelta | None | _Unset = _DECLARED, **key: Any
    ) -> None:
        """Write ``value`` under the key, as :meth:`set` does."""
        await self._async("set").set(self._key(key), _text(value), **self._set_options(ttl))


def _text(value: Any) -> str:
    if not isinstance(value, str):
        raise TypeError(f"a text entry holds a str, not {type(value).__name__}")
    return value


class Counter(_Entry):
    """A ``counter`` entry: an integer under each key, changed atomically. Every operation
    takes the key's parameters as keyword arguments, and has an ``_async`` twin."""

    @staticmethod
    def _count(key: str, value: Any) -> int | None:
        if value is None:
            return None
        text = _decode(value)
        try:
            return int(text)
        except ValueError:
            raise InvalidKVValueError(key, f'holds "{text}", which is no integer') from None

    def get(self, **key: Any) -> int | None:
        """The integer under the key, or ``None`` when there is none."""
        built = self._key(key)
        return self._count(built, self._sync("get").get(built))

    async def get_async(self, **key: Any) -> int | None:
        """The integer under the key, or ``None`` when there is none."""
        built = self._key(key)
        return self._count(built, await self._async("get").get(built))

    def get_many(self, keys: Iterable[Mapping[str, Any]]) -> list[int | None]:
        """The integer under each key, in order, read in one round trip."""
        return [self._count(key, value) for key, value in self._get_many(keys)]

    async def get_many_async(self, keys: Iterable[Mapping[str, Any]]) -> list[int | None]:
        """The integer under each key, in order, read in one round trip."""
        return [self._count(key, value) for key, value in await self._get_many_async(keys)]

    def set(
        self, value: int, /, *, ttl: str | timedelta | None | _Unset = _DECLARED, **key: Any
    ) -> None:
        """Write ``value`` under the key, treating ``ttl`` as :meth:`Text.set` does."""
        self._sync("set").set(self._key(key), str(_integer(value)), **self._set_options(ttl))

    async def set_async(
        self, value: int, /, *, ttl: str | timedelta | None | _Unset = _DECLARED, **key: Any
    ) -> None:
        """Write ``value`` under the key, as :meth:`set` does."""
        await self._async("set").set(self._key(key), str(_integer(value)), **self._set_options(ttl))

    def increment(
        self, by: int = 1, /, *, ttl: str | timedelta | None | _Unset = _DECLARED, **key: Any
    ) -> int:
        """Add ``by`` to the integer under the key, from 0 when there is none, and answer the
        sum. The ttl is applied in the same transaction."""
        built = self._key(key)
        step = _integer(by)
        return self._write("increment", built, ttl, lambda p: p.incrby(built, step))

    async def increment_async(
        self, by: int = 1, /, *, ttl: str | timedelta | None | _Unset = _DECLARED, **key: Any
    ) -> int:
        """Add ``by`` to the integer under the key, as :meth:`increment` does."""
        built = self._key(key)
        step = _integer(by)
        return await self._write_async("increment", built, ttl, lambda p: p.incrby(built, step))

    def decrement(
        self, by: int = 1, /, *, ttl: str | timedelta | None | _Unset = _DECLARED, **key: Any
    ) -> int:
        """Subtract ``by`` from the integer under the key, as :meth:`increment` adds."""
        built = self._key(key)
        step = _integer(by)
        return self._write("decrement", built, ttl, lambda p: p.decrby(built, step))

    async def decrement_async(
        self, by: int = 1, /, *, ttl: str | timedelta | None | _Unset = _DECLARED, **key: Any
    ) -> int:
        """Subtract ``by`` from the integer under the key, as :meth:`increment` adds."""
        built = self._key(key)
        step = _integer(by)
        return await self._write_async("decrement", built, ttl, lambda p: p.decrby(built, step))


def _integer(value: Any) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise TypeError(f"a counter holds an int, not {type(value).__name__}")
    return value


class Json(_Entry, Generic[M]):
    """A ``json`` entry: a value of its model under each key. Every operation takes the
    key's parameters as keyword arguments, and has an ``_async`` twin."""

    def __init__(self, store, name, pattern, shape, ttl, source, model, on_invalid):
        if on_invalid not in ("raise", "miss"):
            raise ValueError(f'on_invalid is "raise" or "miss", not {on_invalid!r}')
        try:
            from pydantic import TypeAdapter, ValidationError
        except ImportError:
            raise ImportError(
                f"a json entry's model {model!r} is validated with pydantic, which is not "
                f"installed. Install it with the extra: pip install 'ocel[pydantic]'"
            ) from None
        super().__init__(store, name, pattern, shape, ttl, source)
        self._adapter = TypeAdapter(model)
        self._invalid = ValidationError
        self._miss_on_invalid = on_invalid == "miss"

    def _read(self, key: str, raw: Any) -> M | None:
        if raw is None:
            return None
        try:
            return self._adapter.validate_json(raw)
        except self._invalid as error:
            if self._miss_on_invalid:
                return None
            raise InvalidKVValueError(key, f"holds a value its model refuses: {error}") from None

    def _encode(self, key: str, value: Any) -> str:
        try:
            validated = self._adapter.validate_python(value)
        except self._invalid as error:
            raise InvalidKVValueError(
                key, f"would hold a value its model refuses: {error}"
            ) from None
        return self._adapter.dump_json(validated).decode("utf-8")

    def get(self, **key: Any) -> M | None:
        """The value under the key, or ``None`` when there is none."""
        built = self._key(key)
        return self._read(built, self._sync("get").get(built))

    async def get_async(self, **key: Any) -> M | None:
        """The value under the key, or ``None`` when there is none."""
        built = self._key(key)
        return self._read(built, await self._async("get").get(built))

    def get_many(self, keys: Iterable[Mapping[str, Any]]) -> list[M | None]:
        """The value under each key, in order, read in one round trip."""
        return [self._read(key, value) for key, value in self._get_many(keys)]

    async def get_many_async(self, keys: Iterable[Mapping[str, Any]]) -> list[M | None]:
        """The value under each key, in order, read in one round trip."""
        return [self._read(key, value) for key, value in await self._get_many_async(keys)]

    def set(
        self, value: M, /, *, ttl: str | timedelta | None | _Unset = _DECLARED, **key: Any
    ) -> None:
        """Validate ``value`` against the model and write it under the key as JSON, treating
        ``ttl`` as :meth:`Text.set` does."""
        built = self._key(key)
        encoded = self._encode(built, value)
        self._sync("set").set(built, encoded, **self._set_options(ttl))

    async def set_async(
        self, value: M, /, *, ttl: str | timedelta | None | _Unset = _DECLARED, **key: Any
    ) -> None:
        """Validate ``value`` and write it under the key, as :meth:`set` does."""
        built = self._key(key)
        encoded = self._encode(built, value)
        await self._async("set").set(built, encoded, **self._set_options(ttl))


def _strings(values: tuple[Any, ...]) -> list[str]:
    if not values or any(not isinstance(value, str) for value in values):
        raise TypeError("a list or set entry takes one or more str values")
    return list(values)


def _slice_stop(stop: int | None) -> int:
    return -1 if stop is None else stop - 1


class List(_Entry):
    """A ``list`` entry: a list of strings under each key. Every operation takes the key's
    parameters as keyword arguments, and has an ``_async`` twin."""

    def append(
        self, *values: str, ttl: str | timedelta | None | _Unset = _DECLARED, **key: Any
    ) -> int:
        """Append ``values`` to the end of the list and answer its new length, applying the
        ttl in the same transaction."""
        built, items = self._key(key), _strings(values)
        return self._write("append", built, ttl, lambda p: p.rpush(built, *items))

    async def append_async(
        self, *values: str, ttl: str | timedelta | None | _Unset = _DECLARED, **key: Any
    ) -> int:
        """Append ``values`` to the end of the list, as :meth:`append` does."""
        built, items = self._key(key), _strings(values)
        return await self._write_async("append", built, ttl, lambda p: p.rpush(built, *items))

    def appendleft(
        self, *values: str, ttl: str | timedelta | None | _Unset = _DECLARED, **key: Any
    ) -> int:
        """Prepend ``values``, in the order given, to the start of the list and answer its
        new length, as :meth:`append` does."""
        built, items = self._key(key), _strings(values)[::-1]
        return self._write("appendleft", built, ttl, lambda p: p.lpush(built, *items))

    async def appendleft_async(
        self, *values: str, ttl: str | timedelta | None | _Unset = _DECLARED, **key: Any
    ) -> int:
        """Prepend ``values`` to the start of the list, as :meth:`appendleft` does."""
        built, items = self._key(key), _strings(values)[::-1]
        return await self._write_async("appendleft", built, ttl, lambda p: p.lpush(built, *items))

    def pop(self, **key: Any) -> str | None:
        """Remove and answer the last value, or ``None`` when the list is empty."""
        return _decode(self._sync("pop").rpop(self._key(key)))

    async def pop_async(self, **key: Any) -> str | None:
        """Remove and answer the last value, or ``None`` when the list is empty."""
        return _decode(await self._async("pop").rpop(self._key(key)))

    def popleft(self, **key: Any) -> str | None:
        """Remove and answer the first value, or ``None`` when the list is empty."""
        return _decode(self._sync("popleft").lpop(self._key(key)))

    async def popleft_async(self, **key: Any) -> str | None:
        """Remove and answer the first value, or ``None`` when the list is empty."""
        return _decode(await self._async("popleft").lpop(self._key(key)))

    def get(self, index: int, /, **key: Any) -> str | None:
        """The value at ``index``, counting back from the end when negative, or ``None`` past
        either end."""
        return _decode(self._sync("get").lindex(self._key(key), index))

    async def get_async(self, index: int, /, **key: Any) -> str | None:
        """The value at ``index``, as :meth:`get` reads it."""
        return _decode(await self._async("get").lindex(self._key(key), index))

    def slice(self, start: int = 0, stop: int | None = None, /, **key: Any) -> list[str]:
        """The values from ``start`` up to but not including ``stop``, as a Python slice
        reads them."""
        if stop == 0:
            return []
        values = self._sync("slice").lrange(self._key(key), start, _slice_stop(stop))
        return [_decode(value) for value in values]

    async def slice_async(
        self, start: int = 0, stop: int | None = None, /, **key: Any
    ) -> list[str]:
        """The values from ``start`` up to but not including ``stop``, as :meth:`slice`."""
        if stop == 0:
            return []
        values = await self._async("slice").lrange(self._key(key), start, _slice_stop(stop))
        return [_decode(value) for value in values]

    def length(self, **key: Any) -> int:
        """How many values the list holds."""
        return self._sync("length").llen(self._key(key))

    async def length_async(self, **key: Any) -> int:
        """How many values the list holds."""
        return await self._async("length").llen(self._key(key))


class Set(_Entry):
    """A ``set`` entry: a set of strings under each key. Every operation takes the key's
    parameters as keyword arguments, and has an ``_async`` twin."""

    def add(
        self, *members: str, ttl: str | timedelta | None | _Unset = _DECLARED, **key: Any
    ) -> int:
        """Add ``members`` and answer how many were not in the set already, applying the ttl
        in the same transaction."""
        built, items = self._key(key), _strings(members)
        return self._write("add", built, ttl, lambda p: p.sadd(built, *items))

    async def add_async(
        self, *members: str, ttl: str | timedelta | None | _Unset = _DECLARED, **key: Any
    ) -> int:
        """Add ``members``, as :meth:`add` does."""
        built, items = self._key(key), _strings(members)
        return await self._write_async("add", built, ttl, lambda p: p.sadd(built, *items))

    def discard(self, *members: str, **key: Any) -> int:
        """Remove ``members`` and answer how many were in the set."""
        return self._sync("discard").srem(self._key(key), *_strings(members))

    async def discard_async(self, *members: str, **key: Any) -> int:
        """Remove ``members`` and answer how many were in the set."""
        return await self._async("discard").srem(self._key(key), *_strings(members))

    def contains(self, member: str, /, **key: Any) -> bool:
        """Whether ``member`` is in the set."""
        return bool(self._sync("contains").sismember(self._key(key), member))

    async def contains_async(self, member: str, /, **key: Any) -> bool:
        """Whether ``member`` is in the set."""
        return bool(await self._async("contains").sismember(self._key(key), member))

    def size(self, **key: Any) -> int:
        """How many members the set holds."""
        return self._sync("size").scard(self._key(key))

    async def size_async(self, **key: Any) -> int:
        """How many members the set holds."""
        return await self._async("size").scard(self._key(key))

    def members(self, **key: Any) -> set[str]:
        """Every member of the set."""
        return {_decode(member) for member in self._sync("members").smembers(self._key(key))}

    async def members_async(self, **key: Any) -> set[str]:
        """Every member of the set."""
        members = await self._async("members").smembers(self._key(key))
        return {_decode(member) for member in members}


def kv(
    name: str,
    *,
    version: Literal["8", "9"] | None = None,
    eviction: str | None = None,
    memory: str | None = None,
) -> KV:
    """Declare a key-value store named ``name``, one Valkey instance of its own, and return
    the handle its entries are declared on. ``version`` is the major Valkey version,
    ``eviction`` the policy keys are evicted by once the store is full (``noeviction`` when
    unset), and ``memory`` the size it holds, such as ``"256mb"``; the provider picks a
    version and a size when they are unset.

    Declare its entries with :meth:`KV.text`, :meth:`KV.counter`, :meth:`KV.json`,
    :meth:`KV.list` and :meth:`KV.set`. Two entries whose patterns could name the same key
    are refused where the second is declared. During discovery every operation, client and
    connection string raises :class:`UnprovisionedResourceError`."""
    caller = inspect.stack(0)[1]
    store = KV(
        name,
        KvConfig(version=version or "", eviction=eviction or "", memory=memory or ""),
        f"{caller.filename}:{caller.lineno}",
    )
    store._declare()
    return store
