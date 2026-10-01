import os

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


class UnprovisionedResourceError(RuntimeError):
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
    return _read_properties(name, "postgres")


def read_bucket_binding(name: str) -> BucketProperties:
    return _read_properties(name, "bucket")


def read_kv_binding(name: str) -> KvProperties:
    return _read_properties(name, "kv")


def read_realtime_binding(name: str) -> RealtimeProperties:
    return _read_properties(name, "realtime")


def refuse_unbound(name: str, kind: str) -> RuntimeError | None:
    found = _find_properties(name, kind)
    return found if isinstance(found, RuntimeError) else None


def read_runtime() -> tuple[str, dict[str, str]]:
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
    return address.rstrip("/"), {"Authorization": f"Bearer {token}"}


def _read_properties(name: str, kind: str):
    found = _find_properties(name, kind)
    if isinstance(found, RuntimeError):
        raise found
    return found


def _find_properties(name: str, kind: str):
    key = f"OCEL_RESOURCE_{kind.upper()}_{name}"
    raw = os.environ.get(key) or live_value(key)
    if not raw:
        return RuntimeError(
            f"Value for {key} is not defined. Run `ocel dev` to resolve it locally, "
            f"or `ocel deploy` to have it delivered from the resource this app binds."
        )
    try:
        delivered = Binding.from_json(raw, ignore_unknown_fields=True)
    except Exception:
        return RuntimeError(
            f"{key} does not contain a binding record, "
            f"so this app cannot read it as a {kind.upper()}"
        )
    properties = delivered.properties
    if properties is None or properties.field != kind:
        found = properties.field.upper() if properties else "UNSPECIFIED"
        return RuntimeError(
            f"{key} contains a {found} binding, and this app reads it as a {kind.upper()}"
        )
    return properties.value
