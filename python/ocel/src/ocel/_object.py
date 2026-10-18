from collections.abc import Mapping
from dataclasses import dataclass, field
from datetime import datetime

from pyqwest import Headers

from ocel._error import OcelError, RuntimeRefusedError
from ocel.gen.app.bucket.v1.bucket_pb import PresignedTarget


class ObjectNotFound(OcelError, FileNotFoundError):
    """Raised when an operation that cannot answer with ``None`` names an object the bucket
    does not have."""

    #: The key that named nothing.
    key: str

    def __init__(self, key: str):
        super().__init__(f'the bucket has no object under "{key}"')
        self.key = key

    def __str__(self) -> str:
        return f'the bucket has no object under "{self.key}"'


class PreconditionFailed(OcelError):
    """Raised when a write set ``if_not_exists`` or ``if_match`` and the object did not
    meet it, or when the object changed under every attempt to read one version of it."""

    #: The key whose current state refused the write or the read.
    key: str

    def __init__(self, key: str, message: str | None = None):
        super().__init__(
            message or f'the object under "{key}" did not meet the condition this write set'
        )
        self.key = key


@dataclass(frozen=True)
class ObjectInfo:
    """What a bucket knows about one of its objects."""

    #: The key the object is addressed by.
    key: str
    #: The object's length in bytes.
    size: int
    #: The store's opaque version tag for these bytes.
    etag: str
    #: The media type the object was written with.
    content_type: str
    #: When the object last took its current bytes, as the store reports it. It is
    #: ``None`` on the info a write returns, which describes the write itself.
    uploaded_at: datetime | None = None
    #: The user metadata written alongside the object.
    metadata: Mapping[str, str] = field(default_factory=dict[str, str])


def read_etag(headers: Headers, target: PresignedTarget) -> str:
    return headers.get(target.etag_header, "") if target.etag_header else ""


def refine_refusal(key: str, refused: RuntimeRefusedError) -> OcelError:
    if refused.code == "not_found":
        return ObjectNotFound(key)
    if refused.code == "failed_precondition":
        return PreconditionFailed(key)
    return refused


def refuse_status(key: str, status: int) -> OcelError | None:
    if status == 404:
        return ObjectNotFound(key)
    if status in (409, 412):
        return PreconditionFailed(key)
    return None
