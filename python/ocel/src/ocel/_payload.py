import inspect
import json
import types
from collections.abc import Callable
from typing import Any, Union, get_args, get_origin

PAYLOAD_LIMIT = 262144

_PLAIN = (dict, list, str, int, float, bool, type(None), object)


class Codec:
    def __init__(self, payload_type: Any):
        self.schema = ""
        self._adapter = None
        if not _is_plain(payload_type):
            try:
                from pydantic import TypeAdapter
            except ImportError:
                raise ImportError(
                    f"a payload typed {payload_type!r} is validated with pydantic, which is "
                    f"not installed. Install it with the extra: pip install 'ocel[pydantic]'"
                ) from None
            self._adapter = TypeAdapter(payload_type)
            self.schema = json.dumps(self._adapter.json_schema())

    def decode(self, value: Any) -> Any:
        if self._adapter is None:
            return value
        return self._adapter.validate_python(value)

    def to_jsonable(self, value: Any) -> Any:
        if self._adapter is None:
            return json.loads(encode_json(value))
        return self._adapter.dump_python(value, mode="json")

    def encode(self, payload: Any) -> bytes:
        if self._adapter is None:
            encoded = encode_json(payload)
        else:
            encoded = self._adapter.dump_json(payload).decode()
        data = encoded.encode()
        if len(data) > PAYLOAD_LIMIT:
            raise ValueError(
                f"a payload is at most {PAYLOAD_LIMIT} bytes (256 KiB) of JSON, "
                f"and this one is {len(data)} bytes"
            )
        return data


def encode_json(value: Any) -> str:
    try:
        return json.dumps(value, separators=(",", ":"))
    except TypeError as error:
        try:
            from pydantic_core import to_jsonable_python
        except ImportError:
            raise error from None
        return json.dumps(to_jsonable_python(value), separators=(",", ":"))


def decode_json(text: bytes) -> Any:
    return json.loads(text) if text else None


def find_payload_type(handler: Callable[..., Any] | None, batch: bool) -> Any:
    if handler is None:
        return None
    try:
        parameters = list(inspect.signature(handler, eval_str=True).parameters.values())
    except (NameError, TypeError, ValueError):
        return None
    if not parameters:
        return None
    annotation = parameters[0].annotation
    if annotation is inspect.Parameter.empty:
        return None
    if batch:
        arguments = get_args(annotation)
        return arguments[0] if get_origin(annotation) is list and arguments else None
    return annotation


def _is_plain(payload_type: Any) -> bool:
    if payload_type is None or payload_type is Any or payload_type in _PLAIN:
        return True
    origin = get_origin(payload_type)
    if origin in (dict, list, Union, types.UnionType):
        return all(_is_plain(argument) for argument in get_args(payload_type))
    return False
