import os

from ocel._live import live_value
from ocel.gen.common.bindings.v1.bindings_pb import (
    Binding,
    BucketProperties,
    PostgresProperties,
)


class UnprovisionedResourceError(RuntimeError):
    """Raised when app code reaches for a resource this run never provisioned, which is
    discovery: the pass that reads the declarations before anything stands. Catch it to
    keep a boot path alive when the resource is optional there; anything else raised from
    the same call means the resource exists and is genuinely broken."""


def unprovisioned(what: str, access: str) -> UnprovisionedResourceError:
    return UnprovisionedResourceError(
        f"'{what}' cannot be used during discovery: "
        f"tried to access '{access}' before the resource was provisioned"
    )


def postgres_binding(name: str) -> PostgresProperties:
    return _properties(name, "postgres")


def bucket_binding(name: str) -> BucketProperties:
    return _properties(name, "bucket")


def _properties(name: str, kind: str):
    key = f"OCEL_RESOURCE_{kind.upper()}_{name}"
    raw = os.environ.get(key) or live_value(key)
    if not raw:
        raise RuntimeError(
            f"Value for {key} is not defined. Run `ocel dev` to resolve it locally, "
            f"or `ocel deploy` to have it delivered from the resource this app binds."
        )
    try:
        delivered = Binding.from_json(raw, ignore_unknown_fields=True)
    except Exception:
        raise RuntimeError(
            f"{key} does not contain a binding record, so this app cannot read it as a {kind.upper()}"
        ) from None
    properties = delivered.properties
    if properties is None or properties.field != kind:
        found = properties.field.upper() if properties else "UNSPECIFIED"
        raise RuntimeError(
            f"{key} contains a {found} binding, and this app reads it as a {kind.upper()}"
        )
    return properties.value
