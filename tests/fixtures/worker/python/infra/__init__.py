import ocel

starts = 0
wrapped_by = ""


def count_start() -> None:
    global starts
    starts += 1


async def record_wrapper(ctx: ocel.RunContext, next):
    global wrapped_by
    wrapped_by = f"{ctx.kind}:{ctx.name}"
    return await next()


background = ocel.worker("worker", on_start=count_start, middleware=record_wrapper)


@ocel.task("greet", worker=background)
async def greet(payload: dict, ctx: ocel.RunContext) -> dict:
    return {"greeting": f"hello {payload['name']}", "starts": starts, "wrappedBy": wrapped_by}
