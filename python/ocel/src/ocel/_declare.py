import os

from ocel.gen.app.resources.v1.resources_connect import ResourceServiceClientSync
from ocel.gen.app.resources.v1.resources_pb import DeclareRequest

DISCOVERY_PHASE = "discovery"
_PHASE_ENV = "OCEL_PHASE"
_DEV_SERVER_ENV = "OCEL_DEV_SERVER"


def discovering() -> bool:
    return os.environ.get(_PHASE_ENV) == DISCOVERY_PHASE


def declare(request: DeclareRequest) -> None:
    address = os.environ.get(_DEV_SERVER_ENV, "").rstrip("/")
    try:
        with ResourceServiceClientSync(address, send_compression=None) as client:
            client.declare(request)
    except Exception as error:
        raise _failed(request, error) from None


def _failed(request: DeclareRequest, error: Exception) -> RuntimeError:
    said = str(error).strip() or type(error).__name__
    kind = request.config.field if request.config else "resource"
    return RuntimeError(f"ocel: declare {kind} '{request.resource.name}': {said}")
