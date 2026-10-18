import os

from ocel._error import OcelError
from ocel._live import live_value
from ocel.gen.common.bindings.v1.bindings_pb import (
    Binding,
    BucketProperties,
    KvProperties,
    PostgresProperties,
    RealtimeProperties,
)

_RUNTIME_ADDRESS_ENV = "OCEL_RUNTIME_ADDRESS"
_SESSION_TOKEN_ENV = "OCEL_SESSION_TOKEN"


class UnprovisionedResourceError(OcelError):
    """Raised when app code reaches for a resource this run never provisioned, which is
    discovery: the pass that reads the declarations before anything is provisioned. Catch
    it to keep a boot path alive when the resource is optional there; anything else raised
    from the same call means the resource exists and is genuinely broken."""


def refuse_unprovisioned(what: str, access: str) -> UnprovisionedResourceError:
    return UnprovisionedResourceError(
        f"'{what}' cannot be used during discovery: "
        f"tried to access '{access}' before the resource was provisioned"
    )


def read_postgres_binding(name: str) -> PostgresProperties:
    return _read_properties(name, "postgres", PostgresProperties)


def read_bucket_binding(name: str) -> BucketProperties:
    return _read_properties(name, "bucket", BucketProperties)


def read_kv_binding(name: str) -> KvProperties:
    return _read_properties(name, "kv", KvProperties)


def read_realtime_binding(name: str) -> RealtimeProperties:
    return _read_properties(name, "realtime", RealtimeProperties)


def require_binding(name: str, kind: str) -> None:
    _read_properties(name, kind, object)


def read_runtime() -> tuple[str, dict[str, str]]:
    address = os.environ.get(_RUNTIME_ADDRESS_ENV)
    if not address:
        raise OcelError(
            f"{_RUNTIME_ADDRESS_ENV} is not defined, so no resource the ocel runtime "
            f"serves can be reached. Run `ocel dev` to serve it locally, or "
            f"`ocel deploy` to have the deployed runtime's address delivered."
        )
    token = os.environ.get(_SESSION_TOKEN_ENV)
    if not token:
        raise OcelError(
            f"{_SESSION_TOKEN_ENV} is not defined, so the ocel runtime at {address} "
            f"would refuse every call. It is delivered beside {_RUNTIME_ADDRESS_ENV} by "
            f"`ocel dev` and by the deployed runtime, never set by hand."
        )
    return address.rstrip("/"), {"Authorization": f"Bearer {token}"}


def _read_properties[P](name: str, kind: str, wanted: type[P]) -> P:
    key = f"OCEL_RESOURCE_{kind.upper()}_{name}"
    raw = os.environ.get(key) or live_value(key)
    if not raw:
        raise OcelError(
            f"{key} is not delivered to this process: `ocel dev` delivers it locally and "
            "`ocel deploy` to the deployed app, and a build gets it only from an `ocel deploy` "
            "whose provider forwards a port to the resource, so code that runs while building, "
            "such as prerendering a page, cannot use it under `ocel build`"
        )
    try:
        delivered = Binding.from_json(raw, ignore_unknown_fields=True)
    except Exception:
        raise OcelError(
            f"{key} does not contain a binding record, "
            f"so this app cannot read it as a {kind.upper()}"
        ) from None
    properties = delivered.properties
    if properties is None or properties.field != kind or not isinstance(properties.value, wanted):
        found = properties.field.upper() if properties else "UNSPECIFIED"
        raise OcelError(
            f"{key} contains a {found} binding, and this app reads it as a {kind.upper()}"
        )
    return properties.value
