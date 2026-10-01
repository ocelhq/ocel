from datetime import datetime, timedelta, timezone
from typing import Any

from protobuf.wkt import Duration, Timestamp

from ocel.gen.app.resources.v1.resources_pb import RetryPolicy
from ocel.gen.app.topic.v1.topic_pb import Lane as WireLane

Seconds = timedelta | int | float


def encode_duration(value: Seconds | None) -> Duration | None:
    if value is None:
        return None
    if isinstance(value, timedelta):
        return Duration.from_timedelta(value)
    return Duration.from_seconds(value)


def encode_due_at(delay: Seconds | datetime | None) -> Timestamp | None:
    if delay is None:
        return None
    if isinstance(delay, datetime):
        return Timestamp.from_datetime(delay)
    span = delay if isinstance(delay, timedelta) else timedelta(seconds=delay)
    return Timestamp.from_datetime(datetime.now(timezone.utc) + span)


def encode_retry_policy(retry: Any) -> RetryPolicy | None:
    if retry is None:
        return None
    return RetryPolicy(
        max_attempts=retry.max_attempts or 0,
        min_delay=encode_duration(retry.min_delay),
        max_delay=encode_duration(retry.max_delay),
    )


def encode_lane(value: Any) -> WireLane:
    if value is None:
        return WireLane.UNSPECIFIED
    name = str(value).upper()
    if name not in ("HIGH", "DEFAULT", "LOW"):
        raise ValueError(f"a lane is 'high', 'default' or 'low', not {value!r}")
    return WireLane[name]


def decode_timestamp(value: Timestamp | None) -> datetime | None:
    return value.to_datetime() if value else None
