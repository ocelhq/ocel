import os

from ocel.gen.app.resources.v1.resources_connect import ResourceServiceClientSync
from ocel.gen.app.resources.v1.resources_pb import DeclareRequest
from ocel.gen.app.resources.v1.variables_pb import (
    DeclareEnvRequest,
    DeclareEnvResponse,
    ReportEnvProblemsRequest,
)

DISCOVERY_PHASE = "discovery"
_PHASE_ENV = "OCEL_PHASE"
_DEV_SERVER_ENV = "OCEL_DEV_SERVER"


def discovering() -> bool:
    return os.environ.get(_PHASE_ENV) == DISCOVERY_PHASE


def _client() -> ResourceServiceClientSync:
    address = os.environ.get(_DEV_SERVER_ENV, "").rstrip("/")
    return ResourceServiceClientSync(address, send_compression=None)


def declare(request: DeclareRequest) -> None:
    try:
        with _client() as client:
            client.declare(request)
    except Exception as error:
        raise _failed(request, error) from None


def declare_env(request: DeclareEnvRequest) -> DeclareEnvResponse:
    try:
        with _client() as client:
            return client.declare_env(request)
    except Exception as error:
        raise _env_failed(error) from None


def report_env_problems(request: ReportEnvProblemsRequest) -> None:
    try:
        with _client() as client:
            client.report_env_problems(request)
    except Exception as error:
        raise _env_failed(error) from None


def _failed(request: DeclareRequest, error: Exception) -> RuntimeError:
    said = _said(error)
    kind = request.config.field if request.config else "resource"
    return RuntimeError(f"ocel: declare {kind} '{request.resource.name}': {said}")


def _env_failed(error: Exception) -> RuntimeError:
    return RuntimeError(f"ocel: declare env: {_said(error)}")


def _said(error: Exception) -> str:
    return str(error).strip() or type(error).__name__
