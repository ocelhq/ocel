import os

from ocel.gen.common.bindings.v1.bindings_pb import Binding, PostgresProperties


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


def binding(name: str) -> PostgresProperties:
    key = f"OCEL_RESOURCE_POSTGRES_{name}"
    raw = os.environ.get(key)
    if not raw:
        raise RuntimeError(
            f"Value for {key} is not defined. Run `ocel dev` to resolve it locally, "
            f"or `ocel deploy` to have it delivered from the resource this app binds."
        )
    try:
        delivered = Binding.from_json(raw, ignore_unknown_fields=True)
    except Exception:
        raise RuntimeError(
            f"{key} does not carry a binding record, so this app cannot read it as a POSTGRES"
        ) from None
    properties = delivered.properties
    if properties is None or properties.field != "postgres":
        carried = properties.field.upper() if properties else "UNSPECIFIED"
        raise RuntimeError(
            f"{key} carries a {carried} binding, and this app reads it as a POSTGRES"
        )
    return properties.value
