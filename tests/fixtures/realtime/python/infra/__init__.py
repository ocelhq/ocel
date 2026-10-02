from dataclasses import dataclass
from urllib.parse import parse_qs

from pydantic import BaseModel, ConfigDict

import ocel


class Env(ocel.Env):
    journey_nonce: ocel.Secret


@dataclass(frozen=True)
class Caller:
    id: str
    orders: list[str]
    projects: list[str]
    may_publish: bool


def read_list(claims: dict[str, list[str]], name: str) -> list[str]:
    value = claims.get(name, [""])[0]
    return value.split(",") if value else []


def read_caller(request: ocel.RealtimeRequest) -> Caller | None:
    scheme, _, credential = request.headers.get("authorization", "").partition(" ")
    if scheme != "Bearer" or not credential:
        return None
    claims = parse_qs(credential)
    user = claims.get("user", [""])[0]
    if not user:
        return None
    return Caller(
        id=user,
        orders=read_list(claims, "orders"),
        projects=read_list(claims, "projects"),
        may_publish=claims.get("publish", [""])[0] == "yes",
    )


live = ocel.realtime("app", authorize=read_caller)


class OrderEvent(BaseModel):
    status: str


class DeployEvent(BaseModel):
    state: str


class Note(BaseModel):
    model_config = ConfigDict(extra="forbid")

    text: str


@live.channel("orders/:orderId", schema=OrderEvent)
def orders(ctx: ocel.RuleContext) -> bool:
    return ctx.params["orderId"] in ctx.auth.orders


@live.channel("projects/:projectId/deploys/:deployId", schema=DeployEvent, wildcard=True)
def deploys(ctx: ocel.RuleContext) -> bool:
    project_id = ctx.params.get("projectId")
    return project_id is not None and project_id in ctx.auth.projects


@live.channel("rooms/:roomId", schema=Note, publish=lambda ctx: ctx.auth.may_publish)
def rooms(ctx: ocel.RuleContext) -> bool:
    return True


status = live.channel("status", schema=Note, subscribe="public")
