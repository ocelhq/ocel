import asyncio
import inspect
from collections.abc import Callable
from typing import Any


async def call_function(function: Callable[..., Any] | None, *arguments: Any) -> Any:
    if function is None:
        return None
    if is_async(function):
        return await function(*arguments)
    answer = await asyncio.to_thread(function, *arguments)
    return await answer if inspect.isawaitable(answer) else answer


def is_async(function: Callable[..., Any]) -> bool:
    return inspect.iscoroutinefunction(function) or inspect.iscoroutinefunction(
        type(function).__call__
    )
