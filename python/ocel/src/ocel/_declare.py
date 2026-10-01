import inspect
import os
from importlib.metadata import PackageNotFoundError
from importlib.metadata import version as _installed

from ocel._error import format_error
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
_DEV_SERVER_TOKEN_ENV = "OCEL_DEV_SERVER_TOKEN"
_SDK_VERSION_HEADER = "Ocel-Sdk-Version"


def read_sdk_version() -> str:
    try:
        return _installed("ocel")
    except PackageNotFoundError:
        return "dev"


def is_discovering() -> bool:
    return os.environ.get(_PHASE_ENV) == DISCOVERY_PHASE


def _new_client() -> ResourceServiceClientSync:
    address = os.environ.get(_DEV_SERVER_ENV, "").rstrip("/")
    return ResourceServiceClientSync(address, send_compression=None)


def _build_headers() -> dict[str, str]:
    return {
        "Authorization": f"Bearer {os.environ.get(_DEV_SERVER_TOKEN_ENV, '')}",
        _SDK_VERSION_HEADER: f"python/{read_sdk_version()}",
    }


def find_caller_source() -> str:
    caller = inspect.stack(0)[2]
    return f"{caller.filename}:{caller.lineno}"


def declare(request: DeclareRequest) -> None:
    try:
        with _new_client() as client:
            client.declare(request, headers=_build_headers())
    except Exception as error:
        raise _build_declare_error(request, error) from None


def declare_env(request: DeclareEnvRequest) -> DeclareEnvResponse:
    try:
        with _new_client() as client:
            return client.declare_env(request, headers=_build_headers())
    except Exception as error:
        raise _build_env_error(error) from None


def report_env_problems(request: ReportEnvProblemsRequest) -> None:
    try:
        with _new_client() as client:
            client.report_env_problems(request, headers=_build_headers())
    except Exception as error:
        raise _build_env_error(error) from None


def _build_declare_error(request: DeclareRequest, error: Exception) -> RuntimeError:
    kind = request.config.field if request.config else "resource"
    return RuntimeError(f"ocel: declare {kind} '{request.resource.name}': {format_error(error)}")


def _build_env_error(error: Exception) -> RuntimeError:
    return RuntimeError(f"ocel: declare env: {format_error(error)}")
