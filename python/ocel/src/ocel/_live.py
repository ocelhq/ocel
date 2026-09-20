import os

LIVE_DIR_ENV = "OCEL_LIVE_DIR"


def live_value(key: str) -> str | None:
    directory = os.environ.get(LIVE_DIR_ENV)
    if not directory:
        return None
    try:
        with open(os.path.join(directory, key), "rb") as handle:
            return handle.read().decode()
    except (OSError, ValueError):
        return None
