import json
import os


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


def link(name: str, kind: str) -> dict:
    key = f"OCEL_RESOURCE_{kind.upper()}_{name}"
    raw = os.environ.get(key)
    if not raw:
        raise RuntimeError(
            f"Value for {key} is not defined. Run `ocel dev` to resolve it locally, "
            f"or `ocel deploy` to have it delivered from the resource this app links."
        )
    try:
        delivered = json.loads(raw)
    except json.JSONDecodeError:
        raise RuntimeError(
            f"{key} does not carry a link record, so this app cannot read it as a {kind.upper()}"
        ) from None
    properties = delivered.get(kind)
    if not isinstance(properties, dict):
        raise RuntimeError(
            f"{key} carries a {_carried(delivered)} link, and this app reads it as a {kind.upper()}"
        )
    return properties


def _carried(delivered: dict) -> str:
    for field in delivered:
        if field != "name":
            return field.upper()
    return "UNSPECIFIED"
