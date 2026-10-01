import base64
import re
from collections.abc import Mapping
from typing import Literal

MAX_SEGMENTS = 4
MAX_VALUE_BYTES = 30
CHANNEL_SEGMENT = re.compile(r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,48}[A-Za-z0-9])?")
_PARAMETER = re.compile(r":[A-Za-z_][A-Za-z0-9_]*")
WireRefusal = Literal["missing-param", "unknown-param", "empty-value", "value-too-long"]
SEGMENT_RULE = (
    "letters, digits and -, at most 50 characters, starting and ending with a letter or digit"
)


class ChannelPattern:
    def __init__(self, written: str):
        self.written = written
        if written == "":
            raise ValueError(
                f"the pattern is empty: write one to {MAX_SEGMENTS} segments joined by /, "
                "each a literal or a :parameter"
            )
        parts = written.split("/")
        if len(parts) > MAX_SEGMENTS:
            raise ValueError(
                f'pattern "{written}" has {len(parts)} segments, and a channel pattern has at '
                f"most {MAX_SEGMENTS}"
            )
        self.segments: list[tuple[str, str]] = []
        for part in parts:
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
                if part[1:] in self.parameters:
                    raise ValueError(
                        f'pattern "{written}" names parameter "{part[1:]}" twice, so a channel '
                        "could not say which value is which"
                    )
                self.segments.append(("parameter", part[1:]))
            elif not CHANNEL_SEGMENT.fullmatch(part):
                raise ValueError(
                    f'pattern "{written}" has segment "{part}", which is no literal: a literal '
                    f"is {SEGMENT_RULE}"
                )
            else:
                self.segments.append(("literal", part))

    @property
    def parameters(self) -> list[str]:
        return [value for kind, value in self.segments if kind == "parameter"]


def _encode_value(value: str, data: bytes) -> str:
    if CHANNEL_SEGMENT.fullmatch(value) and not value.startswith("0z"):
        return value
    return "0z" + base64.b32encode(data).decode().rstrip("=").lower()


def encode_wire_channel(
    namespace: str, pattern: ChannelPattern, params: Mapping[str, str], wildcard: bool
) -> tuple[str, None] | tuple[None, WireRefusal]:
    names = pattern.parameters
    if any(name not in names for name in params):
        return None, "unknown-param"
    parts = ["", namespace]
    for i, (kind, value) in enumerate(pattern.segments):
        if kind == "literal":
            parts.append(value)
            continue
        if value not in params:
            later = any(
                later_kind == "parameter" and later_value in params
                for later_kind, later_value in pattern.segments[i:]
            )
            if not wildcard or later:
                return None, "missing-param"
            return "/".join([*parts, "*"]), None
        given = params[value]
        if given == "":
            return None, "empty-value"
        data = given.encode()
        if len(data) > MAX_VALUE_BYTES:
            return None, "value-too-long"
        parts.append(_encode_value(given, data))
    return "/".join(parts), None
