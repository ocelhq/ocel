from collections.abc import Callable
from dataclasses import dataclass, field
from typing import Any, Literal

DEFAULT_WORKER = "worker"


@dataclass(frozen=True)
class Hooks:
    on_success: Callable[..., Any] | None = None
    on_failure: Callable[..., Any] | None = None
    on_complete: Callable[..., Any] | None = None
    on_cancel: Callable[..., Any] | None = None
    catch_error: Callable[..., Any] | None = None
    middleware: Callable[..., Any] | None = None
    on_start_attempt: Callable[..., Any] | None = None


@dataclass(frozen=True)
class Registration:
    kind: Literal["task", "consumer"]
    topic: str
    name: str
    worker: str
    handler: Callable[..., Any]
    decode: Callable[[Any], Any]
    batch: bool
    hooks: Hooks = field(default_factory=Hooks)


registrations: dict[tuple[str, str], Registration] = {}
workers: dict[str, Any] = {}


def register(registration: Registration) -> None:
    registrations[(registration.topic, registration.name)] = registration
