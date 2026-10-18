import asyncio

import ocel

starts = 0
wrapped_by = ""
audited = ""
cancelled = ""


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
    return {
        "greeting": f"hello {payload['name']}",
        "starts": starts,
        "wrappedBy": wrapped_by,
        "audited": audited,
        "cancelled": cancelled,
    }


def record_cancel(payload: dict, ctx: ocel.RunContext) -> None:
    global cancelled
    cancelled = f"{ctx.name} {payload['name']}"


@ocel.task("linger", worker=background, on_cancel=record_cancel)
async def linger(payload: dict, ctx: ocel.RunContext) -> None:
    await asyncio.sleep(3600)


orders = ocel.topic("orders")


@orders.consumer("audit", worker=background)
async def audit(payload: dict, ctx: ocel.RunContext) -> None:
    global audited
    audited = f"{wrapped_by} {payload['name']}"
