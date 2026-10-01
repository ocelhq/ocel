def format_error(error: BaseException) -> str:
    return str(error).strip() or type(error).__name__
