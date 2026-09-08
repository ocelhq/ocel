import json
import os
import urllib.error
import urllib.request

DISCOVERY_PHASE = "discovery"
_PHASE_ENV = "OCEL_PHASE"
_DEV_SERVER_ENV = "OCEL_DEV_SERVER"
_DECLARE_PATH = "/app.resources.v1.ResourceService/Declare"


def discovering() -> bool:
    return os.environ.get(_PHASE_ENV) == DISCOVERY_PHASE


def declare(kind: str, name: str, config: dict, source: str) -> None:
    body = json.dumps(
        {
            "resource": {"type": f"LINK_TYPE_{kind.upper()}", "name": name},
            kind: config,
            "source": source,
        }
    ).encode()
    url = os.environ.get(_DEV_SERVER_ENV, "").rstrip("/") + _DECLARE_PATH
    request = urllib.request.Request(
        url, data=body, headers={"Content-Type": "application/json"}, method="POST"
    )
    try:
        with urllib.request.urlopen(request) as response:
            if response.status != 200:
                raise _failed(kind, name, response.status, response.read())
    except urllib.error.HTTPError as error:
        raise _failed(kind, name, error.code, error.read()) from None
    except urllib.error.URLError as error:
        raise RuntimeError(f"ocel: declare {kind} '{name}': {error.reason}") from None


def _failed(kind: str, name: str, status: int, body: bytes) -> RuntimeError:
    said = body.decode(errors="replace").strip()
    return RuntimeError(f"ocel: declare {kind} '{name}': {status} {said}")
