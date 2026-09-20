import inspect
import os
from collections.abc import Sequence

from protobuf import Oneof

from ocel._binding import bucket_binding, unprovisioned
from ocel._declare import declare, discovering
from ocel.gen.app.bucket.v1.bucket_connect import BucketServiceClientSync
from ocel.gen.app.bucket.v1.bucket_pb import HeadRequest
from ocel.gen.app.resources.v1.resources_pb import (
    BucketConfig,
    DeclareRequest,
    ResourceIdentifier,
    ResourceType,
)

_KIND = "bucket"
_RUNTIME_ADDRESS_ENV = "OCEL_RUNTIME_ADDRESS"
_SESSION_TOKEN_ENV = "OCEL_SESSION_TOKEN"


class _Reached:
    def __init__(self, client: BucketServiceClientSync, bucket: str, public_base_url: str):
        self.client = client
        self.bucket = bucket
        self.public_base_url = public_base_url


class Bucket:
    """A bucket an app declares and reads and writes its objects through."""

    #: The name the bucket was declared under, and the name its binding is delivered as.
    name: str

    def __init__(self, name: str):
        """Take the handle for the bucket named ``name``. Prefer :func:`bucket`, which
        declares the bucket as well as handing back its handle."""
        self.name = name
        self._reached: _Reached | None = None

    def head(self, key: str):
        """What the bucket knows about the object under ``key``, or ``None`` when it holds
        none."""
        reached = self._runtime("head")
        reached.client.head(HeadRequest(bucket=reached.bucket, key=key))

    def _runtime(self, access: str) -> _Reached:
        if discovering():
            raise unprovisioned(f'bucket("{self.name}")', access)
        if self._reached is None:
            properties = bucket_binding(self.name)
            address = os.environ.get(_RUNTIME_ADDRESS_ENV)
            if not address:
                raise RuntimeError(
                    f"{_RUNTIME_ADDRESS_ENV} is not defined, so no resource the ocel runtime "
                    f"serves can be reached. Run `ocel dev` to serve it locally, or "
                    f"`ocel deploy` to have the deployed runtime's address delivered."
                )
            token = os.environ.get(_SESSION_TOKEN_ENV)
            if not token:
                raise RuntimeError(
                    f"{_SESSION_TOKEN_ENV} is not defined, so the ocel runtime at {address} "
                    f"would refuse every call. It is delivered beside {_RUNTIME_ADDRESS_ENV} by "
                    f"`ocel dev` and by the deployed runtime, never set by hand."
                )
            self._reached = _Reached(
                BucketServiceClientSync(address.rstrip("/"), send_compression=None),
                properties.bucket,
                properties.public_base_url,
            )
        return self._reached


def bucket(name: str, *, public: bool = False, allowed_origins: Sequence[str] = ()) -> Bucket:
    """Declare a bucket named ``name`` and return the handle an app reads and writes its
    objects through. Call it from a file under the project's discovery folder: during
    discovery the call is the declaration, and at runtime it reads the binding the deploy
    delivered for that name. A ``public`` bucket serves every object it holds anonymously
    over HTTP, and ``allowed_origins`` names the browser origins allowed to upload straight
    to the store."""
    if not discovering():
        return Bucket(name)
    caller = inspect.stack(0)[1]
    declare(
        DeclareRequest(
            resource=ResourceIdentifier(type=ResourceType.BUCKET, name=name),
            config=Oneof(_KIND, BucketConfig(public=public, allowed_origins=list(allowed_origins))),
            source=f"{caller.filename}:{caller.lineno}",
        )
    )
    return Bucket(name)
