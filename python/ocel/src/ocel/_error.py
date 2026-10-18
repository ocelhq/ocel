from connectrpc.errors import ConnectError


class OcelError(Exception):
    """The base of every error the SDK raises, so one ``except ocel.OcelError`` catches
    them all."""


class RuntimeRefusedError(OcelError):
    """Raised when the ocel runtime refuses or fails a call an operation made to it."""

    #: The Connect code the runtime answered with, such as ``"unavailable"``, which may pass
    #: on a retry, or ``"invalid_argument"``, which never will.
    code: str

    def __init__(self, what: str, access: str, code: str, message: str):
        super().__init__(f"{what}: the ocel runtime refused {access} ({code}): {message}")
        self.code = code


def refuse_runtime_call(what: str, access: str, error: ConnectError) -> RuntimeRefusedError:
    return RuntimeRefusedError(what, access, error.code.value, error.message)


def format_error(error: BaseException) -> str:
    return str(error).strip() or type(error).__name__
